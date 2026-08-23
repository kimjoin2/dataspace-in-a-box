package dsp

import (
	"context"
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
// On the public listener, exactly like /negotiations/initiate and for the
// reason that endpoint's doc comment gives: this is the TCK-shaped hook, not a
// management feature. It is not exempt from authentication — §27 put every DSP
// route but the version document behind a participant credential — so with
// require_auth on it is reachable by any roster participant, and only with it
// off by anyone. It still gets no ownership check of its own, and
// DECISIONS.md section 32.3 records why: its caller is this connector's own
// operator, and the only values there are to compare are ones nobody
// verified. Until a real management trigger exists, it is also this
// connector's only way to start a transfer as consumer.
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
	// reports which address a hostname resolved to, and this endpoint is
	// reachable by any roster participant (by anyone with require_auth off),
	// so returning that text would make it a name-resolution oracle for the
	// network this connector sits on.
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

// newDataPullClient returns the client one data pull uses. It is
// deliberately not callbackHTTPClient: a callback push is a small JSON body
// that should be answered at once, and ten seconds is right for it, while a
// data pull's body is the product and may legitimately take hours.
//
// So there is no Client.Timeout here — the body is bounded by
// idleTimeoutReader instead, on time without progress. Three things that
// look like omissions are requirements:
//
// CheckRedirect matches callbackHTTPClient's. validateOutgoingCallback
// checks the endpoint a counterparty supplied and nothing a redirect points
// at, so a client that follows redirects would let that endpoint hop to
// 127.0.0.1:8081 and reach the management listener that binds to localhost
// precisely so a firewall mistake cannot expose it (DECISIONS.md section
// 12).
//
// The transport is a clone of the default rather than a bare literal.
// ResponseHeaderTimeout starts only once the request is written, so without
// the clone's DialContext timeout and TLSHandshakeTimeout — and
// validateCallbackURL permits https — a black-holed endpoint would hold this
// goroutine for as long as the OS is willing to wait.
//
// ResponseHeaderTimeout is the idle timeout itself rather than a constant of
// its own. Waiting for a response header is time without progress, which is
// exactly what data_idle_timeout bounds everywhere else on both sides of
// this connector; a separate constant would be a second bound on the same
// thing, changeable only by editing this file. That is also why the client
// is built per pull rather than once: Transport.ResponseHeaderTimeout is
// fixed the moment the transport exists, so a package-level client could not
// take the value from configuration at all. Pulls are rare and long, which
// makes the connection pool a shared client would preserve worth nothing
// here — and the caller closes the idle connections this one leaves behind.
func newDataPullClient(idle time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = idle
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// downloadDir is where pulled bytes land, under the one directory this
// connector already owns. A second configurable path would be a second thing
// to get wrong, and this milestone gains nothing from it.
const downloadDir = "downloads"

// parseContentRange parses a Content-Range header, reporting the
// first-byte-pos and the complete length separately because either can be
// absent independently.
//
// RFC 9110 section 14.4's shape for a satisfied range is
// "bytes <first>-<last>/<complete-length>", and the same section permits
// "*" in place of the complete length when the total is not known, and in
// place of the range itself on a 416. Each half therefore reports its own
// presence: an unknown total must reach the caller as unknown rather than as
// a parse failure, or a resume that works today starts failing.
func parseContentRange(header string) (first int64, hasFirst bool, complete int64, hasComplete bool) {
	const prefix = "bytes "
	if !strings.HasPrefix(header, prefix) {
		return 0, false, 0, false
	}
	rest := strings.TrimPrefix(header, prefix)

	slash := strings.LastIndexByte(rest, '/')
	if slash < 0 {
		return 0, false, 0, false
	}
	rangePart, completePart := rest[:slash], rest[slash+1:]

	// RFC 9110 §14.4 permits "*" in place of either half. Neither needs its
	// own check: ParseInt rejects "*" because it is not a digit string, and
	// IndexByte finds no '-' in it, so both fall through to "not present"
	// on their own. An explicit guard here would be a branch no test could
	// kill.
	if n, err := strconv.ParseInt(completePart, 10, 64); err == nil && n >= 0 {
		complete, hasComplete = n, true
	}

	if dash := strings.IndexByte(rangePart, '-'); dash > 0 {
		if n, err := strconv.ParseInt(rangePart[:dash], 10, 64); err == nil && n >= 0 {
			first, hasFirst = n, true
		}
	}
	return first, hasFirst, complete, hasComplete
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

	// The cancel is what the idle reader below pulls to stop a body that has
	// gone quiet: cancelling the request's context closes the connection
	// underneath the read, and the cause says why for the log.
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr.Endpoint, nil)
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
	client := newDataPullClient(h.cfg.DataIdleTimeout)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("data pull", "consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "error", err)
		return
	}
	defer resp.Body.Close()

	// The complete length the counterparty states for the whole
	// representation. Only a 206 carries it, and both the resume check below
	// and the expected total further down read it, so Content-Range is
	// parsed exactly once.
	var statedComplete int64
	var hasStatedComplete bool

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
			var first int64
			var hasFirst bool
			first, hasFirst, statedComplete, hasStatedComplete = parseContentRange(contentRange)
			if !hasFirst || first != existingSize {
				slog.Error("206 response's Content-Range does not start where this connector's partial download left off; leaving the partial download in place",
					"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "content_range", contentRange, "had_bytes", existingSize)
				return
			}
			// A counterparty stating a different complete length is
			// answering about a different representation, not continuing
			// this one. Same handling as 416, and for the same reason. An
			// absent or unknown total is not a mismatch.
			if hasStatedComplete && t.ExpectedBytes > 0 && statedComplete != t.ExpectedBytes {
				slog.Warn("the provider states a different complete length than this transfer recorded; discarding the partial download",
					"consumer_pid", t.ConsumerPID, "stated", statedComplete, "recorded", t.ExpectedBytes)
				if err := os.Remove(partial); err != nil {
					slog.Error("remove stale partial download", "path", partial, "error", err)
				}
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

	// The complete length this attempt was told to expect, from whichever
	// header carried it. Zero means not known — never known to be zero —
	// because a counterparty that streams chunked states no length at all,
	// and this connector's own provider did exactly that until this
	// milestone.
	expected := t.ExpectedBytes
	if resuming {
		if hasStatedComplete {
			expected = statedComplete
		}
	} else if resp.ContentLength >= 0 {
		expected = resp.ContentLength
	}
	if expected > 0 && expected != t.ExpectedBytes {
		if err := h.store.SetConsumerTransferExpectedBytes(t.ConsumerPID, expected); err != nil {
			slog.Error("record expected bytes", "consumer_pid", t.ConsumerPID, "error", err)
		}
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
	body := newIdleTimeoutReader(resp.Body, h.cfg.DataIdleTimeout, cancel)
	defer body.Stop()

	// One byte past the ceiling, so a body that would exceed it is caught
	// rather than silently truncated into a file that looks complete.
	remaining := h.cfg.MaxDownloadBytes - existingSize
	n, err := io.Copy(out, io.LimitReader(body, remaining+1))
	if err != nil {
		out.Close()
		if cause := context.Cause(ctx); errors.Is(cause, errIdleTimeout) || errors.Is(err, errIdleTimeout) {
			slog.Error("data pull made no progress within the idle timeout; leaving the partial download in place",
				"consumer_pid", t.ConsumerPID, "had_bytes", existingSize, "appended_bytes", n)
			return
		}
		slog.Error("write download", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	if n > remaining {
		out.Close()
		slog.Error("data pull exceeded max_download_bytes; leaving the partial download in place",
			"consumer_pid", t.ConsumerPID, "limit", h.cfg.MaxDownloadBytes)
		return
	}
	// Sync before the rename. A rename that outruns its data turns a crash
	// into a success log beside a truncated file, which is worse than a
	// failure because it reports itself as one that did not happen.
	if err := out.Sync(); err != nil {
		out.Close()
		slog.Error("sync download", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	if err := out.Close(); err != nil {
		slog.Error("close download", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}

	total := existingSize + n
	if expected > 0 && total != expected {
		slog.Error("the download does not match the length the provider stated; leaving the partial download in place",
			"consumer_pid", t.ConsumerPID, "have", total, "stated", expected)
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
	slog.Info("pulled transfer data", "consumer_pid", t.ConsumerPID, "path", final, "bytes", total, "expected", expected)
}
