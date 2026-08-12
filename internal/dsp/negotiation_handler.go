package dsp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// maxNegotiationRequestBodyBytes bounds every negotiation request body, for
// the same reason catalog_handler.go bounds catalog requests: an unbounded
// read on an unauthenticated public endpoint could exhaust memory before any
// validation runs.
const maxNegotiationRequestBodyBytes = 1 << 20 // 1 MiB

// terminateAfterOfferDelay is how long the provider waits, after pushing an
// informational counter-offer for an expired dataset, before independently
// withdrawing it. The delay exists so a consumer's ACCEPTED event — which
// re-checks validity and would reach the same TERMINATED outcome on its own
// path — has a real chance to arrive first. A var, not a const, so tests can
// shorten it. See the design spec's Risks section: this is a timing
// assumption, not a guarantee.
var terminateAfterOfferDelay = 200 * time.Millisecond

// Callback path suffixes, appended (with the provider pid) to a
// negotiation's stored callback address.
const (
	offerCallbackPath       = "/negotiations/%s/offers"
	agreementCallbackPath   = "/negotiations/%s/agreement"
	eventCallbackPath       = "/negotiations/%s/events"
	terminationCallbackPath = "/negotiations/%s/termination"
)

// negotiationHandler serves the contract negotiation protocol, provider
// role only.
type negotiationHandler struct {
	cfg   config.Config
	store *store.Store
}

// handleContractRequest serves POST /negotiations/request, the only entry
// point into a new negotiation. The synchronous response is always a plain
// REQUESTED acknowledgment — see the design spec's "the protocol is
// asynchronous" section — what the provider decided is pushed afterward.
func (h negotiationHandler) handleContractRequest(w http.ResponseWriter, r *http.Request) {
	var msg RequestMessage
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !slices.Contains(msg.Context, ContextURL) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "@context must contain "+ContextURL)
		return
	}
	if msg.Type != ContractRequestMessageType {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "@type must be "+ContractRequestMessageType)
		return
	}
	if msg.ConsumerPID == "" {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "consumerPid is required")
		return
	}
	if msg.Offer.ID == "" || msg.Offer.Target == "" {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "offer.@id and offer.target are required")
		return
	}
	if msg.CallbackAddress == "" {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "callbackAddress is required")
		return
	}

	providerPID, err := store.NewUUID()
	if err != nil {
		slog.Error("generate provider pid", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	now := time.Now()
	n := store.Negotiation{
		ProviderPID:     providerPID,
		ConsumerPID:     msg.ConsumerPID,
		State:           StateRequested,
		DatasetID:       msg.Offer.Target,
		OfferID:         msg.Offer.ID,
		CallbackAddress: msg.CallbackAddress,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := h.store.Create(n); err != nil {
		slog.Error("create negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, buildNegotiationStateDocument(n))

	outcome := decideInitialRequest(h.cfg, n.DatasetID, n.OfferID, now)
	h.dispatch(n, outcome)
}

// handleReRequest serves POST /negotiations/{id}/request: a consumer
// counter-offer or resend while the negotiation is OFFERED. Resending the
// identical offer is a synchronous rejection (CN:03-04); anything else is
// treated as a decision to walk away, terminated asynchronously (CN:01-02).
func (h negotiationHandler) handleReRequest(w http.ResponseWriter, r *http.Request) {
	n, ok, err := h.lookup(w, r)
	if err != nil || !ok {
		return
	}

	var msg RequestMessage
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !slices.Contains(msg.Context, ContextURL) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "@context must contain "+ContextURL)
		return
	}
	if msg.Type != ContractRequestMessageType {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "@type must be "+ContractRequestMessageType)
		return
	}
	if n.State != StateOffered {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"a counter-offer is only valid from OFFERED, negotiation is "+n.State)
		return
	}
	if decideReRequest(n.OfferID, msg.Offer.ID) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"offer.@id matches the offer already on the table")
		return
	}

	w.WriteHeader(http.StatusOK)
	h.pushAndStore(n, StateTerminated, terminationCallbackPath, buildTerminationMessage(n))
}

