package dsp

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// dataHandler serves dataset bytes for a transfer in progress.
type dataHandler struct {
	cfg   config.Config
	store *store.Store
}

// parseRangeStart reads this connector's one supported Range form,
// "bytes=N-": a single open-ended offset. Anything else — absent,
// unparseable, a closed or multi-range form, or the "bytes=-N" suffix form —
// is reported as absent, which is RFC 7233's own guidance for a range a
// server does not support: ignore it and serve the whole thing.
func parseRangeStart(header string) (int64, bool) {
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(header, prefix)
	if !strings.HasSuffix(rest, "-") {
		return 0, false
	}
	rest = strings.TrimSuffix(rest, "-")
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// copyBufSize is large on purpose. net/http buffers a response internally —
// 2048 bytes at response.w, 4096 at conn.bufw — so with a buffer near those
// sizes "the write succeeded" measures the buffer rather than the wire, and
// the deadline below would never fire on a stalled client.
const copyBufSize = 256 << 10

// copyUnderRollingDeadline streams src to w, pushing the write deadline out
// before each chunk so a transfer is bounded by time without progress rather
// than by total elapsed time.
//
// It does not use io.Copy, and that is the whole point. *http.response
// implements io.ReaderFrom, and on a non-chunked response — which is every
// response that carries a Content-Length — that implementation hands the
// whole file to *net.TCPConn.ReadFrom, one sendfile call that blocks until
// the transfer finishes. A handler parked in that call cannot roll
// anything, so whatever deadline was set before it governs the entire
// response. Writing through w directly is what keeps the loop.
//
// For the same reason w must stay an http.ResponseWriter here rather than an
// io.Writer: a helper taking io.Writer would find ReadFrom again through the
// interface and silently restore the problem. The cost is real and accepted
// — this gives up sendfile on exactly the large-file case this exists for —
// because an unbounded transfer that is merely slower beats a fast one that
// stops at the server's write timeout.
func copyUnderRollingDeadline(w http.ResponseWriter, rc *http.ResponseController, src io.Reader, idle time.Duration) (int64, error) {
	buf := make([]byte, copyBufSize)
	var total int64
	for {
		// Before the write, while the current deadline is still in the
		// future: SetWriteDeadline documents that setting a deadline after
		// it has been exceeded will not extend it.
		if err := rc.SetWriteDeadline(time.Now().Add(idle)); err != nil {
			return total, fmt.Errorf("set write deadline: %w", err)
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := w.Write(buf[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			// io.Copy's invariant, kept because this is a hand-rolled
			// io.Copy: a short write with a nil error would otherwise drop
			// those bytes silently and carry on, producing a response that
			// is missing a hole in the middle and reports success. Nothing
			// net/http hands us does this today; the guard is here because
			// the helper is the copy, and a copy that can lose bytes
			// without saying so is worse than one that stops.
			if written < n {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}

// handleData serves GET {version}/data/{providerPid} — where a consumer
// fetches the bytes a started transfer entitles it to. Behind the same
// middleware as every other counterparty-facing route, because a data pull is
// not a different kind of conversation: the same counterparty, authenticated
// the same way, acting on the same agreement.
//
// The identifier in the path carries no authority, deliberately. It is this
// connector's own provider pid — loggable, correlatable, safe to paste into a
// bug report — and possessing it grants nothing. Authorization is the three
// checks below, and a dataAddress is an address rather than a capability.
//
// The refusals are distinguishable on purpose: an operator reading a log
// should be able to tell "no such transfer" from "not yours" from "the
// access window closed" from "there is nothing configured behind this
// dataset".
func (h dataHandler) handleData(w http.ResponseWriter, r *http.Request) {
	providerPID := r.PathValue("id")

	t, ok, err := h.store.GetTransfer(providerPID)
	if err != nil {
		slog.Error("get transfer for data pull", "provider_pid", providerPID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !ok {
		// The only 404 the data plane produces, and it means what 404 means.
		writeError(w, TransferErrorType, http.StatusNotFound, "no transfer with id "+providerPID)
		return
	}

	// Whose transfer it is. Checked before state, so a counterparty probing
	// someone else's transfer learns nothing about that transfer's progress.
	//
	// The third of this connector's three comparison forms, and hand-rolled
	// rather than shared on purpose. refuseIfNotParty
	// (auth_middleware.go) carries the same rule for the exchange
	// resolvers, and handleTransferRequest's agreement check
	// (transfer_handler.go) carries it with an empty-permitted clause added.
	// This one must never acquire that clause: a transfer row with no
	// counterparty is refused to everyone today, and permitting empty here
	// would serve it to any roster participant. Factoring the three together
	// is what would hand it over — see DECISIONS.md section 32.2.
	if issuer := issuerFrom(r); h.cfg.AuthRequired() && issuer != t.CounterpartyID {
		slog.Warn("refuse data pull from a participant this transfer is not with",
			"provider_pid", providerPID, "issuer", issuer)
		writeError(w, TransferErrorType, http.StatusForbidden, "this transfer is not yours")
		return
	}

	// STARTED is the authorization. Every other state means the provider is
	// not currently willing to serve: REQUESTED has not begun, SUSPENDED is
	// paused, and the terminal states are over.
	if t.State != TransferStarted {
		writeError(w, TransferErrorType, http.StatusConflict,
			"transfer "+providerPID+" is "+t.State+", and data is served only from "+TransferStarted)
		return
	}

	ds, ok := h.datasetFor(t.AgreementID)
	if !ok {
		// The agreement is real and the transfer is real; there is simply
		// nothing configured behind the dataset. A different answer from 404
		// and from 403, and the log line is what tells an operator which.
		slog.Warn("data pull for a dataset this connector does not advertise",
			"provider_pid", providerPID, "agreement_id", t.AgreementID)
		writeError(w, TransferErrorType, http.StatusConflict,
			"no data is configured for this transfer's dataset")
		return
	}

	// The one check STARTED alone cannot make: a transfer that reached
	// STARTED while the offer was still valid keeps that state (nothing
	// re-checks it — see DECISIONS.md §23.4's account of why AGREED's
	// re-check is the only one that ever ran), so every pull has to ask
	// again on its own.
	if ds.ValidityUntil != nil && !time.Now().Before(*ds.ValidityUntil) {
		writeError(w, TransferErrorType, http.StatusConflict,
			"the access window for this transfer's dataset has closed")
		return
	}

	if ds.SourceFile == "" {
		slog.Warn("data pull for a dataset with no source_file",
			"provider_pid", providerPID, "agreement_id", t.AgreementID)
		writeError(w, TransferErrorType, http.StatusConflict,
			"no data is configured for this transfer's dataset")
		return
	}

	f, err := os.Open(ds.SourceFile)
	if err != nil {
		// Validated at load, so reaching here means it moved underneath the
		// connector while running.
		slog.Error("open source_file", "path", ds.SourceFile, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		slog.Error("stat source_file", "path", ds.SourceFile, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Before the Range branch, so all three response shapes carry it — the
	// full 200, the 206, and the simulated interrupt, which is also a 200
	// and returns before the plain path.
	//
	// What setting it on the interrupt branch buys is narrower than this
	// comment once claimed, and DECISIONS.md section 31.4 now records the
	// correction: it is not that a chunked truncation would look
	// well-formed (net/http turns a severed chunked stream into
	// io.ErrUnexpectedEOF either way), and it is not that make demo's
	// resume would otherwise discard its partial (that branch is guarded by
	// ExpectedBytes > 0 and cannot fire when the first attempt recorded
	// nothing). It buys a consumer that can tell how much it was short by,
	// and an expected total recorded on the first attempt rather than only
	// the second.
	//
	// The two refusals inside the Range branch are the exception, and each
	// clears it again: they send an error document or nothing at all, and a
	// response that declares the dataset's length and then sends a shorter
	// body has that body truncated by net/http and the connection closed —
	// the consumer sees an unparseable refusal instead of the reason.
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))

	if rangeStart, hasRange := parseRangeStart(r.Header.Get("Range")); hasRange {
		if rangeStart >= stat.Size() {
			w.Header().Del("Content-Length")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", stat.Size()))
			writeError(w, TransferErrorType, http.StatusRequestedRangeNotSatisfiable,
				"the requested range starts at or after the end of this dataset's current content")
			return
		}
		if _, err := f.Seek(rangeStart, io.SeekStart); err != nil {
			w.Header().Del("Content-Length")
			slog.Error("seek source_file for range request", "path", ds.SourceFile, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rangeStart, stat.Size()-1, stat.Size()))
		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size()-rangeStart, 10))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusPartialContent)
		// After the 416 branch and immediately before the first byte: every
		// refusal path above must stay untouched.
		rc := http.NewResponseController(w)
		n, err := copyUnderRollingDeadline(w, rc, f, h.cfg.DataIdleTimeout)
		if err != nil {
			slog.Error("stream data", "provider_pid", providerPID, "error", err)
			return
		}
		// The audit line for a resumed pull. issuerFrom is called again
		// rather than reusing the check above: that binding is scoped to
		// the `if` that refuses the wrong caller, and a context lookup is
		// cheaper than widening its scope for two call sites.
		slog.Info("served transfer data",
			"issuer", issuerFrom(r), "provider_pid", providerPID, "dataset_id", ds.ID,
			"bytes", n, "range_start", rangeStart)
		return
	}

	if ds.SimulateInterruptAfterBytes > 0 {
		n := ds.SimulateInterruptAfterBytes
		if n > stat.Size() {
			n = stat.Size()
		}
		// Through the same rolling-deadline loop as the two real streaming
		// paths, and for the same reason. This response declares a
		// Content-Length, so it is not chunked, and io.CopyN here would
		// reach (*http.response).ReadFrom — which sniffs the first 512
		// bytes through its own copy and then hands everything after them
		// to *net.TCPConn.ReadFrom, one sendfile call under the server's
		// WriteTimeout. That is the bug this file exists to remove, and
		// declaring a Content-Length is what would have re-enabled it here.
		rc := http.NewResponseController(w)
		//nolint:errcheck // the connection is about to be severed regardless
		copyUnderRollingDeadline(w, rc, io.LimitReader(f, n), h.cfg.DataIdleTimeout)
		// No "served transfer data" line here, and its absence is the
		// decision. This branch truncates deliberately and then severs the
		// connection, so the consumer did not receive the dataset; an audit
		// line would record a delivery that did not happen. The two real
		// streaming paths carry it and this one does not.
		// Whatever the loop left in the response's buffers has to be pushed
		// out explicitly: Hijack does not flush, so without this the n bytes
		// need not have reached the wire before the connection is severed.
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
			}
		}
		return
	}

	// Streamed rather than buffered: memory must not scale with file size.
	// The write deadline rolls forward as bytes leave, so what bounds this
	// is time without progress rather than the server's write timeout —
	// which would otherwise be a file size limit expressed in seconds.
	w.Header().Set("Content-Type", "application/octet-stream")
	rc := http.NewResponseController(w)
	n, err := copyUnderRollingDeadline(w, rc, f, h.cfg.DataIdleTimeout)
	if err != nil {
		slog.Error("stream data", "provider_pid", providerPID, "error", err)
		return
	}
	slog.Info("served transfer data",
		"issuer", issuerFrom(r), "provider_pid", providerPID, "dataset_id", ds.ID, "bytes", n)
}

// datasetFor resolves the agreement a transfer runs under to the dataset
// it covers. The agreement carries the dataset id; this connector's own
// live config carries everything handleData needs from it —
// SourceFile and ValidityUntil alike — the same "config is an operator
// declaration, re-read on every request" choice buildCatalog already makes,
// not a value snapshotted at negotiation time.
func (h dataHandler) datasetFor(agreementID string) (config.Dataset, bool) {
	a, ok, err := h.store.GetAgreement(agreementID)
	if err != nil || !ok {
		if err != nil {
			slog.Error("get agreement for data pull", "agreement_id", agreementID, "error", err)
		}
		return config.Dataset{}, false
	}
	if !servableAsProvider(a) {
		// Deliberately the same not-ok return, and so the same 409 at
		// handleData, as the "nothing configured behind the dataset" case
		// below: reshaping datasetFor's signature to carry a reason is not
		// worth the ripple for a path only a pre-existing transfer_processes
		// row can reach (handleTransferRequest now refuses to create one
		// under an OriginAgreed agreement). This log line is what lets an
		// operator tell the two 409s apart.
		slog.Warn("data pull for an agreement this connector holds as consumer",
			"agreement_id", agreementID, "origin", a.Origin)
		return config.Dataset{}, false
	}
	for _, d := range h.cfg.Datasets {
		if d.ID == a.DatasetID {
			return d, true
		}
	}
	return config.Dataset{}, false
}
