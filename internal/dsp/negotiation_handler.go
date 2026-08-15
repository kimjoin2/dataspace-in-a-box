package dsp

import (
	"encoding/json"
	"errors"
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

// validateOutgoingCallback is validateCallbackURL, held as a var (same
// swappable-for-tests pattern as terminateAfterOfferDelay above). Tests in
// this file exercise negotiation dispatch/state-machine behavior by pointing
// callback addresses at an httptest.Server, which is always bound to
// loopback — exactly what validateCallbackURL exists to reject in
// production. Those tests replace this var with a permissive stub; the
// filter itself gets its own direct table test in callback_test.go.
var validateOutgoingCallback = validateCallbackURL

// Callback path suffixes, appended (with the provider pid) to a
// negotiation's stored callback address.
const (
	offerCallbackPath       = "/negotiations/%s/offers"
	agreementCallbackPath   = "/negotiations/%s/agreement"
	eventCallbackPath       = "/negotiations/%s/events"
	terminationCallbackPath = "/negotiations/%s/termination"
)

// negotiationHandler serves the contract negotiation protocol, both roles.
// Which role a given {id} belongs to is resolved by which store table it is
// found in — see handleEvent's doc comment.
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
	if !checkEnvelope(w, msg.Context, msg.Type, ContractRequestMessageType) {
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
	go h.dispatch(n, outcome)
}

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
	if err := validateOutgoingCallback(body.ConnectorAddress); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "connectorAddress: "+err.Error())
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
		State:           StateRequested,
		DatasetID:       body.DatasetID,
		OfferID:         body.OfferID,
		CreatedAt:       now,
		UpdatedAt:       now,
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
	providerPID, err := sendInitialRequest(n.ProviderBaseURL, msg)
	if err != nil {
		slog.Error("send initial request", "consumer_pid", n.ConsumerPID, "error", err)
		return
	}
	if err := h.store.SetConsumerProviderPID(n.ConsumerPID, providerPID); err != nil {
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
	if !checkEnvelope(w, msg.Context, msg.Type, ContractOfferMessageType) {
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
	go h.reactToOffer(n)
}

// reactToOffer applies this connector's on_offer policy once an offer has
// been durably recorded as OFFERED.
func (h negotiationHandler) reactToOffer(n store.ConsumerNegotiation) {
	policy := resolvePolicy(h.cfg, n.DatasetID)
	switch policy.OnOffer {
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
	if !checkEnvelope(w, msg.Context, msg.Type, ContractAgreementMessageType) {
		return
	}
	if !agreementLegalFrom(n.State) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"an agreement is only valid from REQUESTED or ACCEPTED, negotiation is "+n.State)
		return
	}

	if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateAgreed, time.Now()); err != nil {
		writeStateUpdateError(w, n.ConsumerPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateAgreed
	go h.reactToAgreement(n)
}

// reactToAgreement applies this connector's on_agreement policy. The verify
// branch's state write is gated on sendVerification's return value — see
// the design spec's "03-06 verification-ack rule": this connector must not
// report VERIFIED unless the provider actually acknowledged it.
func (h negotiationHandler) reactToAgreement(n store.ConsumerNegotiation) {
	policy := resolvePolicy(h.cfg, n.DatasetID)
	switch policy.OnAgreement {
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

// handleReRequest serves POST /negotiations/{id}/request: a consumer
// counter-offer or resend while the negotiation is OFFERED. A negotiation
// gets exactly one re-request while OFFERED — a second one, whatever it
// contains, is a synchronous rejection (CN:03-04's second call; tracked via
// store.Negotiation.Rerequested). Within that one allowed re-request:
// repeating the offer already on the table is accepted with no further
// action (CN:03-04's first call — the negotiation simply stays OFFERED);
// anything else is accepted too, but is a decision to walk away, since this
// connector has nothing else to offer for this negotiation — terminated
// asynchronously (CN:01-02).
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
	if !checkEnvelope(w, msg.Context, msg.Type, ContractRequestMessageType) {
		return
	}
	if n.State != StateOffered {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"a counter-offer is only valid from OFFERED, negotiation is "+n.State)
		return
	}
	// The "one re-request" rule is enforced by the conditional update rather
	// than by reading n.Rerequested first: two re-requests arriving together
	// would both read the flag clear, and only the update can decide which
	// one actually got it. A lost race is the rejection, not an error.
	if err := h.store.SetRerequested(n.ProviderPID); err != nil {
		if errors.Is(err, store.ErrStateChanged) {
			writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
				"this negotiation already received its one re-request while OFFERED")
			return
		}
		slog.Error("update negotiation state", "provider_pid", n.ProviderPID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	// An absent offer.@id is not rejected: it cannot match the offer on the
	// table, so it takes the mismatch path below and the negotiation is
	// terminated — the same answer a wrong identifier gets, which is the
	// right one for a counter-offer this connector cannot satisfy.
	if !decideReRequestMatches(n.OfferID, msg.Offer.ID) {
		go h.pushAndStore(n, StateTerminated, terminationCallbackPath, buildTerminationMessage(n))
	}
}

// handleEvent serves POST /negotiations/{id}/events. {id} names either a
// provider-role negotiation (the consumer's ACCEPTED event) or a
// consumer-role one (the provider's FINALIZED event) — the two suites
// register the identical path shape, which Go's ServeMux would reject as a
// duplicate pattern if this milestone tried to register a second route for
// it, so this dispatches on which table {id} is actually found in.
func (h negotiationHandler) handleEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if n, ok, err := h.store.Get(id); err != nil {
		slog.Error("get negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		h.handleProviderAcceptedEvent(w, r, n)
		return
	}
	if cn, ok, err := h.store.GetConsumer(id); err != nil {
		slog.Error("get consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		h.handleConsumerFinalizedEvent(w, r, cn)
		return
	}
	writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+id)
}

// handleProviderAcceptedEvent is handleEvent's provider-role branch —
// unchanged behavior from before this milestone.
func (h negotiationHandler) handleProviderAcceptedEvent(w http.ResponseWriter, r *http.Request, n store.Negotiation) {
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
	if !checkEnvelope(w, msg.Context, msg.Type, ContractNegotiationEventMessageType) {
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
	if err := h.store.SetState(n.ProviderPID, n.State, StateAccepted, now); err != nil {
		writeStateUpdateError(w, n.ProviderPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateAccepted
	outcome := decideAccept(h.cfg, n.DatasetID, now)
	go h.dispatch(n, outcome)
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
	if !checkEnvelope(w, msg.Context, msg.Type, ContractNegotiationEventMessageType) {
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
	var msg envelope
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, msg.Context, msg.Type, ContractAgreementVerificationMessageType) {
		return
	}
	if n.State != StateAgreed {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"verification is only valid from AGREED, negotiation is "+n.State)
		return
	}

	if err := h.store.SetState(n.ProviderPID, n.State, StateVerified, time.Now()); err != nil {
		writeStateUpdateError(w, n.ProviderPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateVerified
	go h.pushAndStore(n, StateFinalized, eventCallbackPath, buildFinalizedEventMessage(n))
}

// handleTermination serves POST /negotiations/{id}/termination, from
// either party and, after this milestone, for either role — see
// handleEvent's doc comment for why this dispatches rather than registering
// a second route.
func (h negotiationHandler) handleTermination(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if n, ok, err := h.store.Get(id); err != nil {
		slog.Error("get negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		h.handleProviderTermination(w, r, n)
		return
	}
	if cn, ok, err := h.store.GetConsumer(id); err != nil {
		slog.Error("get consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		h.handleConsumerTermination(w, r, cn)
		return
	}
	writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+id)
}

// handleProviderTermination is handleTermination's provider-role branch —
// unchanged behavior from before this milestone. It is rejected from
// FINALIZED (CN:03-01) and from an already TERMINATED negotiation.
func (h negotiationHandler) handleProviderTermination(w http.ResponseWriter, r *http.Request, n store.Negotiation) {
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	var msg envelope
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, msg.Context, msg.Type, ContractNegotiationTerminationMessageType) {
		return
	}
	if n.State == StateFinalized || n.State == StateTerminated {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"negotiation cannot be terminated from "+n.State)
		return
	}

	if err := h.store.SetState(n.ProviderPID, n.State, StateTerminated, time.Now()); err != nil {
		writeStateUpdateError(w, n.ProviderPID, err)
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
	if !checkEnvelope(w, msg.Context, msg.Type, ContractNegotiationTerminationMessageType) {
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

// handleGetNegotiation serves GET /negotiations/{id}, for either role — see
// handleEvent's doc comment for why this dispatches rather than registering
// a second route.
func (h negotiationHandler) handleGetNegotiation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if n, ok, err := h.store.Get(id); err != nil {
		slog.Error("get negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		writeJSON(w, http.StatusOK, buildNegotiationStateDocument(n))
		return
	}
	if cn, ok, err := h.store.GetConsumer(id); err != nil {
		slog.Error("get consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		writeJSON(w, http.StatusOK, buildConsumerNegotiationStateDocument(cn))
		return
	}
	writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+id)
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

// envelope is the JSON-LD envelope shared by every negotiation message, for
// the two handlers that read nothing else from the body. Only the fields
// this connector inspects are declared, the same reason RequestMessage
// declares so few — DECISIONS.md section 22.5.
type envelope struct {
	Context []string `json:"@context"`
	Type    string   `json:"@type"`
}

// checkEnvelope validates that envelope, writing the error response and
// returning false if it does not hold. Every negotiation handler runs this
// on the body it decoded: a direct field check, not a schema library, per
// CLAUDE.md's JSON-LD convention and DECISIONS.md section 22.5.
func checkEnvelope(w http.ResponseWriter, context []string, gotType, wantType string) bool {
	if !slices.Contains(context, ContextURL) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "@context must contain "+ContextURL)
		return false
	}
	if gotType != wantType {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "@type must be "+wantType)
		return false
	}
	return true
}

// writeStateUpdateError responds to a failed conditional state update. A
// lost race (store.ErrStateChanged) is not a server fault: it means another
// request moved the negotiation on between the handler's read and its write,
// so the state precondition that handler checked no longer holds — the same
// 400 that check would have produced had it run a moment later. Anything
// else is a real storage failure.
func writeStateUpdateError(w http.ResponseWriter, providerPID string, err error) {
	if errors.Is(err, store.ErrStateChanged) {
		slog.Warn("negotiation changed concurrently", "provider_pid", providerPID, "error", err)
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"negotiation "+providerPID+" changed while this request was being handled")
		return
	}
	slog.Error("update negotiation state", "provider_pid", providerPID, "error", err)
	w.WriteHeader(http.StatusInternalServerError)
}

// dispatch carries out a routing decision: it pushes whatever outcome
// requires to n's callback address and persists the resulting state.
//
// Every caller invokes this with `go`, never inline — dispatch (via
// pushAndStore/pushCallback) can block for the length of the whole retry
// schedule, and the handler goroutine calling it has already written the
// synchronous response by this point. That distinction matters more than it
// looks: net/http buffers a response under ~2KB (all of this project's
// responses) and does not put it on the wire until the handler function
// returns, so a handler that calls dispatch inline never actually finishes
// sending its own synchronous response until the push either succeeds or
// exhausts its retries. The real TCK caught this directly — its client
// timed out waiting for the synchronous response itself, with pushes
// racing a consumer that could not have been ready yet because the
// response that would tell it to get ready was the thing stuck behind the
// push. `go dispatch(...)` breaks that: the handler returns immediately
// after writing its response, the response goes out, and the push happens
// genuinely afterward instead of nested inside the same goroutine.
func (h negotiationHandler) dispatch(n store.Negotiation, outcome negotiationOutcome) {
	switch {
	case outcome.pushOffer && outcome.pushTermination:
		h.pushAndStore(n, StateOffered, offerCallbackPath, buildOfferMessage(n))
		go h.delayedTerminate(n)
	case outcome.pushOffer:
		h.pushAndStore(n, StateOffered, offerCallbackPath, buildOfferMessage(n))
	case outcome.pushAgreement:
		h.pushAndStore(n, StateAgreed, agreementCallbackPath, buildAgreementMessage(n, h.cfg.PublicURL, h.cfg.ParticipantID))
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
// (formatted with the consumer pid) and then updates the stored state. Push
// failure does not block the state update: the provider is authoritative,
// and a consumer can always recover via GET.
//
// The order is push-then-store, and it is load-bearing — storing first was
// tried and the real TCK rejected it. In DSP the provider does not *become*
// AGREED and then tell the consumer; it becomes AGREED *by* delivering the
// agreement. CN:03-03 asserts exactly that: the consumer accepts, then
// verifies about 100ms later without having received the agreement, and the
// provider must reject it. Storing first makes the negotiation AGREED within
// a millisecond of the accept, so that verification becomes legal and the
// test fails. Pushing first keeps the negotiation in its old state for as
// long as the delivery is still being attempted, which is the same window in
// which the consumer has not been told — so "not AGREED yet" and "the
// consumer does not have the agreement yet" stay the same fact, and the
// state check in handleVerification is a real guard rather than a formality.
// The cost is the converse: GET /negotiations/{id} can report a state one
// transition behind a push that has already landed. §23.12.
//
// The state write is conditional on n.State, the state this call's decision
// was made against (see store.SetState). If it no longer holds, another
// request moved the negotiation on — most likely a termination that arrived
// while this push was retrying — and this goroutine's write is stale, so it
// is dropped rather than allowed to overwrite the newer state.
//
// The path uses n.ConsumerPID, not n.ProviderPID: the design spec confirms
// {id} is the provider's own generated pid only for the synchronous calls a
// consumer makes *to* the provider (`ProviderNegotiationPipelineImpl`) —
// for the async pushes going the other way, the consumer's own callback
// endpoint (`HttpConsumerNegotiationClientImpl`) looks the negotiation up
// by the pid it assigned itself. Confirmed against the real TCK: the
// provider pid produced a 404 on every push.
//
// n.CallbackAddress came from an unauthenticated request body, so the
// constructed URL is validated before anything is sent — see
// validateCallbackURL's doc comment for why a request whose callbackAddress
// resolves to this connector's own loopback network cannot be allowed
// through. A rejection is logged and the push is skipped, matching
// pushCallback's own best-effort, no-error-returned contract.
func (h negotiationHandler) pushAndStore(n store.Negotiation, state, path string, msg any) {
	callbackURL := n.CallbackAddress + fmt.Sprintf(path, n.ConsumerPID)
	if err := validateOutgoingCallback(callbackURL); err != nil {
		slog.Error("reject callback push", "url", callbackURL, "error", err)
	} else {
		pushCallback(callbackURL, msg)
	}
	if err := h.store.SetState(n.ProviderPID, n.State, state, time.Now()); err != nil {
		if errors.Is(err, store.ErrStateChanged) {
			slog.Warn("drop stale negotiation state update",
				"provider_pid", n.ProviderPID, "want_state", state, "error", err)
			return
		}
		slog.Error("update negotiation state", "provider_pid", n.ProviderPID, "error", err)
	}
}
