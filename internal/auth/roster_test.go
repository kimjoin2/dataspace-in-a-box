package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func writeRoster(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roster.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}
	return path
}

func encodedKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(pub)
}

func TestLoadRosterReadsParticipants(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	path := writeRoster(t, `{"participants":[{"id":"alice","public_key":"`+
		base64.RawURLEncoding.EncodeToString(pub)+`"}]}`)

	r, err := LoadRoster(path)
	if err != nil {
		t.Fatalf("LoadRoster: %v", err)
	}
	got, ok := r.KeyFor("alice")
	if !ok || !got.Equal(pub) {
		t.Errorf("KeyFor(alice) = %v, %v", got, ok)
	}
	if _, ok := r.KeyFor("mallory"); ok {
		t.Error("KeyFor answered for an id that is not in the roster")
	}
}

// A connector that starts with no usable roster can verify nobody. Failing at
// startup is louder — and far easier to diagnose — than starting fine and then
// refusing every counterparty.
func TestLoadRosterRejectsUnusableFiles(t *testing.T) {
	k := encodedKey(t)
	for name, body := range map[string]string{
		"not json":          `{`,
		"no participants":   `{"participants":[]}`,
		"missing id":        `{"participants":[{"public_key":"` + k + `"}]}`,
		"missing key":       `{"participants":[{"id":"alice"}]}`,
		"key is not base64": `{"participants":[{"id":"alice","public_key":"!!!!"}]}`,
		"key is wrong size": `{"participants":[{"id":"alice","public_key":"AAAA"}]}`,
		// Ambiguous trust is a configuration error, not a last-one-wins.
		"duplicate id": `{"participants":[{"id":"alice","public_key":"` + k + `"},` +
			`{"id":"alice","public_key":"` + encodedKey(t) + `"}]}`,
	} {
		if _, err := LoadRoster(writeRoster(t, body)); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
	if _, err := LoadRoster(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("a missing file loaded without error")
	}
}
