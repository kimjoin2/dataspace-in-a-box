# Clock leeway, and a lifetime the verifier enforces

**Status:** design, agreed 2026-08-26, revised the same day after a
cross-check found the lifetime half measuring the wrong quantity. §11 records
what that measured.

**The clock half of ordered item 3** of `docs/goal-gap-analysis.md` — that
document's item 3 carries this work in an addendum rather than as a numbered
item of its own, and the roster milestone shipped the other half. Its §2.2
decided the content: add leeway to the `exp` comparison, do not add `nbf`, and
bound the lifetime. This spec fixes the values, the quantity to bound, and the
trade-offs those decisions leave open.

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

*(2026-08-26: both thresholds above are the pre-milestone ones, which is what
a finding section should carry — but a reader carrying them forward gets them
wrong. After §4 the slow-clock refusal starts at `credentialTTL` plus the
leeway rather than at `credentialTTL`, and after §5 the fast-clock extension
is capped rather than ended — an issuer ten minutes ahead still buys its
offset, and what §5 stops is the climb past the maximum. §7's cross-offset
paragraph is the post-milestone shape and is the one to quote. Bracketed
after `DECISIONS.md` §37.1 inherited the first of them in the present
tense.)*

### 1.2 `Verify` does not bound `exp`, and `iat` is never read

`credentialTTL = 5 * time.Minute` lives at the minting site
(`internal/dsp/router.go`). `claims` carries `Iat`, `Mint` sets it, and
nothing ever reads it.

A roster participant can therefore mint, with its own key, a token whose `exp`
is years away, and every other connector accepts it for years. §28 declined
replay defence and named the window's length as what bounds the exposure —
but the verifier does not enforce that length. It is a convention of the
minter.

The roster milestone diagnosed exactly this shape and refused to reproduce
it: `internal/auth/roster.go`'s `maxRosterLifetime` exists because the upper
bound that milestone put on revocation would otherwise have been whatever the
operator typed. Both of that milestone's statements qualify the bound as being
about revocation, so the parallel is a parallel and not the same sentence —
but the shape is identical, and the token had it first.

---

## 2. The decision, in one paragraph

`Verify` compares `exp` against `now` less a fixed leeway, so a slow clock
costs a minute rather than every request. It also refuses a token whose `exp`
is further from `now` than a fixed maximum, so the lifetime is something the
verifier measures rather than something the minter promises. `nbf` is not
added: it would newly refuse pairs that transact fine today, and the property
it reaches for is what the maximum already bounds.

---

## 3. Scope

### 3.1 In

- A leeway constant applied to the `exp` comparison (§4).
- A maximum-lifetime constant applied to `exp - now`, with its own sentinel
  error (§5).
- The documentation the widened window makes false (§7).

### 3.2 Out: `nbf`

Decided by the roster spec's §2.2 and restated because it is
counter-intuitive. Adding `nbf` would refuse a token before its issuer's
clock-ahead offset elapsed — that is, it would start refusing the direction
that works today.

And it is unnecessary, for a reason the first draft of this spec got wrong.
§5's bound is measured at the verifier against the verifier's own clock, so
however far ahead an issuer's clock runs, the token is refused once `exp` sits
further out than the maximum. That is the fast-clock extension closed. **A
bound on `exp - iat` would not have done this** — §11 has the sequence.

**The first paragraph's argument cuts against §5's bound too, and the
difference is one of degree rather than kind.** `nbf` refuses a fast clock at
zero tolerance: an issuer a second ahead is refused for that second. §5's
bound refuses one only past the maximum, so an issuer an hour ahead transacts
and one three hours ahead does not. Both narrow the direction that works
today. Choosing the bound is choosing an hour of tolerance over none, not
choosing to leave the direction alone, and saying otherwise would be the same
overstatement §5.2 exists to avoid.

*(2026-08-26: the paragraph above puts the tolerance line at an hour of
offset — in the sentence about who transacts, and again in the clause about
what choosing the bound chooses. It sits short of that: a freshly minted
credential carries its issuer's offset on top of `credentialTTL`, so
tolerance ends at the maximum less the credential's lifetime. §7's
cross-offset paragraph is the shape to quote, and neither sentence should
have restated it with a rounder number. Bracketed after `DECISIONS.md` §37.5
inherited the error.)*

