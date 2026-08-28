package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// canonicalRosterBytes returns the bytes a roster signature is computed
// over: json.Marshal of the document's signed fields, never the raw file.
// Go's encoding/json marshals struct fields in declaration order, so this is
// deterministic for the fixed shape below without a JSON-canonicalization
// scheme — the signer and LoadRoster both call this same function on their
// own parsed value, so reformatting roster.json's source bytes (whitespace,
// key order) cannot change what gets checked. Every field serialized here is
// a plain string or an int, so json.Marshal cannot fail; its error is
// ignored, the same reasoning buildPermission's doc comment gives for
// ignoring its own unfailable one. That is also why the expiry is carried as
// a string and parsed only for comparison: a time.Time field would make the
// sentence above false and the discarded error reachable.
func canonicalRosterBytes(doc rosterDocument) []byte {
	b, _ := json.Marshal(struct {
		Participants []rosterEntry `json:"participants"`
		Version      int           `json:"version"`
		ExpiresAt    string        `json:"expires_at"`
	}{doc.Participants, doc.Version, doc.ExpiresAt})
	return b
}

// maxRosterLifetime bounds how far ahead an expiry may sit. Without it the
// upper bound this milestone puts on revocation is whatever the operator
// typed, which is the shape a credential's lifetime had on the token side
// until maxCredentialLifetime: chosen by the issuer, unmeasured by the
// verifier. DECISIONS.md section 10's five minutes is still what this
// connector mints; what changed is that a verifier now caps what it accepts,
// so the sentence above draws a parallel rather than naming a live defect.
//
// A constant and not configuration. A configurable maximum is a second
// policy the signature does not carry, so a deployment could widen its own
// and the widest one would be the weak link.
const maxRosterLifetime = 400 * 24 * time.Hour

// Roster is the set of participants whose signatures this connector accepts.
// It is the whole of the trust decision, for as long as the document is
// still trusted: until expiresAt a key here is trusted and anything else is
// not, and from expiresAt onward no key here is trusted either. UsableAt
// below is that boundary, and every surface that refuses for it does so
// through the predicate internal/dsp builds from this value rather than
// comparing again.
//
// Loaded once at startup and never mutated, so nothing needs a lock and there
// is no reload path to get wrong. Adding or removing a participant means
// editing the file, re-signing it, and restarting, which DECISIONS.md
// section 9 already accepted as the cost of a static registry.
type Roster struct {
	keys      map[string]ed25519.PublicKey
	addresses map[string]string
	version   int
	expiresAt time.Time
}

// KeyFor returns the public key trusted for a participant, if any. Its shape
// matches Verify's keyFor parameter so a Roster can be passed straight in.
func (r Roster) KeyFor(id string) (ed25519.PublicKey, bool) {
	pub, ok := r.keys[id]
	return pub, ok
}

// AddressFor returns the address the roster lists for a participant, and
// whether it lists one. False covers both a participant that is absent and
// one that carries no address; a caller that has to tell those apart asks
// KeyFor first, which every caller in this repository already does.
func (r Roster) AddressFor(id string) (string, bool) {
	addr, ok := r.addresses[id]
	return addr, ok
}

// Version returns the revision this roster declares, which a connector
// compares against the newest revision it has already run.
func (r Roster) Version() int { return r.version }

// ExpiresAt returns the instant this roster stops being trusted.
func (r Roster) ExpiresAt() time.Time { return r.expiresAt }

// UsableAt reports whether this roster is still trusted at now. Usable at
// every instant before the expiry and not at it — the same reading of a
// deadline the data endpoint's dataset window and token expiry already use.
//
// A zero Roster is not usable. That is deliberate and it is not how
// "authentication is off" is expressed: absence is a nil predicate at the
// call site, never a zero value here.
func (r Roster) UsableAt(now time.Time) bool { return now.Before(r.expiresAt) }

