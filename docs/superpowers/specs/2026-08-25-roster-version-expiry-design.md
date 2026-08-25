# The roster as a versioned, expiring artifact

**Status:** design, agreed 2026-08-25. Revised twice before it was written
down, then rewritten after a third round of cross-checks — one of which
implemented this spec on a throwaway copy, ran the suite, and applied every
mutation §11 prescribes. What all of that measured is in §12.

**Ordered item 3** of `docs/goal-gap-analysis.md`.

**What it acts on:** that document's P3 bullet, which says revocation "is not
slow; it is undetectable". That bullet lists what the roster lacks — a
version, an issue time, an expiry, and a dataspace identifier; this adds the
version and the expiry, which are the ones revocation turns on, and leaves the
other two for whoever needs them. §1.3 is precise about which half of
"undetectable" this closes and which half it only bounds.

**What it does not amend:** `DECISIONS.md` §25.1. An earlier draft proposed an
exception for a `503`; §5.1 records why that was withdrawn.

---

## 1. The finding this milestone acts on

### 1.1 A superseded roster verifies forever

`rosterDocument` is `{participants, signature}`
(`internal/auth/roster.go:45-52`) and the signature covers
`canonicalRosterBytes(doc.Participants)` alone (`roster.go:84`). Nothing in
the document says which revision it is or when it stops being true, so a
roster that still lists a removed participant verifies as well as the one
that replaced it, and rolling back to it cannot be detected.

### 1.2 The connector never looks again

`auth.LoadRoster` is called once, at `cmd/dsbox/main.go:84`. `Roster`'s doc
comment states the intent: "Loaded once at startup and never mutated, so
nothing needs a lock and there is no reload path to get wrong."
`DECISIONS.md` §9 accepted that as the cost of a static registry and recorded
the consequence as "revocation is only as fast as propagation". The code is
weaker than that sentence: propagation finishing changes nothing for a
connector that has not restarted.

### 1.3 What each half of this milestone actually buys

This distinction is load-bearing and the first two drafts of this design
blurred it.

**The expiry is what closes §1.1.** After it, a superseded roster stops
verifying wherever it is held, including on a connector that never restarted.
"Forever" becomes "until `expires_at`".

**The version is a local anti-rollback memory and nothing more.** It stops
*this* connector from being handed an older roster than one it has already
run. It is not exchanged with anyone, so during a rollout one connector can
be ahead of another and neither can tell. Issuing a new version does not
reach a running connector; only the expiry does.

Stated the other way: revocation gains an upper bound, and that bound is the
expiry. Everything else here serves it.

### 1.4 The decision, in one paragraph

The roster gains a revision number and an expiry, both inside the operator's
signature. The expiry is what the connector enforces while it runs — at load,
on every DSP request, on the initiate hooks, and on everything it sends — so a
superseded roster stops being usable at a known instant even on a connector
that never restarts. The revision is a local ratchet that stops this connector
being handed an older roster than one it has already run. Neither reaches
another participant, and §10.9 is honest about the adversary this closes
against.

---

## 2. Scope

### 2.1 In

- `version` and `expires_at` inside the signed payload (§3).
- Expiry enforced at load, on inbound DSP requests, on the management
  listener's initiate hooks, and on everything this connector sends (§4).
- A refusal that says what is actually wrong, a `/health` that reflects it,
  one warning rather than one per request, and the roster's identity in the
  boot log (§5).
- A monotonic version remembered in the store, wired so that deleting the
  wiring fails a test (§6).
- `dsops roster sign` refuses to sign a roster the connector would not load
  (§8).

### 2.2 Out: clock leeway, `nbf`, and a maximum token lifetime

Their content is already decided and belongs in its own milestone: add leeway
to the `exp` comparison, do not add `nbf`, and bound `exp - iat`.

Not adding `nbf` is the part worth recording now, because it is
counter-intuitive. Without it, a token from an issuer whose clock runs ahead
is accepted — the only refusal today is one-directional. Adding `nbf` would
newly refuse pairs that transact fine, so it tightens rather than loosens, and
what it was reaching for is the lifetime bound. `Verify` does not bound `exp`
at all: `credentialTTL` lives at the minting site
(`internal/dsp/router.go:190`), and the harness already mints past it with
`-ttl 30m` and is accepted. §10's five minutes is therefore a convention of
the minter rather than something the verifier enforces.

The split is because the halves share no code and no decision, and because
their evidence is opposite: both harnesses exercise the roster half simply by
coming up, and neither can exercise a clock difference, since every container
shares one host clock.

**What deferring it costs this milestone** is stated in §10.5 rather than
hidden: without leeway, the fleet does not stop at one instant but across its
clock spread, and a connector whose clock is ahead stops early.

### 2.3 Out: binding `connectorAddress` to a roster entry

These documents assign this to the roster milestone —
`docs/goal-gap-analysis.md:255-256`, `DECISIONS.md:2849-2851`, and
`docs/superpowers/specs/2026-08-24-initiate-hook-authorization-design.md:690-693`.
It moves to ordered item 4, discovery, and all of them are corrected (§9).

The argument is scope, and it is worth stating accurately because a first
draft of this spec got it wrong. It is *not* that the field would be
unverifiable here: `handleInitiate` already takes `providerId` and
`connectorAddress` in the same body and validates both
(`internal/dsp/negotiation_consumer_handler.go`), so a roster address could be
compared at exactly that point with no discovery client.

