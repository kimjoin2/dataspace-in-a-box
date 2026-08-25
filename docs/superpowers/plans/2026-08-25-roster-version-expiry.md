# Roster Version and Expiry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the roster a revision number and an expiry inside the operator's signature, and make the expiry something the connector enforces while it runs rather than only when it starts.

**Architecture:** `internal/auth` gains the two signed fields and a clock parameter; the store gains a one-row ratchet that refuses a roster older than one already run; `internal/dsp` refuses every request, every initiate call, and every outbound message once the roster has expired; `internal/mgmt` reports it on `/health`. Nothing re-reads the file — the expiry is evaluated per request against a value parsed once at load.

**Tech Stack:** Go standard library only, `modernc.org/sqlite` (already present). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-25-roster-version-expiry-design.md` — read it alongside this plan. Where the two disagree, the spec wins and the disagreement is a finding.

## Global Constraints

- Go standard library only. Ask before adding a dependency.
- English for all documentation, code comments, error strings, and commit messages. No emoji.
- **Never put a count in a code comment or in prose.** Rewrite without the number. The one exception is a number naming a fixed pair the design itself defines. Forbidden forms include "three call sites" and "seven places" — both have been found in this repository's own documents and corrected.
- Every documentation edit names the code fact it was checked against.
- Final gates: `go vet ./...`, `go test -race -count=1 ./...`, `make tck` (must stay 65/65), `make demo` (both rounds). CI additionally runs `go test -race -count=2`, which matters for Task 4.
- Work directly on `main` (authorised for this session). **Push requires the user's explicit word each time.**
- `httptest.ResponseRecorder` does not enforce response framing. Anything about framing or routing uses `httptest.NewServer`.
- Existing tests build `config.Config` as a struct literal, bypassing `Load`'s defaults — `AuthRequired()` is true for a zero `Config`.

---

## What is already known, and must not be rediscovered

The spec was implemented twice on throwaway copies before this plan existed, and this plan was then implemented a third time and every mutation below applied. All three runs reached every gate green. This plan is therefore not exploratory; what follows are measurements, not predictions.

**The commit order is forced.** `internal/store` alone is green. `internal/auth` and both harness scripts are **one indivisible commit** — with the scripts unchanged, `make tck` fails in under two seconds at `dsops roster sign` with `version is 0, want at least 1`. The wiring follows.

**No harness verifies any of this.** `make tck` and `make demo` stayed green under every one of the nine mutations. `go test` is the only gate that carries this milestone. Do not take a green `make tck` as evidence that a task worked.

**The outbound minter leaks between tests.** `mintOutboundCredential` is a package-level variable that `NewRouter` assigns and never restores. A test that builds a router with an expired roster leaks a refusing minter into every test after it, and the measured first symptom is not a failure but a **hang** — the package runs until its timeout panics, inside a test waiting on a pull that can no longer be dispatched. Every test that installs an expired router restores the minter.

**`roster_version.id` is a rowid alias.** `CHECK (id = 1)` fires for `id=2` and for an **omitted** `id`, which takes the next rowid — measured. A duplicate `id=1` is caught by the primary key instead, with a different message. Writes name `id` explicitly and upsert.

**Every fixture's expiry must sit inside `maxRosterLifetime`.** A far-future date makes `LoadRoster` refuse on the cap before it reaches whatever the test is about, and a test that fails for the wrong reason passes under the mutation it exists to catch. This happened: a `2099` fixture made the signature-coverage mutation survive every gate including `make tck`. Date fixtures relative to `time.Now()`.

---

## File Structure

| File | Responsibility after this milestone |
|---|---|
| `internal/auth/roster.go` | The signed document's shape, its validation, and the two accessors the rest of the connector reads |
| `internal/auth/roster_test.go` | Fixtures for the new shape; the document-level checks; the cap; the boundary |
| `internal/store/store.go` | The one-row ratchet and its migration |
| `internal/store/roster_version_test.go` (new) | Rollback, equal-version, and the single-row constraint |
| `internal/dsp/router.go` | Builds the usability predicate; returns it on `Routers` |
| `internal/dsp/auth_middleware.go` | Refuses an expired roster before reading a credential |
| `internal/dsp/negotiation_consumer_handler.go`, `transfer_consumer_handler.go` | Initiate hooks refuse first; the pull's failure log carries the body |
| `internal/dsp/callback.go` | The minter's new contract and its permitting default |
| `internal/dsp/roster_expiry_test.go` (new) | Every expiry refusal, and the status codes |
| `internal/mgmt/router.go` | `/health` reports the expiry |
| `cmd/dsbox/main.go` | Records the version; logs the roster's identity and its approach warning |
| `cmd/dsbox/roster_version_test.go` (new) | Source-parsing guard that the recording call exists |
| `cmd/dsops/main.go` | Passes the clock to `SignRoster` |
| `test/tck/run.sh`, `demo/run.sh` | Rosters carrying the new fields |

---

## Task ordering, and why it cannot change

1. **Store** — standalone and green; nothing depends on it yet.
2. **Auth + both harness scripts** — one commit. Splitting it turns `make tck` red at the signing step.
3. **DSP enforcement** — needs the predicate that Task 2 makes possible.
4. **Outbound** — separable from Task 3 but not before it; the predicate must exist.
5. **Health, boot log, and wiring** — needs Task 1's ratchet, Task 2's accessors, and Task 3's predicate.
6. **Documentation** — last, so every sentence describes code that exists.

---

## Task 1: The store's roster-version ratchet

**Files:**
- Modify: `internal/store/store.go`
- Create: `internal/store/roster_version_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func (s *Store) RecordRosterVersion(version int) error` — returns nil when `version` is at least the highest recorded, recording it when strictly higher; returns an error naming both versions when lower.

**The advance path needs its own test.** Recording into an empty table takes the plain insert, and the equal case returns before the write, so the three tests above never reach the `ON CONFLICT` clause. Add one that records 2, then 5, then asserts 4 is refused and that the error names 5 — which is what proves the stored value moved rather than staying at 2.

- [ ] **Step 1: Write the failing tests**

```go
package store

import (
	"strings"
	"testing"
)

