package dsp

import "net/http"

// NewRouter returns the handler for the public DSP listener.
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/dspace-version", handleVersion)

	// Protocol routes mount under VersionPath and are added one protocol at a
	// time, in TCK order: catalog, contract negotiation, transfer process.
	// Until then, requests below that path are correctly 404.

	return mux
}
