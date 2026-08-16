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

// transferStartCallbackPath is appended, formatted with the consumer pid, to
// a transfer's stored callback address — the transfer protocol's counterpart
// to negotiation_handler.go's offerCallbackPath and friends. The address it
// is appended to is used exactly as it arrived: the counterparty's callback
// endpoint matches the whole request path against a pattern it built from
// that same string, so normalising it here — adding a prefix, adding or
// dropping a slash — turns a correct push into a 404.
const transferStartCallbackPath = "/transfers/%s/start"

// transferHandler serves the transfer process protocol in the provider role.
// It lives in its own file because negotiation_handler.go was split at 867
// lines in the previous milestone specifically so this protocol would not
// grow it further.
//
// cfg is held for the same reason negotiationHandler holds it — the handlers
// are constructed from configuration in router.go — even though Phase A's
// provider role reads nothing out of it yet.
type transferHandler struct {
	cfg   config.Config
	store *store.Store
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
	if !checkEnvelope(w, msg.Context, msg.Type, TransferRequestMessageType) {
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
	// every subsequent request about this transfer, so it has to be a single
	// routable path segment — which is what newMessageID produces.
	now := time.Now()
	t := store.TransferProcess{
		ProviderPID:     newMessageID(),
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

	writeJSON(w, http.StatusCreated, buildTransferProcessDoc(t))

	go h.startTransfer(t)
}

// startTransfer pushes the TransferStartMessage that actually begins the
// transfer, then records STARTED.
//
// It is always invoked with `go`, never inline, for the reason DECISIONS.md
// §23.8 gives and negotiation_handler.go's dispatch documents at length:
// net/http buffers a response this small and does not put it on the wire
// until the handler returns, so an inline push would keep the acknowledgment
// above sitting in that buffer for the whole retry schedule — while the
// counterparty waits for it before it is ready to receive the push.
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
// REQUESTED, so a termination that arrived while the push was still retrying
// wins and this goroutine's write is dropped rather than resurrecting a dead
// transfer.
//
// t.CallbackAddress came from an unauthenticated request body, so the
// constructed URL is validated before anything is sent — see
// validateCallbackURL's doc comment.
func (h transferHandler) startTransfer(t store.TransferProcess) {
	callbackURL := t.CallbackAddress + fmt.Sprintf(transferStartCallbackPath, t.ConsumerPID)
	if err := validateOutgoingCallback(callbackURL); err != nil {
		slog.Error("reject callback push", "url", callbackURL, "error", err)
	} else {
		pushCallback(callbackURL, buildTransferStartMessage(t))
	}
	if err := h.store.SetTransferState(t.ProviderPID, TransferRequested, TransferStarted, time.Now()); err != nil {
		if errors.Is(err, store.ErrStateChanged) {
			slog.Warn("drop stale transfer state update",
				"provider_pid", t.ProviderPID, "want_state", TransferStarted, "error", err)
			return
		}
		slog.Error("update transfer state", "provider_pid", t.ProviderPID, "error", err)
	}
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
	if !checkEnvelope(w, msg.Context, msg.Type, wantType) {
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
