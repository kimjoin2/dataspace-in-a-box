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
// On the management listener, exactly like /negotiations/initiate and for
// the reason DECISIONS.md section 35.1 gives: asking this connector to start
// an exchange is an operator action, and this connector already had a
// listener for those. So its caller is the operator rather than any roster
// participant, and its providerId must name a participant this connector's
// roster lists (section 35.2) — which is what turns the counterparty it
// records into something an inbound message can be authorized against
// (section 35.3). It is this connector's only way to start a transfer as
// consumer, and it is now the management trigger rather than a stand-in for
// one.
func (h transferHandler) handleTransferInitiate(w http.ResponseWriter, r *http.Request) {
	// First, ahead of the required fields and the address guard, because this
	// refusal is about this connector rather than about the request: no
	// correction to the body would make the call succeed. The management
	// listener's token check is the only thing between the operator and this
	// hook — requireParticipant never runs here — so without this an expired
	// connector refuses every counterparty and goes on starting exchanges and
	// signing them with its real key.
	if !h.guard.usable() {
		h.guard.warnExpired()
		refuseExpiredRoster(w, TransferErrorType)
		return
	}
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
	// reports which address a hostname resolved to, a fact this connector
	// learned and the caller did not supply, so returning that text would
	// make it a name-resolution oracle for the network this connector sits
	// on. That is about what the connector discloses rather than about who is
	// asking, so moving this hook to the management listener does not relax
	// it; handleInitiate's equivalent comment says the same.
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
	// The roster check is the most general of the checks about the request
	// here too: the required-fields check, the address guard, and the
	// agreement lookup above are each about a specific fact this request
	// must get right, while the roster check only asks whether providerId
	// names a participant at all. It runs after them, so a request with
	// several mistakes is refused for its most specific one — pinned by
	// TestTransferInitiateRefusesAnUnknownAgreementBeforeTheRosterCheck,
	// which sends a request that fails both the roster check and the
	// agreement lookup and asserts the agreement lookup's rejection is the
	// one returned.
	//
	// The expiry guard at the top of this handler is outside that ordering,
	// for the reason handleInitiate's equivalent comment gives: it is about
	// this connector rather than about the request, so it runs before
	// everything here rather than at the general end of it.
	//
	// The rejected providerId is echoed in the response below, for the same
	// reason handleInitiate's equivalent check does.
	if h.knownParticipant != nil && !h.knownParticipant(body.ProviderID) {
		writeError(w, TransferErrorType, http.StatusBadRequest,
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

// dataPullHTTPClient fetches transfer data. It is deliberately not
// callbackHTTPClient: a callback push is a small JSON body that should be
// answered at once, and ten seconds is right for it, while a data pull's
// body is the product and may legitimately take hours.
//
// So there is no Client.Timeout here. A pull is bounded by time without
// progress instead: idleTimeoutReader covers the body, and a timer armed
// around Do covers everything before a body exists. Two things that look
// like omissions are requirements:
//
// CheckRedirect matches callbackHTTPClient's. validateOutgoingCallback
// checks the endpoint a counterparty supplied and nothing a redirect points
// at, so a client that follows redirects would let that endpoint hop to
// 127.0.0.1:8081 and reach the management listener that binds to localhost
// precisely so a firewall mistake cannot expose it (DECISIONS.md section
// 12).
//
// The transport is a clone of the default rather than a bare literal, so
// this client keeps the connection pool and the dial defaults the rest of
// the connector gets. It carries no ResponseHeaderTimeout on purpose: the
// timer around Do bounds the header wait already, and bounds the dial and
// the TLS handshake with it, which ResponseHeaderTimeout does not — one
// mechanism enforcing data_idle_timeout rather than two, one of which could
// not have read the configuration anyway.
var dataPullHTTPClient = &http.Client{
	Transport: http.DefaultTransport.(*http.Transport).Clone(),
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// maxRefusalBodyBytes bounds how much of a refusing data endpoint's body is
// read for the log. Room for the message a DSP error document carries, and
// no room for a counterparty that answers a refusal with the product to
// write it into this connector's log instead. Named here beside the client
// rather than written at the read, the way internal/mgmt names
// maxAgreementBodyBytes.
const maxRefusalBodyBytes = 512

// errConnectorShuttingDown is the cause a pull's context carries when the
// connector is going down. It reaches the row as the reason, so an operator
// reading a half-finished transfer sees why it stopped rather than a bare
// cancellation.
//
// Being the cause rather than a sentence chosen at the exit is what makes
// the attribution honest. A pull's copy can fail for its own reasons — a
// full disk — at the same moment the connector happens to be going down, and
// a check that only asked "is the parent cancelled?" would record shutdown
// for that write failure, in a column an operator reads later with no log
// beside it. Comparing against this value answers the narrower question the
// row needs: was this the thing that stopped the pull?
//
// It lives here rather than beside errIdleTimeout in idle_reader.go because
// that one belongs to the reader that raises it, and this one to the pull.
var errConnectorShuttingDown = errors.New("the connector shut down while the pull was running")

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

// pullOutcome is what a pull leaves behind on its transfer's row. It exists
// so there is exactly one write site: pullTransferData has many failure
// exits and one success, and a recorder called at each would be that many
// chances to miss one with nothing to catch the miss. Every exit sets a
// field or two on this value; the deferred write in pullTransferData turns
// it into a row.
//
// The zero value is a failure with no reason, which is the right default for
// a function that can return from anywhere: an exit that forgets to say why
// still records that the pull did not finish, rather than silently leaving
// the row describing a previous attempt.
type pullOutcome struct {
	received  int64
	path      string
	completed time.Time
	failure   string
}

// fail records why a pull stopped, as a sentence rather than a code:
// DECISIONS.md section 34.2 records that an operator reading this one column
// on one row has no second place to look a code up in.
//
// Wherever an exit logs, the recorded reason is what its log line says, and
// at no exit the same string. Often it is close: at many exits the recorded
// string is a prefix of the log message, usually that message with a
// trailing clause ("; leaving the partial download in place") cut off. Where
// the only difference is a leading "the", the exits are named here so this
// is checkable by reading — "the start message carried no data endpoint",
// "the data endpoint sent no response within the idle timeout", and "the
// data endpoint refused the pull". Where the two diverge further, it is
// mostly because the log follows the short-verb-phrase convention with the
// detail in structured fields, which a column with no fields cannot use; the
// rest simply say something different from the reason recorded.
//
// The exception is the exit the minter's refusal reaches, which logs nothing
// at all: the guard has already warned once for the whole connector, so that
// exit records a reason with no line to pair against. The comment there
// gives the argument.
//
// So neighbouring exits carry near-identical prose and nothing checks the
// pairing. When editing these sentences, diff them against their neighbours
// rather than reading each on its own — a copy-paste between two adjacent
// exits is the likeliest way one of them goes wrong, and it will read as
// correct.
func (o *pullOutcome) fail(reason string) {
	o.failure = reason
}

// succeed records a published download and clears any reason a previous
// attempt left. Setting the completion and clearing the reason together is
// what keeps a row from ever reading as both completed and failed: the store
// writes all four columns from this one value, so no caller can hand it a
// completion and a reason at the same time.
//
// It deliberately does not take the byte count. By the time this is called
// `received` is already authoritative — seeded from the partial's size
// before the request and overwritten with the copy's own total — and a third
// writer for one field would be a third expression to keep in step with the
// other two.
func (o *pullOutcome) succeed(path string, at time.Time) {
	o.path, o.completed, o.failure = path, at, ""
}

// discardPartial deletes a partial download whose representation is no
// longer the one being fetched, and keeps the outcome's byte count in step
// with the file.
//
// The two have to move together, and that is the whole reason this is a
// function rather than two lines twice. received_bytes is what tells an
// operator whether a restart resumes or starts over, so a row still holding
// a count after the bytes were deleted promises a resume that will not
// happen — and a row zeroed while the delete failed says the opposite about
// bytes that are still there. Section 34.1's rule is that the row describes
// the disk; both halves of that are here.
func discardPartial(o *pullOutcome, partial string) {
	if err := os.Remove(partial); err != nil {
		// The bytes survived, so the count must too.
		slog.Error("remove stale partial download", "path", partial, "error", err)
		return
	}
	o.received = 0
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
	outcome := pullOutcome{failure: "the pull ended without recording a reason"}
	defer func() {
		if err := h.store.RecordConsumerTransferOutcome(
			t.ConsumerPID, outcome.received, outcome.path, outcome.completed, outcome.failure,
		); err != nil {
			slog.Error("record pull outcome", "consumer_pid", t.ConsumerPID, "error", err)
		}
	}()
	// What is already on disk, established before the first exit that can
	// record anything — which is the next one. Only the already-in-flight
	// guard above runs earlier, and it deliberately records nothing at all.
	//
	// The order is the mechanism. Every exit from here to the copy leaves
	// whatever is on disk untouched, except the two that delete it, and
	// those put this back to zero through discardPartial. So no exit can
	// report a count that disagrees with the file: not zero while a partial
	// is held, and not a held count after the partial is gone. A row that is
	// wrong is worse than one that is merely incomplete — received_bytes is
	// omitempty on the wire, so GET /transfers renders a wrong zero as no
	// count at all, and a wrong count as a promise that a restart will
	// resume. Commit 0e622ee established this for the exits below the copy.
	//
	// Only os.Stat runs before the endpoint is validated, which is a read
	// and cannot act on a counterparty-supplied address: dir and partial are
	// path joins over this connector's own configuration and the transfer's
	// own pid.
	dir := filepath.Join(h.cfg.DataDir, downloadDir)
	// A fixed name rather than os.CreateTemp's random one, so a later
	// restart of the same transfer can find what an earlier attempt left
	// behind and continue it.
	partial := filepath.Join(dir, ".partial-"+t.ConsumerPID)
	var existingSize int64
	if info, err := os.Stat(partial); err == nil {
		existingSize = info.Size()
	}
	resuming := existingSize > 0
	outcome.received = existingSize

	// Reachable from a real message, not a defensive check: the dispatch site
	// guards addr != nil, but a start message carrying "dataAddress": {}
	// decodes to a non-nil pointer with an empty Endpoint, and checkEnvelope
	// validates only @context and @type. So this is an attempt by a
	// non-conforming counterparty, and it owns the row like any other.
	if addr == nil || addr.Endpoint == "" {
		slog.Error("start message carried no data endpoint", "consumer_pid", t.ConsumerPID)
		outcome.fail("the start message carried no data endpoint")
		return
	}
	// The endpoint came from a counterparty, so it goes through the same
	// guard as every other address this connector is told to contact.
	if err := validateOutgoingCallback(addr.Endpoint); err != nil {
		slog.Error("refuse data endpoint", "consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "error", err)
		outcome.fail("the data endpoint was refused by the outgoing-address guard")
		return
	}

	// After the stat above, not before it: MkdirAll can only fail here when
	// the directory does not already exist, and a partial download implies
	// it does. So this exit cannot be reached with bytes on disk, and the
	// zero it inherits from the seed is the truth rather than a gap.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Error("create download directory", "dir", dir, "error", err)
		outcome.fail("the download directory could not be created")
		return
	}

	// The cancel is what the idle reader below pulls to stop a body that has
	// gone quiet: cancelling the request's context closes the connection
	// underneath the read, and the cause says why for the log.
	//
	// The parent is the connector's lifetime, so shutdown ends this copy
	// instead of leaving it to run the shutdown cap out and be abandoned
	// before the deferred outcome write above ever runs. Background when
	// there is no connector — the handler's field is nil in tests that do
	// not exercise shutdown, and a nil parent would panic.
	parent := h.pullCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr.Endpoint, nil)
	if err != nil {
		slog.Error("build data pull", "consumer_pid", t.ConsumerPID, "error", err)
		outcome.fail("the data pull request could not be built")
		return
	}
	// No log line of its own. The guard has already said the roster expired,
	// once for the whole connector, and a pull is reached by every restart
	// of every transfer — so a line here would be the repetition that rule
	// exists to prevent. The reason reaches an operator through the row this
	// function has already deferred.
	//
	// The exit leaves whatever is on disk untouched, so the count seeded
	// from the partial download above stays true of the file: the property
	// every exit between that seed and the copy holds.
	authorization, maySend := mintOutboundCredential(t.CounterpartyID)
	if !maySend {
		outcome.fail("this connector's roster has expired")
		return
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if resuming {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}
	// Nothing has arrived yet either, so the wait for a response falls under
	// the same cancellation that bounds a stalled body — which covers the
	// dial and the TLS handshake as well as the header wait. If this fires
	// between Do returning and the Stop, the first body read fails with
	// context.Canceled and context.Cause reports errIdleTimeout, which is
	// the branch the copy below already attributes correctly.
	headers := time.AfterFunc(h.cfg.DataIdleTimeout, func() { cancel(errIdleTimeout) })
	resp, err := dataPullHTTPClient.Do(req)
	headers.Stop()
	if err != nil {
		if errors.Is(context.Cause(ctx), errIdleTimeout) {
			slog.Error("data endpoint sent no response within the idle timeout",
				"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint)
			outcome.fail("the data endpoint sent no response within the idle timeout")
			return
		}
		if errors.Is(context.Cause(ctx), errConnectorShuttingDown) {
			slog.Warn("the connector shut down before the data endpoint responded",
				"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint)
			outcome.fail(errConnectorShuttingDown.Error())
			return
		}
		slog.Error("data pull", "consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "error", err)
		outcome.fail("the data pull failed before any bytes arrived")
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
				outcome.fail("the provider's 206 did not start where this connector's partial download left off")
				return
			}
			// A counterparty stating a different complete length is
			// answering about a different representation, not continuing
			// this one. Same handling as 416, and for the same reason. An
			// absent or unknown total is not a mismatch.
			if hasStatedComplete && t.ExpectedBytes > 0 && statedComplete != t.ExpectedBytes {
				slog.Warn("the provider states a different complete length than this transfer recorded; discarding the partial download",
					"consumer_pid", t.ConsumerPID, "stated", statedComplete, "recorded", t.ExpectedBytes)
				discardPartial(&outcome, partial)
				outcome.fail("the provider states a different complete length than this transfer recorded")
				return
			}
			// fall through to the append below
		case http.StatusRequestedRangeNotSatisfiable:
			// The provider's file is no longer at least as long as what this
			// connector already has — it was replaced or shrank between
			// attempts. Not a valid prefix of anything; start over next time.
			slog.Warn("provider's file is no longer past what this connector already has; discarding the partial download",
				"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "had_bytes", existingSize)
			discardPartial(&outcome, partial)
			outcome.fail("the provider's file is no longer past what this connector already has")
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
			outcome.fail("the data endpoint gave an unexpected answer to a resumed pull")
			return
		}
	} else if resp.StatusCode >= 300 {
		// The status alone no longer identifies the refusal: this
		// connector's own DSP listener answers 409 for an expired roster,
		// and a data endpoint answers 409 for reasons of its own. The body
		// is where the counterparty says which. The read error is dropped
		// because this is already a refusal — whatever arrived, truncated or
		// empty, tells an operator more than the status by itself.
		refusal, _ := io.ReadAll(io.LimitReader(resp.Body, maxRefusalBodyBytes))
		slog.Error("data endpoint refused the pull",
			"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "status", resp.StatusCode,
			"body", strings.TrimSpace(string(refusal)))
		outcome.fail("the data endpoint refused the pull")
		return
	}

	// The complete length this attempt was told to expect, from whichever
	// header carried it. Zero means not known — never known to be zero —
	// because a counterparty that streams chunked states no length at all,
	// and this connector's own provider did exactly that until this
	// milestone.
	//
	// A fresh attempt seeds nothing from the row. Its authority is what this
	// response states, and a length recorded from an earlier representation
	// has none: the two paths that discard a partial leave that recorded
	// value behind, and carrying it into the next attempt would refuse a
	// body that is in fact complete while reporting a remembered number as
	// something this counterparty stated. A resume does seed from the row,
	// because the representation it is continuing is the one that recorded
	// it.
	var expected int64
	if resuming {
		expected = t.ExpectedBytes
		if hasStatedComplete {
			expected = statedComplete
		}
	} else if resp.ContentLength >= 0 {
		expected = resp.ContentLength
	}
	// Recorded even when it is zero, which is how a stale total from a
	// discarded representation leaves the row rather than outliving it.
	if expected != t.ExpectedBytes {
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
		outcome.fail("the download file could not be opened")
		return
	}
	body := newIdleTimeoutReader(resp.Body, h.cfg.DataIdleTimeout, cancel)
	defer body.Stop()

	// One byte past the ceiling, so a body that would exceed it is caught
	// rather than silently truncated into a file that looks complete.
	remaining := h.cfg.MaxDownloadBytes - existingSize
	n, err := io.Copy(out, io.LimitReader(body, remaining+1))
	// Set once here rather than at each exit below, which is the work this
	// design exists to avoid. Every exit past this point leaves the bytes
	// copied so far on disk, and that number is what tells an operator
	// whether a restart resumes or starts over — the fact the failure
	// sentences state in words. succeed writes the same total again on the
	// success path.
	outcome.received = existingSize + n
	if err != nil {
		out.Close()
		if cause := context.Cause(ctx); errors.Is(cause, errIdleTimeout) || errors.Is(err, errIdleTimeout) {
			slog.Error("data pull made no progress within the idle timeout; leaving the partial download in place",
				"consumer_pid", t.ConsumerPID, "had_bytes", existingSize, "appended_bytes", n)
			outcome.fail("the data pull made no progress within the idle timeout")
			return
		}
		if errors.Is(context.Cause(ctx), errConnectorShuttingDown) {
			outcome.fail(errConnectorShuttingDown.Error())
			slog.Warn("the connector shut down while the pull was running; leaving the partial download in place",
				"consumer_pid", t.ConsumerPID, "had_bytes", existingSize, "appended_bytes", n)
			return
		}
		slog.Error("write download", "consumer_pid", t.ConsumerPID, "error", err)
		outcome.fail("the download could not be written")
		return
	}
	if n > remaining {
		out.Close()
		// Not a dead end, and the message says so because the recovery is
		// not obvious: the bytes already written are a valid prefix at the
		// right offset, so raising max_download_bytes and restarting the
		// transfer resumes from exactly here rather than starting over.
		slog.Error("data pull exceeded max_download_bytes; leaving the partial download in place — "+
			"raise max_download_bytes and restart the transfer to resume from the bytes already fetched",
			"consumer_pid", t.ConsumerPID, "limit", h.cfg.MaxDownloadBytes, "have_bytes", existingSize+n)
		outcome.fail("the data pull exceeded max_download_bytes")
		return
	}
	// Sync before the rename. A rename that outruns its data turns a crash
	// into a success log beside a truncated file, which is worse than a
	// failure because it reports itself as one that did not happen.
	if err := out.Sync(); err != nil {
		out.Close()
		slog.Error("sync download", "consumer_pid", t.ConsumerPID, "error", err)
		outcome.fail("the download could not be synced")
		return
	}
	if err := out.Close(); err != nil {
		slog.Error("close download", "consumer_pid", t.ConsumerPID, "error", err)
		outcome.fail("the download could not be closed")
		return
	}

	// Read rather than recomputed: outcome.received already holds
	// existingSize + n, and two expressions for one number are two things
	// that can drift apart — the row and the success log would then disagree
	// about the same transfer.
	total := outcome.received
	if expected > 0 && total != expected {
		slog.Error("the download does not match the length the provider stated; leaving the partial download in place",
			"consumer_pid", t.ConsumerPID, "have", total, "stated", expected)
		outcome.fail("the download does not match the length the provider stated")
		return
	}
	if resuming {
		slog.Info("resumed transfer data pull", "consumer_pid", t.ConsumerPID, "had_bytes", existingSize, "appended_bytes", n)
	}
	final := filepath.Join(dir, t.ConsumerPID)
	if err := os.Rename(partial, final); err != nil {
		slog.Error("place download", "path", final, "error", err)
		outcome.fail("the download could not be placed")
		return
	}
	outcome.succeed(final, time.Now().UTC())
	slog.Info("pulled transfer data", "consumer_pid", t.ConsumerPID, "path", final, "bytes", total, "expected", expected)
}
