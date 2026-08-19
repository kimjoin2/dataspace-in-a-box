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
