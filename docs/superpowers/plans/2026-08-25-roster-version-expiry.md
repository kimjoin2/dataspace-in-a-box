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

Two independent throwaway implementations of this spec have been built and mutation-tested. Both reached all five gates green and killed all nine mutations. This plan is therefore not exploratory; the following are measurements, not predictions.

**The commit order is forced.** `internal/store` alone is green. `internal/auth` and both harness scripts are **one indivisible commit** — with the scripts unchanged, `make tck` fails in under two seconds at `dsops roster sign` with `version is 0, want at least 1`. The wiring follows.

**No harness verifies any of this.** `make tck` and `make demo` stayed green under every one of the nine mutations. `go test` is the only gate that carries this milestone. Do not take a green `make tck` as evidence that a task worked.

**The outbound minter leaks between tests.** `mintOutboundCredential` is a package-level variable that `NewRouter` assigns and never restores. A test that builds a router with an expired roster leaks a refusing minter into every test after it, and the measured first symptom is not a failure but a **hang** — the package runs until its timeout panics, inside a test waiting on a pull that can no longer be dispatched. Every test that installs an expired router restores the minter.

**`roster_version.id` is a rowid alias.** `id INTEGER PRIMARY KEY CHECK (id = 1)` fires for `id=2` and for a duplicate `id=1` — and also for an **omitted** `id`, which takes the next rowid. Writes name `id` explicitly and upsert.

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
5. **Health, boot log, and wiring** — needs Tasks 1 and 2 both.
6. **Documentation** — last, so every sentence describes code that exists.

---

## Task 1: The store's roster-version ratchet

**Files:**
- Modify: `internal/store/store.go`
- Create: `internal/store/roster_version_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func (s *Store) RecordRosterVersion(version int) error` — returns nil when `version` is at least the highest recorded, recording it when strictly higher; returns an error naming both versions when lower.

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
| Drop `CHECK (id = 1)` from the literal | `TestRosterVersionTableHoldsOneRow` | The `id=2` insert succeeds and the count reaches two |

---

## Task 2: The signed document, and both harnesses

**This is one commit and cannot be split.** Measured: with the scripts unchanged, `make tck` fails in under two seconds at `dsops roster sign`.

**Files:**
- Modify: `internal/auth/roster.go`, `internal/auth/roster_test.go`
- Modify: `cmd/dsops/main.go`, `cmd/dsops/main_test.go`
- Modify: `internal/dsp/auth_middleware_test.go` (its roster fixture)
- Modify: `test/tck/run.sh`, `demo/run.sh`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces:
  - `func LoadRoster(path string, signer ed25519.PublicKey, now time.Time) (Roster, error)`
  - `func SignRoster(path string, priv ed25519.PrivateKey, now time.Time) (string, error)`
  - `func (r Roster) Version() int`
  - `func (r Roster) ExpiresAt() time.Time`
  - `func (r Roster) UsableAt(now time.Time) bool` — reports whether the roster has not yet expired. A zero `Roster` is not usable; absence is expressed by a nil predicate at the call site, never by this method.

- [ ] **Step 1: Write the failing tests**

Add to `internal/auth/roster_test.go`. The existing fixture helper builds `{"participants":…,"signature":…}`; extend it to take a version and an expiry rather than adding a second helper.