The reason is that this milestone changes the document's *lifecycle* — a
revision and a lifetime, both about the roster as a whole — while an address
changes what an *entry* means and carries its own operational cost: every
roster would have to carry every participant's address, and an address change
would then need a re-signed roster and a fleet restart. That is a second
decision with a second set of trade-offs, and item 4 is where something
actually consumes an address.

---

## 3. What the signature covers

### 3.1 The document

```
{ "participants": [ ... ],
  "version": 3,
  "expires_at": "2027-01-01T00:00:00Z",
  "signature": "..." }
```

The signed bytes are `json.Marshal` of a struct carrying participants,
version, and expiry, in that field order — the same mechanism as today, so
`DECISIONS.md` §27.2's re-marshal argument is unchanged: the signer and
`LoadRoster` both call the same function on their own parsed value, so
reformatting the file cannot change what is checked. The encoding is named
here because "serializes them together" admits several wire-incompatible
readings.

`expires_at` is RFC 3339 with a `Z` offset, parsed with `time.RFC3339`.

### 3.2 Why `expires_at` is a string and not a `time.Time`

`canonicalRosterBytes` discards `json.Marshal`'s error, justified in its doc
comment by "Every field of `rosterEntry` is a plain string, so `json.Marshal`
cannot fail here". A `time.Time` field makes that sentence false and the
discarded error reachable, since `time.Time.MarshalJSON` fails for a year
outside its representable range.

This is the weaker half of a real trade-off and it is recorded as one: the
alternative is to handle the error, which is a small edit to a comment §9
already opens. The string wins because a signed artifact's wire type is the
last thing that should change for a convenience, and because a string is
what the operator edits by hand either way.

### 3.3 `version` is an `int`, and must be at least one

An absent `version` decodes to zero, so requiring at least one turns Go's own
zero value into the rejection and needs no pointer. Version zero is
unreachable after this, which costs nothing because no roster carries a
version today.

### 3.4 What the required-field check is really for

Both fields are required, and the honest reason is narrow. It is *not* that
an attacker could strip a field to evade the version check: canonicalization
re-marshals the parsed document, so a stripped `version` is inside the signed
bytes as zero and the signature already fails. It is *not* the operator's
main safety net either, because §8 makes `dsops roster sign` refuse to
produce such a file.

What it buys is the upgrade message. Every roster that exists today lacks
both fields, and §3.5 puts this check where the operator gets told that in
those words rather than through a signature failure.

**Every existing roster must be re-signed**, and §9 records that as the
upgrade step it is.

### 3.5 The check runs before the signature verifies

`LoadRoster` already draws this line, and the new fields fall on the near
side of it. Document-level structure is checked *before* `ed25519.Verify`
— that participants is non-empty (`roster.go:74`) and that a signature is
present at all (`:77`), both ahead of the verify at `:84`. Per-participant
content is checked *after* (`:89` onward). `version` and `expires_at` are
document-level, so they go where `participants` and `signature` already are.

A second draft of this spec put them after, on the argument that a forged
roster would otherwise report a missing field instead of a bad signature and
send an operator to edit an attacker's file. That argument is real but it
proves too much: it applies identically to the two checks already sitting
there, and this repository accepted the trade for them. Putting the new ones
after also made §3.4's purpose empty — every pre-milestone roster fails the
signature first, so the check would never fire for the case it exists to
serve.

Rejecting on unauthenticated input is fail-closed, which is a different thing
from *acting* on unauthenticated claims — the distinction
`internal/auth/token.go`'s `Verify` doc comment draws, and it belongs in the
comment here too.

---

## 4. Expiry

### 4.1 At load

An expired roster is a startup failure, the same grade as a signature that
does not verify. `LoadRoster`'s doc comment already argues the general case:
a connector that starts with an unusable roster can verify nobody, and
"started fine, refuses everyone" is a much harder symptom to trace than a
refusal to start.

### 4.2 On every inbound DSP request

`requireParticipant` refuses. This is the only place it is wrapped
(`internal/dsp/router.go:169`).

**The expiry check runs before the credential is read**, not after. The answer
does not depend on the credential, and verifying one against a roster this
connector has declared unusable is work that cannot mean anything. It also
keeps the refusal honest: the connector says its own roster expired rather
than making any claim about the caller.

### 4.3 On the management listener's initiate hooks

`requireParticipant` never runs there. §35 moved
`POST /negotiations/initiate` and `POST /transfers/initiate` to a listener
guarded by a shared secret instead, and the second draft of this design
checked only §4.2 — which an implementation measured: with a roster an hour
expired, an initiate call returned 200, authorized its `providerId` against
that expired roster through `knownParticipant`, and dispatched an outbound
message signed with this connector's real key. A connector that has stopped
answering must not still be starting exchanges and signing.

**The expiry check runs first in these handlers**, ahead of the required
fields and the address guard, because it is about this connector rather than
about the request: no correction to the body would make the call succeed.
`TestHandleInitiateRefusesAnUnsendableAddressBeforeTheRosterCheck` pins the
relative order of the address guard and the roster check, and it survives —
but for a reason worth naming, because the obvious one is wrong. It builds its
handler directly, so the usability predicate is nil and the check is *absent*
rather than passing.

This does invert the comment at `internal/dsp/negotiation_consumer_handler.go`
saying the roster check "runs last", and that comment and the test's own are
in §9's list.

