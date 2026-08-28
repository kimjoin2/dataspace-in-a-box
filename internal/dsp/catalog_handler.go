package dsp

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

// CatalogRequestMessageType is the @type a catalog request must carry.
const CatalogRequestMessageType = "CatalogRequestMessage"

// maxCatalogRequestBodyBytes bounds the body read for a catalog request. This
// is the first unauthenticated POST this connector exposes on the public DSP
// listener, and the server's WriteTimeout bounds request time but not request
// size, so an unbounded read could exhaust memory before any validation runs.
// 1 MiB is generous: a well-formed catalog request is a handful of short
// JSON-LD fields.
const maxCatalogRequestBodyBytes = 1 << 20 // 1 MiB

// CatalogRequestMessage is the body of a catalog request. Only the fields this
// connector inspects are declared; unknown fields are ignored, which is what a
// JSON-LD consumer does anyway.
//
// Validation is a set of direct field checks rather than JSON Schema
// validation: the standard library has no schema validator, and one incoming
// message with two required fields does not justify adding an engine. This is
// revisited when negotiation and transfer push the message count past a
// dozen.
type CatalogRequestMessage struct {
	Context []string `json:"@context"`
	Type    string   `json:"@type"`
	// Filter is omitempty for the sake of the one place this type is
	// marshalled rather than decoded: fetchCatalog sends a request carrying no
	// filter, and without the tag a nil RawMessage goes out as an explicit
	// "filter":null. The tag changes nothing on the decoding side, where an
	// absent key and a null are already the same answer -- see hasFilter.
	Filter json.RawMessage `json:"filter,omitempty"`
}

// hasFilter reports whether the message carries a filter expression. An
// explicit JSON null is the absence of one.
func (m CatalogRequestMessage) hasFilter() bool {
	return len(m.Filter) > 0 && string(m.Filter) != "null"
}

// catalogHandler serves the catalog protocol from the connector's
// configuration. It holds no mutable state: an advertised catalog is an
// operator declaration, so there is nothing here for storage to hold.
type catalogHandler struct {
	cfg config.Config
}

// handleCatalogRequest serves the catalog. Every rejection answers with a DSP
// CatalogError so that a consumer learns what was wrong from the protocol
// rather than from a status code alone.
func (h catalogHandler) handleCatalogRequest(w http.ResponseWriter, r *http.Request) {
	var msg CatalogRequestMessage
	body := http.MaxBytesReader(w, r.Body, maxCatalogRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !slices.Contains(msg.Context, ContextURL) {
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"@context must contain "+ContextURL)
		return
	}
	if msg.Type != CatalogRequestMessageType {
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"@type must be "+CatalogRequestMessageType)
		return
	}
	if msg.hasFilter() {
		// DSP leaves the filter expression implementation-defined, so a provider
		// cannot know what an arbitrary filter means. Returning the full catalog
		// would let a consumer that asked for a subset believe it received a
		// filtered view, which is a worse failure than a rejection.
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"catalog filtering is not implemented")
		return
	}
	writeJSON(w, http.StatusOK, buildCatalog(h.cfg))
}

// handleDatasetRequest serves one advertised dataset as a standalone document.
func (h catalogHandler) handleDatasetRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ds, ok := findDataset(h.cfg, id)
	if !ok {
		writeError(w, CatalogErrorType, http.StatusNotFound, "no dataset with id "+id)
		return
	}
	writeJSON(w, http.StatusOK, ds)
}