// A roster older than one this connector has already run is refused. That is
// the whole point of recording anything: without it, an operator handed a
// superseded roster at restart cannot tell.
func TestRecordRosterVersionRefusesARollback(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	if err := s.RecordRosterVersion(4); err != nil {
		t.Fatalf("record 4: %v", err)
	}
	err := s.RecordRosterVersion(3)
	if err == nil {
		t.Fatal("version 3 was accepted after version 4 had been recorded; " +
			"a rollback to a superseded roster would boot silently")
	}
	// The message names both, because an operator meeting this at boot has to
	// know what to re-issue above.
	for _, want := range []string{"3", "4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// An ordinary restart presents the same roster it presented last time. If
// equal were refused, every connector would fail to boot the second time.
func TestRecordRosterVersionAcceptsAnEqualVersion(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	for restart := 0; restart < 3; restart++ {
		if err := s.RecordRosterVersion(2); err != nil {
			t.Fatalf("restart %d with an unchanged roster: %v", restart, err)
		}
	}
}

// The table holds one row by constraint, not by convention: with two rows,
// which one SELECT returns is arbitrary and the ratchet stops meaning
// anything.
func TestRosterVersionTableHoldsOneRow(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	if err := s.RecordRosterVersion(7); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO roster_version (id, highest) VALUES (2, 99)`); err == nil {
		t.Error("a second row was accepted; SELECT highest is now arbitrary")
	}
	// id is the rowid alias, so an omitted id takes the next rowid and fails
	// the check too. Worth pinning: the naive insert would work exactly once.
	if _, err := s.db.Exec(`INSERT INTO roster_version (highest) VALUES (99)`); err == nil {
		t.Error("a row with an omitted id was accepted")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM roster_version`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("row count = %d, want 1", n)
	}
}
```

`internal/store`'s existing tests have **no** helper — each one calls `Open(":memory:")` inline and checks the error. Add one at the top of this new file rather than repeating that three more times, and leave the existing tests alone:

```go
func mustOpen(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
```

Check `Store`'s actual close method name before writing that cleanup; if there is none, drop the line rather than inventing one.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/store/ -run TestRecordRosterVersion -v`
Expected: FAIL — `RecordRosterVersion` undefined.

- [ ] **Step 3: Add the schema literal**

Beside the other schema constants in `internal/store/store.go`:

```go
// roster_version is the highest roster revision this connector has loaded.
// One row, by constraint rather than by convention: with a second row,
// SELECT highest returns an arbitrary one and the ratchet stops meaning
// anything. id is the rowid alias, so it is always named explicitly on
// write — an omitted value takes the next rowid and fails the check.
//
// This says nothing about the schema's own revision. DECISIONS.md section
// 23.1 declined a schema-version table and still declines one.
const rosterVersionSchema = `
CREATE TABLE IF NOT EXISTS roster_version (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    highest INTEGER NOT NULL
);`
```

- [ ] **Step 4: Execute it in `Open`**

After the `consumerTransferSchema` exec and before the `migrate` call:

```go
	if _, err := db.Exec(rosterVersionSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create roster version schema in %s: %w", path, err)
	}
```

- [ ] **Step 5: Write `RecordRosterVersion`**

```go
// RecordRosterVersion refuses a roster older than one this connector has
// already run, and remembers the newest it has seen.
//
// Equal is accepted and writes nothing: an ordinary restart presents the
// roster it presented last time, and refusing that would mean a connector
// boots exactly once.
//
// This is a local memory and not a dataspace property. No version is
// exchanged with any participant, so during a rollout one connector can be
// ahead of another and neither can tell. The design spec's section 1.3 is
// precise about what that does and does not buy.
func (s *Store) RecordRosterVersion(version int) error {
	var highest int
	err := s.db.QueryRow(`SELECT highest FROM roster_version WHERE id = 1`).Scan(&highest)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No memory yet. Fail-open by construction: a fresh store accepts
		// any version, and the expiry is what bounds that window.
	case err != nil:
		return fmt.Errorf("read roster version: %w", err)
	case version < highest:
		return fmt.Errorf("roster version %d is older than version %d, which this connector has already run", version, highest)
	case version == highest:
		return nil
	}
	if _, err := s.db.Exec(
		`INSERT INTO roster_version (id, highest) VALUES (1, ?)
		 ON CONFLICT(id) DO UPDATE SET highest = excluded.highest`,
		version,
	); err != nil {
		return fmt.Errorf("record roster version %d: %w", version, err)
	}
	return nil
}
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/store/ -run 'TestRecordRosterVersion|TestRosterVersionTable' -v`
Expected: PASS, all three.

- [ ] **Step 7: Correct the two schema doc comments**

`addColumnIfMissing`'s comment says "Every migration in this file is a column addition, which is the only schema change SQLite performs cheaply and the only one this connector has needed." Scope it to the helper — column addition is the only cheap one, which is why every migration ran through this helper until this one, and a table needs no helper because `CREATE TABLE IF NOT EXISTS` is already idempotent.

`migrate`'s comment says "There is no migration framework and no version table (DECISIONS.md section 23.1)". Make it say **schema-version** table, and name what `roster_version` actually records.

Check each edit against the code: `rosterVersionSchema` is executed in `Open`, not in `migrate`, and it carries no `addColumnIfMissing` call.

- [ ] **Step 8: Full gates and commit**

Run: `go vet ./... && go test -race -count=1 ./...`

```bash
git add internal/store/
git commit -m "feat: the store remembers the newest roster it has run"
```

**Mutations for this task.** Each row says why the named test breaks; a row that cannot say why is a wrong mutation.

| Mutation | Named test that must fail | Why it fails |
|---|---|---|
| Change `version < highest` to `version > highest` | `TestRecordRosterVersionRefusesARollback` | It records 4 then offers 3; with the comparison flipped, 3 is accepted and the test's explicit failure fires |
| Change `version == highest` to return an error | `TestRecordRosterVersionAcceptsAnEqualVersion` | It presents the same version repeatedly; the second call now errors |
| Drop `CHECK (id = 1)` from the literal | `TestRosterVersionTableHoldsOneRow` | Both the `id=2` and the omitted-id inserts succeed, so the count moves off one |
| Change the upsert to `ON CONFLICT(id) DO NOTHING` | `TestRecordRosterVersionAdvances` | Nothing else reaches the update clause: the other tests record into an empty table or return at the equal case. With `DO NOTHING` the ratchet freezes at whatever version was recorded first and a later rollback is accepted — the exact failure this milestone exists to refuse |

---

## Task 2: The signed document, and both harnesses

**This is one commit and cannot be split.** Measured three times: with the scripts unchanged, `make tck` fails in about a second at `dsops roster sign` with `version is 0, want at least 1`.

**Files:**
- Modify: `internal/auth/roster.go`, `internal/auth/roster_test.go`
- Modify: `cmd/dsops/main.go`, `cmd/dsops/main_test.go`
- Modify: `cmd/dsbox/main.go` — it calls `auth.LoadRoster` and the commit does not build without it
- Modify: `internal/dsp/auth_middleware_test.go` — its roster fixture
- Modify: `test/tck/run.sh`, `demo/run.sh`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces:
  - `func LoadRoster(path string, signer ed25519.PublicKey, now time.Time) (Roster, error)`
  - `func SignRoster(path string, priv ed25519.PrivateKey, now time.Time) (string, error)`
  - `func (r Roster) Version() int`
  - `func (r Roster) ExpiresAt() time.Time`
  - `func (r Roster) UsableAt(now time.Time) bool`

### What the existing tests actually give you

The plan's first draft invented helper names. These are the real ones, and the tests below use only these:

- `writeRoster(t, body string) string` (`roster_test.go:13`)
- `encodedKey(t) string` — a fresh random public key (`:22`)
- `testSigner(t) (ed25519.PublicKey, ed25519.PrivateKey)` — **local to each test**, not package-level (`:31`)
- `signedRosterBody(t, participantsJSON string, priv ed25519.PrivateKey) string` (`:49`) — takes a participants array, not a whole document

`signedRosterBody` gains the two fields as parameters. Its doc comment cites `LoadRoster`'s "nothing is trusted before the signature verifies" — a sentence that is **not in `roster.go`** and whose premise this task inverts. Rewrite it to say what the order now is.

`cmd/dsops/main_test.go`'s `runDsops` calls `t.Fatalf` on error, so it cannot express a refusal. Add a sibling beside it:

```go
// runDsopsExpectingFailure is runDsops's negative twin: it returns the error
// instead of ending the test, which is the only way to assert that a
// subcommand refused.
func runDsopsExpectingFailure(t *testing.T, args ...string) error {
	t.Helper()
	return run(args, io.Discard)
}
```

- [ ] **Step 1: Write the failing tests**

Add to `internal/auth/roster_test.go`. Every fixture dates its expiry relative to `time.Now()` — a literal far-future date is refused by the cap before the test's own subject is reached.

```go
// validRoster returns a signed document with one participant, the given
// version, and an expiry the given distance ahead. Relative, never literal:
// a fixed far-future date trips maxRosterLifetime and the test then passes
// for a reason it is not about.
func validRoster(t *testing.T, priv ed25519.PrivateKey, version int, in time.Duration) string {
	t.Helper()
	participants := `[{"id":"alice","public_key":"` + encodedKey(t) + `"}]`
	return signedRosterBody(t, participants, priv, version, time.Now().Add(in).UTC().Format(time.RFC3339))
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
// The expiry stays inside the cap so that the only thing that can refuse
// this fixture is the signature.
func TestSignatureCoversVersionAndExpiry(t *testing.T) {
	signerPub, signerPriv := testSigner(t)
	body := validRoster(t, signerPriv, 1, 24*time.Hour)
	raised := strings.Replace(body, `"version":1`, `"version":9`, 1)
	if raised == body {
		t.Fatal("the fixture did not contain the field this test rewrites")
	}
	if _, err := LoadRoster(writeRoster(t, raised), signerPub, time.Now()); err == nil {
		t.Fatal("the version was raised after signing and the roster still loaded: the signature does not cover it")
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
```

`TestLoadRosterRejectsBadSignatures` and `TestLoadRosterRejectsUnusableFiles` build documents inline and will now be refused for a missing version rather than the reason each case names. Add a version and a relative expiry to every fixture in both, or they stop testing what they say.

And in `cmd/dsops/main_test.go`:

```go
// Signing a roster the connector would refuse prints a success the operator
// acts on, and a boot failure days later.
func TestRosterSignRefusesWhatLoadRosterWouldRefuse(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "operator.pem")
	runDsops(t, "keygen", "-out", keyPath)
	participants := `[{"id":"alice","public_key":"` + encodedTestKey(t) + `"}]`
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	for name, body := range map[string]string{
		"no version":       `{"participants":` + participants + `,"expires_at":"` + future + `"}`,
		"version zero":     `{"participants":` + participants + `,"version":0,"expires_at":"` + future + `"}`,
		"no expiry":        `{"participants":` + participants + `,"version":1}`,
		"malformed expiry": `{"participants":` + participants + `,"version":1,"expires_at":"2027-01-01"}`,
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
```

`encodedTestKey` is whatever this package already uses to produce a base64url public key; if it has none, generate one inline the way `TestKeygenThenTokenVerifies` does.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/auth/ ./cmd/dsops/ 2>&1 | tail -30`
Expected: compile failure — `LoadRoster` takes two arguments, `maxRosterLifetime` and `UsableAt` undefined, `signedRosterBody` takes three.

- [ ] **Step 3: Extend the document and what the signature covers**

```go
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
	// canonicalRosterBytes of the fields above.
	Signature string `json:"signature"`
}
```

`canonicalRosterBytes` takes the document and marshals a struct carrying participants, version, and expiry in declaration order. Its doc comment's "Every field of `rosterEntry` is a plain string" argument becomes "every field serialized here is a plain string or an int — which is why the expiry is a string parsed only for comparison".

- [ ] **Step 4: Add the cap, the accessors, and one boundary**

```go
// maxRosterLifetime bounds how far ahead an expiry may sit. Without it the
// upper bound this milestone puts on revocation is whatever the operator
// typed, which is the same defect DECISIONS.md section 10's five minutes has
// on the token side: a lifetime the issuer chooses and the verifier does not
// check.
//
// A constant and not configuration. A configurable maximum is a second
// policy the signature does not carry, so a deployment could widen its own
// and the widest one would be the weak link.
const maxRosterLifetime = 400 * 24 * time.Hour
```

`Roster` gains unexported `version int` and `expiresAt time.Time`, with `Version()`, `ExpiresAt()`, and:

```go
// UsableAt reports whether this roster is still trusted at now. Usable at
// every instant before the expiry and not at it — the same reading of a
// deadline the data endpoint's dataset window and token expiry already use.
//
// A zero Roster is not usable. That is deliberate and it is not how
// "authentication is off" is expressed: absence is a nil predicate at the
// call site, never a zero value here.
func (r Roster) UsableAt(now time.Time) bool { return now.Before(r.expiresAt) }
```

**`LoadRoster`'s own expiry check calls `UsableAt`.** One `now.Before` in the package, so a mutation to the boundary has exactly one site and the boundary test reaches it.

- [ ] **Step 5: Reorder and extend `LoadRoster`**

Group the document-level checks so `SignRoster` can share them:

```go
// checkRosterDocument validates what the document must carry regardless of
// who signed it. It runs before the signature verifies, beside the checks
// already there — participants non-empty, and a signature present at all.
//
// Rejecting on unauthenticated input is fail-closed, which is a different
// thing from acting on unauthenticated claims: Verify in token.go draws that
// line, and this stays on the safe side of it. The reason it matters here is
// the upgrade message. Every roster written before this milestone lacks both
// fields, and reporting a signature failure would send an operator to look
// at the wrong thing.
func checkRosterDocument(path string, doc rosterDocument) error {
```

Signature-presence stays inline in `LoadRoster` rather than moving in here: `SignRoster` reads a file that has no signature yet, so it cannot apply that one.

After the signature verifies, `LoadRoster` parses the expiry, refuses one beyond `maxRosterLifetime` from `now`, and refuses one where `!UsableAt(now)`.

- [ ] **Step 6: Make `SignRoster` validate**

It takes `now`, applies `checkRosterDocument`, parses the expiry, refuses one already past, and refuses one beyond the cap. It does not judge how far ahead within the cap — that is the operator's policy. It still writes nothing.

Without the clock parameter no expired-roster fixture can be signed at all, which Task 3's central fixture needs.

- [ ] **Step 7: Fix every caller and fixture**

`cmd/dsbox/main.go:84` and `cmd/dsops/main.go:130` pass `time.Now()`. Every roster literal in `internal/auth/roster_test.go`, `internal/dsp/auth_middleware_test.go`, and `cmd/dsops/main_test.go` gains both fields.

- [ ] **Step 8: Both harness scripts**

In each of `test/tck/run.sh` and `demo/run.sh`, compute the expiry **once** into a variable before the first heredoc, then interpolate it into both. Computing it per heredoc splits the copies across a second boundary, and the signature covers the value.

```sh
# Computed once: the two heredocs below must carry the same value, because
# the signature covers it and the second copy is what the connector loads.
# A day out — long enough for a cold image build plus the suite, short
# enough to be a real timestamp rather than a far-future constant that would
# leave the field decorative.
#
# date has no portable relative form: GNU takes -d, macOS takes -v, and
# busybox has neither, where this aborts under set -eu rather than producing
# a wrong timestamp. Verified on macOS: the GNU form fails cleanly and the
# fallback runs.
roster_expiry=$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
	|| date -u -v+1d +%Y-%m-%dT%H:%M:%SZ)
```

Both heredocs gain `"version": 1,` and `"expires_at": "$roster_expiry",`.

- [ ] **Step 9: Run everything**

Run: `go vet ./... && go test -race -count=1 ./... && make tck && make demo`
Expected: all green, `make tck` reporting 65 required tests passed.

A day is inside the warning window Task 5 adds, so from that task onward every harness run logs that the roster expires soon. Expected, and Task 5's comment says so.

- [ ] **Step 10: Commit**

```bash
git add internal/auth/ cmd/dsops/ cmd/dsbox/main.go internal/dsp/auth_middleware_test.go test/tck/run.sh demo/run.sh
git commit -m "feat: the roster carries a revision and an expiry, both signed"
```

**Mutations for this task.** Every row below was applied to an implementation of this plan and killed by the test named.

| Mutation | Named test that must fail | Why it fails |
|---|---|---|
| Drop the `version >= 1` check | `TestLoadRosterRequiresAVersion` | Its fixture has the field removed after signing, so the signature fails either way — the assertion that catches this is the second one, on the error naming `version` |
| Sign participants alone again | `TestSignatureCoversVersionAndExpiry` | It raises `version` after signing; with a narrower signature the document verifies, and its expiry is inside the cap so nothing else refuses it |
| Remove the expiry comparison from `LoadRoster` | `TestLoadRosterRefusesAnExpiredRoster` | Its fixture is signed an hour in the past and would load |
| Change `UsableAt` to `!now.After(...)` | `TestLoadRosterBoundaryIsExclusive` | It asks at exactly the expiry and asserts not usable |
| Remove the cap check | `TestLoadRosterRefusesAnExpiryTooFarAhead` | Its fixture sits past `maxRosterLifetime` and would load |
| Move `checkRosterDocument` after `ed25519.Verify` | `TestRequiredFieldsAreCheckedBeforeTheSignature` | Its fixture fails both; the assertion is on which error comes back |
| Skip `SignRoster`'s validation | `TestRosterSignRefusesWhatLoadRosterWouldRefuse` | It signs rosters the connector would refuse and asserts a non-nil error |

## Task 3: The DSP listener and the initiate hooks refuse

**Files:**
- Modify: `internal/dsp/router.go`, `internal/dsp/auth_middleware.go`
- Modify: `internal/dsp/negotiation_handler.go`, `internal/dsp/transfer_handler.go` — the handler structs are declared here, beside `knownParticipant`, not in the `*_consumer_handler.go` files that hold the initiate methods
- Modify: `internal/dsp/negotiation_consumer_handler.go`, `internal/dsp/transfer_consumer_handler.go` — the initiate methods themselves
- Create: `internal/dsp/roster_expiry_test.go`

**Interfaces:**
- Consumes: `Roster.UsableAt` from Task 2.
- Produces: `Routers.RosterUsable func() bool` — nil when authentication is off, non-nil otherwise. Tasks 4 and 5 both take this value.

### The guard, and why it is one value

Spec §5.3 wants **one** warning across every surface that refuses, not one per surface. A `*sync.Once` sitting in `requireParticipant` alone cannot do that, and a package-level one is the shape §11.3 warns about, where one test's use changes what a later test observes. So the predicate and its warning travel together:

```go
// rosterGuard answers whether the roster is still usable, and carries the
// warning that says so — once, however many surfaces refuse. A per-request
// warning is the log firehose cmd/dsbox/main.go's authentication-off comment
// exists to prevent, and its reason applies here unchanged.
type rosterGuard struct {
	usable func() bool
	warn   *sync.Once
}
```

`auth_middleware.go` does not import `sync` today.

### Building an expired router in a test

`LoadRoster` refuses an expired roster and `Roster`'s fields are unexported, so a test cannot hand `NewRouter` an already-dead roster directly. It can do this instead: sign a roster whose expiry is an hour in the past, and load it with a `now` a minute before that expiry. It loads, and every later `UsableAt(time.Now())` is false.

```go
// expiredRouter mirrors authedRouter with a roster that has since expired.
// The construction is the only one available: LoadRoster refuses an expired
// document, so the roster is loaded at an instant before its expiry and is
// dead by the time any request arrives.
//
// It restores mintOutboundCredential. NewRouter assigns that package
// variable and never puts it back, so without this every test after this one
// runs with a refusing minter — and the measured symptom is not a failure
// but a package that hangs until its timeout.
func expiredRouter(t *testing.T) (http.Handler, InitiateHandlers) {
```

- [ ] **Step 1: Write the failing tests**

```go
// Every DSP route refuses once the roster has expired. 409 and not 401: the
// caller's credential may be perfect and the fault is local. 409 and not
// 503: this repository's own wire contract records that a 5xx raises
// immediately on the TCK's negative paths, exactly like the 404 that
// DECISIONS.md section 25.1 forbids.
func TestExpiredRosterRefusesEveryDSPRequest(t *testing.T) {
	handler, _ := expiredRouter(t)
	for _, rt := range dspRoutes(t) {
		if openRoutes[rt.path] {
			continue
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}")))
		if rec.Code != http.StatusConflict {
			t.Errorf("%s %s: got %d, want 409 — an expired connector is still answering counterparties",
				rt.method, rt.path, rec.Code)
		}
	}
}

// The hooks live on the management listener, which requireParticipant never
// wraps. Without their own check an expired connector refuses every
// counterparty and goes on starting exchanges and signing with its real key.
//
// The body is empty on purpose. That trips the required-field check, so this
// also pins that the expiry check runs first: move it below and this
// answers 400.
func TestExpiredRosterRefusesTheInitiateHooks(t *testing.T) {
	_, initiate := expiredRouter(t)
	for name, h := range map[string]http.Handler{
		"negotiations": initiate.Negotiation,
		"transfers":    initiate.Transfer,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("{}")))
		if rec.Code != http.StatusConflict {
			t.Errorf("%s/initiate: got %d, want 409 — an expired connector must not start an exchange",
				name, rec.Code)
		}
	}
}

