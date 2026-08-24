package dsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// The callback path suffixes, each appended — formatted with the consumer pid
// — to a transfer's stored callback address, the transfer protocol's
// counterpart to negotiation_handler.go's offerCallbackPath and friends. The
// address they are appended to is used exactly as it arrived: the
// counterparty's callback endpoint matches the whole request path against a
// pattern it built from that same string, so normalising it here — adding a
// prefix, adding or dropping a slash — turns a correct push into a 404.
const (
	transferStartCallbackPath       = "/transfers/%s/start"
	transferSuspensionCallbackPath  = "/transfers/%s/suspension"
	transferCompletionCallbackPath  = "/transfers/%s/completion"
	transferTerminationCallbackPath = "/transfers/%s/termination"
)

// transferStepDelay is how long driveTransfer waits between the steps of a
// configured sequence. It is not a robustness nicety: the counterparty
// registers its handler for step N+1 only once step N has arrived and
// released its latch — its own expectTerminationMessage appends a pipeline
// stage rather than pre-registering everything — so two messages pushed back
// to back can hit a path that has no handler yet, which its callback endpoint
// answers 404. The TCK's own reference actions sleep for the same reason.
//
// pushCallback's retry schedule is a second line of defence rather than the
// first: a 404 from an unregistered path would be retried and might then
// land, but a design that depends on a retry to be correct is one that fails
// on a slower machine.
//
// A var, not a const, so tests can shorten it — the same swappable-for-tests
// pattern as negotiation_handler.go's terminateAfterOfferDelay. It is the
// default source for transferHandler.stepDelay and is read at construction,
// never inside the driver's loop: the package var is written once in TestMain
// (see callback_test.go), and a test that needs a *different* delay sets the
// field on its own handler value instead.
var transferStepDelay = 200 * time.Millisecond

// transferHandler serves the transfer process protocol in the provider role.
// It lives in its own file because negotiation_handler.go was split at 867
// lines in the previous milestone specifically so this protocol would not
// grow it further.
//
// cfg is held for the same reason negotiationHandler holds it — the handlers
// are constructed from configuration in router.go — and driveTransfer reads
// the transfer policies out of it.
//
// stepDelay is transferStepDelay, copied in at construction. It is a field
// rather than a direct read of the var because the handler is a value copied
// into the driver goroutine: a test can give its own handler a longer delay
// and measure the gap between pushes, with no shared mutable state to race
// and no cost to any other test. Without it, the package var is shortened
// once for the whole suite and nothing can raise it, so a regression that
// dropped the pause between steps would pass every test and fail only
// against the real counterparty.
type transferHandler struct {
	cfg       config.Config
	store     *store.Store
	stepDelay time.Duration
	// knownParticipant reports whether an id is one the roster lists. It is
	// how an initiate call's providerId is checked without this handler
	// holding a roster.
	//
	// Nil disables the check rather than refusing everyone, and that
	// direction is the whole point: the roster is loaded only when
	// authentication is on, so with it off there is nothing to consult, and
	// a disabled check is absent rather than silently false. The same
	// convention pulling uses below.
	knownParticipant func(string) bool
	// pulling tracks in-flight pullTransferData calls by ConsumerPID, so a
	// restart that arrives while a previous pull for the same transfer is
	// still running is dropped instead of racing it onto the same
	// deterministic partial file. nil disables the guard rather than
	// panicking — every construction site that does not set this field
	// (most existing tests, which never exercise pullTransferData) is
	// unaffected.
	pulling *sync.Map
	// pulls counts in-flight pullTransferData goroutines so the connector
	// can wait for them at shutdown (DECISIONS.md section 33.6).
	//
	// It is last-act protection now, which it was not when it was written: a
	// pull records its outcome from a deferred write that runs after the
	// copy, so a pull that never reaches that defer leaves the row still
	// describing a previous attempt. The wait holds run()'s deferred
	// st.Close() open for it. What makes the wait short enough to be worth
	// bounding is pullCtx below — without the cancellation, a pull caught
	// mid-copy runs the whole cap out and is abandoned unwritten anyway.
	pulls *sync.WaitGroup
	// pullCtx is the connector's lifetime. A pull derives its own cancellable
	// context from this one, so shutdown can end an in-flight copy rather
	// than wait out its cap and abandon it — which, now that a pull records
	// its outcome after the copy, would lose exactly the row the wait exists
	// to protect. Nil in tests that do not exercise shutdown.
	pullCtx context.Context
}

