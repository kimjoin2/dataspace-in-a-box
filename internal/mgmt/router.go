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
	mux.Handle("GET /agreements", authenticated(cfg.MgmtToken, http.HandlerFunc(h.listAgreements)))

	return mux
}

// bearerPrefix is the auth-scheme token plus its separating space, as it
// appears at the head of an Authorization header value.
const bearerPrefix = "Bearer "

// authenticated rejects any request without the configured bearer token. An
// empty configured token rejects everything: a missing token must never read
// as "no auth required", or the localhost default becomes an open write
// endpoint the moment mgmt_addr changes.
func authenticated(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := cutBearerPrefix(r.Header.Get("Authorization"))
		if token == "" || !ok || subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			// RFC 9110 §15.5.2 makes a challenge a MUST on a 401, and a
			// client that gets none is told it was refused without being told
			// how to authenticate. No realm parameter: this API has exactly
			// one protection space, so naming it would add a string an
			// operator has to keep in step with nothing.
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cutBearerPrefix strips the auth scheme from an Authorization header value,
// reporting whether the value carried that scheme at all.
//
// The scheme is matched case-insensitively because RFC 9110 §11.1 defines it
// as a case-insensitive token: `bearer <t>` and `BEARER <t>` present the same
// credential as `Bearer <t>`. A case-sensitive match fails closed, so this is
// interoperability rather than a hole — but this is the connector's first
// authenticated endpoint, and a client that capitalises the scheme its own
// way would be refused for a reason no error message explains.
//
// Only the scheme is folded. The credential itself is returned byte-exact and
// still compared in constant time by the caller.
func cutBearerPrefix(header string) (string, bool) {
	if len(header) < len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	return header[len(bearerPrefix):], true
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
		//
		// The re-query below is safe against a false 409, not because access
		// to the store is serialized (SetMaxOpenConns(1) does not make the
		// insert and this read atomic — another request can interleave
		// between them), but because agreements are immutable: there is no
		// delete path anywhere in this connector, so a row this query finds
		// cannot have vanished between the failed insert and now. If an
		// agreement delete path is ever added, this inference stops holding.
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

// listAgreements returns every agreement this connector holds, in the order
// they were made. It exists because an operator otherwise has no way to see
// what a negotiation concluded — the negotiation endpoints report a state,
// not the agreement id a transfer has to cite — and because the demo needs
// exactly that to get from "we agreed" to "transfer under it".
//
// Read-only and unpaginated. An agreement list that outgrows one response is
// a problem worth having first.
func (h agreementHandler) listAgreements(w http.ResponseWriter, r *http.Request) {
	agreements, err := h.store.ListAgreements()
	if err != nil {
		slog.Error("list agreements", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	out := make([]agreementView, 0, len(agreements))
	for _, a := range agreements {
		out = append(out, agreementView{
			AgreementID: a.AgreementID,
			DatasetID:   a.DatasetID,
			Origin:      a.Origin,
			CreatedAt:   a.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"agreements": out}); err != nil {
		slog.Error("encode agreements", "error", err)
	}
}

// agreementView is the wire shape, kept separate from store.Agreement so the
// management API does not leak whichever columns storage happens to carry.
// consumerPid is deliberately absent: it is an internal correlation id.
type agreementView struct {
	AgreementID string `json:"agreementId"`
	DatasetID   string `json:"datasetId"`
	Origin      string `json:"origin"`
	CreatedAt   string `json:"createdAt"`
}
