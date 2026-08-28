package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// futureExpiry is an expiry far enough ahead to be uninteresting and near
// enough to stay inside maxRosterLifetime, for fixtures whose subject is
// something other than the expiry. Relative to now, never a literal: a fixed
// date drifts past the cap or past itself as the repository ages.
func futureExpiry() string {
	return time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
}

// signedRosterBody signs participantsJSON — a JSON array literal, valid or
// deliberately malformed — together with the version and expiry it is given,
// and returns a complete roster document carrying all three plus the
// signature. Unmarshaling a malformed entry (a missing key, a bad key
// encoding) into []rosterEntry does not itself fail — json.Unmarshal leaves
// the zero value for anything absent — so this can sign every fixture
// TestLoadRosterRejectsUnusableFiles needs.
//
// The version and expiry are parameters rather than fixed because
// LoadRoster's order is: the document-level fields first, then the
// signature, then per-participant content. A fixture aimed at a
// per-participant check therefore has to carry document-level fields that
// pass and a signature that verifies, or it is rejected for one of those
// instead of the reason its case names.
func signedRosterBody(t *testing.T, participantsJSON string, priv ed25519.PrivateKey, version int, expiresAt string) string {
	t.Helper()
	var entries []rosterEntry
	if err := json.Unmarshal([]byte(participantsJSON), &entries); err != nil {
		t.Fatalf("signedRosterBody: participantsJSON does not parse as []rosterEntry: %v", err)
	}
	doc := rosterDocument{Participants: entries, Version: version, ExpiresAt: expiresAt}
	sig := ed25519.Sign(priv, canonicalRosterBytes(doc))
	return fmt.Sprintf(`{"participants":%s,"version":%d,"expires_at":%q,"signature":%q}`,
		participantsJSON, version, expiresAt, base64.RawURLEncoding.EncodeToString(sig))
}

// validRoster returns a signed document with one participant, the given
// version, and an expiry the given distance ahead. Relative, never literal:
// a fixed far-future date trips maxRosterLifetime and the test then passes
// for a reason it is not about.
func validRoster(t *testing.T, priv ed25519.PrivateKey, version int, in time.Duration) string {
	t.Helper()
	participants := `[{"id":"alice","public_key":"` + encodedKey(t) + `"}]`
	return signedRosterBody(t, participants, priv, version, time.Now().Add(in).UTC().Format(time.RFC3339))
}