// The refusal names the roster rather than the caller's credential.
func TestTheExpiryRefusalNamesTheRoster(t *testing.T) {
	handler, _ := expiredRouter(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", VersionPath+"/catalog/request", strings.NewReader("{}")))
	if !strings.Contains(strings.ToLower(rec.Body.String()), "roster") {
		t.Errorf("body %q does not say what is wrong", rec.Body)
	}
}

// The refusal is logged once, not once per request. An expired connector
// answers every request forever, and a per-request warning buries the line
// an operator is looking for.
func TestTheExpiryWarningIsLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })

	handler, initiate := expiredRouter(t)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", VersionPath+"/catalog/request", strings.NewReader("{}")))
	}
	rec := httptest.NewRecorder()
	initiate.Negotiation.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("{}")))

	if n := strings.Count(buf.String(), "roster has expired"); n != 1 {
		t.Errorf("the expiry was logged %d times across every surface, want once", n)
	}
}
```

Match `"roster has expired"` to whatever wording the refusal actually logs; the assertion is on the count, not the phrase.

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/dsp/ -run 'TestExpiredRoster|TestTheExpiry' -v`
Expected: FAIL — `expiredRouter` undefined; once written, 401 rather than 409.

- [ ] **Step 3: Build the guard in `NewRouter`**