### 4.4 On everything this connector sends

`mintOutboundCredential` is where every outbound DSP message and the data
pull get their credential (`negotiation_client.go:44`,
`transfer_client.go:44`, `callback.go:95`, and
`transfer_consumer_handler.go:550`). Its contract changes:

```
func(aud string) (authorization string, maySend bool)
```

- Roster expired: `("", false)`. The caller does not send.
- No audience: `("", true)`. Unchanged — it proceeds without a credential and
  logs, exactly as today.
- `auth.Mint` returns an error: `("", true)`. Also unchanged, and deliberately
  so: making that case abort is a defensible change and a separate one, and
  bundling it here would hide it.
- Authentication off, so the no-op default: `("", true)`.

The polarity matters. The package-level default must return `true`, or a
connector with authentication off sends nothing.

**A retry loop that is mid-schedule stops.** `pushCallback` mints per attempt
— `callback.go:90-95` says why, in a comment about the credential's five
minute life — so an expiry landing between attempts is observed on the next
one, and the loop returns rather than burning its remaining backoff.

**The caller has already written its state.** §23.12 stores unconditionally
once a push is dispatched, and `DECISIONS.md:870` records that as a
deliberate asymmetry. So a state machine can advance on a message that was
never sent. This milestone does not change that, and §10.4 records it as a
residual rather than leaving it to be discovered.

### 4.5 The predicate's shape, and the nil convention

The handlers and the middleware take a predicate that reports whether the
roster is still usable — separate from `knownParticipant`, which reports
whether the roster lists a participant. Folding them would leave a test
unable to say which of the two refused, and §35 introduced `knownParticipant`
precisely so that a nil predicate reads as "this check is absent" rather than
"this check said no".

The nil convention applies to the initiate hooks, which are reached in both
branches of `NewRouter`. It does **not** apply to `requireParticipant`: the
authentication-off branch does not install that middleware at all
(`router.go:132-140`), so an earlier draft's reasoning about it was wrong. Nor
does it apply to `mintOutboundCredential`, which is never nil — it is a
package-level default that `NewRouter` overwrites only past that same early
return, so with authentication off the default stands and §4.4's polarity is
what makes it permit.

**Absence is expressed by a nil predicate and never by a zero timestamp.** A
zero `auth.Roster` must not read as expired, or `require_auth: false` breaks
on a deployment that has no roster — but the fix is that the predicate is not
built at all, not that the zero time is treated as "never expires". Those two
implementations differ where it matters: the second makes
`"expires_at": "0001-01-01T00:00:00Z"` a permanently valid roster, and §8
would sign it. `LoadRoster` takes the current time as a parameter rather than
reading the clock, matching `Verify`'s existing shape, so this is testable
without waiting.

The comparison is `now.Before(expiresAt)`: usable at every instant before
`expires_at`, unusable at it and after. That matches how this connector
already reads a deadline — `data_handler.go`'s dataset window and
`token.go`'s `exp` both treat the named instant as already past.

### 4.6 Parsed once

The `Roster` holds the parsed instant. Parsing per request is not rejected
for cost — this path already does an `ed25519.Verify` — but because a
malformed expiry would then produce a silent per-request refusal on a
connector whose boot log said nothing. Parsed at load, a malformed value is a
startup failure like every other unusable roster.

### 4.7 What does not stop, stated precisely

"Stops serving" is not "answers nothing", and claiming otherwise would be the
kind of sentence this repository has had to correct before.

- **The version endpoint** keeps answering. It is mounted outside the wrap
  (`router.go:167-169`) and discloses only a protocol version.
- **`/health`** answers, and reports unhealthy (§5.2).
- **The management API's agreement and transfer routes** keep working. They
  are the operator's, not a counterparty's, and an operator inspecting a
  connector that has stopped is exactly who needs them.
- **A data copy already in flight** continues. `copyUnderRollingDeadline`
  bounds a transfer by time-without-progress rather than elapsed time, so a
  counterparty that keeps reading holds bytes flowing past `expires_at`.
  Cutting it needs the pull context cancelled on a timer, and this design
  deliberately has no timer. §10.4 records it.
- **A DNS lookup for a counterparty-chosen host** still happens on the
  consumer transfer path: `validateOutgoingCallback` resolves before the
  minter is consulted. No message leaves; a name is resolved.

---

## 5. What an expired connector says

### 5.1 `409` on the DSP listener, and why not `503`

**Not `401`.** The caller's credential may be perfect and the fault is
entirely local; answering "a valid participant credential is required" sends
their operator hunting their own credential across an organizational
boundary, where they cannot read this connector's log.

**Not `503` either, and this reversed a decision.** An earlier draft chose
`503` and proposed amending §25.1's `[400, 500)` rule, arguing that the rule's
recorded reason is entirely about `404` — which is true — and that `503`
carries no such risk — which is false. This repository's own bytecode-confirmed
wire contract says the opposite:
`docs/superpowers/specs/2026-08-16-transfer-process-tck-wire-contract.md:165`
records that on the negative paths "400 and 409 both pass. 2xx, 3xx, 5xx and
404 all raise `AssertionError`"; `:257` fixes the retry at "only for
4xx-non-404, only when `expectError=false`"; and `:150` says the state poll
"aborts on the first poll" for any non-2xx and "ignores nothing".
A `503` from a DSP endpoint is therefore exactly as fatal as the `404` §25.1
exists to forbid. §25.1 stands unamended and this refusal stays inside it.