```go
// The fields are required, and the message says which one is missing. That
// is the upgrade experience: every roster in existence predates them.
func TestLoadRosterRequiresAVersion(t *testing.T) {
	t.Parallel()
	body := `{"participants":[{"id":"a","public_key":"` + testPub + `"}],"expires_at":"2099-01-01T00:00:00Z"}`
	_, err := LoadRoster(writeRoster(t, signed(t, body)), signerPub, time.Now())
	if err == nil {
		t.Fatal("absent, which decodes to zero: a roster with version 0 loaded without error")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error %q does not name the missing field", err)
	}
}

// The signature covers them, or they are decoration an attacker rewrites.
func TestSignatureCoversVersionAndExpiry(t *testing.T) {
	t.Parallel()
	signedBody := signed(t, validRosterBody(1, "2099-01-01T00:00:00Z"))
	raised := strings.Replace(signedBody, `"version":1`, `"version":9`, 1)
	if raised == signedBody {
		t.Fatal("the fixture did not contain the field this test rewrites")
	}
	if _, err := LoadRoster(writeRoster(t, raised), signerPub, time.Now()); err == nil {
		t.Fatal("changing version did not change the signed bytes")
	}
}

// An expired roster is a startup failure, the same grade as a bad signature.
func TestLoadRosterRefusesAnExpiredRoster(t *testing.T) {
	t.Parallel()
	body := signed(t, validRosterBody(1, "2020-01-01T00:00:00Z"))
	if _, err := LoadRoster(writeRoster(t, body), signerPub, time.Now()); err == nil {
		t.Fatal("an expired roster loaded")
	}
}

// The boundary is exclusive: usable at every instant before expires_at,
// unusable at it and after. This connector already reads a deadline that way.
func TestLoadRosterBoundaryIsExclusive(t *testing.T) {
	t.Parallel()
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	path := writeRoster(t, signed(t, validRosterBody(1, exp.Format(time.RFC3339))))
	if _, err := LoadRoster(path, signerPub, exp.Add(-time.Second)); err != nil {
		t.Errorf("a second before the expiry: %v", err)
	}
	if _, err := LoadRoster(path, signerPub, exp); err == nil {
		t.Error("at the expiry: loaded, want refused")
	}
}

// Without a cap the upper bound this milestone claims is whatever the
// operator typed, and a roster dated far enough ahead makes the mechanism
// decoration. The design spec's section 3.4 has the argument.
func TestLoadRosterRefusesAnExpiryTooFarAhead(t *testing.T) {
	t.Parallel()
	far := time.Now().Add(maxRosterLifetime + 24*time.Hour).UTC().Format(time.RFC3339)
	if _, err := LoadRoster(writeRoster(t, signed(t, validRosterBody(1, far))), signerPub, time.Now()); err == nil {
		t.Fatal("an expiry beyond the cap loaded")
	}
}

// A malformed timestamp is caught at load rather than becoming a silent
// per-request refusal on a connector whose boot log said nothing.
func TestLoadRosterRefusesAMalformedExpiry(t *testing.T) {
	t.Parallel()
	if _, err := LoadRoster(writeRoster(t, signed(t, validRosterBody(1, "2027-01-01"))), signerPub, time.Now()); err == nil {
		t.Fatal("a date without a time loaded")
	}
}

// Document-level structure is checked before the signature, where this file
// already checks that participants is non-empty and that a signature is
// present. The upgrade message depends on it: every roster in existence
// lacks both fields, and "signature does not verify" would send the operator
// to the wrong place.
func TestRequiredFieldsAreCheckedBeforeTheSignature(t *testing.T) {
	t.Parallel()
	// No version AND a signature that cannot verify. If the order were
	// reversed, this would report the signature.
	body := `{"participants":[{"id":"a","public_key":"` + testPub + `"}],"expires_at":"2099-01-01T00:00:00Z","signature":"AAAA"}`
	_, err := LoadRoster(writeRoster(t, body), signerPub, time.Now())
	if err == nil {
		t.Fatal("loaded")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error %q reports the signature; the field check must come first", err)
	}
}
```

And in `cmd/dsops/main_test.go`:

```go
// Signing a roster the connector would refuse prints a success the operator
// acts on and a boot failure days later. The tool refuses instead.
func TestRosterSignRefusesWhatLoadRosterWouldRefuse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, body string }{
		{"no version", `{"participants":[…],"expires_at":"2099-01-01T00:00:00Z"}`},
		{"expiry in the past", `{"participants":[…],"version":1,"expires_at":"2020-01-01T00:00:00Z"}`},
		{"malformed expiry", `{"participants":[…],"version":1,"expires_at":"2027-01-01"}`},
	} {
		out, err := runDsops(t, "roster", "sign", "-roster", writeTemp(t, c.body), "-key", keyPath)
		if err == nil {
			t.Errorf("%s: dsops roster sign printed %q for a roster the connector would refuse", c.name, out)
		}
	}
}
```

