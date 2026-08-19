package dsp

import (
	"crypto/ed25519"
	"net/http"

	"github.com/kimjoin2/dataspace-in-a-box/internal/auth"
	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// NewRouter returns the handler for the public DSP listener. It takes the
// configuration because the catalog is served from it, and the store because
// negotiation state is persisted there.
func NewRouter(cfg config.Config, st *store.Store, roster auth.Roster, signKey ed25519.PrivateKey) http.Handler {
	mux := http.NewServeMux()

	cat := catalogHandler{cfg: cfg}
	mux.HandleFunc("POST "+VersionPath+"/catalog/request", cat.handleCatalogRequest)
	// The identifier is matched as a single path segment, which is why
	// configuration rejects one containing a slash.
	mux.HandleFunc("GET "+VersionPath+"/catalog/datasets/{id}", cat.handleDatasetRequest)

	neg := negotiationHandler{cfg: cfg, store: st}
	mux.HandleFunc("POST "+VersionPath+"/negotiations/request", neg.handleContractRequest)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/request", neg.handleReRequest)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/events", neg.handleEvent)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/agreement/verification", neg.handleVerification)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/termination", neg.handleTermination)
	mux.HandleFunc("GET "+VersionPath+"/negotiations/{id}", neg.handleGetNegotiation)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/initiate", neg.handleInitiate)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/offers", neg.handleOffers)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/agreement", neg.handleAgreement)

	// {id} on the five addressed transfer routes is this connector's own
	// generated provider pid, the same convention the provider-role
	// negotiation routes above use.
	tr := transferHandler{cfg: cfg, store: st, stepDelay: transferStepDelay}
	mux.HandleFunc("POST "+VersionPath+"/transfers/request", tr.handleTransferRequest)
	mux.HandleFunc("POST "+VersionPath+"/transfers/initiate", tr.handleTransferInitiate)
	mux.HandleFunc("GET "+VersionPath+"/transfers/{id}", tr.handleGetTransfer)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/start", tr.handleTransferStart)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/completion", tr.handleTransferCompletion)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/suspension", tr.handleTransferSuspension)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/termination", tr.handleTransferTermination)

	if !cfg.AuthRequired() {
		// A disabled check is absent, not silently true. Installing a
		// middleware that always passes would leave a reader unable to tell
		// the two apart from the router alone.
		outer := http.NewServeMux()
		mountVersionEndpoint(outer)
		outer.Handle("/", mux)
		return outer
	}

	// The version endpoint is mounted outside the wrap rather than exempted
	// inside it. A path comparison in the middleware is a list someone has to
	// remember to update; a route that is simply not behind the check cannot
	// drift. It stays open because it is how a counterparty discovers what to
	// speak before it has any context, and it discloses only a protocol
	// version.
	outer := http.NewServeMux()
	mountVersionEndpoint(outer)
	outer.Handle("/", requireParticipant(roster, cfg.ParticipantID, mux))
	return outer
}

// mountVersionEndpoint puts the version document on a mux, in two patterns.
// The second is not redundant: the catch-all that routes everything else into
// the protocol mux would otherwise swallow a non-GET request to this path and
// answer 404, where the honest answer is 405 — the path exists, the method
// does not. A method-less pattern is less specific than the GET one, so GET
// still reaches the handler and everything else lands here.
func mountVersionEndpoint(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/dspace-version", handleVersion)
	mux.HandleFunc("/.well-known/dspace-version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
}
