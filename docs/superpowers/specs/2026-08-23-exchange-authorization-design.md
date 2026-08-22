# Exchange authorization

**Status.** Design, verified before writing. Adds `DECISIONS.md` §32 and
corrects §23.11, §24.2, §25.1, §25.2, §25.3.

Authentication shipped in §27: every DSP route but the version document now
requires a credential from a roster participant. Authorization did not. This
connector checks *that* a caller is admitted and, in exactly one handler,
*which* row they may touch. Everywhere else, an admitted participant may act on
any exchange whose id they know.

This milestone makes the identity §27 established load-bearing.

---

## 1. Why this is owed rather than chosen

`DECISIONS.md` §23.11 recorded the gap and predicted its fix:

> the provider pid is a de-facto capability token — no handler checks a
> message's `consumerPid`/`providerPid` against the stored negotiation, so
> anyone who learns a pid can terminate or finalize that negotiation. […] Both
> consequences are future work, and both are properly closed by enforcing §10's
> connector-to-connector JWT on this listener, not by patching the handlers one
> field at a time.

**The JWT shipped and did not close it.** It narrowed the attacker set from
anyone on the network to any roster participant, which is not closure — a
roster is shared by parties who are strangers to each other, and that is the
boundary this protocol exists to keep. §24.2 inherits the same posture by
reference and inherits the error with it.

This section is what §23.11 said would not be needed: the handlers, checked one
at a time. It is needed because the prediction was tested and was wrong.

Severity is high, not critical: every path requires a roster credential, and
impersonation requires another participant's private key. There is no release
(`git tag` is empty) and no known deployment beyond this repository's own
harnesses.

---

## 2. What is open

Three gaps, recorded in full in `docs/follow-ups.md`. Summarized by what they
end in:

**State change and disclosure.** `issuerFrom`
(`internal/dsp/auth_middleware.go:22`) recovers the verified counterparty of
every request; `handleData` (`internal/dsp/data_handler.go:77`) is the only
handler that compares it to the row it acts on. Suspending or terminating
another participant's transfer answers `200`. So does driving their negotiation
to ACCEPTED or TERMINATED.

**Bytes, via an agreement id.** `agreements`
(`internal/store/store.go:157`) records no participant. A roster peer that
knows an agreement id opens a transfer under it, becomes that transfer's
counterparty (`internal/dsp/transfer_handler.go:167`), and passes `handleData`'s
check. Two requests and a `GET`.

**Bytes, through an agreement this connector never agreed to.** `handleAgreement`
(`internal/dsp/negotiation_consumer_handler.go:266`) writes an agreement row
from the message body. A peer initiates a negotiation naming itself as
provider, reads this connector's own consumer pid out of the request that
arrives, posts a forged agreement to it, then opens a transfer citing it. The
outbound request does not have to succeed: the row is written and `200`
returned before the push goroutine runs, so the attacker needs an endpoint that
logs a body, not a working connector.

Every ownership check below passes on this path, because the forger is the
honest owner of what it forged — which is why it is in scope. Shipping
ownership checks around a mintable agreement produces confidence, not security.

**What this is not.** It is not access to bytes that were otherwise
unreachable. v1 has no per-participant negotiation policy: `decideInitialRequest`
takes no participant argument, offer ids are derived (`#offer`), and the catalog
lists every dataset — so any roster participant can already negotiate any
advertised, currently-valid dataset through the front door and get the same
bytes. The validity window is re-checked at pull time regardless
(`internal/dsp/data_handler.go:110`), so it is not an expiry bypass either.

Its cost is elsewhere, and is worth stating precisely rather than overstating:
**the moment a per-participant negotiation policy exists, this path routes
around it** — so closing it now keeps a future authorization decision from
being born already bypassable. And three harms are present today: a forged row
is indistinguishable from a real one in `GET /agreements`, which is the
operator's only audit surface; a forger that writes an id first makes it
permanently unimportable, because `CreateAgreement` refuses duplicates and §25.3
guarantees no delete path; and the duplicate refusal is itself an existence
oracle that leaves a row behind on every miss.

---

## 3. Order, and why it is not the obvious one

The obvious decomposition is record → publish → enforce. It is wrong here,
because **the recording is already done for four of five tables.**
`counterparty_id` exists on `negotiations`, `consumer_negotiations`,
`transfer_processes` and `consumer_transfer_processes`
(`internal/store/store.go:284-292`), populated from the verified issuer at
`internal/dsp/negotiation_handler.go:96` and
`internal/dsp/transfer_handler.go:167`.

