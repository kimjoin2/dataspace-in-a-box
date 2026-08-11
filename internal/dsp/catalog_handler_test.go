package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

// catalogRequest is the well-formed request body the TCK sends. Tests that are
// about something other than the body use it unchanged.
const catalogRequest = `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"CatalogRequestMessage"}`

func post(t *testing.T, cfg config.Config, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/2025-1/catalog/request", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewRouter(cfg).ServeHTTP(rec, req)
	return rec
}

func TestCatalogRequestReturnsTheCatalog(t *testing.T) {
	rec := post(t, testConfig("urn:dataset:a"), catalogRequest)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var cat Catalog
	if err := json.Unmarshal(rec.Body.Bytes(), &cat); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(cat.Dataset) != 1 || cat.Dataset[0].ID != "urn:dataset:a" {
		t.Errorf("dataset = %v, want the configured identifier", cat.Dataset)
	}
}

func TestCatalogRequestRejectsAFilter(t *testing.T) {
	// DSP leaves the filter expression implementation-defined, so returning the
	// full catalog would let a consumer believe it received a filtered view.
	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],` +
		`"@type":"CatalogRequestMessage","filter":[{"foo":"bar"}]}`
	rec := post(t, testConfig("urn:dataset:a"), body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCatalogError(t, rec)
}

func TestCatalogRequestAcceptsANullFilter(t *testing.T) {
	// An explicit null is the absence of a filter, not a filter nobody can read.
	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],` +
		`"@type":"CatalogRequestMessage","filter":null}`
	rec := post(t, testConfig("urn:dataset:a"), body)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestCatalogRequestRejectsAMalformedBody(t *testing.T) {
	rec := post(t, testConfig(), "not json at all")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCatalogError(t, rec)
}

func TestCatalogRequestRejectsAnOversizedBody(t *testing.T) {
	// Pad @type with a run of a repeated character rather than embedding a
	// megabyte-long literal in the source; the total body exceeds the limit
	// either way.
	padding := strings.Repeat("a", maxCatalogRequestBodyBytes)
	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],` +
		`"@type":"CatalogRequestMessage` + padding + `"}`
	rec := post(t, testConfig(), body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCatalogError(t, rec)
}

func TestCatalogRequestRejectsTheWrongMessageType(t *testing.T) {
	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"SomethingElse"}`
	rec := post(t, testConfig(), body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCatalogError(t, rec)
}

func TestCatalogRequestRejectsAMissingContext(t *testing.T) {
	rec := post(t, testConfig(), `{"@context":["https://example.org/other"],"@type":"CatalogRequestMessage"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCatalogError(t, rec)
}

func TestDatasetRequestReturnsTheDataset(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/2025-1/catalog/datasets/urn:dataset:a", nil)
	rec := httptest.NewRecorder()
	NewRouter(testConfig("urn:dataset:a")).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var ds Dataset
	if err := json.Unmarshal(rec.Body.Bytes(), &ds); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if ds.ID != "urn:dataset:a" {
		t.Errorf("@id = %q, want the requested identifier", ds.ID)
	}
	if len(ds.Context) != 1 {
		t.Errorf("@context = %v, want the document context", ds.Context)
	}
}

func TestUnknownDatasetIsACatalogError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/2025-1/catalog/datasets/urn:dataset:missing", nil)
	rec := httptest.NewRecorder()
	NewRouter(testConfig("urn:dataset:a")).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	assertCatalogError(t, rec)
}

// assertCatalogError checks that the body is a DSP CatalogError rather than a
// plain-text message. This is the assertion CAT:01-03 turns on.
func assertCatalogError(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON (%v): %s", err, rec.Body)
	}
	if m["@type"] != CatalogErrorType {
		t.Errorf("@type = %v, want %q", m["@type"], CatalogErrorType)
	}
}
