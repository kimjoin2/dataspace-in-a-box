package dsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// transferInitiateBody is what the TCK POSTs to
// dataspacetck.dsp.connector.transfer.initiate.url. Plain JSON, not JSON-LD:
// no @context and no @type. Confirmed from the pinned image —
// HttpConsumerTransferProcessClient.initiateTransferRequest builds a
// four-entry Map.of and calls postJson(url, body, false, true), whose false
// is the JSON-LD flag. The negotiation hook has the same shape and the same
// flags.
type transferInitiateBody struct {
	ProviderID       string `json:"providerId"`
	AgreementID      string `json:"agreementId"`
	Format           string `json:"format"`
	ConnectorAddress string `json:"connectorAddress"`
}

// handleTransferInitiate serves POST /transfers/initiate: the hook that asks
// this connector to start a transfer as consumer. It responds 200 as soon as
// the transfer is recorded and dispatches the outbound request in a
// goroutine, because the caller proceeds by waiting for that request to
// arrive on its own endpoint rather than by reading this response — and
// blocking the response on a network call would make the hook's latency
// depend on the counterparty.
//
// Unauthenticated, on the public listener, exactly like
// /negotiations/initiate and for the reason that endpoint's doc comment
// gives: this is the TCK-shaped hook, not a management feature. Until a real
// management trigger exists, it is also this connector's only way to start a
// transfer as consumer.
func (h transferHandler) handleTransferInitiate(w http.ResponseWriter, r *http.Request) {
	var body transferInitiateBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes))
	if err := dec.Decode(&body); err != nil {
		writeError(w, TransferErrorType, http.StatusBadRequest, "the request body is not a JSON object")
		return
	}
	if body.ProviderID == "" || body.AgreementID == "" || body.Format == "" || body.ConnectorAddress == "" {
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"providerId, agreementId, format, and connectorAddress are all required")
		return
	}
	// The rejection reason is logged, not echoed — validateOutgoingCallback
	// reports which address a hostname resolved to, and this endpoint is open
	// to anonymous callers, so returning that text would make it a
	// name-resolution oracle for the network this connector sits on.
	if err := validateOutgoingCallback(body.ConnectorAddress); err != nil {
		slog.Warn("reject transfer initiate", "connector_address", body.ConnectorAddress, "error", err)
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"connectorAddress is not an address this connector will send to")
		return
	}
	// One rule for both roles. The provider role already refuses a transfer
	// citing an agreement it has no record of; starting one as consumer under
	// a contract this connector never held would be the same defect from the
	// other side, and CLAUDE.md's "never accept a constraint that is not
	// enforced" rules it out either way.
	if _, ok, err := h.store.GetAgreement(body.AgreementID); err != nil {
		slog.Error("get agreement", "agreement_id", body.AgreementID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if !ok {
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"no agreement with id "+body.AgreementID)
		return
	}

	consumerPID, err := store.NewUUID()
	if err != nil {
		slog.Error("generate consumer pid", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	now := time.Now()
	t := store.ConsumerTransfer{
		ConsumerPID:     consumerPID,
		ProviderBaseURL: body.ConnectorAddress,
		// providerId is who the operator asked this connector to transfer
		// with, and is therefore the audience of everything it will send.
		CounterpartyID: body.ProviderID,
		AgreementID:    body.AgreementID,
		Format:         body.Format,
		State:          TransferRequested,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := h.store.CreateConsumerTransfer(t); err != nil {
		slog.Error("create consumer transfer", "consumer_pid", consumerPID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)

	go h.requestTransfer(t)
}

// requestTransfer sends the opening TransferRequestMessage and records what
// the acknowledgment reveals. There is nothing to record and nothing this
// connector can legally send without a provider pid, so a failure here ends
// the transfer where it stands rather than retrying.
func (h transferHandler) requestTransfer(t store.ConsumerTransfer) {
	callbackAddress := h.cfg.PublicURL + VersionPath
	providerPID, err := sendTransferRequest(t.ProviderBaseURL, buildTransferRequestMessage(t, callbackAddress), t.CounterpartyID)
	if err != nil {
		slog.Error("send transfer request", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	h.onTransferRequestAcknowledged(t, providerPID)
}

// onTransferRequestAcknowledged records the provider pid the ACK supplied.
// It is the only place that pid enters the row, and every message this
// connector sends as consumer is addressed to a URL containing it — which is
// why a policy triggered by REQUESTED fires from here rather than when the
// row was written.
func (h transferHandler) onTransferRequestAcknowledged(t store.ConsumerTransfer, providerPID string) {
	if err := h.store.SetConsumerTransferProviderPID(t.ConsumerPID, providerPID, time.Now()); err != nil {
		slog.Error("record provider pid", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	t.ProviderPID = providerPID
	// A policy triggered by REQUESTED fires here rather than when the row was
	// written: every URL this connector addresses as consumer contains the
	// provider pid, and it did not exist until this ACK. TP_C:02-05 is the
	// test that depends on it.
	h.maybeDriveConsumerTransfer(t, TransferRequested)
}

// consumerTransferStepMessage returns the path template and the message that
// move a consumer-role transfer into the given state, plus the predicate
// saying which states that move is legal from.
//
// STARTED is deliberately absent. DSP 2025-1 admits a consumer's start only
// as a resume, and no TP_C test that produces a result asks for one —
// TP_C:02-04, the only test that does, is @Disabled upstream. A policy that
// names STARTED is refused here rather than emitting a message this
// connector's own provider role would answer 400 to.
func consumerTransferStepMessage(t store.ConsumerTransfer, to string) (string, any, func(string) bool, bool) {
	p := store.TransferProcess{ProviderPID: t.ProviderPID, ConsumerPID: t.ConsumerPID}
	switch to {
	case TransferSuspended:
		return consumerTransferSuspensionPath, buildTransferSuspensionMessage(p), suspensionLegalFrom, true
	case TransferCompleted:
		return consumerTransferCompletionPath, buildTransferCompletionMessage(p), completionLegalFrom, true
	case TransferTerminated:
		return consumerTransferTerminationPath, buildTransferTerminationMessage(p), terminationLegalFrom, true
	}
	return "", nil, nil, false
}

// pushConsumerStep sends one configured step to the provider and records it.
// It is pushTransferStep's consumer-role counterpart and differs in exactly
// three ways: the URL base is the provider's rather than the consumer's, the
// path id is the provider pid rather than the consumer pid, and the write
// goes through the consumer table. Every refusal below has the same reason
// pushTransferStep's doc comment gives.
func (h transferHandler) pushConsumerStep(t store.ConsumerTransfer, to string) bool {
	path, msg, legalFrom, ok := consumerTransferStepMessage(t, to)
	if !ok {
		slog.Error("no consumer transfer message for state",
			"consumer_pid", t.ConsumerPID, "want_state", to)
		return false
	}
	// The legality table is enforced against this connector's own configured
	// sequence, not only against the counterparty in applyTransition. A
	// sequence that has gone illegal has no meaningful remainder: the steps
	// after it were written against a state this transfer will never be in.
	if !legalFrom(t.State) {
		slog.Error("refuse illegal configured consumer transfer step",
			"consumer_pid", t.ConsumerPID, "agreement_id", t.AgreementID,
			"state", t.State, "want_state", to)
		return false
	}
	if t.ProviderPID == "" {
		// Unreachable through either trigger: one fires on the ACK that
		// supplies the pid, the other on a message that could not have been
		// routed without it. It stays as the honest guard against a third
		// trigger being added without that ordering in mind.
		slog.Error("refuse consumer transfer step before the provider pid is known",
			"consumer_pid", t.ConsumerPID, "want_state", to)
		return false
	}
	url := t.ProviderBaseURL + fmt.Sprintf(path, t.ProviderPID)
	if err := validateOutgoingCallback(url); err != nil {
		slog.Error("reject consumer transfer push", "url", url, "error", err)
		return false
	}
	pushCallback(url, msg, t.CounterpartyID)
	if err := h.store.SetConsumerTransferState(t.ConsumerPID, t.State, to, time.Now()); err != nil {
		if errors.Is(err, store.ErrStateChanged) {
			slog.Warn("drop stale consumer transfer state update",
				"consumer_pid", t.ConsumerPID, "want_state", to, "error", err)
			return false
		}
		slog.Error("update consumer transfer state", "consumer_pid", t.ConsumerPID, "error", err)
		return false
	}
	return true
}

// driveConsumerTransfer walks the configured sequence, pushing one message
// per step and stopping at the first refusal. Each step's write is the
// precondition the next step is checked against, exactly as in the provider
// role's driver.
func (h transferHandler) driveConsumerTransfer(t store.ConsumerTransfer) {
	_, sequence := resolveConsumerTransferPolicy(h.cfg, t.AgreementID)
	for i, state := range sequence {
		if i > 0 {
			time.Sleep(h.stepDelay)
		}
		if !h.pushConsumerStep(t, state) {
			return
		}
		t.State = state
	}
}

// maybeDriveConsumerTransfer releases the configured sequence if this state
// is the one the policy waits for. Both triggers funnel through here so the
// comparison lives in one place.
func (h transferHandler) maybeDriveConsumerTransfer(t store.ConsumerTransfer, reached string) {
	after, sequence := resolveConsumerTransferPolicy(h.cfg, t.AgreementID)
	if len(sequence) == 0 || reached != after {
		return
	}
	t.State = reached
	go h.driveConsumerTransfer(t)
}

// downloadDir is where pulled bytes land, under the one directory this
// connector already owns. A second configurable path would be a second thing
// to get wrong, and this milestone gains nothing from it.
const downloadDir = "downloads"

// contentRangeFirstByte parses a 206 response's Content-Range header and
// returns its first-byte-pos. RFC 7233 §4.2's shape for a single range is
// "bytes <first>-<last>/<complete-length>" — the same shape this
// connector's own provider emits (data_handler.go's 206 branch). Anything
// else — the header absent, or not in that shape — is reported via the
// bool rather than guessed at.
func contentRangeFirstByte(header string) (int64, bool) {
	const prefix = "bytes "
	if !strings.HasPrefix(header, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(header, prefix)
	dash := strings.IndexByte(rest, '-')
	if dash < 0 {
		return 0, false
	}
	first, err := strconv.ParseInt(rest[:dash], 10, 64)
	if err != nil || first < 0 {
		return 0, false
	}
	return first, true
}

// pullTransferData fetches what a dataAddress points at and writes it under
// data_dir, resuming from a previous attempt when one left bytes behind.
// Called whenever a start message carrying an address arrives — the first
// time for a transfer, and again on every restart after a suspension.
//
// Not self-retried. A failed pull leaves the transfer in STARTED, which is
// the honest state — the provider is still willing to serve and an operator
// can ask again. What changes with resumption is what "ask again" costs: the
// next externally triggered attempt continues from wherever the last one
// left off, rather than starting over.
func (h transferHandler) pullTransferData(t store.ConsumerTransfer, addr *DataAddress) {
	if h.pulling != nil {
		if _, alreadyRunning := h.pulling.LoadOrStore(t.ConsumerPID, struct{}{}); alreadyRunning {
			slog.Warn("a pull for this transfer is already in flight; dropping this restart's trigger",
				"consumer_pid", t.ConsumerPID)
			return
		}
		defer h.pulling.Delete(t.ConsumerPID)
	}
	if addr == nil || addr.Endpoint == "" {
		return
	}
	// The endpoint came from a counterparty, so it goes through the same
	// guard as every other address this connector is told to contact.
	if err := validateOutgoingCallback(addr.Endpoint); err != nil {
		slog.Error("refuse data endpoint", "consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "error", err)
		return
	}

	dir := filepath.Join(h.cfg.DataDir, downloadDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Error("create download directory", "dir", dir, "error", err)
		return
	}
	// A fixed name rather than os.CreateTemp's random one, so a later
	// restart of the same transfer can find what an earlier attempt left
	// behind and continue it.
	partial := filepath.Join(dir, ".partial-"+t.ConsumerPID)
	var existingSize int64
	if info, err := os.Stat(partial); err == nil {
		existingSize = info.Size()
	}
	resuming := existingSize > 0

	req, err := http.NewRequest(http.MethodGet, addr.Endpoint, nil)
	if err != nil {
		slog.Error("build data pull", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	if authorization := mintOutboundCredential(t.CounterpartyID); authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if resuming {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}
	resp, err := callbackHTTPClient.Do(req)
	if err != nil {
		slog.Error("data pull", "consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "error", err)
		return
	}
	defer resp.Body.Close()

	if resuming {
		switch resp.StatusCode {
		case http.StatusPartialContent:
			// A 206 status alone does not prove the body actually starts
			// where this connector's partial download left off — a
			// non-conforming counterparty or a misbehaving proxy could
			// answer 206 with content-range starting somewhere else.
			// Content-Range is the one place that claim is checked before
			// the body is trusted enough to append. Same posture as the
			// default: case below when it does not check out: log and
			// leave the partial exactly as it was. Not the 416 case — a
			// wrong Content-Range is not proof the provider's file
			// changed, just a different-shaped answer to investigate.
			contentRange := resp.Header.Get("Content-Range")
			first, ok := contentRangeFirstByte(contentRange)
			if !ok || first != existingSize {
				slog.Error("206 response's Content-Range does not start where this connector's partial download left off; leaving the partial download in place",
					"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "content_range", contentRange, "had_bytes", existingSize)
				return
			}
			// fall through to the append below
		case http.StatusRequestedRangeNotSatisfiable:
			// The provider's file is no longer at least as long as what this
			// connector already has — it was replaced or shrank between
			// attempts. Not a valid prefix of anything; start over next time.
			slog.Warn("provider's file is no longer past what this connector already has; discarding the partial download",
				"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "had_bytes", existingSize)
			if err := os.Remove(partial); err != nil {
				slog.Error("remove stale partial download", "path", partial, "error", err)
			}
			return
		default:
			// Any other answer to a resumed pull, including an unexpected
			// 200 — this connector's own provider role always answers a
			// Range request with 206 or 416, so a 200 here would mean the
			// counterparty does not honor Range at all, and appending its
			// full-content body to an existing partial would corrupt the
			// file. Safer to abort and leave the partial exactly as it was.
			slog.Error("data endpoint gave an unexpected answer to a resumed pull; leaving the partial download in place",
				"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "status", resp.StatusCode, "had_bytes", existingSize)
			return
		}
	} else if resp.StatusCode >= 300 {
		slog.Error("data endpoint refused the pull",
			"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "status", resp.StatusCode)
		return
	}

	flag := os.O_CREATE | os.O_WRONLY
	if resuming {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	out, err := os.OpenFile(partial, flag, 0o600)
	if err != nil {
		slog.Error("open download file", "path", partial, "error", err)
		return
	}
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		out.Close()
		slog.Error("write download", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	if err := out.Close(); err != nil {
		slog.Error("close download", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	if resuming {
		slog.Info("resumed transfer data pull", "consumer_pid", t.ConsumerPID, "had_bytes", existingSize, "appended_bytes", n)
	}
	final := filepath.Join(dir, t.ConsumerPID)
	if err := os.Rename(partial, final); err != nil {
		slog.Error("place download", "path", final, "error", err)
		return
	}
	slog.Info("pulled transfer data", "consumer_pid", t.ConsumerPID, "path", final, "bytes", n)
}