// handleTransferRequest serves POST /transfers/request, the only entry point
// into a new transfer. Like the negotiation protocol's own request endpoint,
// the synchronous response is a plain REQUESTED acknowledgment and the
// decision to start is pushed afterward.
//
// The acknowledgment must be a 2xx carrying the full TransferProcess
// document: the counterparty deserializes it and reads the providerPid out of
// it to address every subsequent request in this transfer. A 204, or a 2xx
// with an empty body, fails at its deserializer rather than at the protocol.
func (h transferHandler) handleTransferRequest(w http.ResponseWriter, r *http.Request) {
	var msg TransferRequestMessage
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, TransferErrorType, msg.Context, msg.Type, TransferRequestMessageType) {
		return
	}
	if msg.ConsumerPID == "" {
		writeError(w, TransferErrorType, http.StatusBadRequest, "consumerPid is required")
		return
	}
	if msg.AgreementID == "" {
		writeError(w, TransferErrorType, http.StatusBadRequest, "agreementId is required")
		return
	}
	// Any non-empty format is accepted. Nothing in this connector's provider
	// path reads a format back, and the counterparty treats it as a
	// configurable value, so a whitelist here could only reject a transfer
	// this connector would otherwise have served.
	if msg.Format == "" {
		writeError(w, TransferErrorType, http.StatusBadRequest, "format is required")
		return
	}
	if msg.CallbackAddress == "" {
		writeError(w, TransferErrorType, http.StatusBadRequest, "callbackAddress is required")
		return
	}

	// The agreement is resolved by its id and then checked twice below: it must
	// not be one this connector holds as consumer, and, when it names a
	// counterparty, that counterparty must be who sent this request
	// (DECISIONS.md section 32). What is still not required is the request's
	// consumerPid. An imported agreement deliberately carries no consumer pid
	// — the negotiation that produced it did not happen here
	// (store.Agreement's own doc comment) — so requiring it to match would
	// reject every imported agreement, which is exactly the case this endpoint
	// exists to serve. An unknown id is a 400: this connector does not start a
	// transfer under a contract it has no record of.
	a, ok, err := h.store.GetAgreement(msg.AgreementID)
	if err != nil {
		slog.Error("get agreement", "agreement_id", msg.AgreementID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if !ok {
		// Logged, not silent: the id is the only thing that distinguishes a
		// counterparty citing an agreement that was never made from a fixture
		// this connector was supposed to have been given and was not. Both
		// answer 400, and the connector log is where that difference is
		// readable at all.
		slog.Warn("refuse transfer request citing an agreement this connector has no record of",
			"agreement_id", msg.AgreementID)
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"no agreement with id "+msg.AgreementID)
		return
	}
	if !servableAsProvider(a) {
		slog.Warn("refuse transfer request citing an agreement this connector holds as consumer",
			"agreement_id", msg.AgreementID, "origin", a.Origin)
		writeError(w, TransferErrorType, http.StatusForbidden,
			"this connector is not the provider on that agreement")
		return
	}

	// The empty clause is what the exchange checks deliberately lack. An
	// imported agreement may legitimately have no owner — POST /agreements
	// takes it optionally, and the TCK seeds twelve without one — so an
	// unnamed owner has to keep meaning "not known" rather than "nobody". That
	// leaves imports without a named counterparty exactly as open as they were.
	//
	// Removing the a.CounterpartyID != "" clause is not a tightening, it is a
	// 15-test failure: test/tck/run.sh seeds those twelve through
	// POST /agreements with no counterpartyId, and every TP transfer request
	// cites one of them. Fifteen and not thirty, because this is the
	// provider-role intake — in TP_C this connector is the consumer and the
	// TCK never posts here. Thirty is the cost of a different mistake:
	// dropping the seeded rows entirely, or a deny-on-empty general enough to
	// also reach handleTransferInitiate's agreement gate, which is the one
	// TP_C goes through. Spelled out so the consequence does not have to be
	// rediscovered by deleting the clause.
	if issuer := issuerFrom(r); h.cfg.AuthRequired() && a.CounterpartyID != "" && issuer != a.CounterpartyID {
		slog.Warn("refuse transfer request citing an agreement with another participant",
			"agreement_id", msg.AgreementID, "issuer", issuer, "expected", a.CounterpartyID)
		writeError(w, TransferErrorType, http.StatusForbidden,
			"that agreement is not yours")
		return
	}

	// The provider pid is this connector's own identifier for the transfer.
	// It goes out in the acknowledgment below and comes back as the {id} of
	// every subsequent request about this transfer.
	//
	// store.NewUUID directly, not newMessageID, which is the same choice
	// handleContractRequest makes for the same reason: newMessageID discards
	// the generation error and degrades to "", which is a cosmetic omission
	// for a message @id and a corruption here — an empty pid would become the
	// stored primary key and the identifier the counterparty addresses, and
	// the next transfer would collide on it.
	providerPID, err := store.NewUUID()
	if err != nil {
		slog.Error("generate provider pid", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	now := time.Now()
	t := store.TransferProcess{
		// Same as the negotiation row: the verified issuer of the request
		// that created this transfer is who it is with.
		CounterpartyID:  issuerFrom(r),
		ProviderPID:     providerPID,
		ConsumerPID:     msg.ConsumerPID,
		AgreementID:     msg.AgreementID,
		State:           TransferRequested,
		CallbackAddress: msg.CallbackAddress,
		Format:          msg.Format,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := h.store.CreateTransfer(t); err != nil {
		slog.Error("create transfer", "provider_pid", t.ProviderPID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// The agreement id is logged because it is the only field of an accepted
	// transfer request that came from the counterparty's own configuration
	// rather than from this connector, and it is what selects the autonomous
	// sequence below. Against the TCK it is also the only way to see that the
	// harness fixture was read at all: a test whose agreement id is left
	// unconfigured falls back to a random UUID silently, which is
	// indistinguishable from a protocol fault everywhere except here.
	slog.Info("accepted transfer request", "provider_pid", t.ProviderPID,
		"consumer_pid", t.ConsumerPID, "agreement_id", t.AgreementID)

	writeJSON(w, http.StatusCreated, buildTransferProcessDoc(t))

	go h.driveTransfer(t)
}

// resolveTransferSequence resolves the autonomous sequence a provider-role
// transfer's agreement drives to. An agreement_id-keyed entry in
// cfg.TransferPolicies always wins when present. Failing that, datasetID's
// own TransferSequence is used if non-nil — the fallback DECISIONS.md
// section 25.7 named as missing: transfer_policies cannot be keyed by an
// agreement this connector negotiated itself, because that id does not
// exist until the negotiation that produces it is already under way, but
// the agreement's dataset_id is known regardless of how the agreement came
// to be (see hasSourceFor, which resolves it the same way). Neither
// matching leaves [STARTED], today's default.
func resolveTransferSequence(cfg config.Config, agreementID, datasetID string) []string {
	for _, p := range cfg.TransferPolicies {
		if p.AgreementID == agreementID {
			return p.Sequence
		}
	}
	for _, d := range cfg.Datasets {
		if d.ID == datasetID && d.TransferSequence != nil {
			return d.TransferSequence
		}
	}
	return []string{TransferStarted}
}

// resolveConsumerTransferPolicy returns the trigger state and the sequence
// configured for an agreement, and the defaults when nothing matches:
// STARTED, and no steps at all.
//
// The default sequence is empty, unlike the provider role's [STARTED].
// Eleven of the fifteen TP_C tests require this connector to stay silent
// after its initial request, so "no entry" has to mean "send nothing".
func resolveConsumerTransferPolicy(cfg config.Config, agreementID string) (string, []string) {
	for _, p := range cfg.ConsumerTransferPolicies {
		if p.AgreementID != agreementID {
			continue
		}
		after := p.After
		if after == "" {
			after = TransferStarted
		}
		return after, p.Sequence
	}
	return TransferStarted, nil
}

// driveTransfer walks the sequence this transfer's agreement resolves to,
// pushing the message for each state and then recording it. For the default
// sequence that is one step — push the TransferStartMessage that actually
// begins the transfer, then record STARTED — and the longer sequences are
// that same step repeated, not a second way of pushing a message.
//
// The connector drives itself here because the counterparty gives it nothing
// else to react to: after the request it sends no trigger and no control
// call, only polls GET /transfers/{id}. What the sequence should be is
// therefore configuration rather than judgement this connector has — see
// config.TransferPolicy and the design spec's "Autonomous provider behavior,
// keyed by agreement".
//
// It is always invoked with `go`, never inline, for the reason DECISIONS.md
// §23.8 gives and negotiation_handler.go's dispatch documents at length:
// net/http buffers a response this small and does not put it on the wire
// until the handler returns, so an inline push would keep the acknowledgment
// sitting in that buffer for the whole retry schedule — while the
// counterparty waits for it before it is ready to receive the push.
func (h transferHandler) driveTransfer(t store.TransferProcess) {
	datasetID := ""
	if a, ok, err := h.store.GetAgreement(t.AgreementID); err != nil {
		slog.Error("get agreement for transfer sequence", "agreement_id", t.AgreementID, "error", err)
	} else if ok && servableAsProvider(a) {
		datasetID = a.DatasetID
	}
	for _, state := range resolveTransferSequence(h.cfg, t.AgreementID, datasetID) {
		// Before every step, including the first. The first used to go out
		// immediately, and under load that lost a real race: on 2026-08-19,
		// with the demo's image builds competing for the machine, a start
		// pushed 22 ms after the acknowledgment was answered 409 by a
		// counterparty that had not finished recording that acknowledgment
		// yet, and then 404 as it moved on. TP:02-01 failed twice in a row
		// and passed again once the machine was quiet.
		//
		// The acknowledgment is written and sent before this goroutine runs,
		// but the counterparty's own processing of it is not something this
		// connector can observe. The same pause that separates later steps is
		// the cheapest thing that gives it room, and it costs one delay per
		// transfer.
		time.Sleep(h.stepDelay)
		if !h.pushTransferStep(t, state) {
			return
		}
		// The state this step just wrote is the precondition the next step's
		// write is made against, exactly as the first step's was REQUESTED.
		t.State = state
	}
}

// pushTransferStep pushes the message for one target state and then records
// that state, reporting whether the sequence may continue.
//
// The push goes through pushCallback, so it inherits §23.7's retry schedule.
// A retried push may be refused by a counterparty whose handler for it was
// single-shot and already consumed; that refusal is logged and otherwise
// ignored, which is the same best-effort treatment pushAndStore gives a
// failed negotiation push (§23.12 — the provider is authoritative, and a
// consumer can always recover via GET).
//
// Push first, then store, matching pushAndStore's ordering rule (§23.12): in
// DSP the provider does not become STARTED and then say so, it becomes
// STARTED by delivering the start message. The state write is conditional on
// the state this step began from, so a termination that arrived while the
// push was still retrying wins and this goroutine's write is dropped rather
// than resurrecting a dead transfer.
//
// A dropped write ends the sequence. A counterparty that moved the transfer
// on has taken it over, and the remaining steps would push messages for
// transitions this connector cannot make. Any other storage failure ends it
// too, for the same reason from the other direction: the state the next step
// would write against was never recorded.
//
// t.CallbackAddress came from an unauthenticated request body, so the
// constructed URL is validated before anything is sent — see
// validateCallbackURL's doc comment.
func (h transferHandler) pushTransferStep(t store.TransferProcess, to string) bool {
	path, msg, legalFrom, ok := h.transferStepMessage(t, to)
	if !ok {
		// Unreachable through configuration, which validates every state in a
		// sequence, so this catches a new state added to one side only.
		slog.Error("no transfer message for state", "provider_pid", t.ProviderPID, "want_state", to)
		return false
	}
	// The legality table is enforced against this connector's own configured
	// sequence, not only against the counterparty in applyTransition.
	// config.validate checks that each element names a known state and
	// nothing more — it cannot check the walk, because the walk depends on
	// where the previous step left the transfer — so `sequence: [COMPLETED]`,
	// `[STARTED, STARTED]`, and `[TERMINATED, STARTED]` all load cleanly and
	// would otherwise emit messages this same connector answers 400 to when
	// they arrive. That asymmetry is what CLAUDE.md's "never accept a
	// constraint that is not enforced" rules out, read from the side that is
	// easier to forget: a table enforced in one direction only is advisory.
	//
	// The refusal ends the whole sequence rather than skipping to the next
	// step. A sequence that has gone illegal has no meaningful remainder: the
	// steps after it were written against a state this transfer is now never
	// going to be in.
	if !legalFrom(t.State) {
		slog.Error("refuse illegal configured transfer step",
			"provider_pid", t.ProviderPID, "agreement_id", t.AgreementID,
			"state", t.State, "want_state", to)
		return false
	}
	callbackURL := t.CallbackAddress + fmt.Sprintf(path, t.ConsumerPID)
	if err := validateOutgoingCallback(callbackURL); err != nil {
		// Deliberately unlike pushAndStore, which logs the same rejection and
		// then advances the state anyway. There the cost is bounded — one
		// negotiation records one state it never announced, and §23.12's
		// "the provider is authoritative, the consumer can recover via GET"
		// covers it. Here it compounds: the callback address is the same
		// string for every step of the sequence, so one rejection means every
		// remaining push is rejected too, and advancing regardless would walk
		// the transfer REQUESTED -> STARTED -> ... -> COMPLETED with nothing
		// delivered, leaving GET /transfers/{id} reporting a lifecycle that
		// never happened. Phase B compounds it again by making state the
		// access-control mechanism for the data plane.
		slog.Error("reject callback push", "url", callbackURL, "error", err)
		return false
	}
	pushCallback(callbackURL, msg, t.CounterpartyID)
	if err := h.store.SetTransferState(t.ProviderPID, t.State, to, time.Now()); err != nil {
		if errors.Is(err, store.ErrStateChanged) {
			slog.Warn("drop stale transfer state update",
				"provider_pid", t.ProviderPID, "want_state", to, "error", err)
			return false
		}
		slog.Error("update transfer state", "provider_pid", t.ProviderPID, "error", err)
		return false
	}
	return true
}

// transferStepMessage returns the callback path and the message that move a
// transfer into the given state, the predicate saying which states that move
// is legal from, and whether such a step exists at all.
//
// The predicate is returned from here rather than looked up separately in
// pushTransferStep so that a message and its legality rule are chosen by one
// switch on one value: a fifth state cannot be given an outbound message and
// left without a rule. It is the same predicate the inbound endpoint for that
// message hands applyTransition, which is what makes "this connector emits
// only what it would accept" a single fact rather than two that have to be
// kept in step.
func (h transferHandler) transferStepMessage(t store.TransferProcess, to string) (string, any, func(string) bool, bool) {
	switch to {
	case TransferStarted:
		// The address rides along only when there is something behind it, so
		// a control-plane-only dataset keeps the Phase A message shape.
		if h.hasSourceFor(t.AgreementID) {
			return transferStartCallbackPath, buildTransferStartMessageWithData(t, h.cfg.PublicURL), startLegalFrom, true
		}
		return transferStartCallbackPath, buildTransferStartMessage(t), startLegalFrom, true
	case TransferSuspended:
		return transferSuspensionCallbackPath, buildTransferSuspensionMessage(t), suspensionLegalFrom, true
	case TransferCompleted:
		return transferCompletionCallbackPath, buildTransferCompletionMessage(t), completionLegalFrom, true
	case TransferTerminated:
		return transferTerminationCallbackPath, buildTransferTerminationMessage(t), terminationLegalFrom, true
	}
	return "", nil, nil, false
}

// handleGetTransfer serves GET /transfers/{id}. The counterparty polls this
// to confirm every state this connector reports, and aborts on the first
// response that is not a 2xx carrying a readable TransferProcess — there is
// no retry-until-ready, and no grace period for an endpoint that becomes
// correct a moment later. So the only non-2xx it can produce are an unknown
// id and a storage failure, both from lookup.
func (h transferHandler) handleGetTransfer(w http.ResponseWriter, r *http.Request) {
	t, ok := h.lookup(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, buildTransferProcessDoc(t.TransferProcess))
}

// The four endpoints that move a running transfer. Unlike their negotiation
// counterparts, which each carry rules of their own, these differ only in
// which message they accept, which states that message is legal from, and
// which state it lands in — so they share applyTransition rather than
// repeating it four times.
//
// Start is the one whose legality also depends on the row's role, so it
// passes nil and lets applyTransition ask inboundStartLegalFor.
func (h transferHandler) handleTransferStart(w http.ResponseWriter, r *http.Request) {
	h.applyTransition(w, r, TransferStartMessageType, nil, TransferStarted)
}

func (h transferHandler) handleTransferCompletion(w http.ResponseWriter, r *http.Request) {
	h.applyTransition(w, r, TransferCompletionMessageType, completionLegalFrom, TransferCompleted)
}

func (h transferHandler) handleTransferSuspension(w http.ResponseWriter, r *http.Request) {
	h.applyTransition(w, r, TransferSuspensionMessageType, suspensionLegalFrom, TransferSuspended)
}

func (h transferHandler) handleTransferTermination(w http.ResponseWriter, r *http.Request) {
	h.applyTransition(w, r, TransferTerminationMessageType, terminationLegalFrom, TransferTerminated)
}

// applyTransition serves one of the four state-changing endpoints: it
// resolves {id}, checks the envelope, checks that wantType is legal from the
// transfer's current state, and only then writes the new state.
//
// Every check runs before the write, and every rejection is a 400 — not a
// 404, which on a transfer that exists would be read as "this endpoint does
// not exist" and abort the exchange outright rather than being understood as
// the refusal it is. The counterparty asserts that a refused message left the
// state exactly as it was, which is why legality is decided before
// SetTransferState rather than after.
// A nil legalFrom means the rule depends on the row's role, which is true of
// exactly one message: see inboundStartLegalFor.
func (h transferHandler) applyTransition(w http.ResponseWriter, r *http.Request,
	wantType string, legalFrom func(string) bool, to string) {
	t, ok := h.lookup(w, r)
	if !ok {
		return
	}
	if legalFrom == nil {
		legalFrom = inboundStartLegalFor(t)
	}

	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	var msg transferEnvelope
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, TransferErrorType, msg.Context, msg.Type, wantType) {
		return
	}
	if !legalFrom(t.State) {
		writeError(w, TransferErrorType, http.StatusBadRequest,
			wantType+" is not valid from "+t.State)
		return
	}

	if err := h.setTransferState(t, t.State, to, time.Now()); err != nil {
		writeTransferStateUpdateError(w, t.id(), err)
		return
	}
	t.State = to
	writeJSON(w, http.StatusOK, buildTransferProcessDoc(t.TransferProcess))

	// After the response, never before: the counterparty's 200 must not wait
	// on this connector's own outbound calls. Provider-role rows are driven
	// from handleTransferRequest instead, at the point the transfer is
	// created.
	if t.Consumer {
		// A start carrying an address is this connector's cue to fetch. Read
		// from the body that was just accepted rather than re-decoded: the
		// address is only meaningful on the message that delivered it.
		if to == TransferStarted && msg.DataAddress != nil {
			// Add before the goroutine starts, never inside it: a Wait that
			// ran in the window between `go` and the goroutine's first line
			// would return with this pull unregistered, which is precisely
			// the lost store write the counter exists to prevent. Done stays
			// inside, so the pair is balanced by the wrapper rather than by
			// pullTransferData — which many tests call directly, and which
			// would decrement a counter nothing had incremented.
			if h.pulls != nil {
				h.pulls.Add(1)
			}
			go func() {
				if h.pulls != nil {
					defer h.pulls.Done()
				}
				h.pullTransferData(store.ConsumerTransfer{
					ConsumerPID:    t.ConsumerPID,
					ProviderPID:    t.ProviderPID,
					AgreementID:    t.AgreementID,
					CounterpartyID: t.CounterpartyID,
					State:          to,
					ExpectedBytes:  t.ExpectedBytes,
				}, msg.DataAddress)
			}()
		}
		h.maybeDriveConsumerTransfer(store.ConsumerTransfer{
			ConsumerPID:     t.ConsumerPID,
			ProviderPID:     t.ProviderPID,
			ProviderBaseURL: t.ProviderBaseURL,
			AgreementID:     t.AgreementID,
			CounterpartyID:  t.CounterpartyID,
			Format:          t.Format,
			State:           to,
		}, to)
	}
}

// resolvedTransfer is a transfer found by path id, in whichever role owns
// it. The embedded TransferProcess is the shape every existing helper
// already takes — buildTransferProcessDoc, the message builders — so a
// consumer-role row is projected into it rather than given a parallel set of
// functions.
type resolvedTransfer struct {
	store.TransferProcess
	// Consumer reports which table the row came from. It decides which
	// start-legality rule applies and which setter moves the row.
	Consumer bool
	// ProviderBaseURL is set for consumer-role rows only: the base every
	// message this connector sends as consumer is addressed against. The
	// provider role's equivalent is TransferProcess.CallbackAddress.
	ProviderBaseURL string
	// ExpectedBytes is set for consumer-role rows only. It reaches
	// pullTransferData through the struct the caller assembles rather than a
	// store read, so the pull's read path stays store-free and the tests
	// that call it directly keep working.
	ExpectedBytes int64
}

// id is the identifier this transfer's endpoints are addressed by, which is
// the provider pid in the provider role and this connector's own consumer
// pid in the consumer role. Error messages and log lines use it so they name
// the identifier the counterparty actually sent.
func (r resolvedTransfer) id() string {
	if r.Consumer {
		return r.ConsumerPID
	}
	return r.ProviderPID
}

// lookup resolves {id} to a stored transfer in either role, refuses a caller
// that is not party to it, and writes the appropriate error response,
// returning ok=false in either case.
//
// The consumer table is tried first. That is this function's own order and not
// a shared convention: the three negotiation handlers that dispatch on role —
// handleEvent, handleTermination, handleGetNegotiation — try the *provider*
// table first. Either order is correct, because both id spaces are
// independently generated UUIDs and a row can only be in one of them; this
// comment used to claim the order was shared, which it never was.
//
// Both branches now carry the refusal, and they carry it separately on
// purpose. resolvedTransfer carries CounterpartyID for consumer rows too, so
// a single comparison written against the value this function returns — or
// hoisted above the branch split — would apply the provider-role rule to
// consumer rows. That happens to be correct now, because only the operator
// can make an initiate call and it may only name a participant the roster
// lists, but it was catastrophically wrong before those checks existed: it
// would have refused every consumer-role transfer the TCK drives. The
// placement is deliberate, not incidental.
//
// {id} is a pid this connector generated itself — the provider pid it
// returned in the acknowledgment to POST /transfers/request, or the consumer
// pid it minted at the management listener's POST /transfers/initiate — so an
// id in neither table names a transfer that never existed, and 404 is the
// honest answer. It is also the only 404 this protocol produces.
func (h transferHandler) lookup(w http.ResponseWriter, r *http.Request) (resolvedTransfer, bool) {
	id := r.PathValue("id")

	c, ok, err := h.store.GetConsumerTransfer(id)
	if err != nil {
		slog.Error("get consumer transfer", "consumer_pid", id, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return resolvedTransfer{}, false
	}
	if ok {
		if refuseIfNotParty(w, r, TransferErrorType, c.CounterpartyID, h.cfg.AuthRequired()) {
			return resolvedTransfer{}, false
		}
		return resolvedTransfer{
			TransferProcess: store.TransferProcess{
				ProviderPID:    c.ProviderPID,
				CounterpartyID: c.CounterpartyID,
				ConsumerPID:    c.ConsumerPID,
				AgreementID:    c.AgreementID,
				State:          c.State,
				Format:         c.Format,
				CreatedAt:      c.CreatedAt,
				UpdatedAt:      c.UpdatedAt,
			},
			Consumer:        true,
			ProviderBaseURL: c.ProviderBaseURL,
			ExpectedBytes:   c.ExpectedBytes,
		}, true
	}

	t, ok, err := h.store.GetTransfer(id)
	if err != nil {
		slog.Error("get transfer", "provider_pid", id, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return resolvedTransfer{}, false
	}
	if !ok {
		writeError(w, TransferErrorType, http.StatusNotFound, "no transfer with id "+id)
		return resolvedTransfer{}, false
	}
	if refuseIfNotParty(w, r, TransferErrorType, t.CounterpartyID, h.cfg.AuthRequired()) {
		return resolvedTransfer{}, false
	}
	return resolvedTransfer{TransferProcess: t}, true
}

// inboundStartLegalFor returns the rule governing a TransferStartMessage
// this connector receives, which depends on who sent it. DSP 2025-1 gives
// the message a single permitted sender ("Sent by: Provider") and admits the
// consumer's copy only as a resume, so a start arriving from REQUESTED is
// legal when this connector is the consumer and illegal when it is the
// provider. The other three transfer messages need no such split: the spec
// names both parties in their Sent by rows.
func inboundStartLegalFor(r resolvedTransfer) func(string) bool {
	if r.Consumer {
		return startLegalFrom
	}
	return providerInboundStartLegalFrom
}

// setTransferState writes through whichever table owns the row.
func (h transferHandler) setTransferState(r resolvedTransfer, from, to string, at time.Time) error {
	if r.Consumer {
		return h.store.SetConsumerTransferState(r.ConsumerPID, from, to, at)
	}
	return h.store.SetTransferState(r.ProviderPID, from, to, at)
}

// writeTransferStateUpdateError is writeStateUpdateError's transfer-protocol
// counterpart, and rejects for the same reason: a lost race
// (store.ErrStateChanged) means another request moved the transfer on between
// this handler's read and its write, so the state precondition it checked no
// longer holds — the same 400 that check would have produced had it run a
// moment later. It is a separate function rather than a shared one because
// the error document names the protocol that produced it.
//
// The 500 below is unreachable through any caller that exists: applyTransition
// is the only one, it resolves the transfer through lookup first, and nothing
// in this connector deletes a transfer row — so store.ErrNotFound cannot come
// back from a SetTransferState made against a row this request just read. It
// stays as the honest answer for a storage failure, and would become reachable
// the day a delete path is added.
func writeTransferStateUpdateError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, store.ErrStateChanged) {
		slog.Warn("transfer changed concurrently", "transfer_id", id, "error", err)
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"transfer "+id+" changed while this request was being handled")
		return
	}
	slog.Error("update transfer state", "transfer_id", id, "error", err)
	w.WriteHeader(http.StatusInternalServerError)
}

// servableAsProvider reports whether this connector may serve data as the
// provider under a. OriginAgreed means the opposite of what a transfer
// request asks for: this connector accepted that agreement as the *consumer*,
// from a counterparty's ContractAgreementMessage, so serving bytes under it
// would be role confusion regardless of who asked.
//
// It is also the only exit a forged agreement has. handleAgreement writes the
// message body verbatim and is the sole writer of OriginAgreed, so a peer that
// initiates a negotiation naming itself as provider can mint a row — but it
// cannot reach OriginImported, which is behind the management token on a
// localhost listener, or OriginNegotiated, whose id this connector generates.
// The forged row itself is not detectably forged: it is exactly the message an
// honest provider sends in a negotiation the peer legitimately owns.
func servableAsProvider(a store.Agreement) bool {
	return a.Origin != store.OriginAgreed
}

// hasSourceFor reports whether the dataset behind an agreement has bytes
// configured. It is the same resolution dataHandler does, and it is asked
// here so a start message advertises an address only when a pull would
// actually succeed.
func (h transferHandler) hasSourceFor(agreementID string) bool {
	a, ok, err := h.store.GetAgreement(agreementID)
	if err != nil || !ok {
		return false
	}
	if !servableAsProvider(a) {
		return false
	}
	for _, d := range h.cfg.Datasets {
		if d.ID == a.DatasetID && d.SourceFile != "" {
			return true
		}
	}
	return false
}

// transferEnvelope is the envelope plus the one field a transfer message can
// carry that changes what this connector does next. Only start messages have
// a dataAddress; on every other message the field is simply absent, which is
// why one type serves all four endpoints.
type transferEnvelope struct {
	Context     []string     `json:"@context"`
	Type        string       `json:"@type"`
	DataAddress *DataAddress `json:"dataAddress,omitempty"`
}