So the largest gap closes with comparisons against data already on disk, and
the schema work serves only the smaller one. Ordered by hole-closed per unit of
risk:

1. **Enforce on exchanges.** No schema, no API, no wire change.
2. **Close the forging path.** One condition.
3. **Publish `assignee`.** One line and a fallback; no security content.
4. **Record on agreements.** Column, migration, optional import field.
5. **Enforce on agreements.** Depends on 4.

Each step is independently shippable and independently verifiable. This is the
same argument `docs/milestone-sequence.md` used to put authentication before
the data plane: do the step that makes the next one safe, first.

---

## 4. The four decisions

### 4.1 A refusal is `403`, never `404`

§25.1 already fixed this as a standing rule:

> every rejection a DSP endpoint emits is in `[400, 500)` and never `404`, and
> `404` means only that the `{id}` names nothing. The reason is the
> counterparty. Its own client checks for `404` before it consults whether an
> error was expected, so a `404` raises immediately and aborts the exchange
> instead of being read as the refusal it is.

The TCK enforces the same: a `404` is fatal on every path, including ones that
expect an error. `403` is also what this connector already answers for exactly
this condition — "this transfer is not yours"
(`internal/dsp/data_handler.go:80`).

`403` is an existence oracle: it distinguishes "not yours" from `404`'s "no
such id". That is accepted rather than solved, for two reasons. The sibling
oracle already exists at `internal/dsp/transfer_handler.go:141`, and collapsing
this one into `404` would break the rule above.

**Ordering:** the ownership check runs *before* the state check, so a prober
learns nothing about an exchange's progress. `handleData` already made this
choice (`:75-76`) and a test already pins it.

### 4.2 An empty counterparty means "not known", and is permitted

`counterparty_id` defaults to `''`, described where it was introduced as
"[e]mpty on rows created before authentication existed […] those exchanges
predate anyone to address" (`internal/store/store.go:280-283`). The only
existing consumer of an empty value warns and continues
(`internal/dsp/router.go:71`).

Refusing on empty would be the safer-sounding choice and it is not available:
`test/tck/run.sh` seeds twelve agreements through `POST /agreements` with no
owner, so a deny fails all fifteen TP and all fifteen TP_C results. The same
holds for any operator who imports an agreement today.

The comparison is therefore `stored != "" && issuer != stored` — **for the
agreement check only.**

The exchange checks of Step 1 keep the stricter form `handleData` already uses,
which has no empty clause:

```go
if issuer := issuerFrom(r); h.cfg.AuthRequired() && issuer != t.CounterpartyID {
```

Two reasons for the asymmetry. Nothing forces the permissive form on exchange
rows — every one the TCK creates carries a verified issuer — and permissiveness
should be bought only where it is required. And `handleData` must not be
factored into a shared helper that acquires the empty clause: a transfer row
with an empty counterparty is refused to everyone today, and adding the clause
would serve it to any roster participant. `internal/dsp/data_handler_test.go`
pins the current behavior; the new checks are written beside it, not through it.

**This makes the agreement half partial by construction, and that is stated
rather than hidden:** an imported agreement with no named owner stays exactly as
open as it is today.

### 4.3 Compare only against a counterparty that came from a verified issuer

`CounterpartyID` has two sources. Provider-role rows take it from
`issuerFrom(r)` — a value this connector verified. Consumer-role rows take it
from the request body of an operator's own initiate call
(`internal/dsp/transfer_consumer_handler.go:95`,
`internal/dsp/negotiation_consumer_handler.go:71`) — a string the caller chose.

Only the first is an identity. Comparing against the second is comparing
against an unverified assertion, which is not authorization, and it breaks
immediately: the TCK harness authenticates as `urn:participant:tck` while
hardcoding `TCK_PARTICIPANT` as the `providerId` in its initiate hook
(`DspConstants.TCK_PARTICIPANT_ID` in the pinned image). Consumer-role rows in
the harness therefore hold a string that no inbound request will ever present.
Enforcing there would fail all fifteen TP_C results, and CN_C for the same
reason.

So the rule is not a carve-out for the harness. **A row's counterparty is an
authorization anchor only where this connector verified it.** The harness
merely demonstrates why.

Two consequences worth naming, both of which this milestone leaves open.

The consumer role's inbound messages stay unauthorized. Closing that needs
`providerId` validated against the roster at initiate time, which is a separate
change with its own compatibility question.

