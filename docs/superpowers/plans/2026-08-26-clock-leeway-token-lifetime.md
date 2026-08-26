# Clock Leeway and Token Lifetime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `Verify` a minute of tolerance for a slow clock, and a maximum distance `exp` may sit from `now`, so a credential's lifetime is something the verifier measures rather than something the minter promises.

**Architecture:** Two comparisons and one sentinel error in `internal/auth/token.go`, plus the documentation the widened replay window makes false. No other package changes. Neither harness script changes.

**Tech Stack:** Go standard library only. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-26-clock-leeway-token-lifetime-design.md` — read it alongside this plan. Where the two disagree, the spec wins and the disagreement is a finding.

## Global Constraints

- Go standard library only. Ask before adding a dependency.
- English for all documentation, code comments, error strings, and commit messages. No emoji.
- **Never put a count in a code comment or in prose.** Rewrite without the number. The one exception is a number naming a fixed pair the design itself defines.
- Every documentation edit names the code fact it was checked against.
- A comment must be true of the code it sits next to, including not describing behaviour a later task adds.
- Final gates: `go vet ./...`, `go test -race -count=2 ./...` (what CI runs), `gofmt -l .` empty, `make tck` (must stay 65/65), `make demo` (both rounds).
- Work directly on `main` (authorised for this session). **Push requires the user's explicit word each time.**

---

## What is already known, and must not be rediscovered

This spec was implemented once on a throwaway copy, in both its drafts, and every mutation below was applied. What follows are measurements.

**The first draft passed every gate while containing a check that stopped nothing.** It bounded `exp - iat` — two integers the issuer signs — and a token dated a year ahead verified clean. `go build`, `go vet`, `go test -race -count=2`, `make tck` 65/65 and `make demo` were all green. The design that ships bounds `exp - now`. Do not reintroduce the other quantity; §9's table has a row whose only job is to catch it.

**Sixty seconds is exactly the boundary, and it is welded to one fixture.** `TestVerifyRefusals`'s "expired" case mints at `now` with a five-minute TTL and verifies at `now + 6m`. At sixty the case still refuses; at sixty-one it returns nil and stops testing expiry. That fixture's `now + 6m` is the only gate on the leeway constant — do not tidy it.

**The boundary also depends on the comparison staying `>=`.** Under `>` the maximum leeway would be fifty-nine, so at sixty the "expired" case would accept.

**A five-minute bound fails sixty-four of the sixty-five required TCK results**, not all of them; the survivor is the metadata suite's version-document test, because that endpoint sits outside the credential check. The bound that ships is an hour, and `test/tck/run.sh`'s `-ttl 30m` sits inside it, so **neither harness script changes**.

**No test holds the order of the two time checks.** Swapping them survives every gate. That is expected and §6 says so; do not write a row for it.

---

## File Structure

| File | Responsibility after this milestone |
|---|---|
| `internal/auth/token.go` | The leeway, the maximum, the new sentinel, and the two comparisons |
| `internal/auth/token_test.go` | The accepted-within-leeway case, the over-long refusals, and the order-vs-signature case |
| `DECISIONS.md` | §28 amended; a new section for this milestone's decisions |
| `README.md`, `internal/dsp/router.go`, `internal/dsp/callback.go`, `docs/goal-gap-analysis.md`, `docs/milestone-sequence.md` | The sentences the widened window makes false |

---

## Task 1: The two checks

**Files:**
- Modify: `internal/auth/token.go`, `internal/auth/token_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `ErrLifetimeTooLong`, and a `Verify` that applies both comparisons. No signature change — `Verify` already takes `now time.Time`.

- [ ] **Step 1: Write the failing tests**

