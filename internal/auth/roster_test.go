package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

func testSigner(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// signedRosterBody signs participantsJSON — a JSON array literal, valid or
// deliberately malformed — with priv, and returns a complete roster
// document carrying both. Unmarshaling a malformed entry (a missing key, a
// bad key encoding) into []rosterEntry does not itself fail — json.Unmarshal
// leaves the zero value for anything absent — so this can sign every fixture
// TestLoadRosterRejectsUnusableFiles needs: LoadRoster's own precedence (see
// its doc comment, "nothing is trusted before the signature verifies") means
// signature verification runs before those per-participant checks, so a
// fixture with no signature would be rejected for that reason instead of the
// one each case names.
func signedRosterBody(t *testing.T, participantsJSON string, priv ed25519.PrivateKey) string {
	t.Helper()
	var entries []rosterEntry
	if err := json.Unmarshal([]byte(participantsJSON), &entries); err != nil {
		t.Fatalf("signedRosterBody: participantsJSON does not parse as []rosterEntry: %v", err)
	}
	sig := ed25519.Sign(priv, canonicalRosterBytes(entries))
	return fmt.Sprintf(`{"participants":%s,"signature":%q}`, participantsJSON, base64.RawURLEncoding.EncodeToString(sig))
}

func TestLoadRosterReadsParticipants(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signerPub, signerPriv := testSigner(t)
	participants := `[{"id":"alice","public_key":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}]`
	path := writeRoster(t, signedRosterBody(t, participants, signerPriv))

	r, err := LoadRoster(path, signerPub)
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
	signerPub, signerPriv := testSigner(t)
	k := encodedKey(t)
	for name, participantsJSON := range map[string]string{
		"missing id":        `[{"public_key":"` + k + `"}]`,
		"missing key":       `[{"id":"alice"}]`,
		"key is not base64": `[{"id":"alice","public_key":"!!!!"}]`,
		"key is wrong size": `[{"id":"alice","public_key":"AAAA"}]`,
		// Ambiguous trust is a configuration error, not a last-one-wins.
		"duplicate id": `[{"id":"alice","public_key":"` + k + `"},` +
			`{"id":"alice","public_key":"` + encodedKey(t) + `"}]`,
	} {
		body := signedRosterBody(t, participantsJSON, signerPriv)
		if _, err := LoadRoster(writeRoster(t, body), signerPub); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
	for name, body := range map[string]string{
		"not json":        `{`,
		"no participants": `{"participants":[]}`,
	} {
		if _, err := LoadRoster(writeRoster(t, body), signerPub); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
	if _, err := LoadRoster(filepath.Join(t.TempDir(), "absent.json"), signerPub); err == nil {
		t.Error("a missing file loaded without error")
	}
}

// The signature is DECISIONS.md section 9's trust anchor: a roster with
// perfectly well-formed participants is exactly as unusable as one with a
// bad key if nothing vouches for it, or if what vouches for it does not
// match roster_signer.
func TestLoadRosterRejectsBadSignatures(t *testing.T) {
	signerPub, signerPriv := testSigner(t)
	_, otherPriv := testSigner(t)
	aliceEntries := []rosterEntry{{ID: "alice", PublicKey: encodedKey(t)}}
	participants := `[{"id":"alice","public_key":"` + aliceEntries[0].PublicKey + `"}]`

	cases := map[string]string{
		"no signature field":        `{"participants":` + participants + `}`,
		"empty signature":           `{"participants":` + participants + `,"signature":""}`,
		"signature not base64url":   `{"participants":` + participants + `,"signature":"!!!!"}`,
		"signed by a different key": signedRosterBody(t, participants, otherPriv),
	}
	for name, body := range cases {
		if _, err := LoadRoster(writeRoster(t, body), signerPub); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}

	// A signature that verifies against the right key but over different
	// content — the file was re-signed correctly, then a participant was
	// added or edited afterward without re-signing.
	staleSig := ed25519.Sign(signerPriv, canonicalRosterBytes(aliceEntries))
	tampered := fmt.Sprintf(`{"participants":[{"id":"mallory","public_key":%q}],"signature":%q}`,
		encodedKey(t), base64.RawURLEncoding.EncodeToString(staleSig))
	if _, err := LoadRoster(writeRoster(t, tampered), signerPub); err == nil {
		t.Error("a roster edited after signing loaded without error")
	}

	// The positive control: the same signer, the same content, does load —
	// otherwise every case above could be passing because LoadRoster rejects
	// everything.
	validBody := signedRosterBody(t, participants, signerPriv)
	if _, err := LoadRoster(writeRoster(t, validBody), signerPub); err != nil {
		t.Errorf("a validly signed roster failed to load: %v", err)
	}
}
