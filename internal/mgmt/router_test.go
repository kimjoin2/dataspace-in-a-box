// This file is `package mgmt` (an internal test package), so NewRouter is
// referenced unqualified while store and config keep their package prefixes.
package mgmt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

const testToken = "0123456789abcdef"

func newTestRouter(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return NewRouter(config.Config{MgmtToken: testToken}, st), st
}

func TestHealthReturnsOK(t *testing.T) {
	h, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), `{"status":"ok"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestHealthNeedsNoToken(t *testing.T) {
	h, _ := newTestRouter(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /health = %d, want 200 without a token", rec.Code)
	}
}

// TestDSPRouteIsNotServedByManagement makes the listener split (DECISIONS.md
// §12) a permanent test rather than a manually verified claim: the management
// listener must not also answer public DSP routes.
func TestDSPRouteIsNotServedByManagement(t *testing.T) {
	h, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/dspace-version", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: the management listener must not serve DSP endpoints", rec.Code)
	}
}

func TestPostAgreementsRecordsIt(t *testing.T) {
	h, st := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /agreements = %d, want 201 (body %q)", rec.Code, rec.Body.String())
	}
	got, ok, err := st.GetAgreement("urn:uuid:a-1")
	if err != nil {
		t.Fatalf("GetAgreement: %v", err)
	}
	if !ok {
		t.Fatal("POST /agreements returned 201 but stored no agreement")
	}
	if got.DatasetID != "urn:dataset:a" {
		t.Errorf("DatasetID = %q, want urn:dataset:a", got.DatasetID)
	}
	if got.Origin != store.OriginImported {
		t.Errorf("Origin = %q, want %q", got.Origin, store.OriginImported)
	}
}

func TestPostAgreementsWithoutTokenIs401(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agreements", body))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /agreements with no Authorization header = %d, want 401", rec.Code)
	}
}

func TestPostAgreementsWithWrongTokenIs401(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	// One character off testToken, not an unrelated literal, so the
	// relationship to the real token stays visible if testToken ever changes.
	wrongToken := testToken[:len(testToken)-1] + "0"
	req.Header.Set("Authorization", "Bearer "+wrongToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /agreements with a wrong token = %d, want 401", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("401 body = %q, want empty — a rejection must not say why it was rejected", rec.Body.String())
	}
}

func TestPostAgreementsIs401WhenNoTokenIsConfigured(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	h := NewRouter(config.Config{}, st) // no token configured
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /agreements with no configured token = %d, want 401 — an unset token must never mean open access", rec.Code)
	}
}

func TestPostAgreementsMissingFieldIs400(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /agreements without datasetId = %d, want 400", rec.Code)
	}
}

func TestPostAgreementsDuplicateIs409(t *testing.T) {
	h, _ := newTestRouter(t)
	post := func() int {
		body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
		req := httptest.NewRequest(http.MethodPost, "/agreements", body)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(); code != http.StatusCreated {
		t.Fatalf("first POST = %d, want 201", code)
	}
	if code := post(); code != http.StatusConflict {
		t.Errorf("second POST with the same id = %d, want 409", code)
	}
}
