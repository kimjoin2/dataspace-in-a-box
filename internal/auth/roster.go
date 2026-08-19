package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

// Roster is the set of participants whose signatures this connector accepts.
// It is the whole of the trust decision: a key here is trusted, and anything
// else is not.
//
// Loaded once at startup and never mutated, so nothing needs a lock and there
// is no reload path to get wrong. Adding or removing a participant means
// editing the file and restarting, which DECISIONS.md section 9 already
// accepted as the cost of a static registry.
//
// What this is not, yet: section 9 makes the operator's signature over this
// file the trust anchor, which is what would let a roster travel over an
// untrusted channel. That signature is deferred, so this file is trusted
// exactly as far as config.yaml on the same disk is — meaning a roster
// fetched from anywhere else is not safe under this milestone. See the design
// spec, "What section 9 says that this does not do".
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
}

type rosterEntry struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
}

// LoadRoster reads and validates the roster. Every failure below is a startup
// failure rather than a warning: a connector that starts with an unusable
// roster can verify nobody, and "started fine, refuses everyone" is a much
// harder symptom to trace than a refusal to start.
func LoadRoster(path string) (Roster, error) {
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