And the same unverified `providerId` becomes the *audience* of a credential this
connector signs (`mintOutboundCredential`, reached from
`internal/dsp/transfer_consumer_handler.go:322`), addressed to a caller-chosen
endpoint. That is an impersonation primitive against a third participant, and it
is reachable through `/negotiations/initiate` with no agreement at all — so it
is not created by anything here, but it is the sharpest reason the initiate
hooks deserve their own milestone.

### 4.4 Serving data as provider under a consumer-role agreement is refused

This is the forging path's fix, and it needs no new state. `store.Agreement`
already records `Origin` (`internal/store/store.go:95`, values at `:99-109`),
and the three answer the question directly:

- `OriginNegotiated` — this connector is the provider. Serve.
- `OriginImported` — an operator asserted it. Serve.
- `OriginAgreed` — this connector is the **consumer**. Never serve as provider.

`handleTransferRequest` refuses the third with `403`, and so does every other
provider-role reader of an agreement — `datasetFor`, `hasSourceFor`,
`driveTransfer` are protected only transitively today, and one shared helper
makes each testable on its own.

That closes the forged path's **byte exit** completely: `transfer_processes` has
exactly one writer and this check sits in front of it. A forged agreement can
only ever be recorded with `OriginAgreed`, which
`internal/dsp/negotiation_consumer_handler.go:270` is the sole writer of; the
attacker cannot reach the other two origins, because `OriginImported` is behind
the management token on a localhost listener and `OriginNegotiated` carries an
id this connector generates.

**Why the fix has to be at consumption rather than intake.** The forged message
is not detectably forged — it is exactly what an honest provider sends in a
negotiation the attacker legitimately owns. No field comparison catches it:
`assigner` matches, because the attacker named itself as provider at initiate;
"must arrive from the participant the negotiation is with" matches, for the same
reason. `handleAgreement`'s existing `wrongTarget` (`:291`) does not help either
— it compares against the negotiation's own dataset id, which the forger also
chose, and it is not an intake check at all: the row is already written at
`:266-277`, and `wrongTarget` only selects the reaction afterwards. The defect
is role confusion at consumption, so consumption is where it is caught.

**What §4.4 does not close**, recorded rather than discovered. The forged row
survives, so: an id written first is permanently unimportable by its rightful
owner (no delete path, §25.3); the duplicate refusal remains an existence oracle
that leaves a row behind on every miss; the row is indistinguishable from a real
one in `GET /agreements`; and `handleTransferInitiate`'s own agreement gate
(`internal/dsp/transfer_consumer_handler.go:73-83`) is satisfiable by a minted
id, which makes it decorative. Closing those needs the consumer-role agreement
id space separated from the provider-role one — a different table or a
`(agreement_id, origin)` key — which revisits the "one table, one rule"
argument at `internal/store/store.go:80-87` and is not attempted here.

---

## 5. Change surface

### Step 1 — enforce on exchanges

Seven resolvers, not two. `transferHandler.lookup`
(`internal/dsp/transfer_handler.go:558`) is the only one on the transfer side
and covers `handleGetTransfer` (`:413`) and `applyTransition` (`:459`).
`negotiationHandler.lookup` (`internal/dsp/negotiation_handler.go:372`) covers
only `handleReRequest` and `handleVerification`; its own doc comment says so.
The rest each resolve their own row and each need the check:
`handleEvent` (`:180`), `handleTermination` (`:288`), `handleGetNegotiation`
(`:339`), `handleOffers` (`internal/dsp/negotiation_consumer_handler.go:120`),
`handleAgreement` (`:231`).

`handleTermination` is not reachable through either `lookup`, and it is the
harm this gap is usually described by.

**Five of those seven get a check; two cannot.** `handleOffers` and
`handleAgreement` resolve only through `GetConsumer`, so they can never produce
a provider-role row and §4.3 leaves them alone. They are listed because an
implementer enumerating resolvers will find them and must know they are
deliberately unguarded — and because §6's mutation testing will therefore find
no failing test for them, which is expected rather than a gap. What covers them
is §4.3's second paragraph: the consumer role's inbound messages stay
unauthorized in this milestone.

**Where the check goes matters, and getting it wrong is the 35-of-65 outcome.**
`resolvedTransfer` carries `CounterpartyID` for consumer rows too
(`internal/dsp/transfer_handler.go:571`), so a check written in `handleGetTransfer`,
in `applyTransition`, or against the returned struct compiles, reads correctly,
and silently refuses all fifteen TP_C results. In `transferHandler.lookup` the
check belongs **after** `GetTransfer` succeeds — between `:593` and `:594` —
where the consumer branch has already returned at `:567-581`. The four other
live points are `negotiationHandler.lookup` (provider-only by construction),
`handleProviderAcceptedEvent`, `handleProviderTermination`, and
`handleGetNegotiation`'s provider branch, which has no branch function and takes
the check inline before `writeJSON`.

