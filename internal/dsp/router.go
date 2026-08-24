package dsp

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/auth"
	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// Routers is what NewRouter returns. It is a struct rather than a longer
// return list because a flat list would hand a call site several
// http.Handler values in a row with nothing to tell them apart by.
type Routers struct {
	// Protocol serves the DSP listener.
	Protocol http.Handler
	// Initiate holds the hooks that belong on the management listener: they
	// ask this connector to start an exchange, which is an operator action,
	// not a message from a counterparty. cmd/dsbox hands them to
	// internal/mgmt, so that package needs no opinion about this one.
	Initiate InitiateHandlers
	// Pulls counts the data pulls the protocol handler has in flight, and
	// CancelPulls ends them. The caller uses them in one order: cancel, then
	// wait. DECISIONS.md section 34.3 has the argument.
	Pulls       *sync.WaitGroup
	CancelPulls context.CancelFunc
}

// InitiateHandlers carries the initiate hooks by name. They are handlers on
// unexported types, which is fine: a method value is assignable to
// http.Handler without exporting anything.
type InitiateHandlers struct {
	Negotiation http.Handler
	Transfer    http.Handler
}

// NewRouter returns the handler for the public DSP listener, the initiate
// hooks that belong on the management listener, the WaitGroup counting the
// data pulls the protocol handler has in flight, and the function that
// cancels them. It takes the configuration because the catalog is served
// from it, and the store because negotiation state is persisted there.
//
// The WaitGroup and the cancel are returned rather than kept inside because
// the thing that uses them lives outside: a pull writes to the store on its
// way out, so the caller has to hold the store open until every pull it
// started has finished — and has to end those pulls first, or the wait is a
// wait for a copy the counterparty controls the length of. Nothing else in
// this package outlives a request.
//
// They are one mechanism and the caller uses them in one order: cancel,
// then wait. DECISIONS.md section 34.3 has the argument.
//
// The cancel carries a cause rather than being a bare one, which is what
// lets a pull tell a shutdown apart from every other reason its context
// could end. The returned closure is still a context.CancelFunc — that type
// is func() — so the caller holds one function and calls it once.
func NewRouter(cfg config.Config, st *store.Store, roster auth.Roster, signKey ed25519.PrivateKey) Routers {
	mux := http.NewServeMux()
	pulls := &sync.WaitGroup{}
	// The connector's lifetime, as every pull sees it. Cancelled by the
	// returned function at shutdown, which is what lets an in-flight copy
	// stop and record its outcome inside the caller's cap. The cause is what
	// a pull reads to attribute the stop: an error it can compare against is
	// the difference between "the connector shut down" and "something
	// cancelled this", and only the first is true of every cancellation this
	// function issues.
	pullCtx, cancelWithCause := context.WithCancelCause(context.Background())
	cancelPulls := func() { cancelWithCause(errConnectorShuttingDown) }

	cat := catalogHandler{cfg: cfg}
	mux.HandleFunc("POST "+VersionPath+"/catalog/request", cat.handleCatalogRequest)
	// The identifier is matched as a single path segment, which is why
	// configuration rejects one containing a slash.
	mux.HandleFunc("GET "+VersionPath+"/catalog/datasets/{id}", cat.handleDatasetRequest)

	// Non-nil only when there is a roster to consult. NewRouter's own rule
	// applies: with authentication off the check is absent, not silently
	// false.
	var knownParticipant func(string) bool
	if cfg.AuthRequired() {
		knownParticipant = func(id string) bool {
			_, ok := roster.KeyFor(id)
			return ok
		}
	}

	neg := negotiationHandler{cfg: cfg, store: st, knownParticipant: knownParticipant}
	mux.HandleFunc("POST "+VersionPath+"/negotiations/request", neg.handleContractRequest)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/request", neg.handleReRequest)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/events", neg.handleEvent)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/agreement/verification", neg.handleVerification)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/termination", neg.handleTermination)
	mux.HandleFunc("GET "+VersionPath+"/negotiations/{id}", neg.handleGetNegotiation)
	// The initiate hooks are not registered here. They are operator actions
	// and live on the management listener; NewRouter returns them so
	// cmd/dsbox can mount them there. Note what the removal leaves behind: a
	// POST to either old path now matches the GET route with a path
	// parameter and answers 405, not 404. The TCK fails immediately on a 404
	// and retries anything else, so a stale URL in its configuration
	// produces the slow diagnosis rather than the fast one.
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/offers", neg.handleOffers)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/agreement", neg.handleAgreement)

	// {id} on the five addressed transfer routes is this connector's own
	// generated provider pid, the same convention the provider-role
	// negotiation routes above use.
	tr := transferHandler{cfg: cfg, store: st, stepDelay: transferStepDelay, pulling: &sync.Map{}, pulls: pulls, pullCtx: pullCtx, knownParticipant: knownParticipant}
	mux.HandleFunc("POST "+VersionPath+"/transfers/request", tr.handleTransferRequest)

	data := dataHandler{cfg: cfg, store: st}
	mux.HandleFunc("GET "+VersionPath+"/data/{id}", data.handleData)
	mux.HandleFunc("GET "+VersionPath+"/transfers/{id}", tr.handleGetTransfer)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/start", tr.handleTransferStart)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/completion", tr.handleTransferCompletion)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/suspension", tr.handleTransferSuspension)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/termination", tr.handleTransferTermination)

	// Built above the early return below, not inside the authenticated
	// branch: with authentication off these still have to be non-nil, or the
	// management listener registers a route whose handler panics the moment
	// a caller gets past the token check.
	initiate := InitiateHandlers{
		Negotiation: http.HandlerFunc(neg.handleInitiate),
		Transfer:    http.HandlerFunc(tr.handleTransferInitiate),
	}

	if !cfg.AuthRequired() {
		// A disabled check is absent, not silently true. Installing a
		// middleware that always passes would leave a reader unable to tell
		// the two apart from the router alone.
		outer := http.NewServeMux()
		mountVersionEndpoint(outer)
		outer.Handle("/", mux)
		return Routers{Protocol: outer, Initiate: initiate, Pulls: pulls, CancelPulls: cancelPulls}
	}

	// Outbound is armed here rather than in each client, so "authentication
	// is on" is one decision made in one place. With it off the minter stays
	// the no-op default and nothing is attached.
	mintOutboundCredential = func(aud string) string {
		if aud == "" {
			// Nothing to address. Sending an unaddressed credential would be
			// worse than sending none: a token with an empty audience is one
			// any participant would accept as its own.
			slog.Warn("outbound message has no counterparty to address; sending it unsigned")
			return ""
		}
		tok, err := auth.Mint(signKey, cfg.ParticipantID, aud, time.Now(), credentialTTL)
		if err != nil {
			slog.Error("mint outbound credential", "aud", aud, "error", err)
			return ""
		}
		return "Bearer " + tok
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
	return Routers{Protocol: outer, Initiate: initiate, Pulls: pulls, CancelPulls: cancelPulls}
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

// credentialTTL is how long a credential this connector mints stays valid.
// Five minutes, from DECISIONS.md section 10. Short enough to bound replay,
// long enough that a whole TCK run (54 seconds) fits inside one token.
const credentialTTL = 5 * time.Minute