```go
// A slow clock costs a minute, not every request. Before this, an issuer
// whose clock lagged the verifier by more than the credential's life was
// refused on every call, and the reason was hidden from it.
func TestVerifyAcceptsTokenExpiredWithinLeeway(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	tok := mustMint(t, priv, "alice", "bob", now)
	// Minted for five minutes, checked thirty seconds after it lapsed.
	if _, err := Verify(tok, staticKey("alice", pub), "bob", now.Add(5*time.Minute+30*time.Second)); err != nil {
		t.Errorf("token expired by 30s: err = %v, want accepted", err)
	}
}

// The lifetime is measured against the verifier's own clock, so no issuer can
// buy one by moving its claims. A token dated a year ahead carries a
// perfectly ordinary hour between iat and exp — which is why the quantity
// being measured has to be the distance from now.
func TestVerifyRefusesLifetimeBoughtByAClockRunningAhead(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	aYearAhead := now.Add(365 * 24 * time.Hour)
	tok := mustMint(t, priv, "alice", "bob", aYearAhead) // iat and exp both a year out
	if _, err := Verify(tok, staticKey("alice", pub), "bob", now); !errors.Is(err, ErrLifetimeTooLong) {
		t.Errorf("an hour dated a year ahead: err = %v, want %v", err, ErrLifetimeTooLong)
	}
}

// The plain case: a token whose exp is simply too far out.
func TestVerifyRefusesOverlongLifetime(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	for name, ttl := range map[string]time.Duration{
		"just past the maximum": maxCredentialLifetime + time.Minute,
		"a year":                365 * 24 * time.Hour,
	} {
		tok, err := Mint(priv, "alice", "bob", now, ttl)
		if err != nil {
			t.Fatalf("Mint: %v", err)
		}
		if _, err := Verify(tok, staticKey("alice", pub), "bob", now); !errors.Is(err, ErrLifetimeTooLong) {
			t.Errorf("%s: err = %v, want %v", name, err, ErrLifetimeTooLong)
		}
	}
}

// The maximum is a policy about a claim, so it is read only after the
// signature says the claim is the issuer's. A token that is both over-long
// and badly signed is refused for the signature.
func TestVerifyChecksSignatureBeforeLifetime(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	tok, err := Mint(otherPriv, "alice", "bob", now, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := Verify(tok, staticKey("alice", pub), "bob", now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("over-long and badly signed: err = %v, want %v", err, ErrBadSignature)
	}
}

// A counterparty may omit iat — RFC 7519 leaves it optional — and this
// connector reads none, so it must not refuse one for that.
func TestVerifyAcceptsTokenWithoutIat(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	tok := mintWithoutIat(t, priv, "alice", "bob", now, 5*time.Minute)
	if _, err := Verify(tok, staticKey("alice", pub), "bob", now); err != nil {
		t.Errorf("a token with no iat: err = %v, want accepted", err)
	}
}
```

`mintWithoutIat` is genuinely new, and the nearest-looking helper is a trap: `tamperPayload` rewrites a payload and **deliberately does not re-sign it**, which is how it produces `ErrBadSignature`. This one must sign what it builds, or the test passes for the wrong reason — it would be refused for the signature and never reach the check it is about.

Build the header and a claims map without an `iat` key at all (not `iat: 0`), marshal both, base64url them, and sign `header.payload` with the key, the way `Mint` does. Assert inside the helper that the payload it produced has no `iat`, so a later refactor cannot quietly reintroduce one and leave the test green.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/auth/ 2>&1 | tail -20`
Expected: compile failure — `ErrLifetimeTooLong` and `maxCredentialLifetime` undefined.

- [ ] **Step 3: Add the constants**

```go
// clockLeeway is how far behind this connector's clock an issuer's may run
// before its credentials start being refused. Applied to the expiry
// comparison only: there is no other time claim to apply it to.
//
// Sixty seconds, and the value is measured rather than chosen.
// TestVerifyRefusals mints at a fixed instant with a five-minute life and
// verifies six minutes later, so at sixty seconds that comparison lands
// exactly on the boundary and still refuses. At sixty-one it does not, and
// that case stops testing expiry at all. The constant and that fixture are
// welded: widening one means moving the other, deliberately.
//
// A constant rather than configuration. A configurable leeway is a policy
// nothing signs, so the most generous deployment would be the weak link.
const clockLeeway = 60 * time.Second

// maxCredentialLifetime is how far ahead of now a credential's expiry may
// sit. Without it the lifetime is whatever the issuer wrote, and a
// participant can mint itself a decade with its own key.
//
// Measured against now rather than against iat, which is the whole point: iat
// is the issuer's to choose, so the distance between two claims it signs
// bounds nothing. Against the verifier's own clock, no issuer offset buys
// lifetime.
//
// An hour rather than the five minutes DECISIONS.md section 10 sets for a
// minted credential, because the TCK harness mints with a longer life for a
// recorded reason — it mints before a cold image build. So this refuses an
// absurd lifetime rather than enforcing section 10, and the design spec's
// section 5.2 is precise about how little that buys.
const maxCredentialLifetime = time.Hour
```

- [ ] **Step 4: Add the sentinel**

Into the existing `var (...)` block. Its doc comment says every one of them is a fact about the credential, and a lifetime longer than this connector accepts is such a fact.

```go
	ErrLifetimeTooLong = errors.New("token lifetime is longer than this connector accepts")
```

- [ ] **Step 5: Apply both comparisons**

In `Verify`, below the "Authenticated from here down" line. Expiry keeps its place; the maximum goes after it.

```go
	if now.Add(-clockLeeway).Unix() >= c.Exp {
		return "", ErrExpired
	}
	if c.Exp-now.Unix() > int64(maxCredentialLifetime/time.Second) {
		return "", ErrLifetimeTooLong
	}
```

The order changes no answer — an expired token's distance is negative and passes the maximum; a far-future token is not expired and fails it. Say that in a comment rather than implying a behaviour the order does not have.

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/auth/ -v 2>&1 | tail -30`
Expected: PASS, including `TestVerifyRefusals` unchanged.

- [ ] **Step 7: Full gates and commit**

Run: `go vet ./... && go test -race -count=2 ./... && gofmt -l . && make tck && make demo`

`make tck` must still report 65 required tests passed. The harness mints with a life inside the maximum, so nothing there changes — but run it, because that is the claim the maximum's value rests on.

