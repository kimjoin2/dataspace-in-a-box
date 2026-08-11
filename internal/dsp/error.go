package dsp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
)

// DSP error type names. Each protocol names its own; the document is otherwise
// identical, which is why the writer takes the name as a parameter.
const CatalogErrorType = "CatalogError"

// ErrorResponse is a DSP error document.
type ErrorResponse struct {
	Context []string `json:"@context"`
	Type    string   `json:"@type"`
	Code    string   `json:"code"`
	// Reason is an array because the context declares it @container: @set.
	Reason []string `json:"reason"`
}

// errorDocument builds the DSP error document for an HTTP status.
func errorDocument(dspType string, status int, reason string) ErrorResponse {
	return ErrorResponse{
		Context: []string{ContextURL},
		Type:    dspType,
		Code:    strconv.Itoa(status),
		Reason:  []string{reason},
	}
}

// writeError sends a DSP error document. The body is JSON even for a 404,
// because a consumer parses every response as JSON-LD: a plain-text body fails
// at its parser rather than at the protocol, which tells it nothing about what
// went wrong.
func writeError(w http.ResponseWriter, dspType string, status int, reason string) {
	writeJSON(w, status, errorDocument(dspType, status, reason))
}

// writeJSON marshals v and sends it as a DSP response. Marshalling happens
// before the status is written so that a failure can still be reported as a
// 500 — once WriteHeader has been called, the status is no longer negotiable.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal response", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}
