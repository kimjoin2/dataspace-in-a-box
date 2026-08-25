# The roster as a versioned, expiring artifact

**Status:** design, agreed 2026-08-25. Revised twice before it was written
down, then rewritten after a third round of cross-checks — one of which
implemented this spec on a throwaway copy, ran the suite, and applied every
mutation §11 prescribes. What all of that measured is in §12.

**Ordered item 3** of `docs/goal-gap-analysis.md`.

**What it closes:** that document's P3 bullet, which says revocation "is not
slow; it is undetectable" — but see §1.3, which is precise about which half of
that this closes and which half it only bounds.

**What it amends:** `DECISIONS.md` §25.1's standing rule that every rejection
a DSP endpoint emits is in `[400, 500)`. §5.1 has the exception and its
reasoning.

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

The argument: this milestone changes what the roster document *is* — it gains
a revision and a lifetime — while an address changes what an *entry* means.
An address is only checkable once something resolves one and contacts it,
which is what item 4 builds. Shipping it here would add a field nothing can
verify.

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

What it buys is a clear error for a hand-edited file that was never signed
through the tool, in place of "signature does not verify". That is worth one
check and it is not worth more claim than that.

### 3.5 The check runs after the signature verifies

`LoadRoster` already puts its per-participant checks after `ed25519.Verify`
(`roster.go:84`, then the loop from `:89`). Putting the new ones first would
split one class of check across the signature boundary, and it would make a
forged roster report that its version is missing — sending an operator to
edit an attacker's file instead of discovering it is a forgery.

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

The nil convention applies to the initiate hooks and to the outbound minter,
which are reached in both branches of `NewRouter`. It does **not** apply to
`requireParticipant`: the authentication-off branch does not install that
middleware at all (`router.go:132-140`), so the earlier draft's reasoning
about it was wrong.

A zero `auth.Roster` must not read as expired. Its zero expiry would
otherwise break `require_auth: false` on a deployment that has no roster.

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

### 5.1 `503`, and the `DECISIONS.md` §25.1 amendment

**Not `401`.** The caller's credential may be perfect and the fault is
entirely local; answering "a valid participant credential is required" sends
their operator hunting their own credential across an organizational
boundary, where they cannot read this connector's log.

**`503`, which is outside §25.1's `[400, 500)`.** That rule is amended rather
than worked around, because the reason §25.1 records is entirely about `404`:
"Its own client checks for `404` *before* it consults whether an error was
expected, so a `404` raises immediately and aborts the exchange instead of
being read as the refusal it is." `503` has no such problem — the TCK's own
client retries a non-`404` non-2xx — and an expired roster is not a rejection
of the message at all. It is this connector being unable to serve anyone. The
amendment states that: the `[400, 500)` rule governs refusals *of a message*,
and `404` remains forbidden outright.

The body says the roster expired. That is not reconnaissance: it discloses a
fact about this connector's own configuration, not about the caller and not
about who else is in the roster.

### 5.2 `/health`

Today it answers `{"status":"ok"}` unconditionally
(`internal/mgmt/router.go:32-35`), so a connector that can serve no
counterparty stays in rotation. It reports the expiry instead.

`mgmt.NewRouter` has no roster and must not acquire one — §35 settled that
this package holds no opinion about `internal/dsp` and takes plain values from
`cmd/dsbox`. It takes the same predicate, as a plain function, by the same
route the initiate handlers already travel.

Two consequences to record rather than discover:

- The comment on that route says it is unauthenticated because "it carries no
  information". That stops being true, and §9 lists it. The disclosure is the
  same one §5.1 argues is fine.
- **Both harnesses poll `/health` for readiness** (`test/tck/run.sh:107`,
  `demo/run.sh:85`) with `curl -sf`. A roster that is already expired kills
  the process at load, so the probe fails on a refused connection and the
  harness prints its own timeout message — the real reason appears only in
  the dumped container logs. A roster that expires *during* a run would hang
  the probe. Neither is a defect of this design, but the first is a
  diagnosis trap and the harness comment should say so.

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

Version and expiry, on the existing startup line. Nothing today reports which
roster a running connector holds, so an operator whose repository says
version four has no way to learn that a connector is still on three.

### 5.5 A warning before expiry, and its limit

If the roster expires within a week, the boot log says so.

That is the whole of it, and the limit is stated because §10.1 leans on it: a
connector that has been up for a month gets no warning, because warning while
running needs a timer and this design has none. An operator who restarts
rarely learns nothing from this.

---

## 6. Monotonicity, and making the wiring undeletable

### 6.1 Where the state lives