**`409`, on every endpoint that refuses for this reason** — the DSP routes
behind `requireParticipant` and the initiate hooks on the management listener
alike. The hooks are `internal/dsp` handlers writing DSP error documents and
the harness drives them with the same client, so splitting the code by
listener would put two answers on one event for no reader's benefit. `/health`
is the exception and §5.2 says why.

It has a precedent here: the data endpoint answers `409` for a transfer that
is not `STARTED` and for a closed dataset window
(`internal/dsp/data_handler.go:159,:182`).

**What "passes" does and does not mean.** On the twelve negative paths the
wire contract names, a `409` is absorbed. On a positive path it is retried
three times and then raises, like any 4xx. So a permanently expired roster
fails a TCK run whatever code it returns — the point of `409` is that it is
inside §25.1 and behaves like the connector's other refusals, not that it is
free.

**It collides with the data endpoint, and that is a real cost.**
`requireParticipant` wraps the whole mux (`router.go:169`), which includes
`GET /2025-1/data/{id}` (`router.go:116`), so the new refusal lands on the one
route that already answers `409` for other reasons. The bodies are disjoint
and the middleware's refusal preempts the handler's, but a caller reading only
the status cannot tell a closed dataset window from an expired provider
roster — and this connector's own consumer is such a caller: it collapses
every status at or above 300 into "the data endpoint refused the pull",
logging the status alone (`transfer_consumer_handler.go:647-651`). That line
gains the response body, because after this milestone the status is no longer
enough to tell an operator what happened.

The body says the roster expired. §10.8 records what that discloses.

**A correction this milestone also makes.** `DECISIONS.md` §35 states that
"any other non-2xx is retried with backoff first". That is over-broad and the
wire contract contradicts it; the sentence is corrected as part of §9.

### 5.2 `/health`

Today it answers `{"status":"ok"}` unconditionally
(`internal/mgmt/router.go:32-35`), so a connector that can serve no
counterparty stays in rotation. It reports the expiry instead.

`mgmt.NewRouter` has no roster and must not acquire one — §35 settled that
this package holds no opinion about `internal/dsp` and takes plain values from
`cmd/dsbox`. It takes the same predicate, as a plain function, by the same
route the initiate handlers already travel.

**`503` here, and §25.1 does not reach it.** That rule governs what a DSP
endpoint emits; `/health` is on the management listener and answers a probe,
not a counterparty. `503` is the right code for a probe and carries none of
§5.1's problem.

Three consequences to record rather than discover:

- The comment on that route says it is unauthenticated because "it carries no
  information". That stops being true, and §9 lists it.
- **Both harnesses poll `/health` for readiness** (`test/tck/run.sh:107`,
  `demo/run.sh:85`) with `curl -sf`, which exits non-zero on a `503`, so the
  `until` loop never breaks. A roster already expired at boot kills the
  process, so that case reaches the same "did not become ready" message
  through a refused connection instead — measured, and it is the case the
  harnesses actually hit. The `503` path is reached only by a roster that
  expires mid-run, and it costs the loop's full cap before failing. Either
  way the real reason is only in the dumped container log, so the harness
  comment says so.
- The predicate reaches this package from `cmd/dsbox`, which is also where
  `internal/dsp` hands it over. Both listeners must answer from the same
  value; building a second one here would let the two disagree.

### 5.3 One warning, not one per request

`refuse` logs `slog.Warn` per call, and after expiry that is every inbound
request forever. `cmd/dsbox/main.go:88-92` states the principle and its
reason for the authentication-off warning: "One line at startup, not one per
request... a per-request warning would bury the rest of the log under it."

The expiry refusal follows it: the connector says so once, the first time it
refuses. The guard is a `*sync.Once` and not a `sync.Once` value, because a
test that resets it copies a lock and `go vet` reports it — and `go vet` is a
gate.

### 5.4 The boot log carries the roster's identity

Version and expiry, on their own line at load time — **not** on the existing
`connector started` line (`cmd/dsbox/main.go:158-164`). That line runs after
both listeners are serving, which is past the version check that can refuse to
start, so a connector refused for a rollback would print nothing about the
roster that caused it.

Nothing today reports which roster a running connector holds, so an operator
whose repository says version four has no way to learn that a connector is
still on three.

### 5.5 A warning before expiry, and its limit

If the roster expires within thirty days, the boot log says so. One
comparison, sharing the `time.Now()` that already decided the load check.

**The limit is the point and §10.1 leans on it.** A connector that has been up
for a month gets no warning at all. This is not because a running warning
would need a timer — §4.2 already evaluates the expiry per request, so it
would not — but because a warning is only useful if someone reads it, and the
boot log is the one place this project already expects an operator to look.
A per-request warning is the log firehose §5.3 exists to prevent. An operator
who restarts rarely learns nothing from this, and that is the weakest part of
§10.1's argument against a grace period.

---

## 6. Monotonicity, and making the wiring undeletable

### 6.1 Where the state lives

