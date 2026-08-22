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

**Bytes, without knowing anything.** `handleAgreement`
(`internal/dsp/negotiation_consumer_handler.go:266`) writes an agreement row
from the message body. A peer initiates a negotiation naming itself as
provider, reads this connector's own consumer pid out of the request that
arrives, posts a forged agreement to it, then opens a transfer citing it. Every
ownership check below passes on this path, because the forger is the honest
owner of what it forged — which is why it is in scope. Shipping ownership
checks around a mintable agreement produces confidence, not security.

---

## 3. Order, and why it is not the obvious one

The obvious decomposition is record → publish → enforce. It is wrong here,
because **the recording is already done for four of five tables.**
`counterparty_id` exists on `negotiations`, `consumer_negotiations`,
`transfer_processes` and `consumer_transfer_processes`
(`internal/store/store.go:284-291`), populated from the verified issuer at
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

The comparison is therefore `stored != "" && issuer != stored`. **This makes
the fix partial by construction, and that is stated rather than hidden:** an
imported agreement with no named owner stays exactly as open as it is today.

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

Consequence worth naming: the consumer role's inbound messages stay
unauthorized in this milestone. Closing that needs `providerId` validated
against the roster at initiate time, which is a separate change with its own
compatibility question.

### 4.4 Serving data as provider under a consumer-role agreement is refused

This is the forging path's fix, and it needs no new state. `store.Agreement`
already records `Origin` (`internal/store/store.go:106-116`), and the three
values answer the question directly:

- `OriginNegotiated` — this connector is the provider. Serve.
- `OriginImported` — an operator asserted it. Serve.
- `OriginAgreed` — this connector is the **consumer**. Never serve as provider.

`handleTransferRequest` refuses the third with `403`. That closes the forged
path at its only exit, because a forged agreement can only ever be recorded
with `OriginAgreed` (`internal/dsp/negotiation_consumer_handler.go:266-277` is
its sole writer).

Rejected alternative: checking the forged agreement's *target* at intake.
`handleAgreement` already computes `wrongTarget` (`:291`), but it compares
against the negotiation's own dataset id — which the forger chose when it
initiated. It is false on the attack, and it must stay usable for its real
purpose, since a consumer legitimately records agreements for datasets this
connector does not advertise.

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

Guarded exactly as `handleData` guards its own:
`cfg.AuthRequired() && stored != "" && issuer != stored`. Per §4.3 the check
applies only where the row is provider-role.

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

Safe on the wire: CN passes 15/15 today with a bare UUID in that field, which
is not even an IRI, so nothing asserts its value. Nothing in this connector
reads `assignee` back.

### Step 4 — record on agreements

`counterparty_id` on `agreements`, in the schema literal *and* in
`addColumnIfMissing`'s loop (`internal/store/store.go:284-291`). Note the
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

`agreementView` (`internal/mgmt/router.go:154`, `:167`) exposes it: an operator who
cannot see who an agreement is with cannot audit a check that depends on it.

### Step 5 — enforce on agreements

`handleTransferRequest` compares the stored owner, empty permitted.
`handleTransferInitiate` (`internal/dsp/transfer_consumer_handler.go:73-83`)
gets the symmetric check.

### Also, while in these files

`internal/dsp/transfer_handler.go:506-513` builds a `store.ConsumerTransfer`
without `CounterpartyID`, while the sibling call three lines above includes it.
Every consumer-driven follow-up message therefore goes out unsigned — four
warnings in a TCK run, matching the four after-`STARTED` steps configured. One
field.

---

## 6. Verification

**The TCK is a regression gate, not evidence.** No TCK test sends a message
about another participant's exchange, so nothing in the suite exercises a
refusal. `make tck` must stay 65 of 65, and §4.3 exists precisely because the
naive version of this change takes it to 35.

**Evidence is unit tests**, per enforcement point: a matching issuer passes, a
mismatched one gets `403`, an empty stored owner passes, and the check precedes
the state check. Plus the three attack sequences from the cross-check, as
regression tests — they are the only artifacts that demonstrate the gaps, and
they should live in the repository rather than in a report.

**Mutation testing**, per this project's convention: remove each check in turn
and confirm a specific named test fails.

**`make demo` must stay green.** It passes today and should keep passing
unchanged: both connectors use real participant ids, so every comparison
matches. That it passes is a weaker signal than it looks — the fixture happens
to use the same string as `participant_id`, and nothing validates that it must.

Final gate: `go test -race ./...`, `go vet ./...`, `make tck` 65/65, `make demo`.

---

## 7. Documentation

**New `DECISIONS.md` §32**, following the repo's recent practice (§26, §28,
§30) of a superseding section that points back rather than an in-place rewrite:
§23.11's falsified prediction as the justification, the four decisions of §4,
and two trade-offs — an imported agreement with no owner stays open, and
because there is no update path (§25.3) an operator who names the wrong
participant on import has locked the real one out with no recourse but a new
agreement id.

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
(`internal/store/store.go:40-45, 67-72, 122-127, 726-731`), `Agreement`'s
`ConsumerPID` comment (`:91-93`) which becomes misleading beside a real
counterparty column, both `lookup` doc comments, `handleTransferRequest`'s
"looked up by its id alone" (`:123-129`), and the two initiate-hook comments
still describing themselves as "open to anonymous callers"
(`internal/dsp/negotiation_consumer_handler.go:47-51`,
`internal/dsp/transfer_consumer_handler.go:41-45`).

**`README.md`** — the auth paragraph implies ownership is checked at one
endpoint. After this it is a property of the connector.

**`docs/follow-ups.md`** — all three entries deleted when closed, per that
file's own rule.

**`docs/milestone-sequence.md`** is stale independently of this work and should
be corrected in the same pass: milestones 3 and 4 shipped (§26, §29), and two
shipped milestones are missing entirely (§27 roster signing, §31
range/resumption). This milestone is added with its own ordering argument.
