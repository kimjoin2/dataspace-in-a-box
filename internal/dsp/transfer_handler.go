package dsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

	// The agreement is looked up by its id alone. An imported agreement
	// deliberately carries no consumer pid — the negotiation that produced it
	// did not happen here (store.Agreement's own doc comment) — so also
	// requiring the request's consumerPid to match would reject every
	// imported agreement, which is exactly the case this endpoint exists to
	// serve. An unknown id is a 400: this connector does not start a transfer
	// under a contract it has no record of.
	if _, ok, err := h.store.GetAgreement(msg.AgreementID); err != nil {
		slog.Error("get agreement", "agreement_id", msg.AgreementID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if !ok {
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"no agreement with id "+msg.AgreementID)
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

// resolveTransferSequence returns the states this connector walks on its own
// after accepting a transfer request under this agreement. An agreement with
// no configured entry starts and stops there; an entry with an empty sequence
// deliberately does nothing, which is how a transfer stays in REQUESTED.
func resolveTransferSequence(cfg config.Config, agreementID string) []string {
	for _, p := range cfg.TransferPolicies {
		if p.AgreementID == agreementID {
			return p.Sequence
		}
	}
	return []string{TransferStarted}
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
	for i, state := range resolveTransferSequence(h.cfg, t.AgreementID) {
		if i > 0 {
			time.Sleep(h.stepDelay)
		}
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
	path, msg, ok := transferStepMessage(t, to)
	if !ok {
		// Unreachable through configuration, which validates every state in a
		// sequence, so this catches a new state added to one side only.
		slog.Error("no transfer message for state", "provider_pid", t.ProviderPID, "want_state", to)
		return false
	}
	callbackURL := t.CallbackAddress + fmt.Sprintf(path, t.ConsumerPID)
	if err := validateOutgoingCallback(callbackURL); err != nil {
		slog.Error("reject callback push", "url", callbackURL, "error", err)
	} else {
		pushCallback(callbackURL, msg)
	}
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
// transfer into the given state, and whether one exists.
func transferStepMessage(t store.TransferProcess, to string) (string, any, bool) {
	switch to {
	case TransferStarted:
		return transferStartCallbackPath, buildTransferStartMessage(t), true
	case TransferSuspended:
		return transferSuspensionCallbackPath, buildTransferSuspensionMessage(t), true
	case TransferCompleted:
		return transferCompletionCallbackPath, buildTransferCompletionMessage(t), true
	case TransferTerminated:
		return transferTerminationCallbackPath, buildTransferTerminationMessage(t), true
	}
	return "", nil, false
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
	writeJSON(w, http.StatusOK, buildTransferProcessDoc(t))
}

// The four endpoints that move a running transfer. Unlike their negotiation
// counterparts, which each carry rules of their own, these differ only in
// which message they accept, which states that message is legal from, and
// which state it lands in — so they share applyTransition rather than
// repeating it four times.
func (h transferHandler) handleTransferStart(w http.ResponseWriter, r *http.Request) {
	h.applyTransition(w, r, TransferStartMessageType, startLegalFrom, TransferStarted)
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
func (h transferHandler) applyTransition(w http.ResponseWriter, r *http.Request,
	wantType string, legalFrom func(string) bool, to string) {
	t, ok := h.lookup(w, r)
	if !ok {
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	var msg envelope
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

	if err := h.store.SetTransferState(t.ProviderPID, t.State, to, time.Now()); err != nil {
		writeTransferStateUpdateError(w, t.ProviderPID, err)
		return
	}
	t.State = to
	writeJSON(w, http.StatusOK, buildTransferProcessDoc(t))
}

// lookup resolves {id} to a stored transfer, writing the appropriate error
// response and returning ok=false if it cannot. {id} is this connector's own
// generated provider pid — the value it returned in the acknowledgment to
// POST /transfers/request — so an id that is not in the table names a
// transfer that never existed, and 404 is the honest answer. It is also the
// only 404 this protocol produces.
func (h transferHandler) lookup(w http.ResponseWriter, r *http.Request) (store.TransferProcess, bool) {
	providerPID := r.PathValue("id")
	t, ok, err := h.store.GetTransfer(providerPID)
	if err != nil {
		slog.Error("get transfer", "provider_pid", providerPID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return store.TransferProcess{}, false
	}
	if !ok {
		writeError(w, TransferErrorType, http.StatusNotFound, "no transfer with id "+providerPID)
		return store.TransferProcess{}, false
	}
	return t, true
}

// writeTransferStateUpdateError is writeStateUpdateError's transfer-protocol
// counterpart, and rejects for the same reason: a lost race
// (store.ErrStateChanged) means another request moved the transfer on between
// this handler's read and its write, so the state precondition it checked no
// longer holds — the same 400 that check would have produced had it run a
// moment later. It is a separate function rather than a shared one because
// the error document names the protocol that produced it.
func writeTransferStateUpdateError(w http.ResponseWriter, providerPID string, err error) {
	if errors.Is(err, store.ErrStateChanged) {
		slog.Warn("transfer changed concurrently", "provider_pid", providerPID, "error", err)
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"transfer "+providerPID+" changed while this request was being handled")
		return
	}
	slog.Error("update transfer state", "provider_pid", providerPID, "error", err)
	w.WriteHeader(http.StatusInternalServerError)
}