### 3.3 Out: making `credentialTTL` the enforced bound

This is the one a reader will expect and §5.2 is why it cannot be done.

### 3.4 Out: replay defence

§28 declined it and this does not reopen it. What changes is only the
window's length, which §7 records.

---

## 4. The leeway

`Verify` compares against `now.Add(-leeway)` rather than `now`. Applied to
`exp` only — there is no other time claim to apply it to, since §3.2 declines
`nbf` and §5 reads no other claim.

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

**The constant and that fixture are welded, and the comment beside each says
so.** The fixture's `now + 6m` is the only thing standing between the leeway
and any value at all — tidy it to a rounder number and the leeway loses its
only gate silently. Measured: at sixty the case still refuses, at sixty-one it
returns no error.

A constant, not configuration. A configurable leeway is a second policy
nothing signs, so the most generous deployment would be the weak link — the
same argument `maxRosterLifetime` records for its own cap.

---

## 5. The lifetime bound

`Verify` refuses a token whose `exp` sits further from `now` than a maximum,
with a new sentinel error. This is what makes the lifetime a property of
verification rather than of minting.

### 5.1 Against `now`, never against `iat`

The first draft of this spec bounded `exp - iat`, and that measures nothing.
Both are integers the token's issuer chose and signed, so an issuer who wants
a decade sets `iat` a decade ahead and `exp` fifty-nine minutes after it: the
difference is inside any maximum, the token is not expired, and it is accepted
for a decade. That is byte for byte the token §1.2 names, surviving the check
written to stop it. §11 has the sequence as the cross-check found it.

Measuring `exp - now` fixes it, and simplifies twice over. It reads no `iat`,
so this milestone does not make a claim RFC 7519 leaves optional into a
requirement, and a conformant counterparty that omits `iat` is unaffected —
which matters, because the refusal is a bodiless 401 whose reason is
deliberately hidden, so the counterparty's operator would have had no way to
learn what was wrong.

*(2026-08-26: this says "bodiless 401" here and in §11, and that is wrong —
`refuse` calls `writeError`, which emits a JSON error document saying a valid
participant credential is required. What the argument needs is that the caller
cannot learn **which** check it tripped, and that holds. Found by the
implementation, which declined to propagate it.)*

**`iat` is still a parse gate, though, and that is pre-existing rather than
fixed here.** `claims` declares it as an integer, so a counterparty sending it
as a float or a string is refused as malformed — the same hidden-reason 401,
arrived at a different way. This milestone does not widen that and does not
close it; it only declines to add a second such refusal. And it is what actually closes §1.1's fast-clock
extension: the measurement happens on the verifier's clock, so an issuer's
offset cannot buy lifetime.

### 5.2 The bound is one hour, and it cannot be five minutes

The obvious value is `credentialTTL`. It cannot be, and the reason is in this
repository's own harness.

`test/tck/run.sh` mints with `-ttl 30m`, for a recorded reason: it mints
**before** a cold image build, because the same string has to reach the
connector as `DSBOX_MGMT_TOKEN` before it starts. That reason is in §35's
trade-off block, after §35.5 — not in §35.4, which is about the harness's
participant identity. At five minutes the credential dies before the suite
begins, so a five-minute bound turns `make tck` red — measured: sixty-four of
the sixty-five required results fail. The survivor is the metadata suite's
version-document test, because that endpoint is mounted outside the credential
check, which is the same fact the roster milestone had to write down.

One hour, then. It holds the harness's thirty minutes with room, and it
refuses the year-long token §1.2 describes — which the first draft's quantity
did not.

**What that buys, stated plainly, because overstating it would repeat the
defect this milestone exists to fix.** §10's five minutes remains a convention
of what this connector mints. A participant can still mint a fifty-minute
token and be accepted, and against a participant that holds its own key the
bound buys little: it can re-mint a five-minute token whenever it likes.

What the bound does close is narrower and worth naming rather than dressing
up. A key compromised once and then discarded cannot mint a credential that
outlives the operator's response — the attacker gets an hour, not a decade.
And a counterparty emitting milliseconds where this protocol expects seconds
is refused with a reason instead of being trusted for forty years.

Two things already limit a long token, and neither is this: it dies when the
roster drops its issuer, and it dies when the roster itself expires, which the
previous milestone capped. The bound is the third and smallest of the three.

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
both read `exp`, which is a claim.