Above the early return, so the initiate hooks have it in both branches:

```go
	// Non-nil only when there is a roster to expire. NewRouter's own rule
	// applies: with authentication off the check is absent, not silently
	// false — a zero Roster's zero expiry must not read as expired.
	var guard rosterGuard
	if cfg.AuthRequired() {
		guard = rosterGuard{
			usable: func() bool { return roster.UsableAt(time.Now()) },
			warn:   &sync.Once{},
		}
	}
```

Both return statements populate `Routers.RosterUsable` from `guard.usable`. The handlers take the whole `guard`.

- [ ] **Step 4: Refuse in `requireParticipant`**

Before the credential is read. The answer does not depend on the credential, and verifying one against a roster this connector has declared unusable cannot mean anything.

The status and the message live in **one shared helper** used by every refusing surface, so there is one place to change and one place to read.

- [ ] **Step 5: Refuse in both initiate handlers**

First, ahead of the required-field check and the address guard, because the refusal is about this connector rather than about the request.

This inverts the comment in each handler saying the roster check "runs last" — `negotiation_consumer_handler.go:67-68` and `transfer_consumer_handler.go:95`. Task 6 corrects both, and the pre-existing ordering test survives because it builds its handler directly, leaving the guard zero.

- [ ] **Step 6: Run everything**

