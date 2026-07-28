package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVersionEndpointReturnsProtocolVersions(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/.well-known/dspace-version", nil)
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body VersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Context) != 1 || body.Context[0] != ContextURL {
		t.Errorf("@context = %v, want [%s]", body.Context, ContextURL)
	}
	if len(body.ProtocolVersions) != 1 {
		t.Fatalf("protocolVersions has %d entries, want 1", len(body.ProtocolVersions))
	}
	v := body.ProtocolVersions[0]
	if v.Version != Version || v.Path != VersionPath || v.Binding != Binding {
		t.Errorf("protocolVersions[0] = %+v, want {%s %s %s}", v, Version, VersionPath, Binding)
	}
}

func TestVersionEndpointRejectsPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/.well-known/dspace-version", nil)
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/2025-1/catalog/request", nil)
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 while the catalog protocol is unimplemented", rec.Code)
	}
}
