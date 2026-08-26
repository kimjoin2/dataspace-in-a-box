# Clock leeway, and a lifetime the verifier enforces

**Status:** design, agreed 2026-08-26.

**Ordered item 3a** of `docs/goal-gap-analysis.md` — the half the roster
milestone split out. Its §2.2 already decided the content: add leeway to the
`exp` comparison, do not add `nbf`, and bound `exp - iat`. This spec fixes the
values and the trade-offs those decisions leave open.

**What it acts on:** `docs/goal-gap-analysis.md`'s P3 clock-skew bullet — "no
leeway, no `nbf`, and `iat` is minted but never checked".

**What it amends:** `DECISIONS.md` §28's statement of what bounds replay
exposure. The window it names moves, and §7 says by how much and why that is
the whole of the change.

---

## 1. The finding this milestone acts on

`Verify`'s only time check is `now.Unix() >= c.Exp`
(`internal/auth/token.go`). Two consequences follow, and the second is the
larger one.

### 1.1 The refusal is one-directional

A common intuition is that clock skew fails both ways. Here it does not.

An issuer whose clock runs **behind** mints a token that is already closer to
expiry than the verifier thinks, and past `credentialTTL` of skew it is
refused outright. An issuer whose clock runs **ahead** is simply accepted, and
its token lives `credentialTTL` plus the skew. Nothing refuses it.

So the failure this milestone fixes is the slow-clock direction, and the
fast-clock direction is not a failure at all — it is a silent extension of the
lifetime, which §1.2 is about.

### 1.2 `Verify` does not bound `exp`, and `iat` is never read

`credentialTTL = 5 * time.Minute` lives at the minting site
(`internal/dsp/router.go`). `claims` carries `Iat`, `Mint` sets it, and
nothing ever reads it.

A roster participant can therefore mint, with its own key, a token whose `exp`
is years away, and every other connector accepts it for years. §28 declined
replay defence and named the window's length as what bounds the exposure —
but the verifier does not enforce that length. It is a convention of the
minter.

The roster milestone diagnosed exactly this shape and refused to reproduce it:
`internal/auth/roster.go`'s `maxRosterLifetime` exists because "the upper
bound is whatever the operator typed" was unacceptable for the roster. The
same sentence was true of the token the whole time.

---

## 2. The decision, in one paragraph

`Verify` compares `exp` against `now` less a fixed leeway, so a slow clock
costs a minute rather than every request. It also refuses a token whose
`exp - iat` exceeds a fixed maximum, so the lifetime is something the
verifier enforces rather than something the minter promises. `nbf` is not
added: it would newly refuse pairs that transact fine today, and the property
it reaches for is the lifetime bound.

---

## 3. Scope

### 3.1 In

- A leeway constant applied to the `exp` comparison (§4).
- A maximum-lifetime constant applied to `exp - iat`, with its own sentinel
  error (§5).
- The documentation the widened window makes false (§7).

### 3.2 Out: `nbf`

Decided by the roster spec's §2.2 and restated because it is
counter-intuitive. Adding `nbf` would refuse a token before its issuer's
clock-ahead offset elapsed — that is, it would start refusing the direction
that works today. It tightens rather than loosens, and §5's bound closes the
fast-clock lifetime extension more precisely than `nbf` would.

### 3.3 Out: making `credentialTTL` the enforced bound

This is the one a reader will expect and §5.2 is why it cannot be done.

### 3.4 Out: replay defence

§28 declined it and this does not reopen it. What changes is only the
window's length, which §7 records.

---

## 4. The leeway

`Verify` compares against `now.Add(-leeway)` rather than `now`. Applied to
`exp` only — there is no other time claim to apply it to, since §3.2 declines
`nbf` and §5 uses `iat` as a duration rather than as an instant.

**Sixty seconds, and the number is measured rather than chosen.**
`TestVerifyRefusals`'s "expired" case mints at `now` with a five-minute TTL
and verifies at `now + 6m`. With sixty seconds of leeway the comparison lands
exactly on the boundary — `now + 6m - 60s` equals `exp` — and `>=` still
refuses. At sixty-one seconds it does not, and that case stops testing
expiry at all.

So sixty is the largest value the existing suite tolerates without moving its
fixture, and moving that fixture to buy more leeway would mean weakening the
test that proves expiry works in order to widen expiry's tolerance. If a
deployment ever needs more, the fixture moves first and deliberately.

A constant, not configuration. A configurable leeway is a second policy
nothing signs, so the most generous deployment would be the weak link — the
same argument `maxRosterLifetime` records for its own cap.

---

## 5. The lifetime bound

`Verify` refuses a token whose `exp - iat` exceeds a maximum, with a new
sentinel error. This is what makes the lifetime a property of verification
rather than of minting.

### 5.1 `iat` becomes required in practice

An absent `iat` decodes to zero, so `exp - 0` is enormous and the token is
refused. That is the right answer and it is a new requirement worth stating:
every minting path in this repository already sets `iat`, so nothing observable
changes, but a token minted elsewhere without one is now refused.

### 5.2 The bound is one hour, and it cannot be five minutes

