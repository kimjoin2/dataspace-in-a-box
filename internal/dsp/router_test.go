package dsp

import (
	"crypto/ed25519"
	"encoding/base64"
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

// NewRouter returns from more than one place and the authentication-off
// return is easy to miss. A nil Initiate handler is not caught by the
// management listener's route-coverage test, because a nil handler behind
// authenticated still answers 401 to an anonymous request — the panic only
// arrives once a caller authenticates, and neither harness runs with
// authentication off.
func TestNewRouterReturnsInitiateHandlersWithAuthenticationOff(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	off := false
	cfg := config.Config{
		ParticipantID: "urn:participant:self",
		PublicURL:     "http://self:8080",
		DevMode:       true,
		RequireAuth:   &off,
	}

	// Without this the test could pass by taking the authenticated path,
	// which is not the return it exists to cover.
	if cfg.AuthRequired() {
		t.Fatal("the config above no longer selects the authentication-off path")
	}

	r := NewRouter(cfg, st, auth.Roster{}, nil)
	if r.Initiate.Negotiation == nil || r.Initiate.Transfer == nil {
		t.Fatal("NewRouter returned a nil initiate handler on the authentication-off path")
	}
}

// RosterUsable is how something holding no roster of its own asks whether
// this connector's is still usable, and nil is how "there is no roster to
// expire" is expressed. That convention is what makes absence different from
// a refusal, and it is easy to destroy while every other test stays green:
// populating the field from the guard's method rather than its predicate
// compiles, answers the same way today, and is never nil.
//
// No t.Parallel: the authenticated branch assigns mintOutboundCredential,
// which is a package variable.
func TestNewRouterReturnsRosterUsableOnlyWithAuthenticationOn(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	restoreMinter := mintOutboundCredential
	t.Cleanup(func() { mintOutboundCredential = restoreMinter })

	base := config.Config{
		ParticipantID: "urn:participant:self",
		PublicURL:     "http://self:8080",
		DevMode:       true,
	}

	off := base
	no := false
	off.RequireAuth = &no
	r := NewRouter(off, st, auth.Roster{}, nil)
	t.Cleanup(r.CancelPulls)
	if r.RosterUsable != nil {
		t.Error("RosterUsable is set with authentication off, so absence reads as an answer about a roster this connector does not hold")
	}

	// A zero roster is enough here. Whether the predicate exists is decided
	// by the configuration alone; what it would answer is not this test's
	// subject, and calling it is what the expiry tests do.
	on := NewRouter(base, st, auth.Roster{}, nil)
	t.Cleanup(on.CancelPulls)
	if on.RosterUsable == nil {
		t.Error("RosterUsable is nil with authentication on, so nothing outside this package can ask whether the roster expired")
	}
}

// rosterListing builds a signed roster naming one participant and carrying no
// address for it, and loads it the way a connector does. The participant is
// listed, so the membership check passes and the address predicate is what
// answers — which is the only way a test can tell whether NewRouter handed a
// hook that predicate at all.
func rosterListing(t *testing.T, id string) auth.Roster {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signerPub, signerPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	path := filepath.Join(t.TempDir(), "roster.json")
	// Held in one variable and interpolated into both copies of the document:
	// the signature covers the expiry, so a second reading of the clock would
	// sign one value and load another.
	fields := `,"version":1,"expires_at":"` + time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339) + `"`
	participants := `[{"id":"` + id + `","public_key":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}]`
	if err := os.WriteFile(path, []byte(`{"participants":`+participants+fields+`}`), 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}
	sig, err := auth.SignRoster(path, signerPriv, time.Now())
	if err != nil {
		t.Fatalf("SignRoster: %v", err)
	}
	signed := `{"participants":` + participants + fields + `,"signature":"` + sig + `"}`
	if err := os.WriteFile(path, []byte(signed), 0o600); err != nil {
		t.Fatalf("write signed roster: %v", err)
	}
	roster, err := auth.LoadRoster(path, signerPub, time.Now())
	if err != nil {
		t.Fatalf("LoadRoster: %v", err)
	}
	return roster
}

// NewRouter handing the address predicate to the initiate hooks is wiring no
// handler test can see: those build their handler as a struct literal and set
// the field themselves. Deleting the assignment leaves them all green and
// silently restores the caller's authority over the address that section 35.5
// closed. This is the same shape of hole cmd/dsbox/roster_version_test.go
// guards for its own wiring.
//
// No t.Parallel: the authenticated branch assigns mintOutboundCredential,
// which is a package variable.
func TestNewRouterGivesTheInitiateHooksTheRosterAddress(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	restoreMinter := mintOutboundCredential
	t.Cleanup(func() { mintOutboundCredential = restoreMinter })

	cfg := config.Config{ParticipantID: testSelf, PublicURL: "http://self:8080"}
	if !cfg.AuthRequired() {
		t.Fatal("the config above no longer selects the authenticated path, where the predicate is built")
	}
	// A roster listing the participant with no address. The predicate is
	// therefore non-nil and answers false, which the hooks must refuse — and
	// they can only refuse it if NewRouter handed them the predicate at all.
	routers := NewRouter(cfg, st, rosterListing(t, testPeer), nil)
	t.Cleanup(routers.CancelPulls)

	// The request's own address is a documentation-range literal, so the real
	// guard this router carries admits it without resolving a name and the
	// refusal below can only be the roster's answer.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/negotiations/initiate",
		strings.NewReader(`{"providerId":"`+testPeer+`","offerId":"o",`+
			`"datasetId":"d","connectorAddress":"`+testSendableAddress+`"}`))
	routers.Initiate.Negotiation.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "connector_address") {
		t.Errorf("initiate = %d %s; NewRouter did not hand the hook the roster address predicate",
			rec.Code, rec.Body)
	}
}
