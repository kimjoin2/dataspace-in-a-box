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
	// (auth_middleware.go) carries this identical rule for the five exchange
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

	if rangeStart, hasRange := parseRangeStart(r.Header.Get("Range")); hasRange {
		if rangeStart >= stat.Size() {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", stat.Size()))
			writeError(w, TransferErrorType, http.StatusRequestedRangeNotSatisfiable,
				"the requested range starts at or after the end of this dataset's current content")
			return
		}
		if _, err := f.Seek(rangeStart, io.SeekStart); err != nil {
			slog.Error("seek source_file for range request", "path", ds.SourceFile, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rangeStart, stat.Size()-1, stat.Size()))
		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size()-rangeStart, 10))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusPartialContent)
		if _, err := io.Copy(w, f); err != nil {
			slog.Error("stream data", "provider_pid", providerPID, "error", err)
		}
		return
	}

	if ds.SimulateInterruptAfterBytes > 0 {
		n := ds.SimulateInterruptAfterBytes
		if n > stat.Size() {
			n = stat.Size()
		}
		io.CopyN(w, f, n) //nolint:errcheck // the connection is about to be severed regardless
		// The truncated bytes are sitting in ResponseWriter's own
		// pre-chunking buffer (net/http buffers up to 2KB before deciding
		// chunked vs. Content-Length) and Hijack does not flush it — only
		// explicitly flushing here gets the n bytes onto the wire before
		// the connection is severed.
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
	// The server's write timeout still bounds how large a file can finish.
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, f); err != nil {
		slog.Error("stream data", "provider_pid", providerPID, "error", err)
	}
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