The obvious value is `credentialTTL`. It cannot be, and the reason is in this
repository's own harness.

`test/tck/run.sh` mints with `-ttl 30m`, for a recorded reason: it mints
**before** a cold image build, because the same string has to reach the
connector as `DSBOX_MGMT_TOKEN` before it starts (`DECISIONS.md` §35.4). At
five minutes the credential dies before the suite begins. A five-minute bound
turns `make tck` red immediately.

One hour, then. It holds the harness's thirty minutes with room, and it
refuses the year-long token §1.2 describes.

**What that buys, stated plainly, because overstating it would repeat the
defect this milestone exists to fix.** §10's five minutes remains a convention
of what this connector mints. The bound stops an absurd lifetime; it does not
enforce §10. A participant can still mint a fifty-minute token and be accepted.
Closing that gap means either changing what the harness needs or making the
bound configurable per deployment, and §3.3 declines both here.

### 5.3 The sentinel

A new error joins the set in `internal/auth/token.go`. Its doc comment says
"Every one of them is a fact about the credential", and a lifetime longer than
this connector accepts is such a fact, so it belongs there. That comment no
longer carries a count, so nothing else has to move.

The middleware keeps logging the reason and never echoing it.

---

## 6. Where the checks sit

Both go **after** the signature verifies, beside the existing expiry and
audience checks. `Verify`'s doc comment already draws that line — the claims
are not trusted before the signature — and neither of these is an exception:
both read `exp` and `iat`, which are claims.

Order among them: the lifetime bound before the expiry comparison. A token
with an absurd lifetime is refused for that rather than for whichever side of
its enormous window `now` happens to fall on, which is the more useful answer
in a log.

---

## 7. What becomes false

The accepted replay window moves from the token's lifetime to its lifetime
plus the leeway. Every sentence that states the number is affected, and one of
them says explicitly that the number did not change.

- `DECISIONS.md` §28 — "it until it expires, five minutes after minting —
  unchanged from before this". That clause is what this milestone changes, and
  §28 is amended rather than corrected: the exposure is now the lifetime plus
  the leeway, and the leeway exists for the reason §4 gives.
- `DECISIONS.md` §28's statement of what bounds the exposure — §10's window
  length is now enforced by the verifier only up to §5.2's ceiling, and that
  distinction is the honest version.
- `README.md`'s "can be replayed until it expires, five minutes after it was".
- `internal/auth/token.go`'s package comment, "valid for five minutes".
- `internal/dsp/callback.go`'s retry comment, which reasons about "the
  credential's five-minute life" when deciding to mint per attempt. The
  reasoning survives; the number gains a qualifier.
- `docs/goal-gap-analysis.md`'s P3 clock-skew bullet, which this closes, and
  its ordered list, which carries this as item 3a.
- `docs/milestone-sequence.md` — this milestone joins the shipped list.
- This milestone's decisions land in a new `DECISIONS.md` section.

Each edit names the code fact it was checked against.

---

## 8. Trade-offs accepted

**The replay window widens.** By the leeway, on every credential. §28 accepted
a window bounded by its length and this makes that window longer. The
alternative is refusing every slow-clock pair, which is the failure §1.1
describes.

**No `nbf`, so a fast clock still extends a lifetime — up to §5.2's ceiling.**
The bound caps it; it does not eliminate it.

**The bound does not enforce §10.** §5.2 says why and what it would cost to
change.

**A constant rather than configuration, twice.** Neither the leeway nor the
bound is per-deployment, for the reason §4 gives: a policy nothing signs is
only as strong as the most generous deployment.

**Nothing here requires NTP and nothing checks for it.** A minute of leeway is
an assumption about how far apart two participants' clocks are, and no
document states it as a requirement. This milestone does not add one — it
makes the assumption survivable rather than removing it.

---

## 9. Evidence

`go test -race` carries this, as it carried the roster half — no harness
exercises a clock difference, because every container shares one host clock.

| Mutation | Killed by |
|---|---|
| Remove the leeway from the `exp` comparison | a test that verifies a token expired by less than the leeway and expects it accepted |
| Raise the leeway past sixty seconds | `TestVerifyRefusals`'s existing "expired" case |
| Remove the lifetime bound | a test that mints far past the maximum and expects a refusal |
| Compare `exp - iat` against the wrong constant | the same test, bounded just past the maximum |
| Accept a token with no `iat` | a test that mints one without it |
| Put the lifetime bound before the signature verifies | a test whose token is both over-long and badly signed, asserting which error comes back |

Each row must be applied and confirmed to kill the test named beside it. A row
whose test cannot be made to fail is a wrong row, and this repository has
shipped several — the roster milestone found one that survived every gate
because its fixture tripped a different check first.

**The harness must stay green**, and §5.2 is the reason it is worth checking
rather than assuming: `-ttl 30m` sits inside the bound, and a bound chosen
without looking would have broken it.

---

## 10. What this does not decide

Whether `credentialTTL` should be what the verifier enforces. §5.2 records
what stands in the way — the harness's legitimate need to mint before a cold
build — and closing it is a decision about the harness, not about `Verify`.
