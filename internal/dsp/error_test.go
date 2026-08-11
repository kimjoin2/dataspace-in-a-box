package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestErrorDocumentShape(t *testing.T) {
	doc := errorDocument(CatalogErrorType, http.StatusNotFound, "Dataset not found")

	if len(doc.Context) != 1 || doc.Context[0] != ContextURL {
		t.Errorf("@context = %v, want [%s]", doc.Context, ContextURL)
	}
	if doc.Type != "CatalogError" {
		t.Errorf("@type = %q, want CatalogError", doc.Type)
	}
	if doc.Code != "404" {
		t.Errorf("code = %q, want \"404\"", doc.Code)
	}
	if len(doc.Reason) != 1 || doc.Reason[0] != "Dataset not found" {
		t.Errorf("reason = %v, want one entry", doc.Reason)
	}
}

func TestErrorReasonSerializesAsAnArray(t *testing.T) {
	// The context declares reason as @container: @set, so a bare string would
	// expand differently.
	b, err := json.Marshal(errorDocument(CatalogErrorType, http.StatusBadRequest, "nope"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["reason"].([]any); !ok {
		t.Errorf("reason = %v, want a JSON array", m["reason"])
	}
}

func TestErrorTypeNameIsParameterized(t *testing.T) {
	// ContractNegotiationError and TransferError are the same document with a
	// different @type, so the writer must not hard-code one.
	doc := errorDocument("ContractNegotiationError", http.StatusBadRequest, "nope")
	if doc.Type != "ContractNegotiationError" {
		t.Errorf("@type = %q, want ContractNegotiationError", doc.Type)
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
	var doc ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("the error body is not JSON: %v", err)
	}
}