Fill the elided participant lists from whatever the existing tests in each file already use.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/auth/ ./cmd/dsops/ -v 2>&1 | tail -40`
Expected: compile failure — `LoadRoster` takes two arguments, `maxRosterLifetime` undefined.

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
	// json.Marshal's error on the argument that every field here is a plain
	// string or an int, and a time.Time makes that false and the discarded
	// error reachable.
	ExpiresAt string `json:"expires_at"`
	// Signature is the operator's Ed25519 signature (base64url) over
	// canonicalRosterBytes over participants, version, and expiry.
	Signature string `json:"signature"`
}
```

`canonicalRosterBytes` takes the document rather than the participant slice, and marshals a struct carrying the three signed fields in declaration order. Its doc comment's "Every field of rosterEntry is a plain string" argument becomes "every field serialized here is a plain string or an int — which is why the expiry is a string parsed only for comparison".

- [ ] **Step 4: Add the cap and the accessors**

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

`Roster` gains unexported `version int` and `expiresAt time.Time`, with `Version()`, `ExpiresAt()`, and `UsableAt(now time.Time) bool` returning `now.Before(r.expiresAt)`.

- [ ] **Step 5: Reorder and extend `LoadRoster`**

The required-field checks go **beside the existing document-level ones**, before `ed25519.Verify`: participants non-empty, version at least 1, expiry present and parseable, signature present. The expiry comparison and the cap check go **after** the signature verifies — they are decisions about a document this connector has decided to trust.

Rejecting on unauthenticated input is fail-closed, which is different from *acting* on unauthenticated claims — the distinction `Verify` in `token.go` draws. Say so in the comment.

- [ ] **Step 6: Make `SignRoster` validate**

It takes `now`, applies the same required-field rules and the same cap, and refuses an expiry already in the past. It does not judge how far ahead within the cap — that is the operator's policy. It still writes nothing.

Without the clock parameter no expired-roster fixture can be signed at all, which every expiry test needs.

- [ ] **Step 7: Fix every caller and fixture**

`cmd/dsbox/main.go` and `cmd/dsops/main.go` pass `time.Now()`. Every roster literal in `internal/auth/roster_test.go`, `internal/dsp/auth_middleware_test.go`, and `cmd/dsops/main_test.go` gains both fields.

- [ ] **Step 8: Both harness scripts**

In each of `test/tck/run.sh` and `demo/run.sh`, compute the expiry **once** into a variable before the first heredoc, then interpolate it into both. Computing it per heredoc splits the two copies across a second boundary, and the signature covers the value.

```sh
# Twenty-four hours, computed once: the two heredocs below must carry the
# same value, because the signature covers it and the second copy is what
# the connector loads. Long enough for a cold image build plus the suite,
# short enough to be a real timestamp rather than a far-future constant
# that would leave the field decorative.
#
# date has no portable relative form: GNU takes -d, macOS takes -v, and
# busybox has neither, where this aborts under set -eu rather than
# producing a wrong timestamp.
roster_expiry=$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
	|| date -u -v+1d +%Y-%m-%dT%H:%M:%SZ)
```

Both heredocs gain `"version": 1,` and `"expires_at": "$roster_expiry",`.

- [ ] **Step 9: Run everything**

Run: `go vet ./... && go test -race -count=1 ./... && make tck && make demo`
Expected: all green, `make tck` reporting 65 required tests passed.

A day is inside the thirty-day warning window Task 5 adds, so from that task onward every harness run logs that the roster expires soon. That is expected and Task 5's comment says so.

- [ ] **Step 10: Commit**