`auth.LoadRoster` stays stateless and the returned `Roster` carries its
version. The store keeps a table `roster_version` constrained to one row —
`id INTEGER PRIMARY KEY CHECK (id = 1)` beside `highest INTEGER NOT NULL` —
because "single-row by convention" makes `SELECT highest` arbitrary the first
time a second row appears. Verified against this project's own driver: `id=2`
fails the check, a duplicate `id=1` fails the unique constraint, and — the
edge worth knowing — **omitting `id` also fails**, because it is the rowid
alias and an omitted value takes the next rowid. Writes name `id` explicitly
and upsert. A lower version is refused at startup. An equal or
higher one is accepted, and only a higher one writes: equal is a no-op, so the
ordinary restart path does not touch the database.

Equal must be accepted, or an ordinary restart with an unchanged roster fails
to boot.

**The check runs after `store.Open` and before `dsp.NewRouter`**, so a
rollback is refused before the outbound minter global is armed and before the
pull context exists.

**It runs only when there is a roster**, which needs a second conditional
rather than the roster load's own. That block closes at `cmd/dsbox/main.go:93`
and `store.Open` is at `:98`, so the version check cannot live inside it —
there is no store there to ask. It is a new `if cfg.AuthRequired()` after the
store opens, and §10.7 records that the whole milestone is inert under that
flag.

### 6.2 Why `internal/auth` does not reach the store

Not a layering inversion. §35 says of its own `mgmt`/`dsp` rule that it was
"a layering choice rather than cycle avoidance", and `auth` and `store` are
both leaves that import nothing else in this repository. The basis is
`internal/auth/token.go:80-82`, which takes a function rather than a roster
type "so this package never imports one and can be tested without a file".

### 6.3 The call site is what needs guarding

An implementation of an earlier draft measured this: deleting the call that
records the version left `go build`, `go vet`, `go test -race`, `make tck`,
and `make demo` all green. Go does not error on an unreferenced function and
`go vet` does not report one, so moving the logic into a testable function
tests the logic and leaves the call deletable. `drainPulls` — the precedent
that draft cited — has the same hole open today, and its own doc comment
concedes that dropping it "would compile, pass every test".

So the guard is a test that reads `cmd/dsbox/main.go` and asserts the call is
there. That technique is already this repository's answer to this problem:
`internal/dsp/auth_middleware_test.go` and
`internal/mgmt/route_coverage_test.go` both parse `router.go` rather than
keeping a list.

**What it does not catch**, said plainly: a call that is present but passes
the wrong argument, or whose error is swallowed. It kills deletion. The
behaviour itself is covered by the store's own tests, which is a different
guarantee from the two being connected.

### 6.4 The schema comments

This is the first schema change here that is not a column addition.
`addColumnIfMissing`'s doc comment says "Every migration in this file is a
column addition, which is the only schema change SQLite performs cheaply and
the only one this connector has needed" (`internal/store/store.go:370-373`) —
that is the function to edit, not `migrate`, whose own doc
(`store.go:308-311`) separately says there is "no migration framework and no
version table". Both change: the new table records the highest roster version
this connector has loaded and says nothing about the schema's own revision,
and `CREATE TABLE IF NOT EXISTS` needs no check-and-add helper because it is
already idempotent.

---

## 7. The harness

### 7.1 The heredocs that write a roster

Each script writes the roster twice, because `dsops roster sign` prints a
signature rather than writing the file: `demo/run.sh:38-45` and `:49-57`,
`test/tck/run.sh:58-65` and `:67-75`. The unsigned copy is signed and the
signed copy is what the connector loads, so both must carry the same
`version` and the same `expires_at`.

JSON whitespace need not match, because canonicalization re-marshals. **The
timestamp string must match exactly** — it is a value, not formatting — which
is why the expiry is computed once into a variable before the first heredoc
rather than by calling `date` inside each, where the two would split across a
second boundary.

### 7.2 The interval, and why

Twenty-four hours from the moment the script runs. Long enough that a cold
image build plus the suite plus any local re-run sits inside it — the same
reasoning that put the harness credential at `-ttl 30m` — and short enough
that it is a real timestamp computed per run rather than a far-future
constant that would leave the field decorative.

`date` is not portable for this: GNU takes `-d`, macOS takes `-v`, and POSIX
has no relative-date form at all. A two-way fallback, written once per script.
Measured: busybox has no relative-date form either and the script aborts
under `set -eu` — a loud failure rather than a wrong timestamp, and neither CI
nor a development machine here is busybox, so it is recorded rather than
solved.

**This interval permanently trips §5.5's warning**: a day is inside thirty, so
every harness run logs that the roster expires soon. That is correct and it is
noise, and whoever reads `tck-connector.txt` should know it is expected.

### 7.3 What the harnesses cannot verify

An implementation of this design was mutation-tested, and `make tck` and
`make demo` stayed green under every mutation in §11 — all of them, including
the two checkpoints §4.3 and §4.4 add. **`go test` is the only gate that
carries this milestone**, and `docs/milestone-sequence.md`'s "what can verify
each remaining milestone" section should say so rather than let it be
discovered.

The version-regression refusal in particular is unreachable from either
harness, for two different reasons: the TCK connector mounts no volume for
`data_dir`, so its database dies with the container, and `demo/run.sh:22`
removes the generated directory at the start of every run even though the
demo consumer does bind-mount `data_dir` (`demo/compose.yaml:49`).

---

## 8. `dsops roster sign` validates

Today `SignRoster` checks only that participants is non-empty
(`internal/auth/roster.go:127-129`), so after this change it would print a
signature and exit zero for a roster missing the new fields. The operator
sees success and the connector refuses to boot days later, which is the worst
available ordering.

