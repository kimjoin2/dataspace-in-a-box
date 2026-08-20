package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

// canonicalRosterBytes returns the bytes a roster signature is computed
// over: json.Marshal of participants alone, never the raw file. Go's
// encoding/json marshals struct fields in declaration order, so this is
// deterministic for the fixed rosterEntry shape without a JSON-
// canonicalization scheme — the signer and LoadRoster both call this same
// function on their own parsed value, so reformatting roster.json's source
// bytes (whitespace, key order) cannot change what gets checked. Every field
// of rosterEntry is a plain string, so json.Marshal cannot fail here; its
// error is ignored, the same reasoning buildPermission's doc comment gives
// for ignoring its own unfailable one.
func canonicalRosterBytes(participants []rosterEntry) []byte {
	b, _ := json.Marshal(participants)
	return b
}

// Roster is the set of participants whose signatures this connector accepts.
// It is the whole of the trust decision: a key here is trusted, and anything
// else is not.
//
// Loaded once at startup and never mutated, so nothing needs a lock and there
// is no reload path to get wrong. Adding or removing a participant means
// editing the file, re-signing it, and restarting, which DECISIONS.md
// section 9 already accepted as the cost of a static registry.
type Roster struct {
	keys map[string]ed25519.PublicKey
}

// KeyFor returns the public key trusted for a participant, if any. Its shape
// matches Verify's keyFor parameter so a Roster can be passed straight in.
func (r Roster) KeyFor(id string) (ed25519.PublicKey, bool) {
	pub, ok := r.keys[id]
	return pub, ok
}

type rosterDocument struct {
	Participants []rosterEntry `json:"participants"`
	// Signature is the operator's Ed25519 signature (base64url) over
	// canonicalRosterBytes(Participants) — DECISIONS.md section 9's trust
	// anchor, without which a roster is only as trustworthy as the disk it
	// arrived on. Produced by `dsops roster sign`.
	Signature string `json:"signature"`
}

type rosterEntry struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
}

// LoadRoster reads, verifies, and validates the roster. Every failure below
// is a startup failure rather than a warning: a connector that starts with
// an unusable roster can verify nobody, and "started fine, refuses everyone"
// is a much harder symptom to trace than a refusal to start. That now
// includes signer: an unsigned or forged roster is exactly as unusable as
// one with no participants in it.
func LoadRoster(path string, signer ed25519.PublicKey) (Roster, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Roster{}, fmt.Errorf("read roster %q: %w", path, err)
	}
	var doc rosterDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return Roster{}, fmt.Errorf("parse roster %q: %w", path, err)
	}
	if len(doc.Participants) == 0 {
		return Roster{}, fmt.Errorf("roster %q lists no participants", path)
	}
	if doc.Signature == "" {
		return Roster{}, fmt.Errorf("roster %q carries no signature — sign it with `dsops roster sign`", path)
	}
	sig, err := base64.RawURLEncoding.DecodeString(doc.Signature)
	if err != nil {
		return Roster{}, fmt.Errorf("roster %q: signature is not base64url: %w", path, err)
	}
	if !ed25519.Verify(signer, canonicalRosterBytes(doc.Participants), sig) {
		return Roster{}, fmt.Errorf("roster %q: signature does not verify against roster_signer", path)
	}

	keys := make(map[string]ed25519.PublicKey, len(doc.Participants))
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
	}
	return Roster{keys: keys}, nil
}

// SignRoster reads the roster at path, ignoring any signature already in it,
// and returns the base64url Ed25519 signature `dsops roster sign` prints for
// an operator to paste into the file's own signature field. It does not
// write anything: dsops does not manage the roster file (see its package
// doc), and signing is no exception to that rather than a reason to make
// one.
func SignRoster(path string, priv ed25519.PrivateKey) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read roster %q: %w", path, err)
	}
	var doc rosterDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("parse roster %q: %w", path, err)
	}
	if len(doc.Participants) == 0 {
		return "", fmt.Errorf("roster %q lists no participants", path)
	}
	sig := ed25519.Sign(priv, canonicalRosterBytes(doc.Participants))
	return base64.RawURLEncoding.EncodeToString(sig), nil
}
