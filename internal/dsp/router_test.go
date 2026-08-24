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