Run: `go vet ./... && go test -race -count=1 ./... && make tck`
Expected: green. `make tck` proves nothing about this task — see the note at the top of this plan.

- [ ] **Step 7: Commit**

```bash
git add internal/dsp/
git commit -m "feat: an expired roster refuses every request and every initiate call"
```

**Mutations for this task.**

| Mutation | Named test that must fail | Why it fails |
|---|---|---|
| Delete the check in `requireParticipant` | `TestExpiredRosterRefusesEveryDSPRequest` | Every route falls through to the credential check and answers 401 |
| Delete the check in the initiate handlers | `TestExpiredRosterRefusesTheInitiateHooks` | Both hooks fall through to the required-field check and answer 400 |
| Move the initiate check below the address guard | `TestExpiredRosterRefusesTheInitiateHooks` | Its body is empty, so required-fields answers 400 before the expiry is consulted. An earlier draft declined this mutation as undetectable; measured, it is detectable, and only the pre-existing ordering test is blind to it |
| Return 503 instead of 409 | `TestExpiredRosterRefusesEveryDSPRequest` and `TestExpiredRosterRefusesTheInitiateHooks` | The status lives in one shared helper, so both fail — 503 is the code the spec's §5.1 rejects and the reason is recorded there |
| Warn per request instead of once | `TestTheExpiryWarningIsLoggedOnce` | It drives several requests plus a hook and counts the lines |

