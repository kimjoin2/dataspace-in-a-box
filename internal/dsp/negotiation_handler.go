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
	if !checkEnvelope(w, ContractNegotiationErrorType, msg.Context, msg.Type, ContractRequestMessageType) {
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
		// The row exists because this party made an authenticated request,
		// which makes the verified issuer the only honest record of who it is
		// with. Empty when authentication is off.
		CounterpartyID:  issuerFrom(r),
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
	if !checkEnvelope(w, ContractNegotiationErrorType, msg.Context, msg.Type, ContractRequestMessageType) {
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
	if !checkEnvelope(w, ContractNegotiationErrorType, msg.Context, msg.Type, ContractNegotiationEventMessageType) {
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
	if !checkEnvelope(w, ContractNegotiationErrorType, msg.Context, msg.Type, ContractAgreementVerificationMessageType) {
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
	if !checkEnvelope(w, ContractNegotiationErrorType, msg.Context, msg.Type, ContractNegotiationTerminationMessageType) {
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

// lookup resolves {id} to a stored *provider-role* negotiation, writing the
// appropriate error response and returning ok=false if it cannot.
//
// After this milestone's role-dispatch routing, only handleReRequest and
// handleVerification use it. Those two serve the messages DSP sends
// consumer-to-provider, so their {id} can be nothing but a pid this connector
// generated as provider. The three handlers that serve both roles cannot use
// it — handleEvent, handleTermination, and handleGetNegotiation must try the
// consumer table before concluding {id} is unknown, so each does its own
// two-table lookup and dispatches on which one answered (see handleEvent's
// doc comment). handleOffers and handleAgreement are consumer-role only and
// call GetConsumer directly.
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
// returning false if it does not hold. Every negotiation and transfer handler
// runs this on the body it decoded: a direct field check, not a schema
// library, per CLAUDE.md's JSON-LD convention and DECISIONS.md section 22.5.
//
// dspType names the protocol whose error document to emit —
// ContractNegotiationErrorType or TransferErrorType. It is a parameter rather
// than a constant for the same reason writeError takes one: every node this
// connector emits carries a @type, and a rejection labelled with a protocol
// that did not produce it is worse than an unlabelled one, because a wrong
// label survives being read.
func checkEnvelope(w http.ResponseWriter, dspType string, context []string, gotType, wantType string) bool {
	if !slices.Contains(context, ContextURL) {
		writeError(w, dspType, http.StatusBadRequest, "@context must contain "+ContextURL)
		return false
	}
	if gotType != wantType {
		writeError(w, dspType, http.StatusBadRequest, "@type must be "+wantType)
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
//
// negotiationPID is whichever pid identifies the negotiation in the table the
// caller writes to: the provider pid from a provider-role handler, the
// consumer pid from a consumer-role one. The log key is role-neutral for that
// reason — naming it provider_pid mislabelled every consumer-role conflict.
func writeStateUpdateError(w http.ResponseWriter, negotiationPID string, err error) {
	if errors.Is(err, store.ErrStateChanged) {
		slog.Warn("negotiation changed concurrently", "negotiation_pid", negotiationPID, "error", err)
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"negotiation "+negotiationPID+" changed while this request was being handled")
		return
	}
	slog.Error("update negotiation state", "negotiation_pid", negotiationPID, "error", err)
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

		// pushAndStore is void and swallows store.ErrStateChanged, so the
		// transition above may have been dropped in favour of a newer state —
		// a termination that arrived while the push was still retrying, for
		// instance. Re-read before recording: an agreement row is what the
		// transfer protocol treats as proof a contract exists, so it must
		// follow the state that actually landed, not the one this branch
		// intended. A termination arriving between this Get and the INSERT
		// below is a residual race, accepted rather than closed here — closing
		// it needs one transaction spanning the state write and the agreement
		// insert, which is a larger change than this call site should make.
		//
		// AGREED, VERIFIED, and FINALIZED are the only states reachable
		// through a committed AGREED: handleVerification refuses to move a
		// negotiation to VERIFIED unless n.State == StateAgreed already, and
		// FINALIZED is reached only from VERIFIED. So observing any of the
		// three is proof the SetState(AGREED) above actually landed — even
		// under Store's single connection (SetMaxOpenConns(1)), where a
		// concurrent verification can win the connection and carry the
		// negotiation past AGREED before this Get gets its turn. That is
		// evidence the transition happened, not a reason to doubt it.
		// TERMINATED stays excluded because it is genuinely ambiguous: it
		// means either the AGREED write above never landed, or the
		// negotiation became AGREED and was terminated afterward — and a
		// transfer must not proceed against a dead negotiation either way,
		// so both readings agree on skipping the record.
		current, ok, err := h.store.Get(n.ProviderPID)
		if err != nil {
			slog.Error("re-read negotiation before recording agreement", "provider_pid", n.ProviderPID, "error", err)
			return
		}
		agreementWasIssued := current.State == StateAgreed || current.State == StateVerified || current.State == StateFinalized
		if !ok || !agreementWasIssued {
			slog.Warn("negotiation state does not prove its agreement was issued",
				"provider_pid", n.ProviderPID, "current_state", current.State)
			return
		}

		// The agreement this connector just issued becomes a durable record, so
		// the transfer protocol can answer "does this agreement exist" without
		// scanning negotiations. The id is the one buildAgreementMessage puts on
		// the wire: this negotiation's provider pid.
		if err := h.store.CreateAgreement(store.Agreement{
			AgreementID: n.ProviderPID,
			DatasetID:   n.DatasetID,
			ConsumerPID: n.ConsumerPID,
			Origin:      store.OriginNegotiated,
			CreatedAt:   time.Now().UTC(),
		}); err != nil {
			// Log and continue. The agreement has already been announced to the
			// counterparty, so refusing to advance here would leave the two sides
			// disagreeing about a contract that was in fact made.
			slog.Error("record agreement", "provider_pid", n.ProviderPID, "error", err)
		}
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
// through. A rejection is logged and the push is skipped; the state write
// below still runs, which is the same best-effort treatment pushAndStore
// gives a push that was attempted and failed — it discards pushCallback's
// bool return for the reason §23.12 gives, that the provider is
// authoritative and a consumer can always recover via GET.
func (h negotiationHandler) pushAndStore(n store.Negotiation, state, path string, msg any) {
	callbackURL := n.CallbackAddress + fmt.Sprintf(path, n.ConsumerPID)
	if err := validateOutgoingCallback(callbackURL); err != nil {
		slog.Error("reject callback push", "url", callbackURL, "error", err)
	} else {
		pushCallback(callbackURL, msg, n.CounterpartyID)
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