It applies the same required-field rules as `LoadRoster`, **including parsing
`expires_at`** so a malformed timestamp is caught at signing rather than at a
boot during a recovery. It also **refuses an expiry already in the past**:
"next week" and "last week" are distinguishable, and printing a signature for
a roster that cannot be loaded is the same worst-available-ordering this
section exists to eliminate. What it does not do is judge how far in the
future the expiry is — that is the operator's policy (§10.1).

**`SignRoster` takes the current time as a parameter**, the same shape §4.5
gives `LoadRoster`. Without one the past-expiry refusal cannot be tested, and
— sharper — no expired-roster fixture could be signed at all, which every
test in §11's first three rows needs.

The CLI surface does not change and it still writes nothing, so §27.3's
print-don't-write principle is intact — validating is not managing. The
behaviour change is recorded as one.

---

## 9. What becomes false

Each edit names the code fact it was checked against. The implementation plan
carries the full list; these are the ones that change meaning rather than
wording.

- `internal/auth/roster.go` — `rosterDocument`'s shape; the `Signature`
  field's description of what it covers; `canonicalRosterBytes`'s
  cannot-fail argument; `LoadRoster`'s enumeration of what makes a roster
  unusable; `Roster`'s "a key here is trusted, and anything else is not",
  which becomes true only until the expiry; `SignRoster`'s silence about
  validation.
- `internal/auth/token.go:33` and `internal/dsp/auth_middleware.go:45` — both
  say "the six ways" a credential can be wrong. Correct today and forbidden
  by this project's own rule against counts; this milestone adds a refusal
  that is not about the credential at all, which is the occasion to fix them.
- `internal/store/store.go` — `addColumnIfMissing`'s "every migration is a
  column addition" and `migrate`'s "no version table".
- `internal/mgmt/router.go` — `/health`'s "it carries no information".
- `cmd/dsbox/main.go` — the boot log line, and the roster-load block, which
  gains a roster failure that cannot live in it, because the store is not open
  yet.
- `DECISIONS.md` §9's trade-off, which gains an upper bound and the cost that
  bound carries. **§25.1 is not amended** — an earlier draft said it would be,
  and §5.1 records the reversal. §27.2 is, though: "the bytes signed are
  `json.Marshal` of the parsed `[]rosterEntry`" stops being literally true
  under §3.1.
- This milestone's decisions land in a new `DECISIONS.md` §36. §35 is
  currently the last section and every section from §21 is a per-milestone
  append.
- `config.example.yaml:75-76`, which shows the roster JSON inline and is the
  only onboarding document that does, and `:82-86`, which says an unusable
  roster fails "at boot, not by refusing every request later" — refusing
  every request later is now deliberate.
- The `connectorAddress` assignments listed in §2.3.
- `docs/goal-gap-analysis.md`'s P3 revocation bullet, which proposed this fix,
  and its ordered item 3, which still bundles the clock work §2.2 splits out.
- `README.md`'s description of the roster and of what the DSP listener
  requires.
- `test/tck/compose.yaml:24-25`, which says the connector reads the roster
  once at startup — still true and no longer sufficient. (`test/tck/dsbox.yaml`
  carries no such sentence; an earlier draft listed it and was wrong.)
- `DECISIONS.md` §35's "any other non-2xx is retried with backoff first",
  which the wire contract at
  `docs/superpowers/specs/2026-08-16-transfer-process-tck-wire-contract.md:165`
  and `:257` contradicts. §5.1 depends on getting this right, and the sentence is the
  reason an earlier draft got it wrong.
- `internal/dsp/negotiation_consumer_handler.go` and
  `internal/dsp/transfer_consumer_handler.go`, whose comments say the roster
  check "runs last" — §4.3 puts the expiry check ahead of it — and the
  ordering test's own comment alongside them.
- `internal/dsp/transfer_consumer_handler.go`'s pull-failure log, which
  records only the status, per §5.1.

Additions rather than corrections, listed here because §10.1 and §10.7 promise
them and nothing else would carry them: `config.example.yaml` gains the
recommended interval and what it trades (§10.1); `SECURITY.md` gains the
inert-under-`require_auth` sentence (§10.7) and the disclosure (§10.8);
`test/tck/run.sh` and `demo/run.sh` gain readiness-loop comments (§5.2);
`docs/follow-ups.md`'s package-variable entry gains why this milestone cannot
follow it (§11.2).

**The upgrade step, which is not a documentation edit but belongs in the same
list:** every existing roster must be re-signed with the two new fields. There
is no compatibility path and none is wanted — §3.3 and §3.4 explain why — but
the operator has to be told, so `config.example.yaml` says it and the load
error names it.

---

## 10. Trade-offs accepted

### 10.1 The fleet stops, and only the signer can restart it

Every connector shares one `expires_at`. Recovery needs a signature from
`roster_signer`, a key §27.1 deliberately places outside every participant,
plus redistribution and a restart.

**The interval is the operator's, and this milestone does not pick it for
them — but it names one.** `config.example.yaml` recommends ninety days and
says what the choice trades: the interval *is* the upper bound §1.3 claims, so
a shorter one is a stronger revocation guarantee and a more frequent
fleet-wide restart. Nothing enforces a maximum, because a maximum is a second
policy the signature does not carry.

