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
	mux.Handle("GET /transfers", authenticated(cfg.MgmtToken, http.HandlerFunc(h.listTransfers)))

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
		AgreementID    string `json:"agreementId"`
		DatasetID      string `json:"datasetId"`
		CounterpartyID string `json:"counterpartyId"`
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
		AgreementID:    body.AgreementID,
		DatasetID:      body.DatasetID,
		Origin:         store.OriginImported,
		CounterpartyID: body.CounterpartyID,
		CreatedAt:      time.Now().UTC(),
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
			AgreementID:    a.AgreementID,
			DatasetID:      a.DatasetID,
			Origin:         a.Origin,
			CreatedAt:      a.CreatedAt.UTC().Format(time.RFC3339Nano),
			CounterpartyID: a.CounterpartyID,
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
	// CounterpartyID is declared last, after CreatedAt, on purpose: Go emits
	// struct fields in declaration order, and demo/run.sh's resume round
	// extracts its agreement with a sed that requires agreementId and
	// datasetId to stay adjacent in the JSON body. Putting this field between
	// them would break that extraction.
	CounterpartyID string `json:"counterpartyId"`
}

// listTransfers returns every transfer this connector holds, in both roles.
// It exists for the same reason GET /agreements does — an operator otherwise
// has no way to see whether the data a transfer was for actually arrived —
// and it is read-only for the same reason. DECISIONS.md section 34.4 records
// why this does not move the boundary section 25.3 drew.
//
// Both roles, with a role field, because a route named /transfers that
// showed half the transfers would be a trap for whoever read it next.
// Provider-role rows carry no download fields: they never fetch anything.
//
// An empty dataPath does not mean no data was ever fetched. The row
// describes the latest attempt, not the history: a re-pull that fails writes
// an empty path and a reason over what a successful earlier attempt
// recorded, while the file that attempt published is still on disk under
// data_dir. This route is where an operator meets that first, so it is said
// here.
func (h agreementHandler) listTransfers(w http.ResponseWriter, r *http.Request) {
	consumers, err := h.store.ListConsumerTransfers()
	if err != nil {
		slog.Error("list consumer transfers", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	providers, err := h.store.ListTransfers()
	if err != nil {
		slog.Error("list transfers", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	out := make([]transferView, 0, len(consumers)+len(providers))
	for _, c := range consumers {
		v := transferView{
			Role: "consumer", ConsumerPID: c.ConsumerPID, ProviderPID: c.ProviderPID,
			AgreementID: c.AgreementID, State: c.State, CounterpartyID: c.CounterpartyID,
			CreatedAt:     c.CreatedAt.UTC().Format(time.RFC3339Nano),
			ExpectedBytes: c.ExpectedBytes, ReceivedBytes: c.ReceivedBytes,
			DataPath: c.DataPath, DataError: c.DataError,
		}
		if !c.DataCompletedAt.IsZero() {
			v.DataCompletedAt = c.DataCompletedAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, v)
	}
	for _, p := range providers {
		out = append(out, transferView{
			Role: "provider", ConsumerPID: p.ConsumerPID, ProviderPID: p.ProviderPID,
			AgreementID: p.AgreementID, State: p.State, CounterpartyID: p.CounterpartyID,
			CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"transfers": out}); err != nil {
		slog.Error("encode transfers", "error", err)
	}
}

// transferView is the wire shape, kept separate from the store structs for
// the reason agreementView records: the management API does not leak
// whichever columns storage happens to carry. The download fields are
// omitted when empty so a provider-role row does not carry four blanks that
// mean nothing for it.
type transferView struct {
	Role            string `json:"role"`
	ConsumerPID     string `json:"consumerPid"`
	ProviderPID     string `json:"providerPid"`
	AgreementID     string `json:"agreementId"`
	State           string `json:"state"`
	CounterpartyID  string `json:"counterpartyId"`
	CreatedAt       string `json:"createdAt"`
	ExpectedBytes   int64  `json:"expectedBytes,omitempty"`
	ReceivedBytes   int64  `json:"receivedBytes,omitempty"`
	DataPath        string `json:"dataPath,omitempty"`
	DataCompletedAt string `json:"dataCompletedAt,omitempty"`
	DataError       string `json:"dataError,omitempty"`
}
