package dsp

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
)

// pushCallback sends v as a JSON POST to url, best-effort: a failure is
// logged and never returned to the caller. The provider is authoritative
// over negotiation state in this protocol, so a dropped push does not
// corrupt anything a consumer cannot recover from GET /negotiations/{id};
// no retry queue is built for v1.
func pushCallback(url string, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal callback push", "url", url, "error", err)
		return
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("push callback", "url", url, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Error("callback endpoint rejected push", "url", url, "status", resp.StatusCode)
	}
}
