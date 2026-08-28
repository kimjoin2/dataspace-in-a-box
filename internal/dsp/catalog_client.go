// Package dsp: this file holds the outbound catalog request this connector
// makes as consumer, and the handler that triggers it. The same split
// negotiation_client.go makes, for the same reason -- everything in
// catalog_handler.go answers an inbound request; this initiates one.
//
// The handler lives here rather than in internal/mgmt, and reaches the
// management listener as an http.Handler on Routers, which is the route the
// initiate hooks already travel: they live in package dsp as code and on the
// management listener as routes, so that package needs no opinion about the
// protocol package they came from.
package dsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// consumerCatalogPath is the path a catalog request is POSTed to, formatted
// against a counterparty's base URL the way negotiation_client.go's constants
// are.
const consumerCatalogPath = "/catalog/request"

// maxCatalogResponseBytes bounds a counterparty's catalog. The shared client's
// timeout covers the body, so a hostile provider is already bounded in time --
// but a streamed response can allocate a great deal inside that window, and
// this is the one DSP body whose size scales with the counterparty's holdings.
// Every other inbound read in this connector is bounded; so is this one. A
// catalog larger than this is refused, which is the deliberate answer.
const maxCatalogResponseBytes = 1 << 20 // 1 MiB

// fetchCatalog asks the connector at baseURL for its catalog, addressing the
// credential to aud.
//
// It reuses callbackHTTPClient. sendInitialRequest and sendTransferRequest
// already do, and this is structurally what they are: one POST, bounded,
// response decoded, no retry. That client's behaviours come with it, and these
// are the ones worth knowing here -- redirects are not followed, so a load
// balancer's 308 is reported rather than chased; the connection pool is shared
// with the callback pushes; and the timeout covers the body, which is why the
// bound above exists rather than instead of it.
//
// No retry, for the reason sendInitialRequest records and which is stronger
// here: an operator asked, and a failure they are told about beats a silent
// retry.
func fetchCatalog(baseURL, aud string) (remoteCatalog, error) {
	msg := CatalogRequestMessage{Context: []string{ContextURL}, Type: CatalogRequestMessageType}
	body, err := json.Marshal(msg)
	if err != nil {
		return remoteCatalog{}, fmt.Errorf("marshal catalog request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+consumerCatalogPath, bytes.NewReader(body))
	if err != nil {
		return remoteCatalog{}, fmt.Errorf("build catalog request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authorization, maySend := mintOutboundCredential(aud)
	if !maySend {
		return remoteCatalog{}, fmt.Errorf("post catalog request: %w", errRosterExpired)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := callbackHTTPClient.Do(req)
	if err != nil {
		return remoteCatalog{}, fmt.Errorf("post catalog request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return remoteCatalog{}, fmt.Errorf("post catalog request: provider responded %d", resp.StatusCode)
	}

	var c remoteCatalog
	// A type error is fatal rather than tolerated: encoding/json populates
	// what it can before returning one, so a document with a malformed policy
	// list would otherwise decode into a structurally valid catalog with its
	// offers silently missing.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCatalogResponseBytes)).Decode(&c); err != nil {
		return remoteCatalog{}, fmt.Errorf("decode catalog: %w", err)
	}
	// participantId is required of a catalog and is the value one cannot omit,
	// so an empty one means this is not a catalog. Without this an empty
	// object, an error document, an unrelated document and a bare null all
	// decode cleanly into a catalog with no datasets, and the operator would
	// be told the counterparty advertises nothing rather than that the request
	// failed. sendInitialRequest refuses a response with no providerPid for
	// the same reason.
	if c.ParticipantID == "" {
		return remoteCatalog{}, fmt.Errorf("the response carries no participantId and is not a catalog")
	}
	return c, nil
}

// catalogLookupHandler serves the operator's request to fetch a counterparty's
// catalog. It holds no store: nothing here is written down. That is the
// concrete guard on DECISIONS.md section 25.3's boundary -- the boundary is
// about writing, and caching a fetched catalog is the write this route must
// not make.
type catalogLookupHandler struct {
	guard            rosterGuard
	knownParticipant func(string) bool
	providerAddress  func(string) (string, bool)
}

// handleCatalogLookup serves GET /catalog?providerId=... on the management
// listener.
func (h catalogLookupHandler) handleCatalogLookup(w http.ResponseWriter, r *http.Request) {
	// First, for the reason handleInitiate's equivalent guard runs first: this
	// refusal is about this connector rather than about the request, so no
	// correction to the query would make the call succeed.
	if !h.guard.usable() {
		h.guard.warnExpired()
		refuseExpiredRoster(w, CatalogErrorType)
		return
	}
	providerID := r.URL.Query().Get("providerId")
	if providerID == "" {
		writeError(w, CatalogErrorType, http.StatusBadRequest, "providerId is required")
		return
	}
	// Absence here is not a check that is skipped, the convention the initiate
	// hooks follow. What is absent with authentication off is the roster
	// itself, and with it the only thing that could turn a participant id into
	// an address.
	if h.providerAddress == nil {
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"this connector is running without a roster, so it cannot resolve a participant to an address")
		return
	}
	if h.knownParticipant != nil && !h.knownParticipant(providerID) {
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"providerId "+providerID+" is not a participant this connector's roster lists")
		return
	}
	address, ok := h.providerAddress(providerID)
	if !ok {
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"the roster lists no connector_address for providerId "+providerID)
		return
	}
	// The guard runs where it already runs: on the address this connector is
	// about to dial. LoadRoster deliberately does not run it -- internal/auth
	// cannot import this package, and a counterparty's host does not resolve
	// while this connector is booting -- so a roster entry naming a loopback
	// or link-local host is admitted at boot and refused here. Without this,
	// discovery would dial an address both initiate hooks refuse, and hand the
	// operator pairs for an exchange this connector will not transact.
	//
	// The reason is logged rather than echoed, for the reason handleInitiate's
	// equivalent guard gives: it reports what name resolution told this
	// connector, which the caller did not supply. The refusal names the roster
	// as what is at fault, because nothing the caller sent can correct it.
	if err := validateOutgoingCallback(address); err != nil {
		slog.Warn("reject catalog lookup", "provider_id", providerID,
			"connector_address", address, "error", err)
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"the roster's connector_address for "+providerID+
				" is not an address this connector will send to")
		return
	}

	c, err := fetchCatalog(address, providerID)
	if err != nil {
		slog.Warn("catalog lookup", "provider_id", providerID, "connector_address", address, "error", err)
		writeError(w, CatalogErrorType, http.StatusBadGateway,
			"the catalog request to "+providerID+" did not succeed")
		return
	}
	// The declared participant is an unauthenticated claim, and refusing on
	// one is fail-closed -- a different thing from acting on one, the line
	// LoadRoster's own comment draws. It is the one place where evidence about
	// what an address actually serves can contradict the roster, which is the
	// shape DECISIONS.md section 35.5 named.
	if c.ParticipantID != providerID {
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"the catalog at that address declares participantId "+c.ParticipantID+
				", not "+providerID)
		return
	}

	pairs, skipped := c.pairs()
	// Nothing is truncated silently. A dataset with no offer cannot be
	// negotiated for, and a catalog of sub-catalogs is not walked -- a
	// federated broker advertises that way and reporting it as empty would be
	// a lie.
	if skipped > 0 {
		slog.Info("catalog lookup omitted datasets advertising no offer",
			"provider_id", providerID, "omitted", skipped)
	}
	if len(c.Catalog) > 0 {
		slog.Info("catalog lookup did not walk the sub-catalogs this catalog advertises",
			"provider_id", providerID, "sub_catalogs", len(c.Catalog))
	}
	writeJSON(w, http.StatusOK, catalogLookupResponse{
		ParticipantID:    c.ParticipantID,
		ConnectorAddress: address,
		Datasets:         pairs,
	})
}