type rosterDocument struct {
	Participants []rosterEntry `json:"participants"`
	// Version is this roster's revision. It only ever goes up: a connector
	// refuses one older than it has already run, which is what makes a
	// superseded roster unusable rather than merely stale. Required and at
	// least 1 — an absent value decodes to zero, and that zero is the
	// rejection rather than a default.
	Version int `json:"version"`
	// ExpiresAt is when this roster stops being trusted, RFC 3339. A string
	// rather than a time.Time on purpose: canonicalRosterBytes discards
	// json.Marshal's error on the argument that every field it serializes is
	// a plain string or an int, and a time.Time makes that false and the
	// discarded error reachable.
	ExpiresAt string `json:"expires_at"`
	// Signature is the operator's Ed25519 signature (base64url) over
	// canonicalRosterBytes of the fields above — DECISIONS.md section 9's
	// trust anchor, without which a roster is only as trustworthy as the
	// disk it arrived on. Produced by `dsops roster sign`.
	Signature string `json:"signature"`
}

type rosterEntry struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
	// ConnectorAddress is where this participant receives DSP messages: the
	// base that message paths are appended to, which is the same string an
	// initiate call names as connectorAddress. Optional, and omitempty is
	// load-bearing — canonicalRosterBytes re-marshals the parsed document, so
	// without it every roster signed before this field existed stops
	// verifying and the error an operator meets names the signer key rather
	// than the upgrade.
	//
	// Optional is not a weakness. The signature covers the field in both
	// directions: stripping one out, adding one, and rewriting one each break
	// verification. What is optional is the operator's choice, not an
	// attacker's.
	//
	// public_key serves the inbound direction and this serves the outbound
	// one, which is why a participant this connector only ever receives from
	// needs no address. DECISIONS.md section 36.9 declined this field on the
	// cost of re-signing; scoping it to the participants an operator dials is
	// what bounds that cost.
	ConnectorAddress string `json:"connector_address,omitempty"`
}

// checkRosterDocument validates what the document must carry regardless of
// who signed it: participants, a version, and an expiry. It runs before the
// signature verifies, which is where the participants check already ran —
// that one moved in here. The signature-presence check stays beside it in
// LoadRoster, because SignRoster reads a file that has no signature yet and
// cannot apply it.
//
// Rejecting on unauthenticated input is fail-closed, which is a different
// thing from acting on unauthenticated claims: Verify in token.go draws that
// line, and this stays on the safe side of it. The reason it matters here is
// the upgrade message. Every roster written before this milestone lacks both
// fields, and reporting a signature failure would send an operator to look
// at the wrong thing.
func checkRosterDocument(path string, doc rosterDocument) error {
	if len(doc.Participants) == 0 {
		return fmt.Errorf("roster %q lists no participants", path)
	}
	if doc.Version < 1 {
		return fmt.Errorf("roster %q: version is %d, want at least 1 — add a \"version\" field and re-sign it", path, doc.Version)
	}
	if doc.ExpiresAt == "" {
		return fmt.Errorf("roster %q carries no expires_at — add one in RFC 3339 form and re-sign it", path)
	}
	return nil
}

// checkRosterExpiry parses expires_at and applies the two bounds that do not
// depend on who signed the document: it may not sit further ahead than
// maxRosterLifetime, and it may not have passed already. SignRoster shares
// it with LoadRoster so that `dsops roster sign` refuses what the connector
// would refuse at boot, instead of printing a signature for a file that
// cannot be loaded.
func checkRosterExpiry(path string, doc rosterDocument, now time.Time) (time.Time, error) {
	expiresAt, err := time.Parse(time.RFC3339, doc.ExpiresAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("roster %q: expires_at %q is not an RFC 3339 timestamp: %w", path, doc.ExpiresAt, err)
	}
	if expiresAt.After(now.Add(maxRosterLifetime)) {
		return time.Time{}, fmt.Errorf("roster %q: expires_at %s is further ahead than the %d days this connector accepts",
			path, doc.ExpiresAt, int(maxRosterLifetime.Hours()/24))
	}
	// Through UsableAt rather than a second comparison: the package reads
	// this boundary in one place, so the boundary is one decision.
	if !(Roster{expiresAt: expiresAt}).UsableAt(now) {
		return time.Time{}, fmt.Errorf("roster %q expired at %s — re-sign it with a later expires_at", path, doc.ExpiresAt)
	}
	return expiresAt, nil
}