---

## Task 4: Nothing goes out under an expired roster

**Files:**
- Modify: `internal/dsp/callback.go`, `internal/dsp/negotiation_client.go`, `internal/dsp/transfer_client.go`, `internal/dsp/transfer_consumer_handler.go`, `internal/dsp/router.go`
- Modify: `internal/dsp/callback_test.go` and `internal/dsp/transfer_consumer_handler_test.go` — both assign `mintOutboundCredential` with the one-value shape and break on the signature change
- Modify: `internal/dsp/roster_expiry_test.go`

**Interfaces:**
- Consumes: the guard from Task 3.
- Produces: `mintOutboundCredential func(aud string) (authorization string, maySend bool)`.

- [ ] **Step 1: Write the failing tests**

```go
// An expired connector that keeps sending is worse than one that stops: it
// signs with its real key while refusing every reply, so the exchange it
// starts cannot finish.
func TestExpiredRosterSendsNothing(t *testing.T) {
	expiredRouter(t) // installs the minter and restores it on cleanup
	if _, maySend := mintOutboundCredential("urn:participant:peer"); maySend {
		t.Error("the minter permitted a send under an expired roster")
	}
}

// The branches that are not about expiry keep their present behaviour. This
// milestone deliberately does not decide whether they should.
func TestTheMinterStillPermitsItsOtherFailures(t *testing.T) {
	restore := mintOutboundCredential
	t.Cleanup(func() { mintOutboundCredential = restore })
	authedRouter(t)
	if _, maySend := mintOutboundCredential(""); !maySend {
		t.Error("an empty audience must proceed unsigned, as it does today")
	}
}

// With authentication off there is no roster, and the package default must
// permit — otherwise a dev-mode connector silently sends nothing.
func TestTheDefaultMinterPermits(t *testing.T) {
	restore := mintOutboundCredential
	t.Cleanup(func() { mintOutboundCredential = restore })
	mintOutboundCredential = defaultMintOutboundCredential
	if _, maySend := mintOutboundCredential("urn:participant:peer"); !maySend {
		t.Error("the default refused; a connector with authentication off would send nothing")
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/dsp/ -run 'TestExpiredRosterSendsNothing|TestTheMinter|TestTheDefaultMinter' -v`
Expected: compile failure — the minter returns one value.

- [ ] **Step 3: Change the contract**

```go
// mintOutboundCredential returns the Authorization header value for a
// message to aud, and whether the message may be sent at all.
//
// The second return is not the first being empty. An empty audience and a
// minting error both proceed without a credential, exactly as they did
// before this; only an expired roster stops a send. Merging the two would
// silently change what happens when a counterparty has no address.
//
// The package default permits. A connector with authentication off never
// reaches the assignment in NewRouter, and a refusing default would leave it
// sending nothing.
var mintOutboundCredential = defaultMintOutboundCredential

func defaultMintOutboundCredential(string) (string, bool) { return "", true }
```

- [ ] **Step 4: Make the assigned minter refuse**

In `NewRouter`, the minter this milestone installs consults the guard **first**:

```go
	mintOutboundCredential = func(aud string) (string, bool) {
		if !guard.usable() {
			guard.warnExpired()
			return "", false
		}
		// ... unchanged from here: empty audience, Mint error, success
	}
```

This step is the one the mutation table's first row is about, and an earlier draft of this plan omitted it entirely — the contract changed and nothing ever returned `false`.

- [ ] **Step 5: Fix every call site**

Each takes the same shape: on `!maySend`, do not send.

- `callback.go:95` abandons the retry schedule and returns as it does for an exhausted one. The credential is minted per attempt, so an expiry landing between attempts is observed on the next.
- `negotiation_client.go:44` and `transfer_client.go:44` return an error to their caller, which is what they already do when a request fails.
- `transfer_consumer_handler.go:550`'s pull records its failure through the outcome it has already deferred.

The two test files that assign the variable move with them.

- [ ] **Step 6: The pull's refusal log carries the body**

`transfer_consumer_handler.go:647-651` logs `"data endpoint refused the pull"` with the status alone. After this milestone the DSP listener answers 409 for an expired roster and the data endpoint already answers 409 for its own reasons, so the status no longer tells an operator what happened.

Read at most a small fixed number of bytes from the body and log them. Name the bound as a constant beside the client rather than inlining a literal, the way `maxAgreementBodyBytes` is named in `internal/mgmt`.

- [ ] **Step 7: Run everything**

Run: `go vet ./... && go test -race -count=2 ./... && make tck && make demo`

`-count=2` is what CI runs. If the minter leaks, the symptom is a hang rather than a failure — a package that stops producing output is this, and it was reproduced during validation.

- [ ] **Step 8: Commit**

```bash
git add internal/dsp/
git commit -m "feat: an expired roster stops what this connector sends"
```

**Mutations for this task.**

| Mutation | Named test that must fail | Why it fails |
|---|---|---|
| Return `true` unconditionally from the assigned minter | `TestExpiredRosterSendsNothing` | It asks the minter directly after installing an expired router |
| Make the empty-audience branch return `false` | `TestTheMinterStillPermitsItsOtherFailures` | It asserts the unchanged branch still permits |
| Make `defaultMintOutboundCredential` return `false` | `TestTheDefaultMinterPermits` | It installs the default and asserts it permits |

---

## Task 5: `/health`, the boot log, and the wiring

**Files:**
- Modify: `internal/mgmt/router.go`, `internal/mgmt/router_test.go`, `cmd/dsbox/main.go`
- Create: `cmd/dsbox/roster_version_test.go`

**Interfaces:**
- Consumes: `Routers.RosterUsable` (Task 3), `Store.RecordRosterVersion` (Task 1), `Roster.Version`/`ExpiresAt` (Task 2).
- Produces: `mgmt.NewRouter(cfg config.Config, st *store.Store, rosterUsable func() bool, negotiationInitiate, transferInitiate http.Handler) http.Handler`.