```bash
git add internal/auth/ cmd/dsops/ internal/dsp/auth_middleware_test.go test/tck/run.sh demo/run.sh
git commit -m "feat: the roster carries a revision and an expiry, both signed"
```

**Mutations for this task.**

| Mutation | Named test that must fail | Why it fails |
|---|---|---|
| Drop the `version >= 1` check | `TestLoadRosterRequiresAVersion` | Its fixture omits the field, which decodes to zero and would then load |
| Sign participants alone again | `TestSignatureCoversVersionAndExpiry` | It rewrites `version` after signing; with a narrower signature the document still verifies |
| Remove the expiry comparison from `LoadRoster` | `TestLoadRosterRefusesAnExpiredRoster` | Its fixture is dated in the past and would load |
| Change `now.Before(exp)` to `!now.After(exp)` | `TestLoadRosterBoundaryIsExclusive` | It loads at exactly the expiry and asserts a refusal |
| Remove the cap check | `TestLoadRosterRefusesAnExpiryTooFarAhead` | Its fixture sits past `maxRosterLifetime` and would load |
| Move the required-field checks after `ed25519.Verify` | `TestRequiredFieldsAreCheckedBeforeTheSignature` | Its fixture fails both; the assertion is on which error comes back |
| Skip `SignRoster`'s validation | `TestRosterSignRefusesWhatLoadRosterWouldRefuse` | It signs three rosters the connector would refuse and asserts a non-zero exit |

---

## Task 3: The DSP listener and the initiate hooks refuse

**Files:**
- Modify: `internal/dsp/router.go`, `internal/dsp/auth_middleware.go`, `internal/dsp/negotiation_consumer_handler.go`, `internal/dsp/transfer_consumer_handler.go`
- Create: `internal/dsp/roster_expiry_test.go`

**Interfaces:**
- Consumes: `auth.Roster`'s `UsableAt` from Task 2.
- Produces: `Routers.RosterUsable func() bool` — nil when authentication is off, non-nil otherwise. Task 5 hands the same value to `internal/mgmt`.

- [ ] **Step 1: Write the failing tests**

```go
// Every DSP route refuses once the roster has expired, and says why. 409 and
// not 401: the caller's credential may be perfect and the fault is local.
// 409 and not 503: this repository's own wire contract records that a 5xx
// raises immediately on the TCK's negative paths, exactly like the 404
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
```

`expiredRouter` mirrors `authedRouter` with a roster whose expiry is in the past, and **restores `mintOutboundCredential`** on cleanup. Without that restore the package hangs until its timeout — measured.

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/dsp/ -run TestExpiredRoster -v`
Expected: FAIL — `expiredRouter` undefined, then 401 rather than 409 once it exists.

- [ ] **Step 3: Build the predicate in `NewRouter`**

Above the early return, so it is non-nil in both branches where the initiate hooks need it:

```go
	// Non-nil only when there is a roster to expire. NewRouter's own rule
	// applies: with authentication off the check is absent, not silently
	// false — a zero Roster's zero expiry must not read as expired.
	var rosterUsable func() bool
	if cfg.AuthRequired() {
		rosterUsable = func() bool { return roster.UsableAt(time.Now()) }
	}
