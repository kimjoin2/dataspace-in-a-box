// This file holds the consumer role's negotiation handlers and reactions,
// split out of negotiation_handler.go so that file's provider role, dual-role
// routing, and shared helpers do not keep growing alongside the
// transfer-process milestone.

package dsp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// initiateRequestBody is the plain-JSON (not JSON-LD) body the TCK's own
// negotiation.initiate.url hook POSTs to trigger this connector to start a
// negotiation as consumer. Not a DSP protocol message — see the design
// spec's "The initiate endpoint is not a management feature".
type initiateRequestBody struct {
	ProviderID       string `json:"providerId"`
	OfferID          string `json:"offerId"`
	DatasetID        string `json:"datasetId"`
	ConnectorAddress string `json:"connectorAddress"`
}

// handleInitiate serves POST /negotiations/initiate. It responds 200 as
// soon as the negotiation is recorded and dispatches the actual outbound
// ContractRequestMessage in a goroutine — the same requirement as every
// other handler in this file, even though this endpoint is not itself a
// DSP message: net/http still will not put the 200 on the wire until this
// handler returns.
func (h negotiationHandler) handleInitiate(w http.ResponseWriter, r *http.Request) {
	var body initiateRequestBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes))
	if err := dec.Decode(&body); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "the request body is not a JSON object")
		return
	}
	if body.ProviderID == "" || body.OfferID == "" || body.DatasetID == "" || body.ConnectorAddress == "" {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"providerId, offerId, datasetId, and connectorAddress are all required")
		return
	}
	// The rejection reason is logged, not echoed. validateOutgoingCallback
	// reports which address a hostname resolved to, and this endpoint is
	// reachable by any roster participant — §24.2 called it "open to anonymous
	// callers", which stopped being true at §27, when every DSP route but the
	// version document went behind a participant credential; with require_auth
	// off it is open to anyone. Either way, returning that text would make it
	// a name-resolution oracle for the network this connector sits on. The
	// provider role's equivalent rejection (pushAndStore) logs for the same
	// reason.
	if err := validateOutgoingCallback(body.ConnectorAddress); err != nil {
		slog.Warn("reject initiate", "connector_address", body.ConnectorAddress, "error", err)
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"connectorAddress is not an address this connector will send to")
		return
	}
	// The roster check is the most general precondition here: the
	// required-fields check and the address guard above are each about a
	// specific fact this request must get right, while the roster check
	// only asks whether providerId names a participant at all. It runs
	// last, so a request with multiple mistakes is refused for its most
	// specific one — pinned by
	// TestHandleInitiateRefusesAnUnsendableAddressBeforeTheRosterCheck,
	// which sends a request that fails both the roster check and the
	// address guard and asserts the address guard's rejection is the one
	// returned.
	//
	// The rejected providerId is echoed in the response below.
	// validateOutgoingCallback's rejection above is not: it reports what
	// name resolution told this connector, information the caller does not
	// already have, so echoing it would turn this endpoint into a
	// name-resolution oracle. This check only repeats a string the caller
	// already sent, so echoing it costs nothing and helps an operator
	// debugging a typo see which name was refused.
	if h.knownParticipant != nil && !h.knownParticipant(body.ProviderID) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"providerId "+body.ProviderID+" is not a participant this connector's roster lists")
		return
	}

	consumerPID, err := store.NewUUID()
	if err != nil {
		slog.Error("generate consumer pid", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	now := time.Now()
	n := store.ConsumerNegotiation{
		ConsumerPID:     consumerPID,
		ProviderBaseURL: body.ConnectorAddress,
		// providerId is who the operator asked this connector to negotiate
		// with, and is therefore the audience of everything it will send.
		CounterpartyID: body.ProviderID,
		State:          StateRequested,
		DatasetID:      body.DatasetID,
		OfferID:        body.OfferID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := h.store.CreateConsumer(n); err != nil {
		slog.Error("create consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	go h.startNegotiation(n)
}

// startNegotiation sends the initial ContractRequestMessage and, once the
// provider's synchronous response reveals its providerPid, applies this
// connector's on_idle policy: wait (do nothing further) or abandon (an
// immediate termination through sendConsumerTermination's retrying path —
// see the design spec's "on_idle: abandon" policy row for why this must
// not be a bespoke one-shot send).
func (h negotiationHandler) startNegotiation(n store.ConsumerNegotiation) {
	msg := buildConsumerRequestMessage(n.ConsumerPID, n.DatasetID, n.OfferID, h.cfg.PublicURL+VersionPath)
	providerPID, err := sendInitialRequest(n.ProviderBaseURL, msg, n.CounterpartyID)
	if err != nil {
		slog.Error("send initial request", "consumer_pid", n.ConsumerPID, "error", err)
		return
	}
	if err := h.store.SetConsumerProviderPID(n.ConsumerPID, providerPID, time.Now()); err != nil {
		slog.Error("record provider pid", "consumer_pid", n.ConsumerPID, "error", err)
		return
	}
	n.ProviderPID = providerPID

	policy := resolvePolicy(h.cfg, n.DatasetID)
	if policy.OnIdle == "abandon" {
		sendConsumerTermination(n)
		if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateTerminated, time.Now()); err != nil {
			slog.Warn("drop stale consumer negotiation state update", "consumer_pid", n.ConsumerPID, "error", err)
		}
	}
}

// handleOffers serves POST /negotiations/{id}/offers, a ContractOfferMessage
// pushed by the provider. {id} is this connector's own consumer pid.
func (h negotiationHandler) handleOffers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, ok, err := h.store.GetConsumer(id)
	if err != nil {
		slog.Error("get consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !ok {
		writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+id)
		return
	}

	var msg OfferMessage
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, ContractNegotiationErrorType, msg.Context, msg.Type, ContractOfferMessageType) {
		return
	}
	if !offerLegalFrom(n.State) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"an offer is only valid from REQUESTED, negotiation is "+n.State)
		return
	}

	if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateOffered, time.Now()); err != nil {
		writeStateUpdateError(w, n.ConsumerPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateOffered
	// The offer's own content does not survive into the stored row, so
	// whether it carried a constraint is decided here, while the decoded
	// message is still in hand, and passed to the reaction.
	go h.reactToOffer(n, hasUnenforceableConstraint(msg.Offer.Permission))
}

// reactToOffer applies this connector's on_offer policy once an offer has
// been durably recorded as OFFERED. unenforceable reports whether the offer
// carried a constraint this connector cannot enforce — see
// decideOfferReaction for what that changes.
func (h negotiationHandler) reactToOffer(n store.ConsumerNegotiation, unenforceable bool) {
	n = h.resolveProviderPID(n)
	policy := resolvePolicy(h.cfg, n.DatasetID)
	action := decideOfferReaction(policy.OnOffer, unenforceable)
	if action != policy.OnOffer {
		slog.Info("rejecting an offer whose constraints this connector cannot enforce",
			"consumer_pid", n.ConsumerPID, "dataset_id", n.DatasetID, "configured_on_offer", policy.OnOffer)
	}
	switch action {
	case "accept":
		sendAcceptedEvent(n)
		if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateAccepted, time.Now()); err != nil {
			slog.Warn("drop stale consumer negotiation state update", "consumer_pid", n.ConsumerPID, "error", err)
		}
	case "reject":
		sendConsumerTermination(n)
		if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateTerminated, time.Now()); err != nil {
			slog.Warn("drop stale consumer negotiation state update", "consumer_pid", n.ConsumerPID, "error", err)
		}
	case "counter":
		sendCounterRequest(n)
	case "passive":
		// Take no action; the negotiation durably holds OFFERED.
	}
}