**The recovery sequence, written down because it has to be possible:** the
operator edits the roster's `version` (if a participant changed) and
`expires_at`, runs `dsops roster sign`, pastes the signature, distributes the
file, and restarts each connector. Every step exists today except the two new
fields.

**A revocation must bump `version`.** An expiry-only re-issue need not: equal
is accepted, and the older document's earlier expiry limits it on its own.

**The high-water mark only goes up, so a typo costs a number, not a
connector.** An operator who signs `40` where they meant `4` has to keep
going from `41`; there is no reset, and none is added, because a reset is
exactly the capability the mark exists to deny. The load error names both
versions so the operator can see what happened.

**There is no grace period.** A grace period is a second expiry the signature
does not cover, so its length would vary per deployment and an attacker
holding a superseded roster would pick the most generous one. The mitigation
is warning, and §5.5 is honest that the warning only reaches a connector that
restarts.

### 10.2 A restart after expiry is a crash loop

§4.1 makes an expired roster a startup failure, so under a restart policy a
connector that goes down after `expires_at` restarts forever, and stops
answering `/health` while it does. That is the same fact as §4.1 seen from
operations, and it is the strongest argument the other way.

### 10.3 A fresh store is fail-open on version

With nothing recorded, any version is accepted, and the code cannot tell
"first start" from "database wiped". Expiry is what bounds that window, which
is why the two mechanisms ship together rather than separately.

### 10.4 What expiry does not reach

A data copy already in flight, and a state machine that has already recorded
a message that will not be sent (§4.4, §4.7). Both are pre-existing shapes
this milestone declines to change; both are named here so that "everything
stops" is not read as more than §4.7 says.

A third: when the minter refuses a data pull, the consumer records the failure
over whatever the row held, and `internal/mgmt/router.go` already warns that
this "writes an empty path and a reason over what a successful earlier attempt
recorded". So an expiry can overwrite the record of a transfer that had
already succeeded. The file on disk is untouched; the row's account of it is
not.

### 10.5 The clock arrives in the milestone that defers the clock

Startup now depends on the wall clock: a boot before NTP converges can refuse
an unexpired roster, and `run()` has no retry, which under a restart policy
is §10.2 again. And without the leeway §2.2 defers, the fleet stops across
its clock spread rather than at one instant.

### 10.6 Monotonicity is one connector's memory

No version is exchanged between participants. A signer rotation that restarts
numbering is refused forever, which compounds the absent key-rotation path
§27 already admits.

### 10.7 Inert under `require_auth: false`

No roster, no version recorded, no expiry enforced. That matches the router's
existing rule and belongs in `SECURITY.md`'s paragraph on that flag.

### 10.8 A new unauthenticated disclosure

Before this, a connector with an unusable roster refused to start and a prober
got a connection refused. After it, a live connector says the roster expired
on three surfaces: `409` on the DSP routes, `409` from the initiate hooks
(behind the management token, so to the operator only), and `503` on
`/health`, which is open to anyone who can reach that listener. Since §10.1 makes `expires_at` fleet-wide, that is a fact about the
dataspace's governance and not only about this connector — which is more than
§5.1's "its own configuration" framing admits.

It is accepted because the alternative is a refusal that misdescribes itself,
and because an expired roster is not a secret an attacker can act on: it names
no participant and it opens nothing. `SECURITY.md` gets the sentence.

### 10.9 What the adversary model actually is

`version` defends against a superseded roster being placed where this
connector will read it. §27.1 puts `roster_signer`'s public half in
`config.yaml` under the same "this file's integrity is already assumed"
reasoning, so an attacker who can write that file, or delete `dsbox.db`,
defeats both halves of this milestone. The adversary this closes against is
therefore one who can substitute the *roster* — over its distribution channel,
or in a directory the connector reads — and not one who owns the host. That is
narrower than "revocation is detectable" sounds, and it is the honest scope.

---

## 11. Evidence

`go test -race` carries this milestone (§7.3). Every row below was applied to
an implementation of this spec and killed by the test named beside it, while
`go build`, `go vet`, `make tck`, and `make demo` stayed green under all of
them. The named test is the one that must fail; some rows fail others too,
which is expected — §8 requires `SignRoster` and `LoadRoster` to share a
validator, so a mutation to that validator is visible from both.

| Mutation | Killed by |
|---|---|
| Delete the expiry check on inbound DSP requests (§4.2) | `TestExpiredRosterRefusesEveryDSPRequest` |
| Delete the expiry check on the initiate hooks (§4.3) | `TestExpiredRosterRefusesTheInitiateHooks` |
| Delete the expiry refusal in the outbound minter (§4.4) | `TestExpiredRosterSendsNothing` |
| Delete the call that records the version (§6.3) | `TestMainRecordsTheRosterVersion` |
| Flip the monotonic comparison (§6.1) | `TestRecordRosterVersionRefusesARollback` |
| Refuse an equal version (§6.1) | `TestRecordRosterVersionAcceptsAnEqualVersion` |
| Drop the version requirement (§3.3) | `TestLoadRosterRequiresAVersion` |
| Remove version and expiry from the signed bytes (§3.1) | `TestSignatureCoversVersionAndExpiry` |
| Skip the signing tool's validation (§8) | `TestRosterSignRefusesWhatLoadRosterWouldRefuse` |

The checkpoint rows are separate on purpose. An earlier draft of this
table had a single row for a check that reaches the DSP listener, the initiate
hooks, and the outbound minter alike — and the two it omitted are exactly the
ones an earlier draft of the *design* got wrong.

