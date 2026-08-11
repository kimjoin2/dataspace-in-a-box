package dsp

import (
	"net/http"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

// NewRouter returns the handler for the public DSP listener. It takes the
// configuration because the catalog is served from it: what this participant
// advertises is a declaration, not runtime state.
func NewRouter(cfg config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/dspace-version", handleVersion)

	cat := catalogHandler{cfg: cfg}
	mux.HandleFunc("POST "+VersionPath+"/catalog/request", cat.handleCatalogRequest)
	// The identifier is matched as a single path segment, which is why
	// configuration rejects one containing a slash.
	mux.HandleFunc("GET "+VersionPath+"/catalog/datasets/{id}", cat.handleDatasetRequest)

	// Contract negotiation and transfer process mount here next, in TCK order.
	// Until then, requests below those paths are correctly 404.

	return mux
}