func TestLoadRosterReadsParticipants(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signerPub, signerPriv := testSigner(t)
	participants := `[{"id":"alice","public_key":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}]`
	path := writeRoster(t, signedRosterBody(t, participants, signerPriv, 1, futureExpiry()))

	r, err := LoadRoster(path, signerPub, time.Now())
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
	if r.Version() != 1 {
		t.Errorf("Version() = %d, want the version the document declares", r.Version())
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
		body := signedRosterBody(t, participantsJSON, signerPriv, 1, futureExpiry())
		if _, err := LoadRoster(writeRoster(t, body), signerPub, time.Now()); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
	for name, body := range map[string]string{
		"not json":        `{`,
		"no participants": `{"participants":[],"version":1,"expires_at":"` + futureExpiry() + `"}`,
	} {
		if _, err := LoadRoster(writeRoster(t, body), signerPub, time.Now()); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
	if _, err := LoadRoster(filepath.Join(t.TempDir(), "absent.json"), signerPub, time.Now()); err == nil {
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
	// One value, so the document the stale signature covers and the tampered
	// document differ in their participants alone.
	expires := futureExpiry()
	fields := `,"version":1,"expires_at":"` + expires + `"`

	cases := map[string]string{
		"no signature field":        `{"participants":` + participants + fields + `}`,
		"empty signature":           `{"participants":` + participants + fields + `,"signature":""}`,
		"signature not base64url":   `{"participants":` + participants + fields + `,"signature":"!!!!"}`,
		"signed by a different key": signedRosterBody(t, participants, otherPriv, 1, expires),
	}
	for name, body := range cases {
		if _, err := LoadRoster(writeRoster(t, body), signerPub, time.Now()); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}

	// A signature that verifies against the right key but over different
	// content — the file was re-signed correctly, then a participant was
	// added or edited afterward without re-signing.
	staleSig := ed25519.Sign(signerPriv, canonicalRosterBytes(
		rosterDocument{Participants: aliceEntries, Version: 1, ExpiresAt: expires}))
	tampered := fmt.Sprintf(`{"participants":[{"id":"mallory","public_key":%q}]%s,"signature":%q}`,
		encodedKey(t), fields, base64.RawURLEncoding.EncodeToString(staleSig))
	if _, err := LoadRoster(writeRoster(t, tampered), signerPub, time.Now()); err == nil {
		t.Error("a roster edited after signing loaded without error")
	}

	// The positive control: the same signer, the same content, does load —
	// otherwise every case above could be passing because LoadRoster rejects
	// everything.
	validBody := signedRosterBody(t, participants, signerPriv, 1, expires)
	if _, err := LoadRoster(writeRoster(t, validBody), signerPub, time.Now()); err != nil {
		t.Errorf("a validly signed roster failed to load: %v", err)
	}
}

// The fields are required, and the message says which is missing. That is
// the upgrade experience: every roster in existence predates them, and
// "signature does not verify" would send the operator to the wrong place.
func TestLoadRosterRequiresAVersion(t *testing.T) {
	signerPub, signerPriv := testSigner(t)
	body := validRoster(t, signerPriv, 1, 24*time.Hour)
	body = strings.Replace(body, `"version":1,`, "", 1)
	_, err := LoadRoster(writeRoster(t, body), signerPub, time.Now())
	if err == nil {
		t.Fatal("a roster with no version loaded; absent decodes to zero and the ratchet could never move off it")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error %q does not name the missing field", err)
	}
}

// The signature covers them, or they are decoration an attacker rewrites.
// One field is rewritten per document, never both in one: a document with
// both rewritten is refused as long as the signature covers either of them,
// so it would pass while half the property went unheld. Every replacement
// stays in the future and inside the cap, so the signature is the only thing
// that can refuse either fixture.
func TestSignatureCoversVersionAndExpiry(t *testing.T) {
	signerPub, signerPriv := testSigner(t)
	participants := `[{"id":"alice","public_key":"` + encodedKey(t) + `"}]`
	expiry := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	body := signedRosterBody(t, participants, signerPriv, 1, expiry)

	for name, tampered := range map[string]string{
		"version raised": strings.Replace(body, `"version":1`, `"version":9`, 1),
		"expiry moved": strings.Replace(body, expiry,
			time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339), 1),
	} {
		if tampered == body {
			t.Fatalf("%s: the fixture did not contain the field this case rewrites", name)
		}
		if _, err := LoadRoster(writeRoster(t, tampered), signerPub, time.Now()); err == nil {
			t.Errorf("%s: the field was rewritten after signing and the roster still loaded: the signature does not cover it", name)
		}
	}
}

// An expired roster is a startup failure, the same grade as a bad signature.
func TestLoadRosterRefusesAnExpiredRoster(t *testing.T) {
	signerPub, signerPriv := testSigner(t)
	// Signed as valid a day ago, loaded now: inside the cap, past the expiry.
	body := validRoster(t, signerPriv, 1, -time.Hour)
	if _, err := LoadRoster(writeRoster(t, body), signerPub, time.Now()); err == nil {
		t.Fatal("an expired roster loaded")
	}
}

// Usable at every instant before expires_at, unusable at it and after. This
// connector already reads a deadline that way.
func TestLoadRosterBoundaryIsExclusive(t *testing.T) {
	signerPub, signerPriv := testSigner(t)
	in := 24 * time.Hour
	body := validRoster(t, signerPriv, 1, in)
	path := writeRoster(t, body)
	r, err := LoadRoster(path, signerPub, time.Now())
	if err != nil {
		t.Fatalf("LoadRoster: %v", err)
	}
	exp := r.ExpiresAt()
	if !r.UsableAt(exp.Add(-time.Second)) {
		t.Error("a second before the expiry: not usable")
	}
	if r.UsableAt(exp) {
		t.Error("at the expiry: usable, want not")
	}
}

// Without a cap the upper bound this milestone claims is whatever the
// operator typed. The design spec's section 3.4 has the argument.
func TestLoadRosterRefusesAnExpiryTooFarAhead(t *testing.T) {
	signerPub, signerPriv := testSigner(t)
	body := validRoster(t, signerPriv, 1, maxRosterLifetime+24*time.Hour)
	if _, err := LoadRoster(writeRoster(t, body), signerPub, time.Now()); err == nil {
		t.Fatal("an expiry beyond the cap loaded")
	}
}

// A malformed timestamp is caught at load rather than becoming a silent
// per-request refusal on a connector whose boot log said nothing.
func TestLoadRosterRefusesAMalformedExpiry(t *testing.T) {
	signerPub, signerPriv := testSigner(t)
	participants := `[{"id":"alice","public_key":"` + encodedKey(t) + `"}]`
	body := signedRosterBody(t, participants, signerPriv, 1, "2027-01-01")
	if _, err := LoadRoster(writeRoster(t, body), signerPub, time.Now()); err == nil {
		t.Fatal("a date with no time loaded")
	}
}

// Document-level structure is checked before the signature, beside the two
// checks already there. The fixture fails both, and the assertion is on
// which answer comes back.
func TestRequiredFieldsAreCheckedBeforeTheSignature(t *testing.T) {
	signerPub, _ := testSigner(t)
	participants := `[{"id":"alice","public_key":"` + encodedKey(t) + `"}]`
	body := `{"participants":` + participants + `,"expires_at":"` +
		time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339) + `","signature":"AAAA"}`
	_, err := LoadRoster(writeRoster(t, body), signerPub, time.Now())
	if err == nil {
		t.Fatal("loaded")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error %q reports the signature; the field check must come first", err)
	}
}

// The signed bytes are what a signature is computed over, so a field added to
// rosterEntry changes them for every roster ever signed unless it is
// omitempty. Measured: without omitempty every other test in this file still
// passes, because signedRosterBody re-marshals the participants it is given
// and is therefore self-consistent under any struct shape. This assertion is
// not, which is the whole reason it is written against bytes.
func TestCanonicalRosterBytesOmitAnAbsentConnectorAddress(t *testing.T) {
	t.Parallel()
	doc := rosterDocument{
		Participants: []rosterEntry{{ID: "alice", PublicKey: "AAAA"}},
		Version:      3,
		ExpiresAt:    "2030-01-01T00:00:00Z",
	}
	const want = `{"participants":[{"id":"alice","public_key":"AAAA"}],` +
		`"version":3,"expires_at":"2030-01-01T00:00:00Z"}`
	if got := string(canonicalRosterBytes(doc)); got != want {
		t.Errorf("an entry with no connector_address no longer signs as it did:\n got %s\nwant %s", got, want)
	}
}

// The companion. An entry with no address serializes identically whatever
// order the fields are declared in, so the test above cannot see a reordering
// — and a reordering changes the signed bytes for every entry that does carry
// an address.
func TestCanonicalRosterBytesCarryAPresentConnectorAddress(t *testing.T) {
	t.Parallel()
	doc := rosterDocument{
		Participants: []rosterEntry{{ID: "alice", PublicKey: "AAAA", ConnectorAddress: "http://alice:8080/2025-1"}},
		Version:      3,
		ExpiresAt:    "2030-01-01T00:00:00Z",
	}
	const want = `{"participants":[{"id":"alice","public_key":"AAAA",` +
		`"connector_address":"http://alice:8080/2025-1"}],` +
		`"version":3,"expires_at":"2030-01-01T00:00:00Z"}`
	if got := string(canonicalRosterBytes(doc)); got != want {
		t.Errorf("an entry carrying a connector_address signs differently than expected:\n got %s\nwant %s", got, want)
	}
}

// Optional in the document does not mean removable from a signed one. All
// three mutations an attacker would want are refused, and each is refused by
// the signature rather than by a rule written for it.
func TestSignatureCoversTheConnectorAddress(t *testing.T) {
	t.Parallel()
	pub, priv := testSigner(t)
	key := encodedKey(t)
	withAddr := `[{"id":"alice","public_key":"` + key + `","connector_address":"http://alice:8080/2025-1"}]`
	withoutAddr := `[{"id":"alice","public_key":"` + key + `"}]`
	rewritten := `[{"id":"alice","public_key":"` + key + `","connector_address":"http://mallory:8080/2025-1"}]`

	signedWith := signedRosterBody(t, withAddr, priv, 1, futureExpiry())
	signedWithout := signedRosterBody(t, withoutAddr, priv, 1, futureExpiry())

	// Each case takes the signature from one document and the participants
	// from another, which is exactly the edit the file would suffer.
	for _, c := range []struct {
		name         string
		participants string
		donor        string
	}{
		{"the address is stripped out", withoutAddr, signedWith},
		{"an address is added", withAddr, signedWithout},
		{"the address is rewritten", rewritten, signedWith},
	} {
		t.Run(c.name, func(t *testing.T) {
			sig := signatureOf(t, c.donor)
			body := `{"participants":` + c.participants + `,"version":1,"expires_at":"` +
				expiryOf(t, c.donor) + `","signature":"` + sig + `"}`
			if _, err := LoadRoster(writeRoster(t, body), pub, time.Now()); err == nil {
				t.Fatal("LoadRoster accepted a roster whose participants do not match its signature")
			} else if !strings.Contains(err.Error(), "signature does not verify") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
		})
	}
}

// signatureOf and expiryOf pull the two document-level fields back out of a
// body signedRosterBody produced, so a case can pair one document's signature
// with another's participants without rebuilding either by hand.
func signatureOf(t *testing.T, body string) string {
	t.Helper()
	var doc rosterDocument
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("signatureOf: %v", err)
	}
	return doc.Signature
}