`internal/mgmt/router_test.go`'s `newTestRouter(t) (http.Handler, *store.Store)` is used by `route_coverage_test.go` too. Keep that signature and add a sibling rather than changing it:

```go
// newTestRouterWithRoster builds a router whose roster answers usable or
// not. newTestRouter delegates to it with a usable one, so every existing
// caller — including the route-coverage assertions — is untouched.
func newTestRouterWithRoster(t *testing.T, usable func() bool) (http.Handler, *store.Store)
```

- [ ] **Step 1: Write the failing tests**

```go
// A readiness probe that cannot see this keeps a connector in rotation when
// it can serve no counterparty. 503 and not 409: this is a probe on the
// management listener, and DECISIONS.md section 25.1 governs what a DSP
// endpoint emits, not this.
func TestHealthReportsAnExpiredRoster(t *testing.T) {
	t.Parallel()
	h, _ := newTestRouterWithRoster(t, func() bool { return false })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "roster") {
		t.Errorf("body %q does not say what is wrong", rec.Body)
	}
}

func TestHealthStaysOKWithAUsableRoster(t *testing.T) {
	t.Parallel()
	h, _ := newTestRouterWithRoster(t, func() bool { return true })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// With authentication off there is no roster and nothing to be expired.
// A nil predicate must read as "no check", not as "not usable".
func TestHealthIsOKWithNoRosterAtAll(t *testing.T) {
	t.Parallel()
	h, _ := newTestRouterWithRoster(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — a connector with authentication off is healthy", rec.Code)
	}
}
```

And the source-parsing guard, which is the only thing that catches a deleted call:

```go
// Deleting the call that records the roster version leaves build, vet,
// go test, make tck and make demo all green — measured. Go does not error on
// an unreferenced function and go vet does not report one, so extracting the
// logic makes it testable and leaves the call deletable.
//
// This is the technique internal/dsp/auth_middleware_test.go and
// internal/mgmt/route_coverage_test.go already use for the same problem:
// read the source rather than keep a list that goes stale.
//
// The pattern requires the error to be handled, not only the call to be
// present. An earlier draft matched the call alone, and a swallowed error
// then survived — measured.
func TestMainRecordsTheRosterVersion(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	call := regexp.MustCompile(`RecordRosterVersion\(\s*roster\.Version\(\)\s*\)\s*;\s*err\s*!=\s*nil`)
	if !call.Match(src) {
		t.Error("main.go does not call st.RecordRosterVersion(roster.Version()) and act on its error; " +
			"a rollback to a superseded roster would boot silently")
	}
}
```

Write the call in `main.go` in the `if err := …; err != nil` form so this matches. If a different form reads better, change the pattern with it — the point is that the error is handled, not the exact spelling.

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/mgmt/ ./cmd/dsbox/ 2>&1 | tail -30`
Expected: compile failure in `mgmt`, and the source-parsing test failing against a `main.go` with no such call.

- [ ] **Step 3: `mgmt.NewRouter` takes the predicate**

A plain `func() bool`, by the same route `Routers.Initiate` already travels, so this package still holds no opinion about `internal/dsp`. A nil predicate means there is no roster and `/health` stays 200.

`/health`'s comment says it is unauthenticated because "it carries no information". That stops being true. Rewrite it: it now carries one fact about this connector's own configuration, which is the same disclosure the DSP refusal makes, and a readiness probe that cannot see it is not reporting readiness.

- [ ] **Step 4: Wire `cmd/dsbox`**

The version check goes in **a new `if cfg.AuthRequired()` block after `store.Open`** — not in the roster-load block, which closes at `main.go:93` before the store exists at `:98`. **A lower version aborts startup**, returning the error like every other fatal condition in `run`.

It must sit **before `dsp.NewRouter`**, so a rollback is refused before the outbound minter global is armed and before the pull context exists.

The roster's identity goes on **its own line at load time**, not on the `connector started` line at `:158-164`, which runs after both listeners serve and past the check that can refuse to start. Include the approach warning there:

```go
	// The harness rosters are dated a day out, so every make tck and make
	// demo run trips this. That is expected, not a defect.
	const rosterExpiryWarning = 30 * 24 * time.Hour