**The mutation rows pin that a refusal happens, not which code it carries** —
each asserts a status, so changing `409` to anything else fails them, but no
row is *about* the code. §5.1 is this document's most-argued decision and it
needs its own test: that the DSP routes and the initiate hooks both answer
`409` while `/health` answers `503`.

Design points below the mutation line, each covered by an ordinary test rather
than by a mutation, and listed so the plan does not treat them as covered by
the table: load-time expiry (§4.1); the check order inside `LoadRoster`
(§3.5); a malformed expiry (§4.6); the exclusive boundary (§4.5); the
no-audience and `auth.Mint`-error branches staying unchanged (§4.4); the
`cfg.AuthRequired()` guard and the minter's permitting default (§4.5, §6.1);
the single-row constraint (§6.1); `/health` (§5.2); the once-only warning
(§5.3); the boot log (§5.4); and the approach warning (§5.5).

### 11.1 Commit order

The `internal/auth` change and both harness scripts are one commit and cannot
be split: with the scripts unchanged, `make tck` fails in seconds at
`dsops roster sign`. A store-only commit before it is green, measured.

### 11.2 A hazard the plan must carry

`mintOutboundCredential` is a package-level variable that `NewRouter` assigns
and never restores — a trade-off `callback.go:200-208` records. A test that
builds a router with an expired roster leaks a refusing minter into every test
that runs after it, and the damage is broad: measured on a filtered run, a
long list of unrelated transfer and consumer-driver tests fail.

**Measured, the first symptom is a run that never returns.** With the restore
removed, `go test ./internal/dsp` fails a long list of tests and then hangs
until the package timeout panics, inside a test that waits on a pull which can
no longer be dispatched. CI's `-race -count=2`
(`.github/workflows/ci.yml:28`) adds nothing to that; the hang comes first.

The tests this milestone adds must restore the minter.
`docs/follow-ups.md` carries an entry about test overrides of package
variables that prescribes the opposite remedy — set once in `TestMain`, never
restore — and that remedy cannot serve here, because the expiry tests need a
refusing minter while every other test needs a permitting one. The entry is
updated to record why rather than left to look like a rule this milestone
broke.

---

## 12. What the cross-checks measured

Cross-checks ran against each unwritten revision and again against this
document, the last round implementing it and applying every mutation in §11.

**Revision 1 had no runtime effect.** It checked version and expiry at load
only. Since the roster is read once and never re-read, a connector that does
not restart would never have noticed either. §4.2 onward is the answer, and
§1.3 is the framing that draft lacked.

**Revision 2 reached only the DSP listener.** An implementation of it answered
`200` to an initiate call under a roster expired an hour earlier and signed an
outbound message with the connector's real key. §4.3 and §4.4 are the answer.

**Revision 2's remedy for an untested call site did not work.** It proposed
extracting the wiring into a testable function; measured, deleting the call
still passed every gate. §6.3 is the answer.

**This document's first draft carried false statements and violated this
project's own rule against counts throughout.** It gave the wrong number of
call sites for the outbound minter; it attributed `addColumnIfMissing`'s doc
comment to `migrate`, which would have sent the plan to edit the wrong
function; and it said §35 describes its layering rule as "stylistic", a word
that appears in `DECISIONS.md` only in a different section and with the
opposite sense. It also left the refusal's status code unnamed, `/health` with
no way to learn anything, and the approach warning undesigned while §10 leaned
on it.

**Its second draft chose `503` on a false premise, and that is the reversal
worth recording.** It asserted that the TCK retries a non-`404` non-2xx, and
proposed amending §25.1 on that basis. This repository's own wire contract
says a `5xx` raises immediately on the negative paths and is not retried on
the positive ones — so `503` reproduced the exact failure §25.1 forbids. The
false premise came from `DECISIONS.md` §35, which states the over-broad
version of the rule; §9 corrects it. §5.1 is now `409` and §25.1 stands.

**The same draft justified deferring `connectorAddress` by claiming nothing
could verify it here.** `handleInitiate` receives `providerId` and
`connectorAddress` together and already validates both, so the claim was
false. §2.3 now gives the real reason, which is scope.

**A fourth round checked the third round's own corrections, which nothing had
verified.** It found the placement reversal in §3.5 to be against this file's
own convention — `LoadRoster` already checks document-level structure before
the signature and per-participant content after, and the new fields are
document-level — which also restored §3.4's purpose. It found §6.1 claiming
the version check sits in a block that closes before the store opens; §5.4
putting the roster's identity on a line that runs after the check that can
refuse to start; the initiate hooks with no status code assigned; and §11.2's
leak measurement wrong in the unsafe direction, where the real first symptom is
a test run that hangs until the package timeout. It also found the `409` this
document argues for landing on the one route that already answers `409` for
other reasons, which §5.1 now owns rather than waves past. An implementation
probed the result on the wire: an expired connector answers `409` with a
`TransferError` naming the roster, `503` on `/health`, and still serves the
version endpoint.

**Earlier drafts miscounted things that are now enumerated instead.** Each
harness script writes the roster twice rather than once, because signing
prints rather than writes; `config.example.yaml` does document the roster shape, and
the example becomes invalid; the demo consumer does bind-mount `data_dir`;
and the `connectorAddress` assignment appears in a third document that
revision 2 missed while asserting there were two.
