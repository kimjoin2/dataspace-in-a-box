// Package mgmt serves the management API. It listens on a separate port from
// the DSP endpoints and binds to localhost by default, so exposing it is a
// deliberate configuration choice rather than a firewall accident.
package mgmt

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// maxAgreementBodyBytes bounds an import request body. An agreement is two
// short strings; anything larger is a mistake or an attack.
const maxAgreementBodyBytes = 4 << 10

// NewRouter returns the handler for the management listener. It takes the
// configuration for the bearer token and the store because importing an
// agreement writes to it.
func NewRouter(cfg config.Config, st *store.Store) http.Handler {
	mux := http.NewServeMux()

	// /health is deliberately unauthenticated: it carries no information and
	// a readiness probe should not need a credential.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	h := agreementHandler{store: st}
	mux.Handle("POST /agreements", authenticated(cfg.MgmtToken, http.HandlerFunc(h.importAgreement)))

	return mux
}

// authenticated rejects any request without the configured bearer token. An
// empty configured token rejects everything: a missing token must never read
// as "no auth required", or the localhost default becomes an open write
// endpoint the moment mgmt_addr changes.
func authenticated(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || !ok || subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type agreementHandler struct {
	store *store.Store
}

// importAgreement records an agreement concluded outside this connector.
// It records an agreement and nothing else — this is not the beginning of a
// general management CRUD surface.
func (h agreementHandler) importAgreement(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AgreementID string `json:"agreementId"`
		DatasetID   string `json:"datasetId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAgreementBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "malformed request body", http.StatusBadRequest)
		return
	}
	if body.AgreementID == "" || body.DatasetID == "" {
		http.Error(w, "agreementId and datasetId are both required", http.StatusBadRequest)
		return
	}

	err := h.store.CreateAgreement(store.Agreement{
		AgreementID: body.AgreementID,
		DatasetID:   body.DatasetID,
		Origin:      store.OriginImported,
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		// A duplicate is the one failure a caller can act on. Everything else
		// is this connector's problem, and its detail stays in the log.
		if _, found, getErr := h.store.GetAgreement(body.AgreementID); getErr == nil && found {
			http.Error(w, "an agreement with that id already exists", http.StatusConflict)
			return
		}
		slog.Error("import agreement", "agreement_id", body.AgreementID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	slog.Info("imported agreement", "agreement_id", body.AgreementID, "dataset_id", body.DatasetID)
	w.WriteHeader(http.StatusCreated)
}