The form is `handleData`'s exactly — `cfg.AuthRequired() && issuer != stored`,
with no empty clause, per §4.2.

### Step 2 — close the forging path

One condition in `handleTransferRequest`
(`internal/dsp/transfer_handler.go:123-146`): refuse `OriginAgreed`. `GetAgreement`
already returns the whole row.

### Step 3 — publish `assignee`

`buildAgreementMessage` (`internal/dsp/negotiation.go:450`) sets
`Assignee: n.ConsumerPID`. It becomes `n.CounterpartyID`, falling back to
`n.ConsumerPID` when that is empty — mandatory, because with `require_auth:
false` the field would otherwise go out as `""` and the TCK's schema requires
the property present.

The comment justifying the placeholder (`:316-327`) is half stale and gets
rewritten rather than deleted. Its claim that `ContractRequestMessage` carries
no participant field is still true; its conclusion — "negotiation is
unauthenticated in v1 […] so there is no participant identity to put here even
from a trust boundary" — was falsified by §27.

The fallback's real justification is not the schema — `assignee` has no
`omitempty` and the TCK types it as a bare required string, so `""` validates.
It is `internal/dsp/negotiation_test.go:216`, which asserts
`Assignee == n.ConsumerPID` against a fixture whose `CounterpartyID` is empty.
The fallback is what keeps that test meaningful instead of rewriting it.

Safe on the wire: CN passes 15/15 today with a bare UUID in that field, which is
not even an IRI, so nothing asserts its value. Nothing in this connector reads
`assignee` back.

### Step 4 — record on agreements

`counterparty_id` on `agreements`, in the schema literal *and* in
`addColumnIfMissing`'s loop (`internal/store/store.go:284-292`). Note the
existing four columns are in the loop but in no `CREATE` literal, which makes
`migrate`'s own comment false for them; this one does both, and §23.1's open
question — whether a third column-add earns a migration tool — is answered no,
in §32.

Set from `n.CounterpartyID` at both writers
(`internal/dsp/negotiation_handler.go:498-508` for `OriginNegotiated`,
`internal/dsp/negotiation_consumer_handler.go:266` for `OriginAgreed`).
`POST /agreements` gains an **optional** `counterpartyId`; optional, because
required would break `test/tck/run.sh` and every existing operator import in
lockstep, and §4.2 already fixes what empty means.

The field is role-relative — for `OriginAgreed` the counterparty is the
*provider* — which matches the convention the other four tables use and needs
one sentence saying so.

Four SQL statements carry the column besides the schema literal, and missing
one fails on the first query rather than at migration: `CreateAgreement`'s
INSERT (`internal/store/store.go:548`), `GetAgreement`'s SELECT (`:565`),
`CreateAgreementIfNegotiationAgreed`'s INSERT (`:621`), and `ListAgreements`'
SELECT (`:838`).

`agreementView` (`internal/mgmt/router.go:154`, `:167`) exposes it: an operator
who cannot see who an agreement is with cannot audit a check that depends on it.
**The field is appended after `createdAt`, not grouped beside `agreementId`.**
Go emits struct fields in declaration order, and `demo/run.sh`'s resume round
extracts its agreement with a `sed` that requires `agreementId` and `datasetId`
to be adjacent in the JSON. The natural grouping breaks the demo; the ordering
requirement is therefore part of this step, not a detail left to the
implementer.

### Step 5 — enforce on agreements

`handleTransferRequest` compares the stored owner against the verified issuer,
empty permitted per §4.2.

