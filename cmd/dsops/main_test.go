package main

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
)

// runDsops runs a subcommand and returns what it printed. The harness and the
// demo both depend on these two commands composing, so their contract is a
// test rather than a README line.
func runDsops(t *testing.T, args ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := run(args, f); err != nil {
		f.Close()
		t.Fatalf("dsops %s: %v", strings.Join(args, " "), err)
	}
	f.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// runDsopsExpectingFailure is runDsops's negative twin: it returns the error
// instead of ending the test, which is the only way to assert that a
// subcommand refused. run writes to an *os.File, so what it prints on the way
// to failing goes to the null device rather than to io.Discard.
func runDsopsExpectingFailure(t *testing.T, args ...string) error {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	return run(args, f)
}

func TestKeygenThenTokenVerifies(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "k.pem")

	pubB64 := runDsops(t, "keygen", "-out", keyPath)
	pub, err := base64.RawURLEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatalf("keygen printed an unusable public key %q: %v", pubB64, err)
	}

	tok := runDsops(t, "token", "-key", keyPath, "-iss", "alice", "-aud", "bob")
	keys := func(id string) (ed25519.PublicKey, bool) { return ed25519.PublicKey(pub), id == "alice" }
	if _, err := auth.Verify(tok, keys, "bob", time.Now()); err != nil {
		t.Fatalf("the credential dsops minted does not verify: %v", err)
	}
}

// TestRosterSignThenLoadVerifies is roster sign and resolve's counterpart to
// TestKeygenThenTokenVerifies: the harness, the demo, and an operator
// following the README all depend on `dsops roster sign`'s printed value
// pasting straight into a roster's own "signature" field and verifying.
func TestRosterSignThenLoadVerifies(t *testing.T) {
	participantPub := runDsops(t, "keygen", "-out", filepath.Join(t.TempDir(), "participant.key"))
	operatorKeyPath := filepath.Join(t.TempDir(), "operator.key")
	operatorPubB64 := runDsops(t, "keygen", "-out", operatorKeyPath)
	operatorPub, err := base64.RawURLEncoding.DecodeString(operatorPubB64)
	if err != nil {
		t.Fatalf("keygen printed an unusable public key %q: %v", operatorPubB64, err)
	}

	rosterPath := filepath.Join(t.TempDir(), "roster.json")
	// One value for both copies: the signature covers the expiry, so reading
	// the clock again below would sign one document and load another.
	fields := `,"version":1,"expires_at":"` + time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339) + `"`
	unsigned := `{"participants":[{"id":"alice","public_key":"` + participantPub + `"}]` + fields + `}`
	if err := os.WriteFile(rosterPath, []byte(unsigned), 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}

	sig := runDsops(t, "roster", "sign", "-roster", rosterPath, "-key", operatorKeyPath)
	signed := `{"participants":[{"id":"alice","public_key":"` + participantPub + `"}]` + fields + `,"signature":"` + sig + `"}`
	if err := os.WriteFile(rosterPath, []byte(signed), 0o600); err != nil {
		t.Fatalf("write signed roster: %v", err)
	}

	if _, err := auth.LoadRoster(rosterPath, ed25519.PublicKey(operatorPub), time.Now()); err != nil {
		t.Fatalf("the roster dsops signed does not load: %v", err)
	}
}

// Signing a roster the connector would refuse prints a success the operator
// acts on, and a boot failure days later.
func TestRosterSignRefusesWhatLoadRosterWouldRefuse(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "operator.pem")
	runDsops(t, "keygen", "-out", keyPath)
	// keygen prints the base64url public key, which is the form a roster
	// entry carries — the same way TestKeygenThenTokenVerifies gets one.
	participantPub := runDsops(t, "keygen", "-out", filepath.Join(dir, "participant.pem"))
	participants := `[{"id":"alice","public_key":"` + participantPub + `"}]`
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	for name, body := range map[string]string{
		"no version":         `{"participants":` + participants + `,"expires_at":"` + future + `"}`,
		"version zero":       `{"participants":` + participants + `,"version":0,"expires_at":"` + future + `"}`,
		"no expiry":          `{"participants":` + participants + `,"version":1}`,
		"malformed expiry":   `{"participants":` + participants + `,"version":1,"expires_at":"2027-01-01"}`,
		"expiry in the past": `{"participants":` + participants + `,"version":1,"expires_at":"` + past + `"}`,
		"expiry beyond the cap": `{"participants":` + participants + `,"version":1,"expires_at":"` +
			time.Now().Add(500*24*time.Hour).UTC().Format(time.RFC3339) + `"}`,
	} {
		path := filepath.Join(dir, "roster.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := runDsopsExpectingFailure(t, "roster", "sign", "-roster", path, "-key", keyPath); err == nil {
			t.Errorf("%s: dsops roster sign succeeded for a roster the connector would refuse", name)
		}
	}
}

func TestResolveDIDWeb(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"verificationMethod":[{"publicKeyJwk":{"kty":"OKP","crv":"Ed25519","x":"` +
			base64.RawURLEncoding.EncodeToString(pub) + `"}}]}`))
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	id := "did:web:" + strings.ReplaceAll(host, ":", "%3A")
	got := runDsops(t, "resolve", "-allow-http", id)
	if got != base64.RawURLEncoding.EncodeToString(pub) {
		t.Errorf("resolve %s = %q, want the server's own key", id, got)
	}
}

func TestSubcommandsRequireTheirFlags(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"nonsense"},
		{"keygen"},
		{"token", "-iss", "alice", "-aud", "bob"},
		{"token", "-key", "/nonexistent", "-iss", "alice", "-aud", "bob"},
		{"roster"},
		{"roster", "bogus"},
		{"roster", "sign"},
		{"roster", "sign", "-roster", "/nonexistent", "-key", "/nonexistent"},
		{"resolve"},
		{"resolve", "not-a-did-web-id"},
	} {
		f, err := os.Create(filepath.Join(t.TempDir(), "out"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		err = run(args, f)
		f.Close()
		if err == nil {
			t.Errorf("dsops %s succeeded", strings.Join(args, " "))
		}
	}
}