`auth.LoadRoster` stays stateless and the returned `Roster` carries its
version. The store keeps a single-row table `roster_version` with one column,
`highest INTEGER NOT NULL`. A lower version is refused at startup. An equal or
higher one is accepted, and only a higher one writes — equal is a no-op, so
the ordinary restart path does not touch the database.

Equal must be accepted, or an ordinary restart with an unchanged roster fails
to boot.

**The check runs after `store.Open` and before `dsp.NewRouter`**, so a
rollback is refused before the outbound minter global is armed and before the
pull context exists.

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

`date` is not portable for this: GNU takes `-d`, macOS takes `-v`. A two-way
fallback, written once per script.

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
boot during a recovery, and **excluding the expiry comparison**: signing a
roster that expires next week is exactly what an operator does.

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
  gains a third fatal failure that cannot live in it because the store is not
  open yet.
- `DECISIONS.md` §9's trade-off, which gains an upper bound and the cost that
  bound carries; §25.1, per §5.1.
- `config.example.yaml:75-76`, which shows the roster JSON inline and is the
  only onboarding document that does, and `:82-86`, which says an unusable
  roster fails "at boot, not by refusing every request later" — refusing
  every request later is now deliberate.
- The three `connectorAddress` assignments in §2.3.
- `docs/goal-gap-analysis.md`'s P3 revocation bullet, which proposed this fix,
  and its ordered item 3, which still bundles the clock work §2.2 splits out.
- `README.md`'s description of the roster and of what the DSP listener
  requires.
- `test/tck/dsbox.yaml` and `test/tck/compose.yaml` comments about the roster
  being read once at startup, which is still true and no longer sufficient.

---

## 10. Trade-offs accepted

### 10.1 The fleet stops, and only the signer can restart it

Every connector shares one `expires_at`. Recovery needs a signature from
`roster_signer`, a key §27.1 deliberately places outside every participant,
plus redistribution and a restart.

**The recovery sequence, written down because it has to be possible:** the
operator edits the roster's `version` (if a participant changed) and
`expires_at`, runs `dsops roster sign`, pastes the signature, distributes the
file, and restarts each connector. Every step exists today except the two new
fields.

**A revocation must bump `version`.** An expiry-only re-issue need not: equal
is accepted, and the older document's earlier expiry limits it on its own.

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

---

## 11. Evidence

`go test -race` carries this milestone (§7.3). Every row below was applied to
an implementation of this spec; each was killed by the named test and by
nothing else — `go build`, `go vet`, `make tck`, and `make demo` stayed green
under all of them.

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

The three checkpoint rows are separate on purpose. An earlier draft of this
table had one row for a check that reaches three places, and the two it
omitted are exactly the two an earlier draft of the *design* got wrong.

Design points below the mutation line, each covered by an ordinary test
rather than by a mutation, and listed so the plan does not treat them as
covered by the table: load-time expiry (§4.1), the check order inside
`LoadRoster` (§3.5), a malformed expiry (§4.6), the no-audience and
`auth.Mint`-error branches staying unchanged (§4.4), `/health` (§5.2), the
once-only warning (§5.3), the boot log (§5.4), and the approach warning
(§5.5).

### 11.1 Commit order

The `internal/auth` change and both harness scripts are one commit and cannot
be split: with the scripts unchanged, `make tck` fails in seconds at
`dsops roster sign`. A store-only commit before it is green, measured.

### 11.2 A hazard the plan must carry

`mintOutboundCredential` is a package-level variable that `NewRouter` assigns
and never restores — a trade-off `callback.go:200-208` records. A test that
builds a router with an expired roster leaks a refusing minter into every
test that runs after it; measured, one such test failed fifteen unrelated
ones. The tests this milestone adds must restore it. `docs/follow-ups.md`
already carries an entry about this shape.

---

## 12. What the cross-checks measured

Three rounds ran, against two unwritten revisions and against this document.

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

**This document's first draft carried three false statements and violated
this project's own rule against counts throughout.** It gave the wrong number
of call sites for the outbound minter; it attributed
`addColumnIfMissing`'s doc comment to `migrate`, which would have sent the
plan to edit the wrong function; and it said §35 describes its layering rule
as "stylistic", a word that appears in `DECISIONS.md` only in a different
section and with the opposite sense. It also left the refusal's status code
unnamed, `/health` with no way to learn anything, and the approach warning
undesigned while §10 leaned on it.

**Earlier drafts miscounted things that are now enumerated instead.** Each
harness script writes the roster twice rather than once, because signing
prints rather than writes; `config.example.yaml` does document the roster shape, and
the example becomes invalid; the demo consumer does bind-mount `data_dir`;
and the `connectorAddress` assignment appears in a third document that
revision 2 missed while asserting there were two.