// checkConnectorAddress applies the rules a roster address must satisfy for
// this connector to dial it. They are syntactic. Nothing here resolves a name
// or judges a network: validateOutgoingCallback in internal/dsp owns that
// question and runs where the address is about to be used, which is the only
// place it can. internal/dsp imports this package, so the reverse is an import
// cycle, and boot is too early besides — a counterparty's container does not
// exist when this connector starts.
//
// Several checks read the raw string rather than the parse, and that is not
// belt-and-braces. url.Parse is a tokenizer, not a validator: it almost never
// errors; it folds the scheme to lower case, so a check for a lower-case
// scheme could never fire; and a bare "?" or "#" leaves RawQuery and Fragment
// empty while url.String() silently drops the "#", so the stored string and
// the parsed value would disagree. url.IsAbs reports only that a scheme is
// present and is true for an opaque URL with no host at all, which is why the
// host check carries that rule instead.
//
// There is no case rule and no normalization. The address is used rather than
// compared (see the initiate hooks), so the string that is approved is the
// string that is dialed and there is nothing for a normalization to reconcile.
func checkConnectorAddress(path, id, addr string) error {
	if strings.ContainsAny(addr, "?#") {
		return fmt.Errorf("roster %q: participant %q connector_address %q carries a query or a fragment", path, id, addr)
	}
	if strings.ContainsAny(addr, " \t\r\n") {
		return fmt.Errorf("roster %q: participant %q connector_address %q contains whitespace", path, id, addr)
	}
	// Every DSP path this connector appends begins with a slash, so a
	// trailing one produces a doubled separator in the URL it dials.
	if strings.HasSuffix(addr, "/") {
		return fmt.Errorf("roster %q: participant %q connector_address %q ends in a slash", path, id, addr)
	}
	u, err := url.Parse(addr)
	if err != nil {
		return fmt.Errorf("roster %q: participant %q connector_address %q is not a URL: %w", path, id, addr, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("roster %q: participant %q connector_address %q must use http or https", path, id, addr)
	}
	if u.Host == "" {
		return fmt.Errorf("roster %q: participant %q connector_address %q has no host", path, id, addr)
	}
	if u.User != nil {
		return fmt.Errorf("roster %q: participant %q connector_address %q carries userinfo", path, id, addr)
	}
	return nil
}

// LoadRoster reads, verifies, and validates the roster. Every failure below
// is a startup failure rather than a warning: a connector that starts with
// an unusable roster can verify nobody, and "started fine, refuses everyone"
// is a much harder symptom to trace than a refusal to start.
//
// What counts as unusable has outgrown the participant list it began as. An
// unsigned or forged document is unusable; so is one whose version is absent
// or below what checkRosterDocument accepts; so is one whose expires_at is
// absent, not RFC 3339, further ahead than maxRosterLifetime, or already
// past. Those live in checkRosterDocument and checkRosterExpiry, which
// SignRoster applies too, so `dsops roster sign` refuses those before it
// prints anything.
//
// The per-participant checks below are this function's alone. SignRoster
// does not walk doc.Participants, so it will sign a document this refuses
// for an empty or duplicated id, a missing public_key, or one that is not a
// base64url Ed25519 key of the right length. That is the boundary
// SignRoster's own doc comment draws when it says it refuses what this would
// refuse "about the document itself".
//
// A roster failure that belongs with these is not here and cannot be: a
// revision older than one this connector has already run is refused against
// the store, which is not open yet. cmd/dsbox/main.go carries it at the call
// that opens one.
//
// Past expires_at, refusing everyone stops being the symptom above and
// becomes the designed state — a connector already running does not stop,
// it refuses, and says so. internal/dsp's guard is where that lives.
func LoadRoster(path string, signer ed25519.PublicKey, now time.Time) (Roster, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Roster{}, fmt.Errorf("read roster %q: %w", path, err)
	}
	var doc rosterDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return Roster{}, fmt.Errorf("parse roster %q: %w", path, err)
	}
	if err := checkRosterDocument(path, doc); err != nil {
		return Roster{}, err
	}
	if doc.Signature == "" {
		return Roster{}, fmt.Errorf("roster %q carries no signature — sign it with `dsops roster sign`", path)
	}
	sig, err := base64.RawURLEncoding.DecodeString(doc.Signature)
	if err != nil {
		return Roster{}, fmt.Errorf("roster %q: signature is not base64url: %w", path, err)
	}
	if !ed25519.Verify(signer, canonicalRosterBytes(doc), sig) {
		return Roster{}, fmt.Errorf("roster %q: signature does not verify against roster_signer", path)
	}
	expiresAt, err := checkRosterExpiry(path, doc, now)
	if err != nil {
		return Roster{}, err
	}

	keys := make(map[string]ed25519.PublicKey, len(doc.Participants))
	addresses := make(map[string]string)
	for i, p := range doc.Participants {
		if p.ID == "" {
			return Roster{}, fmt.Errorf("roster %q: participants[%d] has no id", path, i)
		}
		if _, seen := keys[p.ID]; seen {
			return Roster{}, fmt.Errorf("roster %q: participant %q appears twice", path, p.ID)
		}
		if p.PublicKey == "" {
			return Roster{}, fmt.Errorf("roster %q: participant %q has no public_key", path, p.ID)
		}
		raw, err := base64.RawURLEncoding.DecodeString(p.PublicKey)
		if err != nil {
			return Roster{}, fmt.Errorf("roster %q: participant %q public_key is not base64url: %w", path, p.ID, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return Roster{}, fmt.Errorf("roster %q: participant %q public_key is %d bytes, want %d",
				path, p.ID, len(raw), ed25519.PublicKeySize)
		}
		keys[p.ID] = ed25519.PublicKey(raw)
		// Absent and explicitly empty are the same entry: omitempty makes
		// them identical in the signed bytes, so they must mean the same
		// thing here too.
		if p.ConnectorAddress != "" {
			if err := checkConnectorAddress(path, p.ID, p.ConnectorAddress); err != nil {
				return Roster{}, err
			}
			addresses[p.ID] = p.ConnectorAddress
		}
	}
	return Roster{keys: keys, addresses: addresses, version: doc.Version, expiresAt: expiresAt}, nil
}

// SignRoster reads the roster at path, ignoring any signature already in it,
// and returns the base64url Ed25519 signature `dsops roster sign` prints for
// an operator to paste into the file's own signature field. It does not
// write anything: dsops does not manage the roster file (see its package
// doc), and signing is no exception to that rather than a reason to make
// one.
//
// It refuses what LoadRoster would refuse about the document itself, now
// included: a signature printed for a roster that cannot boot is a success
// the operator acts on and a failure they meet days later. How far ahead the
// expiry sits within the cap is the operator's policy and is not judged
// here.
func SignRoster(path string, priv ed25519.PrivateKey, now time.Time) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read roster %q: %w", path, err)
	}
	var doc rosterDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("parse roster %q: %w", path, err)
	}
	if err := checkRosterDocument(path, doc); err != nil {
		return "", err
	}
	if _, err := checkRosterExpiry(path, doc, now); err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, canonicalRosterBytes(doc))
	return base64.RawURLEncoding.EncodeToString(sig), nil
}
