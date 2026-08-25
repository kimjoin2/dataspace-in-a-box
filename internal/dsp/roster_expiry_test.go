package dsp

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/auth"
	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// expiredRouter mirrors authedRouter with a roster that has since expired.
// The construction is the only one available: LoadRoster refuses an expired
// document, so the roster is loaded at an instant before its expiry and is
// dead by the time any request arrives.
//
// It restores mintOutboundCredential. NewRouter assigns that package
// variable on the authenticated path and never puts it back, so without this
// the closure built here — holding this router's participant id and a nil
// signing key — outlives the test and stands in for the package default in
// everything that runs after it.
func expiredRouter(t *testing.T) (http.Handler, InitiateHandlers) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signerPub, signerPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Expired an hour ago, signed and loaded a minute before that instant.
	// Both SignRoster and LoadRoster refuse a document already past its
	// expiry, so the only way to hold a dead roster is to have loaded it
	// while it was still alive.
	expiry := time.Now().Add(-time.Hour)
	loadedAt := expiry.Add(-time.Minute)

	path := filepath.Join(t.TempDir(), "roster.json")
	// Held in one variable and interpolated into both copies of the
	// document: the signature covers the expiry, so building the string
	// twice would sign one value and load another.
	fields := `,"version":1,"expires_at":"` + expiry.UTC().Format(time.RFC3339) + `"`
	participants := `[{"id":"` + testPeer + `","public_key":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}]`
	if err := os.WriteFile(path, []byte(`{"participants":`+participants+fields+`}`), 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}
	sig, err := auth.SignRoster(path, signerPriv, loadedAt)
	if err != nil {
		t.Fatalf("SignRoster: %v", err)
	}
	signed := `{"participants":` + participants + fields + `,"signature":"` + sig + `"}`
	if err := os.WriteFile(path, []byte(signed), 0o600); err != nil {
		t.Fatalf("write signed roster: %v", err)
	}
	roster, err := auth.LoadRoster(path, signerPub, loadedAt)
	if err != nil {
		t.Fatalf("LoadRoster: %v", err)
	}

	restoreMinter := mintOutboundCredential
	t.Cleanup(func() { mintOutboundCredential = restoreMinter })

	cfg := config.Config{
		PublicURL:     "https://connector.example.org",
		ParticipantID: testSelf,
		Datasets:      []config.Dataset{{ID: "urn:dataset:a"}},
	}
	routers := NewRouter(cfg, st, roster, nil)
	// Nothing here starts a pull, so this only keeps the pull context from
	// outliving the test.
	t.Cleanup(routers.CancelPulls)
	return routers.Protocol, routers.Initiate
}

// Every DSP route refuses once the roster has expired. 409 and not 401: the
// caller's credential may be perfect and the fault is local. 409 and not
// 503: this repository's own wire contract records that a 5xx raises
// immediately on the TCK's negative paths, exactly like the 404 that
// DECISIONS.md section 25.1 forbids.
func TestExpiredRosterRefusesEveryDSPRequest(t *testing.T) {
	handler, _ := expiredRouter(t)
	for _, rt := range dspRoutes(t) {
		if openRoutes[rt.path] {
			continue
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}")))
		if rec.Code != http.StatusConflict {
			t.Errorf("%s %s: got %d, want 409 — an expired connector is still answering counterparties",
				rt.method, rt.path, rec.Code)
		}
	}
}

// The version document keeps answering after the expiry. "Stops serving" is
// not "answers nothing", and DECISIONS.md section 36.4 makes that claim
// permanently, so it is held here rather than left to the structure.
//
// TestVersionEndpointStaysOpen cannot stand in for this. It builds
// authedRouter, whose roster is still good, so it pins that the route is
// outside the credential check and says nothing about what happens past
// expires_at. TestExpiredRosterRefusesEveryDSPRequest cannot either: it skips
// openRoutes, which is exactly this path.
//
// The status is asserted rather than the body: 200 separates this from both
// refusals the expired router can produce, the 401 a wrapped route would give
// an unauthenticated caller and the 409 the guard would give ahead of it.
func TestExpiredRosterStillServesTheVersionDocument(t *testing.T) {
	handler, _ := expiredRouter(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/dspace-version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — an expired connector still tells a counterparty what it speaks", rec.Code)
	}
}

