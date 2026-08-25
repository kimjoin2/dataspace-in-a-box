package dsp

import (
	"testing"

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