// handleEvent serves POST /negotiations/{id}/events, currently only the
// consumer's ACCEPTED event (this connector never receives FINALIZED — it
// sends that one).
func (h negotiationHandler) handleEvent(w http.ResponseWriter, r *http.Request) {
	n, ok, err := h.lookup(w, r)
	if err != nil || !ok {
		return
	}

	var msg struct {
		EventType string `json:"eventType"`
	}
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if msg.EventType != eventTypeAccepted {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "eventType must be ACCEPTED")
		return
	}
	if n.State != StateOffered {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"accept is only valid from OFFERED, negotiation is "+n.State)
		return
	}

	now := time.Now()
	if err := h.store.SetState(n.ProviderPID, StateAccepted, now); err != nil {
		slog.Error("update negotiation state", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateAccepted
	outcome := decideAccept(h.cfg, n.DatasetID, now)
	h.dispatch(n, outcome)
}

// handleVerification serves POST /negotiations/{id}/agreement/verification.
// Verification is only legal from AGREED (CN:03-02, CN:03-03 both violate
// this). VERIFIED -> FINALIZED has no validity check: a negotiation that
// reached AGREED always finalizes on verification — see the design spec's
// note on CN:02-07.
func (h negotiationHandler) handleVerification(w http.ResponseWriter, r *http.Request) {
	n, ok, err := h.lookup(w, r)
	if err != nil || !ok {
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	var msg json.RawMessage
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if n.State != StateAgreed {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"verification is only valid from AGREED, negotiation is "+n.State)
		return
	}

	if err := h.store.SetState(n.ProviderPID, StateVerified, time.Now()); err != nil {
		slog.Error("update negotiation state", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateVerified
	h.pushAndStore(n, StateFinalized, eventCallbackPath, buildFinalizedEventMessage(n))
}

// handleTermination serves POST /negotiations/{id}/termination, from either
// party. It is rejected from FINALIZED (CN:03-01) and from an already
// TERMINATED negotiation — both are terminal states with nothing left to
// terminate.
func (h negotiationHandler) handleTermination(w http.ResponseWriter, r *http.Request) {
	n, ok, err := h.lookup(w, r)
	if err != nil || !ok {
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	var msg json.RawMessage
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if n.State == StateFinalized || n.State == StateTerminated {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"negotiation cannot be terminated from "+n.State)
		return
	}

	if err := h.store.SetState(n.ProviderPID, StateTerminated, time.Now()); err != nil {
		slog.Error("update negotiation state", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetNegotiation serves GET /negotiations/{id}.
func (h negotiationHandler) handleGetNegotiation(w http.ResponseWriter, r *http.Request) {
	n, ok, err := h.lookup(w, r)
	if err != nil || !ok {
		return
	}
	writeJSON(w, http.StatusOK, buildNegotiationStateDocument(n))
}

// lookup resolves {id} to a stored negotiation, writing the appropriate
// error response and returning ok=false if it cannot. Every handler above
// except handleContractRequest starts with this.
func (h negotiationHandler) lookup(w http.ResponseWriter, r *http.Request) (store.Negotiation, bool, error) {
	providerPID := r.PathValue("id")
	n, ok, err := h.store.Get(providerPID)
	if err != nil {
		slog.Error("get negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return store.Negotiation{}, false, err
	}
	if !ok {
		writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+providerPID)
		return store.Negotiation{}, false, nil
	}
	return n, true, nil
}

// dispatch carries out a routing decision: it pushes whatever outcome
// requires to n's callback address and persists the resulting state. It runs
// after the synchronous response has already been written, matching DSP's
// async model.
func (h negotiationHandler) dispatch(n store.Negotiation, outcome negotiationOutcome) {
	switch {
	case outcome.pushOffer && outcome.pushTermination:
		h.pushAndStore(n, StateOffered, offerCallbackPath, buildOfferMessage(n))
		go h.delayedTerminate(n)
	case outcome.pushOffer:
		h.pushAndStore(n, StateOffered, offerCallbackPath, buildOfferMessage(n))
	case outcome.pushAgreement:
		h.pushAndStore(n, StateAgreed, agreementCallbackPath, buildAgreementMessage(n, h.cfg.PublicURL))
	case outcome.pushTermination:
		h.pushAndStore(n, StateTerminated, terminationCallbackPath, buildTerminationMessage(n))
	}
	// outcomeNone falls through: no push, negotiation stays REQUESTED.
}

// delayedTerminate withdraws an informational counter-offer for an expired
// dataset, after terminateAfterOfferDelay. It re-fetches state first: if the
// negotiation moved on while it slept (the consumer's accept arrived and
// reached TERMINATED on its own path, or the consumer terminated first),
// there is nothing left to withdraw.
func (h negotiationHandler) delayedTerminate(n store.Negotiation) {
	time.Sleep(terminateAfterOfferDelay)
	current, ok, err := h.store.Get(n.ProviderPID)
	if err != nil || !ok || current.State != StateOffered {
		return
	}
	h.pushAndStore(current, StateTerminated, terminationCallbackPath, buildTerminationMessage(current))
}

// pushAndStore pushes msg to n's callback address at the given path
// (formatted with the provider pid) and updates the stored state. The push
// happens first, but its failure does not block the state update: the
// provider is authoritative, and a consumer can always recover via GET.
func (h negotiationHandler) pushAndStore(n store.Negotiation, state, path string, msg any) {
	pushCallback(n.CallbackAddress+fmt.Sprintf(path, n.ProviderPID), msg)
	if err := h.store.SetState(n.ProviderPID, state, time.Now()); err != nil {
		slog.Error("update negotiation state", "provider_pid", n.ProviderPID, "error", err)
	}
}