```

Both return statements populate `Routers.RosterUsable`.

- [ ] **Step 4: Refuse in `requireParticipant`**

Before the credential is read. The answer does not depend on the credential, and verifying one against a roster this connector has declared unusable cannot mean anything.

The warning is logged once, not once per request — `cmd/dsbox/main.go`'s authentication-off comment states the principle and its reason. Use a `*sync.Once` held beside the predicate, not a package-level `sync.Once`: a package-level one is the shape that makes one test's use change what a later test observes.

- [ ] **Step 5: Refuse in both initiate handlers**

First, ahead of the required-field check and the address guard, because the refusal is about this connector rather than about the request — no correction to the body would make the call succeed.

This inverts the comment in each handler saying the roster check "runs last". Task 6 corrects both, and the ordering test survives because it builds its handler directly, leaving the predicate nil.

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
| Delete the check in `requireParticipant` | `TestExpiredRosterRefusesEveryDSPRequest` | Every route falls through to the credential check and answers 401 rather than 409 |
| Delete the check in the initiate handlers | `TestExpiredRosterRefusesTheInitiateHooks` | Both hooks fall through to the required-field check and answer 400 |
| Return `http.StatusServiceUnavailable` instead | `TestExpiredRosterRefusesEveryDSPRequest` | It asserts the status, and 503 is the code the spec's section 5.1 rejects |
| Move the initiate check below the address guard | *not prescribed* | The existing ordering test builds its handler with a nil predicate, so nothing observes the move. Cover the order with an assertion only if a test can be written that fails without it |

---

## Task 4: Nothing goes out under an expired roster

**Files:**
- Modify: `internal/dsp/callback.go`, `internal/dsp/negotiation_client.go`, `internal/dsp/transfer_client.go`, `internal/dsp/transfer_consumer_handler.go`, `internal/dsp/router.go`
- Modify: `internal/dsp/roster_expiry_test.go`

**Interfaces:**
- Consumes: `Routers.RosterUsable` from Task 3.
- Produces: `mintOutboundCredential func(aud string) (authorization string, maySend bool)`.

- [ ] **Step 1: Write the failing test**

```go
// An expired connector that keeps sending is worse than one that stops: it
// signs with its real key while refusing every reply, so the exchange it
// starts cannot finish.
func TestExpiredRosterSendsNothing(t *testing.T) {
	restore := mintOutboundCredential
	t.Cleanup(func() { mintOutboundCredential = restore })
	expiredRouter(t)
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

- [ ] **Step 2: Run and watch it fail**

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
// reaches the assignment below, and a refusing default would leave it
// sending nothing.
var mintOutboundCredential = defaultMintOutboundCredential

func defaultMintOutboundCredential(string) (string, bool) { return "", true }
```

- [ ] **Step 4: Fix every call site**

Each takes the same shape: on `!maySend`, do not send.

- `callback.go` abandons the retry schedule and returns as it does for an exhausted one. The credential is minted per attempt, so an expiry landing between attempts is observed on the next.
- `negotiation_client.go` and `transfer_client.go` return an error to their caller, which is what they already do when a request fails.
- `transfer_consumer_handler.go`'s pull records its failure through the outcome it has already deferred.

- [ ] **Step 5: The pull's refusal log carries the body**

`transfer_consumer_handler.go` currently logs `"data endpoint refused the pull"` with the status alone. After this milestone the DSP listener answers 409 for an expired roster and the data endpoint already answers 409 for its own reasons, so the status is no longer enough to tell an operator what happened. Log the response body alongside it, bounded.

- [ ] **Step 6: Run everything**

Run: `go vet ./... && go test -race -count=2 ./... && make tck && make demo`

`-count=2` is what CI runs. If the minter leaks, the symptom is a hang rather than a failure — if a package stops producing output, that is this.

- [ ] **Step 7: Commit**

```bash
git add internal/dsp/
git commit -m "feat: an expired roster stops what this connector sends"
```

**Mutations for this task.**

| Mutation | Named test that must fail | Why it fails |
|---|---|---|
| Return `true` unconditionally from the assigned minter | `TestExpiredRosterSendsNothing` | It asks the minter directly under an expired roster |
| Make the empty-audience branch return `false` | `TestTheMinterStillPermitsItsOtherFailures` | It asserts the unchanged branch still permits |
| Make `defaultMintOutboundCredential` return `false` | `TestTheDefaultMinterPermits` | It installs the default and asserts it permits |

---

## Task 5: `/health`, the boot log, and the wiring

**Files:**
- Modify: `internal/mgmt/router.go`, `internal/mgmt/router_test.go`, `cmd/dsbox/main.go`
- Create: `cmd/dsbox/roster_version_test.go`

**Interfaces:**
- Consumes: `Routers.RosterUsable` (Task 3), `Store.RecordRosterVersion` (Task 1), `Roster.Version`/`ExpiresAt` (Task 2).
- Produces: `mgmt.NewRouter(cfg, st, rosterUsable func() bool, negotiationInitiate, transferInitiate http.Handler) http.Handler`.

- [ ] **Step 1: Write the failing tests**

```go
// A readiness probe that cannot see this keeps a connector in rotation when
// it can serve no counterparty. 503 and not 409: this is a probe on the
// management listener, and DECISIONS.md section 25.1 governs what a DSP
// endpoint emits.
func TestHealthReportsAnExpiredRoster(t *testing.T) {
	t.Parallel()
	h := newTestRouterWithRoster(t, func() bool { return false })
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
	h := newTestRouterWithRoster(t, func() bool { return true })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
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
// internal/mgmt/route_coverage_test.go already use for the same problem: read
// the source rather than keep a list that goes stale.
//
// It catches deletion only. A call present but passed the wrong argument, or
// one whose error is swallowed, is not caught here — the store's own tests
// cover the behaviour, which is a different guarantee from the two being
// connected.
func TestMainRecordsTheRosterVersion(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !regexp.MustCompile(`RecordRosterVersion\(\s*roster\.Version\(\)\s*\)`).Match(src) {
		t.Error("main.go does not call st.RecordRosterVersion(roster.Version()); " +
			"a rollback to a superseded roster would boot silently")
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/mgmt/ ./cmd/dsbox/ -v 2>&1 | tail -30`
Expected: compile failure in `mgmt`, and the source-parsing test failing on a `main.go` that has no such call.

- [ ] **Step 3: `mgmt.NewRouter` takes the predicate**

A plain `func() bool`, by the same route `Routers.Initiate` already travels, so this package still holds no opinion about `internal/dsp`.

`/health`'s comment says it is unauthenticated because "it carries no information". That stops being true. Rewrite it: it now carries one fact about this connector's own configuration, which is the same disclosure the DSP refusal makes, and a readiness probe that cannot see it is not reporting readiness.

- [ ] **Step 4: Wire `cmd/dsbox`**

The version check goes in **a new `if cfg.AuthRequired()` block after `store.Open`** — not in the roster-load block, which closes before the store exists.

The roster's identity goes on **its own line at load time**, not on the `connector started` line, which runs after both listeners serve and past the check that can refuse to start. Include the approach warning there: if the roster expires within thirty days, say so.

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
| Make `/health` answer 200 unconditionally | `TestHealthReportsAnExpiredRoster` | It builds a router with a refusing predicate and asserts 503 |
| Ignore `RecordRosterVersion`'s error | *not prescribed* | Nothing observes it — the source-parsing test matches the call, not its handling. Worth an assertion only if one can be written that fails |

---

## Task 6: Every sentence this milestone made false

The spec's §9 is the list. Each edit names the code fact it was checked against, and no edit introduces a count.

**Files:** `internal/auth/roster.go`, `internal/store/store.go`, `internal/mgmt/router.go`, `internal/dsp/negotiation_consumer_handler.go`, `internal/dsp/transfer_consumer_handler.go`, `internal/auth/token.go`, `internal/dsp/auth_middleware.go`, `cmd/dsops/main.go`, `DECISIONS.md`, `README.md`, `SECURITY.md`, `config.example.yaml`, `docs/goal-gap-analysis.md`, `docs/milestone-sequence.md`, `docs/follow-ups.md`, `test/tck/compose.yaml`, `docs/superpowers/specs/2026-08-24-initiate-hook-authorization-design.md`

- [ ] **Step 1: The code comments**

`Roster`'s "a key here is trusted, and anything else is not" — true only until the expiry. `LoadRoster`'s enumeration of what makes a roster unusable. The two comments in `negotiation_consumer_handler.go` and `transfer_consumer_handler.go` saying the roster check "runs last", which Task 3 inverted, and the ordering test's own comment. `/health`'s justification, if Task 5 has not already done it. `cmd/dsops`'s "It deliberately does not manage the roster" — validating is not managing.

`internal/auth/token.go` and `internal/dsp/auth_middleware.go` both say "the six ways" a credential can be wrong. Correct today and forbidden by the count rule; this milestone adds a refusal that is not about the credential at all, which is the occasion. Rewrite both without the number.

- [ ] **Step 2: `DECISIONS.md`**

New §36 for this milestone's decisions — §35 is currently the last section and every section from §21 is a per-milestone append.

§9's trade-off gains the upper bound and what that bound costs. §27.2's "the bytes signed are `json.Marshal` of the parsed `[]rosterEntry`" stops being literally true. And §35's "any other non-2xx is retried with backoff first" is over-broad: the wire contract at `docs/superpowers/specs/2026-08-16-transfer-process-tck-wire-contract.md:165` and `:257` records that retry is 4xx-non-404 only and that a 5xx raises.

**§25.1 is not amended.** An earlier draft of the spec proposed an exception for a 503 and withdrew it.

- [ ] **Step 3: The dated spec**

`docs/superpowers/specs/2026-08-24-initiate-hook-authorization-design.md:427` carries the same over-broad sentence — it is what §35 was written from. Dated specs are kept as written and corrected with a dated bracket, not edited.

- [ ] **Step 4: `config.example.yaml`**

Its inline roster JSON becomes invalid, and it is the only onboarding document that shows the shape. Add both fields to the example, say both are inside the signature, and add the recommended interval with what the choice trades: the interval **is** the upper bound, so shorter is a stronger guarantee and a more frequent fleet-wide restart.

Its "a connector that cannot verify anyone should say so at boot, not by refusing every request later" — refusing every request later is now deliberate.

- [ ] **Step 5: `README.md`, `SECURITY.md`, and the working documents**

`README.md`'s roster description and what the DSP listener requires. `SECURITY.md` gains the inert-under-`require_auth` sentence and the disclosure this milestone opens. `docs/goal-gap-analysis.md`'s P3 revocation bullet and its ordered item 3, which still bundles the clock work the spec's §2.2 splits out — and item 4, which takes `connectorAddress`. `docs/milestone-sequence.md` gains this milestone and the fact that no harness verifies it. `docs/follow-ups.md`'s package-variable entry gains why this milestone cannot follow its remedy.

`test/tck/compose.yaml:24-25` says the connector reads the roster once at startup — still true, no longer sufficient. Both harness scripts gain a readiness-loop comment about the 503.

- [ ] **Step 6: Verify each claim, then commit**

For every edited sentence, name the file and line it was checked against. Run `go vet ./... && go test -race -count=1 ./... && make tck && make demo` one last time.

```bash
git commit -m "docs: record the roster milestone and correct what it made false"
```

---

## Two mutations deliberately not prescribed

**The initiate handlers' check order.** The existing ordering test builds its handler directly, leaving the predicate nil, so moving the expiry check below the address guard changes nothing any test observes. Prescribing it would be a mutation that tests nothing — the failure this project has hit in two consecutive milestones.

**A swallowed `RecordRosterVersion` error.** The source-parsing guard matches the call, not what happens to its return. Write an assertion for it only if one can be written that actually fails without it.

## Self-review

Checked against the spec: every section from §3 to §8 has a task; §9's list is Task 6; §10's trade-offs need no code; §11's table is distributed across the tasks with a why-line per row; §11.1's inventory of what changes signature is reflected in each task's Files block; §11.2's commit order is the task order; §11.3's hazard is carried in Tasks 3 and 4.

No placeholders. Every code step carries the code. Types and names are consistent across tasks: `RosterUsable`, `UsableAt`, `RecordRosterVersion`, `maxRosterLifetime`, `defaultMintOutboundCredential`.
