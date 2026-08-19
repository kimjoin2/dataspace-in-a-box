package main

import (
	"crypto/ed25519"
	"encoding/base64"
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

func TestSubcommandsRequireTheirFlags(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"nonsense"},
		{"keygen"},
		{"token", "-iss", "alice", "-aud", "bob"},
		{"token", "-key", "/nonexistent", "-iss", "alice", "-aud", "bob"},
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
