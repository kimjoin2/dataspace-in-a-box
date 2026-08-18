package dsp

import (
	"net/http"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// NewRouter returns the handler for the public DSP listener. It takes the
// configuration because the catalog is served from it, and the store because
// negotiation state is persisted there.
func NewRouter(cfg config.Config, st *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/dspace-version", handleVersion)

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

	return mux
}