// resolveProviderPID re-reads n's row when the copy the handler took carries
// no provider pid yet, so a pid recorded in the meantime is still picked up.
//
// The window is real but narrow. startNegotiation writes the pid only once
// the initial request's synchronous response has returned
// (SetConsumerProviderPID), while the handler that spawned this reaction read
// its row from whatever was stored when the provider's push arrived. A push
// that wins that race leaves n.ProviderPID empty, and every outbound path
// template in negotiation_client.go formats it into the URL — producing
// {base}/negotiations//events, which 404s through all five §23.7 attempts
// while the reaction's own state write lands anyway. Re-reading closes it for
// the local-SQLite-write-versus-network-round-trip timings that actually
// occur; a push that beats the write outright still cannot be repaired here,
// which is why the remaining case is logged at error rather than passed over.
//
// Only the pid is adopted from the fresh row. n.State stays the caller's,
// because it is the precondition this reaction's compare-and-swap was decided
// against (§23.12) — refreshing it would let a stale reaction overwrite a
// state that moved on while it was deciding, which is the exact thing the
// compare-and-swap exists to prevent.
func (h negotiationHandler) resolveProviderPID(n store.ConsumerNegotiation) store.ConsumerNegotiation {
	if n.ProviderPID == "" {
		current, ok, err := h.store.GetConsumer(n.ConsumerPID)
		if err != nil {
			slog.Error("get consumer negotiation", "consumer_pid", n.ConsumerPID, "error", err)
		} else if ok {
			n.ProviderPID = current.ProviderPID
		}
	}
	if n.ProviderPID == "" {
		slog.Error("reacting to a push that arrived before the provider pid was recorded; "+
			"the outbound message will be addressed to a malformed url and cannot be delivered",
			"consumer_pid", n.ConsumerPID, "dataset_id", n.DatasetID)
	}
	return n
}

