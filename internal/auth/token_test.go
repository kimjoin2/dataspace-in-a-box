package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// staticKey answers for exactly one issuer, which is what every test here
// needs: the roster is a separate concern with its own tests.
func staticKey(id string, pub ed25519.PublicKey) func(string) (ed25519.PublicKey, bool) {
	return func(got string) (ed25519.PublicKey, bool) {
		if got != id {
			return nil, false
		}
		return pub, true
	}
}

func noKeys(string) (ed25519.PublicKey, bool) { return nil, false }

func mustMint(t *testing.T, priv ed25519.PrivateKey, iss, aud string, now time.Time) string {
	t.Helper()
	tok, err := Mint(priv, iss, aud, now, 5*time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	return tok
}

// tamperPayload edits a claim and leaves the signature untouched, which is
// exactly what an attacker who cannot sign would do.
func tamperPayload(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	claims["aud"] = "mallory"
	edited, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(edited)
	return strings.Join(parts, ".")
}

// mintWithHeaderAlg builds a token whose only defect is the header's alg.
// The signature is a real Ed25519 signature over the real signing input, so
// a verifier that ignored the header would accept it — which is the point.
func mintWithHeaderAlg(t *testing.T, priv ed25519.PrivateKey, alg, iss, aud string, now time.Time) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"iss": iss, "aud": aud,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(priv, []byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// mintWithoutIat builds a token carrying no iat at all, which RFC 7519
// permits. Modelled on mintWithHeaderAlg rather than on tamperPayload: the
// signature here is a real signature over the real signing input, so the
// token reaches the claim checks instead of being refused at the signature.
func mintWithoutIat(t *testing.T, priv ed25519.PrivateKey, iss, aud string, now time.Time, ttl time.Duration) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": Algorithm, "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"iss": iss, "aud": aud,
		"exp": now.Add(ttl).Unix(),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	// Assert the absence rather than trust it. A later edit that reintroduced
	// iat would leave the test that uses this helper green while it tested
	// nothing.
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, ok := got["iat"]; ok {
		t.Fatalf("payload carries iat: %s", payload)
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(priv, []byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestMintVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	tok, err := Mint(priv, "urn:participant:alice", "urn:participant:bob", now, 5*time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	iss, err := Verify(tok, staticKey("urn:participant:alice", pub), "urn:participant:bob", now.Add(time.Second))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if iss != "urn:participant:alice" {
		t.Errorf("iss = %q", iss)
	}
}

// Each row is a way a token can be wrong. Every one must be refused, and the
// sentinel says which — the caller logs it and never echoes it.
func TestVerifyRefusals(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	good := func() string { return mustMint(t, priv, "alice", "bob", now) }

	for _, c := range []struct {
		name  string
		token string
		keys  func(string) (ed25519.PublicKey, bool)
		aud   string
		at    time.Time
		want  error
	}{
		{"not three segments", "a.b", staticKey("alice", pub), "bob", now, ErrMalformed},
		{"payload edited after signing", tamperPayload(t, good()), staticKey("alice", pub), "bob", now, ErrBadSignature},
		{"signed by a key the roster does not have", mustMint(t, otherPriv, "alice", "bob", now), staticKey("alice", pub), "bob", now, ErrBadSignature},
		{"issuer not in the roster", good(), noKeys, "bob", now, ErrUnknownIssuer},
		// The timing here is what bounds clockLeeway from above. Moving it
		// means moving that constant, deliberately.
		{"expired", good(), staticKey("alice", pub), "bob", now.Add(6 * time.Minute), ErrExpired},
		{"addressed to someone else", good(), staticKey("alice", pub), "carol", now, ErrWrongAudience},
	} {
		if _, err := Verify(c.token, c.keys, c.aud, c.at); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

// Alg confusion is the failure this hand-written verifier exists to avoid.
// The header must never select the algorithm — it is compared against the one
// value this connector accepts, and everything else is refused before any key
// is consulted.
func TestVerifyRefusesAnyAlgorithmButEdDSA(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	for _, alg := range []string{"none", "HS256", "RS256", "eddsa", ""} {
		tok := mintWithHeaderAlg(t, priv, alg, "alice", "bob", now)
		if _, err := Verify(tok, staticKey("alice", pub), "bob", now); !errors.Is(err, ErrBadAlgorithm) {
			t.Errorf("alg %q: err = %v, want ErrBadAlgorithm", alg, err)
		}
	}
}

// A slow clock costs a minute, not every request. Before this, an issuer
// whose clock lagged the verifier by more than the credential's life was
// refused on every call, and the reason was hidden from it.
func TestVerifyAcceptsTokenExpiredWithinLeeway(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	tok := mustMint(t, priv, "alice", "bob", now)
	// Minted for five minutes, checked thirty seconds after it lapsed.
	if _, err := Verify(tok, staticKey("alice", pub), "bob", now.Add(5*time.Minute+30*time.Second)); err != nil {
		t.Errorf("token expired by 30s: err = %v, want accepted", err)
	}
}

// The lifetime is measured against the verifier's own clock, so no issuer can
// buy one by moving its claims. A token dated a year ahead carries a
// perfectly ordinary hour between iat and exp — which is why the quantity
// being measured has to be the distance from now.
func TestVerifyRefusesLifetimeBoughtByAClockRunningAhead(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	aYearAhead := now.Add(365 * 24 * time.Hour)
	tok := mustMint(t, priv, "alice", "bob", aYearAhead) // iat and exp both a year out
	if _, err := Verify(tok, staticKey("alice", pub), "bob", now); !errors.Is(err, ErrLifetimeTooLong) {
		t.Errorf("five minutes dated a year ahead: err = %v, want %v", err, ErrLifetimeTooLong)
	}
}

// The maximum's value, bracketed from both sides with durations that never
// mention the constant. A fixture written as `maxCredentialLifetime + x`
// tracks whatever the constant becomes and so pins nothing — measured: with
// such a fixture, changing the maximum to a day leaves the whole suite green.
//
// The lower bracket is the load-bearing one. The TCK harness mints for longer
// than this connector does, so a maximum set too small refuses the entire
// suite. That belongs in a unit test rather than being discovered by a
// harness run.
func TestTheMaximumLifetimeBracketsTheHarnessAndRefusesAbsurdity(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	mint := func(ttl time.Duration) string {
		t.Helper()
		tok, err := Mint(priv, "alice", "bob", now, ttl)
		if err != nil {
			t.Fatalf("Mint: %v", err)
		}
		return tok
	}

	// What the harness needs. If this fails, `make tck` fails too — and this
	// says so in under a second instead of after a container build.
	if _, err := Verify(mint(30*time.Minute), staticKey("alice", pub), "bob", now); err != nil {
		t.Errorf("a thirty-minute credential: err = %v, want accepted — the TCK harness mints one", err)
	}
	for name, ttl := range map[string]time.Duration{
		"two hours": 2 * time.Hour,
		"a year":    365 * 24 * time.Hour,
	} {
		if _, err := Verify(mint(ttl), staticKey("alice", pub), "bob", now); !errors.Is(err, ErrLifetimeTooLong) {
			t.Errorf("%s: err = %v, want %v", name, err, ErrLifetimeTooLong)
		}
	}
}

// The maximum is a policy about a claim, so it is read only after the
// signature says the claim is the issuer's. A token that is both over-long
// and badly signed is refused for the signature.
func TestVerifyChecksSignatureBeforeLifetime(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	tok, err := Mint(otherPriv, "alice", "bob", now, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := Verify(tok, staticKey("alice", pub), "bob", now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("over-long and badly signed: err = %v, want %v", err, ErrBadSignature)
	}
}

// A counterparty may omit iat — RFC 7519 leaves it optional — and this
// connector reads none, so it must not refuse one for that.
func TestVerifyAcceptsTokenWithoutIat(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	tok := mintWithoutIat(t, priv, "alice", "bob", now, 5*time.Minute)
	if _, err := Verify(tok, staticKey("alice", pub), "bob", now); err != nil {
		t.Errorf("a token with no iat: err = %v, want accepted", err)
	}
}
