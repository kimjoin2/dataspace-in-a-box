package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenerateThenLoadPrivateKeyRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.pem")
	pub, err := GenerateKeyFile(path)
	if err != nil {
		t.Fatalf("GenerateKeyFile: %v", err)
	}

	priv, err := LoadPrivateKey(path)
	if err != nil {
		t.Fatalf("LoadPrivateKey: %v", err)
	}
	// The proof that the pair belongs together is a signature the public half
	// verifies, not a byte comparison.
	tok, err := Mint(priv, "alice", "bob", time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := Verify(tok, staticKey("alice", pub), "bob", time.Now()); err != nil {
		t.Errorf("a token signed by the loaded key does not verify against the generated public key: %v", err)
	}
}

// A key file is a secret. Group and world readability is a mistake worth
// refusing to make silently.
func TestGenerateKeyFileIsNotReadableByOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.pem")
	if _, err := GenerateKeyFile(path); err != nil {
		t.Fatalf("GenerateKeyFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("mode = %o, want no group or world bits", perm)
	}
}

// Overwriting a key locks out every counterparty that has the old public half
// in its roster. That is not something to do by accident.
func TestGenerateKeyFileRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.pem")
	if _, err := GenerateKeyFile(path); err != nil {
		t.Fatalf("first GenerateKeyFile: %v", err)
	}
	if _, err := GenerateKeyFile(path); err == nil {
		t.Error("overwrote an existing key file")
	}
}

func TestLoadPrivateKeyRejectsUnusableFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"not pem":        "hello",
		"wrong pem type": "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
		"pem, not a key": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".pem")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := LoadPrivateKey(path); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
	if _, err := LoadPrivateKey(filepath.Join(dir, "absent.pem")); err == nil {
		t.Error("a missing file loaded without error")
	}
}

// An RSA key in a correctly-formed PKCS#8 file is the realistic wrong-key
// case, and it must not load as an Ed25519 key.
func TestLoadPrivateKeyRejectsANonEd25519Key(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rsa.pem")
	writeNonEd25519Key(t, path)
	if _, err := LoadPrivateKey(path); err == nil {
		t.Error("an RSA key loaded as an Ed25519 key")
	}
}

var _ = ed25519.PublicKeySize

// writeNonEd25519Key writes a valid PKCS#8 PEM holding an RSA key, so the
// only thing wrong with it is the algorithm.
func writeNonEd25519Key(t *testing.T, path string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	body := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