```bash
git add internal/auth/
git commit -m "feat: a minute of clock leeway, and a lifetime the verifier measures"
```

**Mutations for this task.** Each row says why the named test breaks.

| Mutation | Named test that must fail | Why it fails |
|---|---|---|
| Drop `-clockLeeway` from the expiry comparison | `TestVerifyAcceptsTokenExpiredWithinLeeway` | Its token lapsed thirty seconds ago and would be refused |
| Raise `clockLeeway` to sixty-one seconds | `TestVerifyRefusals` | Its "expired" case verifies six minutes after a five-minute mint, which then falls inside the leeway and returns nil |
| Change `>=` to `>` in the expiry comparison | `TestVerifyRefusals` | At sixty seconds of leeway the same case sits exactly on the boundary, so the operator decides it |
| Remove the maximum check | `TestVerifyRefusesOverlongLifetime` | Both its tokens verify cleanly without it |
| Compare against `credentialTTL` instead | `TestVerifyRefusals` and `make tck` | The harness mints past five minutes; this is the row that proves the maximum's value is load-bearing rather than decorative |
| **Compare `c.Exp - c.Iat` instead of `c.Exp - now.Unix()`** | `TestVerifyRefusesLifetimeBoughtByAClockRunningAhead` | Its token's `iat` and `exp` are both a year out, so their difference is an ordinary hour. This is the row that catches what the spec's first draft shipped, and it passed every gate |
| Refuse a token whose `iat` is absent | `TestVerifyAcceptsTokenWithoutIat` | RFC 7519 leaves `iat` optional and this design reads none |
| Move the maximum above `ed25519.Verify` | `TestVerifyChecksSignatureBeforeLifetime` | Its token is both over-long and signed by the wrong key |

**No row for the order of the two time checks.** Swapping them survives every gate, which §6 predicts and this plan will not pretend otherwise.

---

## Task 2: Every sentence the widened window makes false

**Files:** `DECISIONS.md`, `README.md`, `internal/auth/token.go`, `internal/dsp/router.go`, `internal/dsp/callback.go`, `docs/goal-gap-analysis.md`, `docs/milestone-sequence.md`

Each edit names the code fact it was checked against. Open the code for every claim; a previous milestone here shipped documentation checked against its plan instead, and a later cross-check found false statements throughout it.

- [ ] **Step 1: The code comments**

- `internal/auth/token.go`'s package comment says the credential is "valid for five minutes (DECISIONS.md section 10)". Five minutes is still what this connector mints; what changed is that the verifier now bounds what it accepts, and by a different number.
- `internal/dsp/router.go`'s `credentialTTL` comment says "Short enough to bound replay". The window it bounds is now longer by the leeway, and the verifier's own ceiling is elsewhere.
- `internal/dsp/callback.go`'s retry comment reasons about "the credential's five-minute life" when deciding to mint per attempt. The reasoning survives intact; the number gains the leeway.

- [ ] **Step 2: `DECISIONS.md`**

**§28 is amended, not corrected.** Its trade-off says a captured credential is usable "until it expires, five minutes after minting — unchanged from before this investigation", and names §10's window length as what bounds the exposure. Both move:

- the window is the lifetime plus the leeway;
- and the window length now genuinely bounds the exposure, which it did not before — an issuer's clock-ahead offset used to add to it with nothing capping it. The design spec's §7 has the measured shape across offsets, including that past the maximum a token spends the start of its life refused outright.

**A new section** for this milestone's decisions. §36 is currently the last, and every section from §21 is a per-milestone append.

- [ ] **Step 3: `README.md`**

"a captured credential can be replayed until it expires, five minutes after it was minted" — the same amendment, in the register README uses.

- [ ] **Step 4: The working documents**

- `docs/goal-gap-analysis.md`'s P3 clock-skew bullet is what this closes. It also cites `internal/auth/token.go:136` for the expiry check, which is not where that check is — the citation moves with the sentence. Its ordered item 3 carries this work in an addendum; that is where the milestone is recorded.
- `docs/milestone-sequence.md` gains this milestone, and the fact that `go test` is the only gate that carries it — no harness can exercise a clock difference, because every container shares one host clock.

- [ ] **Step 5: Verify and commit**

For every edited sentence, name the file and line of the code fact it was checked against. Then run the full gate set once more.

```bash
git commit -m "docs: record the clock milestone and the window it widened"
```

---

## Self-review

Checked against the spec: §4 and §5 are Task 1; §6's ordering is stated in a comment and deliberately unpinned; §7's list is Task 2; §8's trade-offs need no code; §9's table is Task 1's, with the two rows the spec's own revision added — the `exp - iat` substitution and the `>=` operator.

Two things the spec names that this plan deliberately does not carry into code: the parse-gate on `iat`'s type, which is pre-existing and which §5.1 declines to widen or close, and the management listener, which byte-compares its token and never parses it.

No placeholders. `mintWithoutIat` is the one helper this plan names without writing, because the shape depends on what the test file already has; Step 1 says to extend rather than duplicate.