// handleAgreement serves POST /negotiations/{id}/agreement, a
// ContractAgreementMessage pushed by the provider.
func (h negotiationHandler) handleAgreement(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, ok, err := h.store.GetConsumer(id)
	if err != nil {
		slog.Error("get consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !ok {
		writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+id)
		return
	}

	var msg AgreementMessage
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, ContractNegotiationErrorType, msg.Context, msg.Type, ContractAgreementMessageType) {
		return
	}
	if !agreementLegalFrom(n.State) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"an agreement is only valid from REQUESTED or ACCEPTED, negotiation is "+n.State)
		return
	}

	// Record what was agreed to before recording that it was agreed. If this
	// fails the transition must fail too: a negotiation that reports AGREED
	// while losing the agreement itself is one POST /transfers/initiate will
	// later refuse to act on, and a contract that cannot be transferred under
	// is worse than one that visibly failed to conclude.
	//
	// No TCK test covers this path — the TP_C suite cites seeded agreements
	// and never negotiates one — so its only evidence is a unit test.
	if err := h.store.CreateAgreement(store.Agreement{
		AgreementID:    msg.Agreement.ID,
		DatasetID:      msg.Agreement.Target,
		ConsumerPID:    n.ConsumerPID,
		Origin:         store.OriginAgreed,
		CounterpartyID: n.CounterpartyID,
		CreatedAt:      time.Now(),
	}); err != nil {
		slog.Error("record accepted agreement",
			"consumer_pid", n.ConsumerPID, "agreement_id", msg.Agreement.ID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateAgreed, time.Now()); err != nil {
		writeStateUpdateError(w, n.ConsumerPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateAgreed
	// Same reasoning as handleOffers: the agreement's terms are not stored,
	// so the constraint question is answered here. wrongTarget is answered
	// here too, for the same reason: msg.Agreement.Target is what got
	// written to the agreements row above, but n.DatasetID — what this
	// connector itself requested — is what reactToAgreement's policy lookup
	// uses, and the two are never compared anywhere else.
	go h.reactToAgreement(n, hasUnenforceableConstraint(msg.Agreement.Permission), msg.Agreement.Target != n.DatasetID)
}

// reactToAgreement applies this connector's on_agreement policy. The verify
// branch's state write is gated on sendVerification's return value — see
// the design spec's "03-06 verification-ack rule": this connector must not
// report VERIFIED unless the provider actually acknowledged it.
// unenforceable reports whether the agreement carried a constraint this
// connector cannot enforce, and wrongTarget whether it named a dataset this
// connector did not request — see decideAgreementReaction for both.
func (h negotiationHandler) reactToAgreement(n store.ConsumerNegotiation, unenforceable, wrongTarget bool) {
	n = h.resolveProviderPID(n)
	policy := resolvePolicy(h.cfg, n.DatasetID)
	action := decideAgreementReaction(policy.OnAgreement, unenforceable, wrongTarget)
	if action != policy.OnAgreement {
		slog.Info("rejecting an agreement whose terms this connector cannot adopt",
			"consumer_pid", n.ConsumerPID, "dataset_id", n.DatasetID, "configured_on_agreement", policy.OnAgreement,
			"unenforceable_constraint", unenforceable, "wrong_target", wrongTarget)
	}
	switch action {
	case "verify":
		if !sendVerification(n) {
			return
		}
		if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateVerified, time.Now()); err != nil {
			slog.Warn("drop stale consumer negotiation state update", "consumer_pid", n.ConsumerPID, "error", err)
		}
	case "reject":
		sendConsumerTermination(n)
		if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateTerminated, time.Now()); err != nil {
			slog.Warn("drop stale consumer negotiation state update", "consumer_pid", n.ConsumerPID, "error", err)
		}
	}
}

// handleConsumerFinalizedEvent is handleEvent's consumer-role branch: the
// FINALIZED event a provider sends once this connector's verification is
// acknowledged. Legal only from VERIFIED — see finalizedEventLegalFrom.
func (h negotiationHandler) handleConsumerFinalizedEvent(w http.ResponseWriter, r *http.Request, n store.ConsumerNegotiation) {
	var msg struct {
		Context   []string `json:"@context"`
		Type      string   `json:"@type"`
		EventType string   `json:"eventType"`
	}
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, ContractNegotiationErrorType, msg.Context, msg.Type, ContractNegotiationEventMessageType) {
		return
	}
	if msg.EventType != eventTypeFinalized {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "eventType must be FINALIZED")
		return
	}
	if !finalizedEventLegalFrom(n.State) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"finalized is only valid from VERIFIED, negotiation is "+n.State)
		return
	}

	if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateFinalized, time.Now()); err != nil {
		writeStateUpdateError(w, n.ConsumerPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleConsumerTermination is handleTermination's consumer-role branch.
func (h negotiationHandler) handleConsumerTermination(w http.ResponseWriter, r *http.Request, n store.ConsumerNegotiation) {
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	var msg envelope
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, ContractNegotiationErrorType, msg.Context, msg.Type, ContractNegotiationTerminationMessageType) {
		return
	}
	if n.State == StateFinalized || n.State == StateTerminated {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"negotiation cannot be terminated from "+n.State)
		return
	}

	if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateTerminated, time.Now()); err != nil {
		writeStateUpdateError(w, n.ConsumerPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