**Expiry first, then the bound**, which is the existing check keeping its
place. Under `exp - now` the order no longer changes any answer: an expired
token has a negative distance, passes the bound, and is refused as expired;
a far-future token is not expired and is refused by the bound. The first draft
argued for the reverse order, and that argument only existed because a token
missing `iat` would otherwise have been misreported as over-long — a failure
mode §5.1 removed.

**Which means nothing holds this order, and that is the honest reading.**
Swapping the two checks was applied as a mutation and survived every gate, as
this section predicts it would. The preference is for the smaller diff, not
for a behaviour, and §9 carries no row for it because a row that cannot fail
is the thing this repository has shipped too often already.

---

## 7. What becomes false

The accepted replay window moves from the token's lifetime to its lifetime
plus the leeway. Every sentence that states the number is affected, and one of
them says explicitly that the number did not change.

**That is the number for two clocks that agree, and this is a milestone about
clocks that do not.** Measured across an issuer's clock-ahead offset, the
window is the smaller of the offset plus the lifetime or the maximum, plus the
leeway. At half a minute of offset it is six and a half minutes; at ten
minutes of offset it is sixteen; and it stops climbing at the maximum plus the
leeway however far the offset goes. Past that the token is not merely capped —
it spends the beginning of its life refused outright, which is a second thing
an operator meeting an unexplained refusal should be able to look up. The
documents below take the agreeing-clocks number, because that is what an
operator budgets against, and this paragraph is what they point at for the
rest.

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
- `docs/goal-gap-analysis.md`'s P3 clock-skew bullet, which this closes. That
  bullet also cites a line number for the expiry check that no longer holds,
  so the citation moves with the sentence. Its ordered item 3 carries this
  work in an addendum, which is where this milestone is recorded.
- `internal/dsp/router.go`'s `credentialTTL` doc comment, "Short enough to
  bound replay" — the same class of sentence as the rest of this list, and the
  first draft's edit list missed it.
- `docs/milestone-sequence.md` — this milestone joins the shipped list.
- This milestone's decisions land in a new `DECISIONS.md` section.

Each edit names the code fact it was checked against.

---

## 8. Trade-offs accepted

**The replay window widens.** By the leeway, on every credential. §28 accepted
a window bounded by its length and this makes that window longer. The
alternative is refusing every slow-clock pair, which is the failure §1.1
describes.

**And for the first time it is bounded.** Before this, an issuer's clock-ahead
offset added to the window with nothing capping it, so §28's "the window
length bounds the exposure" was not true. After §5 the worst case is the
maximum plus the leeway regardless of any issuer's clock, because both are
measured against the verifier's own. That is the substantive half of what §7
amends in §28.

**The bound does not enforce §10.** §5.2 says why and what it would cost to
change.

**A constant rather than configuration, twice.** Neither the leeway nor the
bound is per-deployment, for the reason §4 gives: a policy nothing signs is
only as strong as the most generous deployment.

**Nothing here requires NTP and nothing checks for it.** A minute of leeway is
an assumption about how far apart two participants' clocks are, and no
document states it as a requirement. This milestone does not add one — it
makes the assumption survivable rather than removing it. `time.Now()` is the
wall clock, so a step correction larger than the leeway is a refusal this
design does not soften.

**The leeway's value comes from a test fixture and the bound's from a harness
script.** Neither traces to a measurement of clock spread or to an exposure
budget. §4 says so for the leeway; it is equally true of the bound, and
naming it is the alternative to pretending otherwise.

**The management listener is untouched.** It compares its token byte for byte
and never parses it, so neither the leeway nor the bound reaches it — its own
comment already records that a credential used there keeps being accepted
after the credential inside it has expired. §5.2's harness constraint is
therefore about the protocol listener alone.

---

## 9. Evidence

`go test -race` carries this, as it carried the roster half — no harness
exercises a clock difference, because every container shares one host clock.