**`handleTransferInitiate` gets no such check**, and an earlier draft of this
spec was wrong to call for one. Its only candidates both fail: comparing
`issuerFrom(r)` is incoherent, because `/transfers/initiate` is an operator hook
whose caller is this connector's own operator — in `make demo` that token is
self-issued (`iss` = the connector's own id), so the check would refuse the
demo's own transfers; and comparing the initiate body's `providerId` against a
counterparty that was itself filled from a `providerId` compares two unverified
strings, which §4.3 forbids calling authorization.

That leaves the consumer-role agreement gate as the sanity check it already is,
and §4.4 records that a minted id satisfies it. Making it an authorization
decision requires the verified-`providerId` work §4.3 defers.

### Also, while in these files

`internal/dsp/transfer_handler.go:507-514` builds a `store.ConsumerTransfer`
without `CounterpartyID`, while the sibling call three lines above (`:499-505`)
includes it.
Every consumer-driven follow-up message therefore goes out unsigned — four
warnings in a TCK run, matching the four after-`STARTED` steps configured. One
field.

---

## 6. Verification

**The TCK is a regression gate, not evidence.** No TCK test sends a message
about another participant's exchange, so nothing in the suite exercises a
refusal. `make tck` must stay 65 of 65.

§4.3 exists because the naive version does not. The harness's split identity was
read from the pinned image's constant pool — `DspConstants.TCK_PARTICIPANT_ID`
carries the literal `TCK_PARTICIPANT`, and both consumer-role initiate clients
inline it — so comparing against consumer-role rows loses all fifteen TP_C
results and all but one of CN_C: 65 − 15 − 15 = **35**. The most diagnostic
single failure is TP_C:01-01, which resolves a consumer row on the very first
inbound start.

**Evidence is unit tests**, per enforcement point: a matching issuer passes, a
mismatched one gets `403`, an empty stored owner passes, and the check precedes
the state check. Plus the three attack sequences from the cross-check, as
regression tests — they are the only artifacts that demonstrate the gaps, and
they should live in the repository rather than in a report.

**Mutation testing**, per this project's convention: remove each check in turn
and confirm a specific named test fails. Expect no failure for `handleOffers`
and `handleAgreement` — they carry no check by §4.3, and Step 1 says why.

**`make demo` must stay green**, and two things in this spec exist because the
first draft would have broken it: Step 5's removal of the
`handleTransferInitiate` check, and Step 4's field-ordering requirement.

Where it does pass, it passes for a weaker reason than it looks. The provider's
rows match because they were filled from a verified issuer. The consumer's rows
are never checked at all (§4.3), and would have matched anyway only because the
fixture's `providerId` happens to equal the provider's `participant_id` —
nothing validates that it must.

Final gate: `go test -race ./...`, `go vet ./...`, `make tck` 65/65, `make demo`.

---

## 7. Documentation

**New `DECISIONS.md` §32**, following the repo's recent practice (§26, §28,
§30) of a superseding section that points back rather than an in-place rewrite:
§23.11's falsified prediction as the justification, the four decisions of §4,
and four trade-offs: an imported agreement with no owner stays open; because
there is no update path (§25.3), an operator who names the wrong participant on
import has locked the real one out with no recourse but a new agreement id; the
residuals §4.4 leaves (id squatting, the duplicate oracle, the forged audit
row); and one behaviour change worth recording so it is not rediscovered as a
regression — a connector initiating against its own public address, which today
half-works by accident, is refused outright by §4.4.

**In-place corrections**, which are facts rather than reasoning and which a
superseding section is the wrong instrument for:

- §25.2 "exactly two writers" — `internal/store/store.go:78` already says three.
- §25.3 "takes two strings" — becomes two required and one optional. The
  clause that matters, "there is no update path and no delete path", stays
  verbatim: `importAgreement`'s duplicate re-query depends on it and it stays
  true.
- §25.3 "records an agreement and nothing more" — `GET /agreements` exists.
- §23.11 and §24.2 — the prediction, and the reference that inherits it.
- §25.1 "matched by id alone" — becomes conditional.

**Code comments** that assert the narrower world: the four `CounterpartyID`
doc comments calling it an addressing field
(`internal/store/store.go:40-45, 67-72, 122-127, 727-731`), `Agreement`'s
`ConsumerPID` comment (`:91-93`) which becomes misleading beside a real
counterparty column, both `lookup` doc comments, `handleTransferRequest`'s
"looked up by its id alone" (`:123-129`), and the two initiate-hook comments
still describing themselves as "open to anonymous callers"
(`internal/dsp/negotiation_consumer_handler.go:47-51`,
`internal/dsp/transfer_consumer_handler.go:41-45`), and
`internal/dsp/transfer_handler.go:548-551`, which claims the negotiation
resolvers try the consumer table first — they try the provider table first.

**`README.md`** — the auth paragraph implies ownership is checked at one
endpoint. After this it is a property of the connector.

**`docs/follow-ups.md`** — all three entries deleted when closed, per that
file's own rule.

**`docs/milestone-sequence.md`** is stale independently of this work and should
be corrected in the same pass: milestones 3 and 4 shipped (§26, §29), and two
shipped milestones are missing entirely (§27 roster signing, §31
range/resumption). This milestone is added with its own ordering argument.
