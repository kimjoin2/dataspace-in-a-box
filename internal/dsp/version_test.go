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
	NewRouter(testConfig()).ServeHTTP(rec, req)

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
	NewRouter(testConfig()).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// TestVersionDocumentPinsTCKAcceptedValues pins the values against which the
// official TCK's MET:01-01 ("Verify metadata request") succeeded, captured in
// tck-output.txt (2026-07-28 run). A passing TCK run does not reveal which
// fields it inspected, so this only records what the TCK accepted, not what
// it affirmatively requires byte-for-byte. If this test fails, do not edit
// the literal to match the code — change the code and re-run `make tck`.
func TestVersionDocumentPinsTCKAcceptedValues(t *testing.T) {
	doc := versionDocument()

	if got, want := doc.Context[0], "https://w3id.org/dspace/2025/1/context.jsonld"; got != want {
		t.Errorf("@context = %q, want %q", got, want)
	}
	v := doc.ProtocolVersions[0]
	if got, want := v.Version, "2025-1"; got != want {
		t.Errorf("version = %q, want %q", got, want)
	}
	if got, want := v.Path, "/2025-1"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := v.Binding, "HTTPS"; got != want {
		t.Errorf("binding = %q, want %q", got, want)
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	// Contract negotiation is the next protocol in TCK order and is not
	// implemented, so its path is still the honest example of an unrouted one.
	req := httptest.NewRequest(http.MethodPost, "/2025-1/negotiations/request", nil)
	rec := httptest.NewRecorder()
	NewRouter(testConfig()).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 while contract negotiation is unimplemented", rec.Code)
	}
}

// TestManagementRouteIsNotServedByDSP makes the listener split (DECISIONS.md
// §12) a permanent test rather than a manually verified claim: the DSP
// listener must not also answer management API routes.
func TestManagementRouteIsNotServedByDSP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	NewRouter(testConfig()).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: the DSP listener must not serve the management API", rec.Code)
	}
}