| Mutation | Killed by |
|---|---|
| Remove the leeway from the `exp` comparison | a test that verifies a token expired by less than the leeway and expects it accepted |
| Raise the leeway past sixty seconds | `TestVerifyRefusals`'s existing "expired" case |
| Change `>=` to `>` in the expiry comparison | the same case — at sixty seconds of leeway it sits exactly on the boundary, so the operator matters |
| Remove the lifetime bound | a test that mints far past the maximum and expects a refusal |
| Compare against the wrong constant | the same test, with a token bounded just past the maximum |
| **Bound `exp - iat` instead of `exp - now`** | a test whose token carries an `iat` as far ahead as its `exp`, so the difference is small and the distance is not. This is the row that catches what the first draft shipped |
| Put the lifetime bound before the signature verifies | a test whose token is both over-long and badly signed, asserting which error comes back |

Each row must be applied and confirmed to kill the test named beside it. A row
whose test cannot be made to fail is a wrong row, and this repository has
shipped several — the roster milestone found one that survived every gate
because its fixture tripped a different check first.

**The harness must stay green**, and §5.2 is the reason it is worth checking
rather than assuming: `-ttl 30m` sits inside the bound, and a bound chosen
without looking would have broken it. Confirm by running `make tck`, and
confirm the claim underneath it by setting the bound to five minutes and
watching it go red — §5.2 rests on that and nothing had measured it.

---

## 10. What this does not decide

Whether `credentialTTL` should be what the verifier enforces. §5.2 records
what stands in the way — the harness's legitimate need to mint before a cold
build — and closing it is a decision about the harness, not about `Verify`.

---

## 11. What the cross-check measured

**The lifetime half measured the wrong quantity, and the spec contradicted
itself in three places because of it.**

The first draft bounded `exp - iat`. Both are integers the issuer chooses and
signs, so the sequence is: a roster participant signs `iat = now + 10y`,
`exp = iat + 59m`, addressed to its victim. The signature verifies. The
difference is fifty-nine minutes, inside any maximum. The token is not
expired. The audience matches. It is accepted for ten years — which is exactly
the token §1.2 opens by naming.

Three of the draft's own claims fell with it: that the bound closed the
fast-clock extension (a uniform offset leaves `exp - iat` unchanged), that a
fast clock extends a lifetime only up to a ceiling (there was none), and
§1.1's "Nothing refuses it", which survived the milestone written to end it.
§5.1 is the correction and §9 carries the mutation row that would have caught
it.

**The `iat` requirement the draft accepted was hostile at the boundary.** RFC
7519 makes `iat` optional. A conformant counterparty omitting it would have
decoded to zero, been refused for an enormous lifetime, and received a
bodiless 401 whose reason this connector deliberately hides — the same
unexplained refusal across an organizational boundary that
`docs/goal-gap-analysis.md` files as the reason this milestone exists.
Measuring against `now` removes the requirement entirely.

*(2026-08-26: "bodiless" is wrong here for the reason §5.1's bracket gives —
`refuse` calls `writeError`, which emits a JSON error document. What this
paragraph rests on is the half that holds: the caller is not told which check
its credential tripped.)*

**Smaller corrections.** The `-ttl 30m` reason is in §35's trade-off block,
not §35.4. The roster parallel in §1.2 was a compression presented as a quote.
There is no "ordered item 3a" in the gap analysis. And the draft's own edit
list missed `credentialTTL`'s doc comment, which is the same class of sentence
the list exists for.

**The draft was implemented, and it passed everything.** A cross-check built
the first design on a throwaway copy and ran the whole gate set: `go build`,
`go vet`, `go test -race -count=2`, `make tck` sixty-five of sixty-five, and
`make demo` both rounds — all green, with a check in it that stopped nothing.
Three of its six prescribed mutations were killed correctly, which is what a
green board looks like when the quantity being measured is the wrong one. Then
the same implementation minted a token dated a year ahead and `Verify`
returned no error.

That is the strongest argument in this document for why a spec gets
implemented before it gets planned, and it is why §9's table now carries a row
for exactly that substitution.

**What survived unchanged.** The leeway half reproduced exactly: sixty seconds
is the largest value `TestVerifyRefusals` tolerates without moving its fixture,
the boundary is exact, and it depends on the comparison staying `>=` — which
§9 now has a row for.

**And one row of §9's own table could not be applied.** The first draft's
"accept a token with no `iat`" describes, under the revised design, the
correct behaviour rather than a mutation. It is gone. A table written against
a design that changed underneath it is the failure §9's closing paragraph
names, and it happened here between one draft and the next.