func expiryOf(t *testing.T, body string) string {
	t.Helper()
	var doc rosterDocument
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("expiryOf: %v", err)
	}
	return doc.ExpiresAt
}

func TestLoadRosterRejectsBadConnectorAddresses(t *testing.T) {
	t.Parallel()
	pub, priv := testSigner(t)
	for _, c := range []struct{ name, addr, want string }{
		{"a bare question mark", "http://alice:8080/x?", "query or a fragment"},
		{"a bare hash", "http://alice:8080/x#", "query or a fragment"},
		{"a query", "http://alice:8080/x?a=1", "query or a fragment"},
		{"a fragment", "http://alice:8080/x#f", "query or a fragment"},
		{"a trailing slash", "http://alice:8080/2025-1/", "ends in a slash"},
		{"whitespace", "http://alice:8080/a b", "whitespace"},
		{"no scheme", "//alice:8080/x", "http or https"},
		{"an opaque URL with no host", "alice:8080/2025-1", "http or https"},
		{"a scheme this connector does not speak", "ftp://alice/x", "http or https"},
		{"no host", "http:///2025-1", "has no host"},
		{"userinfo", "http://u:p@alice:8080/x", "userinfo"},
	} {
		t.Run(c.name, func(t *testing.T) {
			participants := `[{"id":"alice","public_key":"` + encodedKey(t) +
				`","connector_address":` + strconv.Quote(c.addr) + `}]`
			body := signedRosterBody(t, participants, priv, 1, futureExpiry())
			_, err := LoadRoster(writeRoster(t, body), pub, time.Now())
			if err == nil {
				t.Fatalf("LoadRoster accepted connector_address %q", c.addr)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// The empty string is the one value omitempty makes indistinguishable from
// absence, so it must read as absence rather than as a malformed address.
// The case above expects a refusal; this one records why it is not a
// contradiction: an operator who writes "" gets an entry with no address, and
// every outbound site refuses that in its own words.
func TestAnExplicitlyEmptyConnectorAddressIsNoAddress(t *testing.T) {
	t.Parallel()
	pub, priv := testSigner(t)
	participants := `[{"id":"alice","public_key":"` + encodedKey(t) + `","connector_address":""}]`
	body := signedRosterBody(t, participants, priv, 1, futureExpiry())
	r, err := LoadRoster(writeRoster(t, body), pub, time.Now())
	if err != nil {
		t.Fatalf("LoadRoster: %v", err)
	}
	if addr, ok := r.AddressFor("alice"); ok {
		t.Errorf(`an explicitly empty connector_address reported an address %q`, addr)
	}
}

func TestAddressForReportsPresence(t *testing.T) {
	t.Parallel()
	pub, priv := testSigner(t)
	participants := `[{"id":"alice","public_key":"` + encodedKey(t) +
		`","connector_address":"http://alice:8080/2025-1"},` +
		`{"id":"bob","public_key":"` + encodedKey(t) + `"}]`
	body := signedRosterBody(t, participants, priv, 1, futureExpiry())
	r, err := LoadRoster(writeRoster(t, body), pub, time.Now())
	if err != nil {
		t.Fatalf("LoadRoster: %v", err)
	}
	if addr, ok := r.AddressFor("alice"); !ok || addr != "http://alice:8080/2025-1" {
		t.Errorf(`AddressFor("alice") = %q, %v`, addr, ok)
	}
	if addr, ok := r.AddressFor("bob"); ok {
		t.Errorf(`AddressFor("bob") = %q, true; bob lists no address`, addr)
	}
	if _, ok := r.AddressFor("carol"); ok {
		t.Error(`AddressFor("carol") = true; carol is not in the roster`)
	}
}