// The hooks live on the management listener, which requireParticipant never
// wraps. Without their own check an expired connector refuses every
// counterparty and goes on starting exchanges and signing with its real key.
//
// The body is empty on purpose. That trips the required-field check, so this
// also pins that the expiry check runs first: move it below and this
// answers 400.
func TestExpiredRosterRefusesTheInitiateHooks(t *testing.T) {
	_, initiate := expiredRouter(t)
	for name, h := range map[string]http.Handler{
		"negotiations": initiate.Negotiation,
		"transfers":    initiate.Transfer,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("{}")))
		if rec.Code != http.StatusConflict {
			t.Errorf("%s/initiate: got %d, want 409 — an expired connector must not start an exchange",
				name, rec.Code)
		}
	}
}

// The refusal names the roster rather than the caller's credential.
func TestTheExpiryRefusalNamesTheRoster(t *testing.T) {
	handler, _ := expiredRouter(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", VersionPath+"/catalog/request", strings.NewReader("{}")))
	if !strings.Contains(strings.ToLower(rec.Body.String()), "roster") {
		t.Errorf("body %q does not say what is wrong", rec.Body)
	}
}

// The refusal is logged once, not once per request. An expired connector
// answers every request forever, and a per-request warning buries the line
// an operator is looking for.
//
// No t.Parallel: slog.Default is process-global, so a parallel sibling would
// be logging into this test's buffer.
func TestTheExpiryWarningIsLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })

	// Each surface is asserted to have actually refused for this reason. A
	// count of one is also what a connector where only one of them refuses
	// produces, so without these the assertion below reads as "one warning
	// across every surface" while measuring something weaker.
	handler, initiate := expiredRouter(t)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", VersionPath+"/catalog/request", strings.NewReader("{}")))
		if rec.Code != http.StatusConflict {
			t.Fatalf("catalog request: got %d, want 409 — the listener did not refuse, so the count below proves nothing about it", rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	initiate.Negotiation.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("{}")))
	if rec.Code != http.StatusConflict {
		t.Fatalf("negotiations/initiate: got %d, want 409 — the hook did not refuse, so the count below proves nothing about it", rec.Code)
	}
	// The outbound side warns without writing a response, so it is asserted
	// through the minter rather than a status. Without this call it sits
	// outside the window, and giving it a slog.Warn of its own instead of
	// the guard's would leave the count below unchanged.
	if _, maySend := mintOutboundCredential(testPeer); maySend {
		t.Fatalf("the minter permitted a send, so the count below proves nothing about it")
	}

	if n := strings.Count(buf.String(), "roster has expired"); n != 1 {
		t.Errorf("the expiry was logged %d times across every surface, want once", n)
	}
}

// An expired connector that keeps sending is worse than one that stops: it
// signs with its real key while refusing every reply, so the exchange it
// starts cannot finish.
func TestExpiredRosterSendsNothing(t *testing.T) {
	expiredRouter(t) // installs the minter and restores it on cleanup
	if _, maySend := mintOutboundCredential("urn:participant:peer"); maySend {
		t.Error("the minter permitted a send under an expired roster")
	}
}

// The branches that are not about expiry keep their present behaviour. This
// milestone deliberately does not decide whether they should.
//
// authedRouter's signKey is nil, which auth.Mint rejects as the wrong size,
// so a non-empty audience lands in the minting-error branch and an empty one
// in the branch above it. Each needs its own assertion: with only the
// empty-audience call, flipping the minting-error branch to refuse passes
// every gate in this repository, because nothing else here makes Mint fail.
func TestTheMinterStillPermitsItsOtherFailures(t *testing.T) {
	restore := mintOutboundCredential
	t.Cleanup(func() { mintOutboundCredential = restore })
	authedRouter(t)
	if _, maySend := mintOutboundCredential(""); !maySend {
		t.Error("an empty audience must proceed unsigned, as it does today")
	}
	if _, maySend := mintOutboundCredential(testPeer); !maySend {
		t.Error("a minting failure must proceed unsigned, as it does today")
	}
}

// With authentication off there is no roster, and the package default must
// permit — otherwise a dev-mode connector silently sends nothing.
func TestTheDefaultMinterPermits(t *testing.T) {
	restore := mintOutboundCredential
	t.Cleanup(func() { mintOutboundCredential = restore })
	mintOutboundCredential = defaultMintOutboundCredential
	if _, maySend := mintOutboundCredential("urn:participant:peer"); !maySend {
		t.Error("the default refused; a connector with authentication off would send nothing")
	}
}
