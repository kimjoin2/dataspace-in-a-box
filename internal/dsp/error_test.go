package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestErrorDocumentShape(t *testing.T) {
	m := decode(t, errorDocument(CatalogErrorType, http.StatusNotFound, "Dataset not found"))

	ctx, ok := m["@context"].([]any)
	if !ok || len(ctx) != 1 || ctx[0] != ContextURL {
		t.Errorf("@context = %v, want the array [%s]", m["@context"], ContextURL)
	}
	if got, want := m["@type"], "CatalogError"; got != want {
		t.Errorf("@type = %v, want %q", got, want)
	}
	if got, want := m["code"], "404"; got != want {
		t.Errorf("code = %v, want %q", got, want)
	}
	reason, ok := m["reason"].([]any)
	if !ok || len(reason) != 1 || reason[0] != "Dataset not found" {
		t.Errorf("reason = %v, want one entry", m["reason"])
	}
}

func TestErrorReasonSerializesAsAnArray(t *testing.T) {
	// The context declares reason as @container: @set, so a bare string would
	// expand differently.
	m := decode(t, errorDocument(CatalogErrorType, http.StatusBadRequest, "nope"))
	if _, ok := m["reason"].([]any); !ok {
		t.Errorf("reason = %v, want a JSON array", m["reason"])
	}
}

func TestErrorTypeNameIsParameterized(t *testing.T) {
	// ContractNegotiationError and TransferError are the same document with a
	// different @type, so the writer must not hard-code one.
	m := decode(t, errorDocument("ContractNegotiationError", http.StatusBadRequest, "nope"))
	if got, want := m["@type"], "ContractNegotiationError"; got != want {
		t.Errorf("@type = %v, want %q", got, want)
	}
}

func TestWriteErrorSetsStatusAndContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, CatalogErrorType, http.StatusNotFound, "Dataset not found")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("the error body is not JSON: %v", err)
	}
	ctx, ok := m["@context"].([]any)
	if !ok || len(ctx) != 1 || ctx[0] != ContextURL {
		t.Errorf("@context = %v, want the array [%s]", m["@context"], ContextURL)
	}
	if got, want := m["@type"], "CatalogError"; got != want {
		t.Errorf("@type = %v, want %q", got, want)
	}
	if got, want := m["code"], "404"; got != want {
		t.Errorf("code = %v, want %q", got, want)
	}
}