```

- [ ] **Step 5: Run everything**

Run: `go vet ./... && go test -race -count=1 ./... && make tck && make demo`

`make demo` exercises the readiness loop against the new `/health`. A roster already expired at boot kills the process, so the probe fails on a refused connection; the 503 path is reached only by one that expires mid-run.

- [ ] **Step 6: Commit**

```bash
git add internal/mgmt/ cmd/dsbox/
git commit -m "feat: an expired roster is visible to a probe and to the boot log"
```

**Mutations for this task.**

| Mutation | Named test that must fail | Why it fails |
|---|---|---|
| Delete the `RecordRosterVersion` call from `main.go` | `TestMainRecordsTheRosterVersion` | It reads `main.go` and matches the call |
| Call it but discard the error | `TestMainRecordsTheRosterVersion` | The pattern requires `; err != nil` beside the call. An earlier draft declined this mutation as undetectable; measured, tightening the pattern detects it |
| Make `/health` answer 200 unconditionally | `TestHealthReportsAnExpiredRoster` | It builds a router with a refusing predicate and asserts 503 |
| Treat a nil predicate as not usable | `TestHealthIsOKWithNoRosterAtAll` | It passes nil and asserts 200 |

---

## Task 6: Every sentence this milestone made false

The spec's §9 is the list. Each edit names the code fact it was checked against, and no edit introduces a count.

**Files:** `internal/auth/roster.go`, `internal/store/store.go`, `internal/mgmt/router.go`, `internal/dsp/negotiation_consumer_handler.go`, `internal/dsp/transfer_consumer_handler.go`, `internal/auth/token.go`, `internal/dsp/auth_middleware.go`, `cmd/dsops/main.go`, `DECISIONS.md`, `README.md`, `SECURITY.md`, `config.example.yaml`, `docs/goal-gap-analysis.md`, `docs/milestone-sequence.md`, `docs/follow-ups.md`, `test/tck/compose.yaml`, `docs/superpowers/specs/2026-08-24-initiate-hook-authorization-design.md`

- [ ] **Step 1: The code comments**

`Roster`'s "a key here is trusted, and anything else is not" — true only until the expiry. `LoadRoster`'s enumeration of what makes a roster unusable. The two comments in `negotiation_consumer_handler.go` and `transfer_consumer_handler.go` saying the roster check "runs last", which Task 3 inverted, and the ordering test's own comment. `/health`'s justification, if Task 5 has not already done it. `cmd/dsops`'s "It deliberately does not manage the roster" — validating is not managing.

`internal/auth/token.go:33`, `internal/dsp/auth_middleware.go:45`, and `internal/dsp/auth_middleware_test.go:164` all say "the six ways" a credential can be wrong. Correct today and forbidden by the count rule; this milestone adds a refusal that is not about the credential at all, which is the occasion. Rewrite all three without the number.

`internal/auth/roster_test.go`'s `signedRosterBody` doc comment cites `LoadRoster`'s "nothing is trusted before the signature verifies" — a sentence that is **not in `roster.go`**, and whose premise Task 2 inverts. If Task 2 has not already rewritten it, do it here.

- [ ] **Step 2: `DECISIONS.md`**

New §36 for this milestone's decisions — §35 is currently the last section and every section from §21 is a per-milestone append.

§9's trade-off gains the upper bound and what that bound costs. §27.2's "the bytes signed are `json.Marshal` of the parsed `[]rosterEntry`" stops being literally true. §23.2's "This project only uses CREATE TABLE, INSERT, SELECT, UPDATE" (`DECISIONS.md:497-498`) is outgrown by Task 1's `INSERT … ON CONFLICT DO UPDATE`. And §35's "any other non-2xx is retried with backoff first" is over-broad: the wire contract at `docs/superpowers/specs/2026-08-16-transfer-process-tck-wire-contract.md:165` and `:257` records that retry is 4xx-non-404 only and that a 5xx raises.

**§2.3's `connectorAddress` reassignment lands in all three documents**, not one: `docs/goal-gap-analysis.md:255-256`, `DECISIONS.md:2849-2851`, and — as a dated bracket, per Step 3 — `docs/superpowers/specs/2026-08-24-initiate-hook-authorization-design.md:690-693`.

**§25.1 is not amended.** An earlier draft of the spec proposed an exception for a 503 and withdrew it.

- [ ] **Step 3: The dated spec**

`docs/superpowers/specs/2026-08-24-initiate-hook-authorization-design.md:427` carries the same over-broad sentence — it is what §35 was written from. Dated specs are kept as written and corrected with a dated bracket, not edited.

- [ ] **Step 4: `config.example.yaml`**

Its inline roster JSON becomes invalid, and it is the only onboarding document that shows the shape. Add both fields to the example, say both are inside the signature, and add the recommended interval with what the choice trades: the interval **is** the upper bound, so shorter is a stronger guarantee and a more frequent fleet-wide restart.

Its "a connector that cannot verify anyone should say so at boot, not by refusing every request later" — refusing every request later is now deliberate.

**Do not carry the spec's §10.1 sentence "Nothing enforces a maximum" into this file.** §3.4 supersedes it: `LoadRoster` caps the interval. §10.1's ninety days is a recommendation inside that cap, and that is what the example config should say.

- [ ] **Step 5: `README.md`, `SECURITY.md`, and the working documents**

`README.md`'s roster description and what the DSP listener requires. `SECURITY.md` gains the inert-under-`require_auth` sentence and the disclosure this milestone opens. `docs/goal-gap-analysis.md`'s P3 revocation bullet and its ordered item 3, which still bundles the clock work the spec's §2.2 splits out. Item 4 (`:367-370`) is "Discovery — a catalog client" and says nothing about `connectorAddress` today; this milestone is what puts it there. `docs/milestone-sequence.md` gains this milestone and the fact that no harness verifies it. `docs/follow-ups.md`'s package-variable entry gains why this milestone cannot follow its remedy.

`test/tck/compose.yaml:24-25` says the connector reads the roster once at startup — still true, no longer sufficient. Both harness scripts gain a readiness-loop comment about the 503.

- [ ] **Step 6: Verify each claim, then commit**

For every edited sentence, name the file and line it was checked against. Run `go vet ./... && go test -race -count=1 ./... && make tck && make demo` one last time.

```bash
git commit -m "docs: record the roster milestone and correct what it made false"
```

---

## The two mutations an earlier draft declined, and why they are now prescribed

This plan's first draft declined both of these as untestable. Applying them proved otherwise, and the reasoning that declined them is worth keeping visible because it was wrong in an instructive way.

**The initiate handlers' check order.** The draft reasoned only about the *pre-existing* ordering test, which builds its handler directly and leaves the guard zero, so it is indeed blind to the move. But Task 3's own new test posts an empty body — which trips the required-field check — so moving the expiry check below the address guard makes it answer 400. The mutation is in Task 3's table.

**A swallowed `RecordRosterVersion` error.** The draft's regexp matched the call alone, so discarding the error survived it. Tightening the pattern to require `; err != nil` beside the call detects it. The mutation is in Task 5's table.

The lesson is narrow and worth writing down: a mutation is undetectable only against the tests that exist, and this plan writes new ones. Declining a mutation on the strength of the old tests is the same mistake as prescribing one that nothing observes.

## Self-review

Checked against the spec: every section from §3 to §8 has a task; §9's list is Task 6; §10's trade-offs need no code; §11's table is distributed across the tasks with a why-line per row; §11.1's inventory of what changes signature is reflected in each task's Files block; §11.2's commit order is the task order; §11.3's hazard is carried in Tasks 3 and 4.

Every mutation row in this plan was applied to an implementation of it and killed by the test named beside it. One row was not, in an earlier draft: the signature-coverage mutation survived because its fixture's expiry sat outside `maxRosterLifetime`, so the cap refused the document before the signature was ever the reason. That is why every fixture here dates relative to `time.Now()`.

Names are consistent across tasks: `RosterUsable`, `UsableAt`, `RecordRosterVersion`, `maxRosterLifetime`, `defaultMintOutboundCredential`, `rosterGuard`, `checkRosterDocument`, `expiredRouter`, `newTestRouterWithRoster`, `runDsopsExpectingFailure`, `validRoster`.

Two things are named by behaviour rather than by code and the implementer decides them: the bound on how much of a refused pull's body is logged (Task 4 Step 6), and the exact wording of the expiry refusal, which Task 3's log-count test matches on.
