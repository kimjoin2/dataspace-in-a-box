package dsp

import (
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// dataHandler serves dataset bytes for a transfer in progress.
type dataHandler struct {
	cfg   config.Config
	store *store.Store
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
// The three refusals are distinguishable on purpose: an operator reading a
// log should be able to tell "no such transfer" from "not yours" from "there
// is nothing configured behind this dataset".
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

	source, ok := h.sourceFileFor(t.AgreementID)
	if !ok {
		// The agreement is real and the transfer is real; there is simply
		// nothing configured behind the dataset. A different answer from 404
		// and from 403, and the log line is what tells an operator which.
		slog.Warn("data pull for a dataset with no source_file",
			"provider_pid", providerPID, "agreement_id", t.AgreementID)
		writeError(w, TransferErrorType, http.StatusConflict,
			"no data is configured for this transfer's dataset")
		return
	}

	f, err := os.Open(source)
	if err != nil {
		// Validated at load, so reaching here means it moved underneath the
		// connector while running.
		slog.Error("open source_file", "path", source, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer f.Close()

	// Streamed rather than buffered: memory must not scale with file size.
	// The server's write timeout still bounds how large a file can finish.
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, f); err != nil {
		// Nothing useful can be sent now — the status line is long gone. The
		// log is the only place this can be seen.
		slog.Error("stream data", "provider_pid", providerPID, "error", err)
	}
}

// sourceFileFor resolves the agreement a transfer runs under to the file its
// dataset is served from. The agreement carries the dataset id; the dataset
// carries the path.
func (h dataHandler) sourceFileFor(agreementID string) (string, bool) {
	a, ok, err := h.store.GetAgreement(agreementID)
	if err != nil || !ok {
		if err != nil {
			slog.Error("get agreement for data pull", "agreement_id", agreementID, "error", err)
		}
		return "", false
	}
	for _, d := range h.cfg.Datasets {
		if d.ID == a.DatasetID && d.SourceFile != "" {
			return d.SourceFile, true
		}
	}
	return "", false
}
