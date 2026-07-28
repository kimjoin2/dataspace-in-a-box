// Package dsp implements the Dataspace Protocol 2025-1 HTTPS binding.
package dsp

import (
	"encoding/json"
	"net/http"
)

// Values of the version metadata document.
//
// These are read from DSP 2025-1. Where the official TCK disagrees with any of
// them, the TCK is authoritative and these change — that rule is why this
// project can claim compliance at all.
const (
	ContextURL  = "https://w3id.org/dspace/2025/1/context.jsonld"
	Version     = "2025-1"
	VersionPath = "/2025-1"
	Binding     = "HTTPS"
)

// ProtocolVersion is one entry of the version metadata document. Path is
// relative to the base path hosting the version metadata endpoint, so the
// routes for this version live under {base}{Path}.
type ProtocolVersion struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	Binding string `json:"binding"`
}

// VersionResponse is the version metadata document in the fixed compact form.
// DSP 2025-1 fixes the serialization, so this is ordinary structured JSON and
// no JSON-LD or RDF processing is involved.
type VersionResponse struct {
	Context          []string          `json:"@context"`
	ProtocolVersions []ProtocolVersion `json:"protocolVersions"`
}

func versionDocument() VersionResponse {
	return VersionResponse{
		Context: []string{ContextURL},
		ProtocolVersions: []ProtocolVersion{
			{Version: Version, Path: VersionPath, Binding: Binding},
		},
	}
}

// handleVersion serves the version metadata endpoint. It is unauthenticated:
// discovering which protocol versions a connector speaks precedes any trust
// relationship with it.
func handleVersion(w http.ResponseWriter, r *http.Request) {
	body, _ := json.Marshal(versionDocument()) // a static document cannot fail to marshal
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}
