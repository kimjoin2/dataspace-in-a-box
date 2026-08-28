# Sequencing the milestones to a working dataspace

This is a decision about order, not a design. Each milestone named here gets
its own spec and its own implementation plan when it starts; this document
only argues which one goes first and why, and it exists because the answer is
driven by something easy to miss: **what can still verify the work**.

The TCK has carried every milestone so far. It stops carrying them at
different points for each of the remaining ones, and that — more than
dependency order — is what sets the sequence.

**This document sequences one axis of the goal.** Every milestone it names,
finished or forward, is protocol or authorization work; it has no entry for
onboarding, discovery, the management surface, the UI, observability, or
release. That is not an oversight in the list — it follows from the rule this
document has been applying, "do the step that makes the next one safe, first",
which says nothing about a licence or a metrics endpoint and so silently
excluded everything it could not phrase as safety. The rule stated in the
paragraph above — order by *what can still verify the work* — is the general
one, and it does reach those axes. `docs/goal-gap-analysis.md` measures all
four promises against the goal and proposes an order across them.

**It also disputed this document, and not only its coverage.** That document's
second item argued the milestone below was framed around the wrong question —
that before asking what an initiate call may name, the question is which
listener it belongs on, because moving those hooks to the management listener
makes validating `providerId` unnecessary rather than difficult.

**Settled by whoever started the work, which is how this note said it should
go.** `DECISIONS.md` §35.1 adopts the gap analysis's framing: the prior
question is which listener, and moving the hooks there is what removes the
impersonation primitive rather than mitigating it. It does not follow the gap
analysis to its conclusion — §35.2 validates `providerId` anyway, on a
diagnostic argument rather than a security one, so that an operator who names
a participant this connector cannot verify is refused at the point of the
mistake instead of watching every later inbound message on that exchange be
refused with a log line as the only clue. Nothing below has been rewritten to
match; the entry in the next section records the outcome and the argument
below is kept as it stood.

## What has been done since this was written

**All four milestones below are complete, and more have shipped that this
document never listed.** That is the reason to correct it in place: the section
after this one is still the argument that ordered the work, and it should not
keep reading as the current plan.

Milestones 1 and 2 came first, and both confirmed the reasoning that put them
in this order.

**1. Connector authentication** (2026-08-19). Every DSP route except the
version document requires an EdDSA credential from a roster participant.
The prediction that the TCK could still verify it held: the suite stayed at
64 of 65 with the harness presenting a minted token, and removing that token
failed 63 of the 65 — which is the evidence no unit test could give that the
middleware is wired to the routes the TCK actually calls. Spec:
`specs/2026-08-19-connector-authentication-design.md`.

**2. The data plane** (2026-08-19). `make demo` moves a real file between two
authenticated connectors under a negotiated agreement, and fails if a single
line differs. The prediction that this milestone would have no external
verification also held — no TCK test moves a byte — which is why the demo is
part of the milestone rather than a nicety after it. Spec:
`specs/2026-08-19-data-plane-design.md`.

**The ordering argument was load-bearing, not tidy-minded.** Building the data
plane first would have shipped a server handing bytes to anyone who could
drive a transfer to `STARTED` anonymously — which, before milestone 1, was
anyone. The data endpoint's authorization is now three checks against a
credential that already existed, and it needed no second token type precisely
because authentication came first.

**3. Policy constraints** (2026-08-19). `DECISIONS.md` §26. A dataset's
`validity_until` became a real ODRL constraint on its offer and its agreement,
and the data plane re-checks it on every pull rather than once at `AGREED`. The
prediction below — that the TCK would be a regression guard rather than
evidence, because the CN suites negotiate unconstrained offers — held, with one
correction §26.5 records: three CN tests already used an expired dataset, so
they were the first ever to see a constraint this connector put on the wire,
and they had to be re-run for that specifically rather than covered by the
aggregate count.

**4. `CN:02-07`** (2026-08-21). `DECISIONS.md` §29. The last exemption closed
and the gate reached 65 of 65 with none tracked. It also cost more than "a
single exempted test" below implies: the test was failing on a config gap
unrelated to termination, and finding that meant decompiling the pinned image's
own test methods (§29.2).

More shipped that this document did not plan, listed because a sequence
document that omits shipped work stops being usable as one.

**Roster signing and `did:web` resolution** (2026-08-20, `DECISIONS.md` §27).
Milestone 1 left the roster trusted only because it sat on the right disk. §27
signs it and adds a real resolver. It belongs on this list rather than inside
milestone 1: that milestone's own spec named it as the prerequisite for
distributing a roster at all, and deferred it.

**Transfer `Range` and resumption** (2026-08-21, `DECISIONS.md` §31). An
interrupted pull resumes instead of starting over. Verified by a second `make
demo` round rather than by the TCK — the same "no test moves a byte" finding
that shaped milestone 2, arriving a second time.

**Exchange authorization** (2026-08-23, `DECISIONS.md` §32). Authentication
settled *that* a caller is admitted; this settled *which* exchange an admitted
caller may act on. It could not have come earlier — it compares an inbound
request against a verified identity, and there was no verified identity to
compare against until milestone 1 — which is the same "do the step that makes
the next one safe, first" argument arriving at its own consequence.

**Its verification situation is milestone 1's, not milestone 2's, and that is
worth being exact about.** No TCK test sends a message about another
participant's exchange, so the suite never sees a refusal: `make tck` is a
regression gate, and the evidence is unit tests — milestone 3's framing,
milestone 1's shape ("unauthenticated rejection is the half the TCK cannot
show"). What it is not is milestone 2's situation, which needed a new harness
built as part of the milestone; this one needed none, because a suite that
already exists covers the pass side.

That pass-side coverage is uneven, though, which is worth saying rather than
rounding off. The five exchange checks and the refusal to serve as provider
under a consumer-role agreement are exercised on every run. The agreement's
counterparty comparison is not exercised at all: the twelve agreements
`test/tck/run.sh` seeds carry no owner, so the suite only ever takes that
check's empty-permitted branch, and `make demo` is the only thing that puts a
matching non-empty counterparty through it.

The regression risk was concrete enough to specify before writing: a check
placed on the wrong side of the transfer lookup's consumer branch compiles,
reads correctly, and silently refuses all fifteen `TP_C` results.

**Authorizing the initiate hooks** (2026-08-24, `DECISIONS.md` §35). The
section below is this milestone, and it is done. Both hooks moved off the DSP
listener onto the management listener, so their caller is the operator rather
than any roster participant; an initiate call may only name a participant the
roster lists, wherever authentication is on, so there is a roster to consult;
and `refuseIfNotParty` runs at every consumer-role resolver rather than at the
provider-role ones alone, which is the half §32 recorded as deliberately open.

**Its verification situation is the one this document could not predict, and
the prediction it did make was wrong in a useful way.** The section below
calls the TCK "worse than neutral" — a constraint on the design rather than
evidence for it — because a roster check written the obvious way loses most of
the consumer-role results. That cost was real, and it was never a property of
roster validation: it was the price of the harness authenticating under one
name while hardcoding another as the `providerId` it sends. Correcting the
harness's own identity dissolves it, and `make tck` stays at 65 of 65 (§35.4).
So this milestone is the first where the TCK turned out to be *better* than
the forecast rather than worse.

What the section below got right is the other half of that paragraph. Neither
harness can show a refusal, so every refusal this milestone adds rests on unit
tests — milestone 1's shape, arriving again. And the pass side is no longer
green by accident: the harness now presents the identity it claims, and
`test/tck/run.sh` reads the recorded counterparty back through the management
API's `GET /transfers` and fails the run unless a **consumer-role** row names
the participant the TCK sends.

The role anchor is that check rather than a detail of it. `GET /transfers`
returns provider-role rows too, and a provider-role counterparty comes from
the verified issuer — the same name in this harness — so a match on the
counterparty alone would have held even with no consumer-role row written at
all, which is the one divergence the check exists for. It was mutated on
2026-08-24 and did fail, but that mutation also breaks inbound
authentication and takes the provider-role rows with it, so on its own it
does not isolate the anchor — §35.4 has the fuller account, including what
does.

**The roster as a versioned, expiring artifact** (2026-08-25, `DECISIONS.md`
§36). `roster.json` carries a revision and an expiry, both inside the
operator's signature. The expiry is enforced while the connector runs — at
load, on every inbound DSP request that requires a credential, on both
initiate hooks, and on everything this connector sends — so a superseded
roster stops verifying at a known instant even where nobody restarted. Not on
the version document, which is mounted outside the credential check, and not
on the management API's read and import routes; §36.4 draws that boundary.
The revision is a ratchet in this connector's own store: a roster older than
one it has already run is refused at startup.

**Its verification situation is the one this document had no shape for, and
it is worth adding one.** Milestone 1's shape was "the TCK carries the pass
side, unit tests carry the refusals". Milestone 2's was "no harness exists,
build one". This is neither: both harnesses exercise the pass side simply by
coming up — each writes a roster that expires a day out — and **neither can
show any refusal this milestone adds**. An implementation was mutation-tested
and `make tck` and `make demo` stayed green under every mutation, including
the ones aimed at the initiate-hook and outbound checkpoints. So `go test` is
the only gate that carries it, and that is a fact to know rather than
discover.

The version-regression refusal is further out of reach than the rest, for
reasons neither harness can fix incidentally: the TCK connector mounts no
volume for `data_dir`, so its database dies with the container, and
`demo/run.sh` clears the generated directory at the start of every run even
though the demo consumer does bind-mount `data_dir`. The demo could reach it
cheaply — a second boot with a lowered `version` — and §36.13 records why it
does not, which is a judgement rather than an impossibility.

The harness rosters expire a day out, which permanently trips the boot log's
approaching-expiry warning. Whoever reads `tck-connector.txt` should know
that line is expected.

**Clock leeway, and a lifetime the verifier measures** (2026-08-26,
`DECISIONS.md` §37). The half §36 deferred. `Verify` compares `exp` against
`now` less a minute, so an issuer whose clock lags a counterparty's costs
itself a minute rather than every request; and it refuses an `exp` sitting
more than an hour ahead of the verifier's own clock, so how long a credential
lives is something the verifier measures rather than something the minter
promises. `nbf` stayed declined. Spec:
`specs/2026-08-26-clock-leeway-token-lifetime-design.md`.

**Its verification situation is §36's, and this time it was forecast rather
than discovered.** `go test` is the only gate that carries it, because no
harness can exercise a clock difference — every container shares one host
clock — which is what §36.9 wrote down when it deferred this work. `make tck`
and `make demo` are still worth running rather than assuming, for a reason
this milestone found: `test/tck/run.sh` mints with `-ttl 30m`, so a maximum
picked without reading the harness would have turned the suite red. Thirty
minutes sits inside the hour that shipped, and neither harness script
changed.

**Discovery** (2026-08-28, `DECISIONS.md` §38). A catalog client, a
`GET /catalog` route on the management listener that triggers it, and an
optional `connector_address` on a roster entry that both initiate hooks dial
in place of the address the request names. It closes
`docs/goal-gap-analysis.md`'s P1 for the catalog protocol and the residual
§35.5 left open. This document never planned it — the note at the top says
discovery is one of the axes its ordering rule silently excluded — so there
is no forecast here to correct. Its verification situation is recorded above
instead, under "What can verify each remaining milestone", because the
finding it carries is about that section's own subject: a suite can be unable
to verify a milestone by playing the role the milestone implements. Spec:
`specs/2026-08-28-discovery-catalog-client-design.md`.

## The milestone that was next: authorizing the initiate hooks — done, `DECISIONS.md` §35

> The framing of this section was disputed and is now settled — see the note
> at the top of this document and `docs/goal-gap-analysis.md`'s second ordered
> item. What follows is the argument as it stood before that dispute, kept as
> written. Its closing instruction — settle, before starting, what an
> initiate call may name when the roster does not list it — is answered
> **nothing** (§35.2). Some of its other claims did not survive either. The
> largest is its premise: "both endpoints are on the public listener" is no
> longer true of either one — §35.1 moved them to the management listener, so
> the sentence below describing where they sit describes where they sat. The
> TCK is not "worse than neutral" either (§35.4). And the `counterparty_id`
> asymmetry it says must be explained in several places is no longer an
> asymmetry in *authorization weight*: a consumer-role counterparty now comes
> from an authenticated operator and names a roster participant, so both roles'
> counterparties are something an inbound message is compared against
> (§35.3). The asymmetry in **provenance** is still there and is still
> explained on purpose — `refuseIfNotParty` in
> `internal/dsp/auth_middleware.go`, the `CounterpartyID` doc comments on
> `store.ConsumerNegotiation` and `store.ConsumerTransfer` in
> `internal/store/store.go`, `transferHandler.lookup`'s preserved placement
> warning in `internal/dsp/transfer_handler.go`, and
> `internal/dsp/transfer_consumer_handler.go` — because a provider-role
> counterparty is a verified issuer while a consumer-role one stays an
> operator's assertion. §35.5 used to sharpen that with a roster name not
> necessarily being the participant at `connectorAddress`; §38 closed it —
> the address comes from the signed roster now — so what remains of the
> asymmetry is the provenance of the *name*, and that is enough to keep the
> comments above worth reading.

**Every milestone above reads done, and until this section this document had no
forward entry at all** — which is the exact failure a sequencing document exists
to prevent. The design spec behind §32 names it as owed and §32.3 says so
again, so it is recorded here with the ordering argument that is this
document's whole job.

**The milestone.** `POST /negotiations/initiate` and `POST /transfers/initiate`
are the two endpoints §32.3 deliberately left unauthorized. Both take
`providerId` from the request body, record it as the exchange's counterparty,
and hand it to `mintOutboundCredential` as the audience of a token this
connector signs — sent to a `connectorAddress` the same caller chose. Neither
value is checked against the roster, and both endpoints are on the public
listener. `docs/follow-ups.md` carries the two consequences in full: an
impersonation primitive against a third participant, and consumer-role inbound
messages that cannot be authorized at all, because a row whose counterparty was
never verified is not something to compare an inbound request against.

**Why it goes after §32 rather than inside it.** §32 closed five exchange
resolvers and an agreement check with comparisons against data already on disk
— no wire change, no contract change, each step independently shippable. This
one cannot be built that way: it has to decide what an initiate call is
*allowed to say*, which changes the endpoint's contract rather than adding a
check behind it. Bundling the two would have produced a milestone whose steps
could not ship separately, and it would have put a compatibility argument in
front of six checks that needed none. This is the same rule that put
authentication before the data plane and §32 after authentication: do the step
that makes the next one safe, first.

**Why it should not wait long.** §32 spent a whole milestone making a verified
identity load-bearing. The initiate hooks are now the one place a caller can
still supply an *un*verified identity and have this connector act on it, so
every future authorization decision built on `counterparty_id` inherits a value
that is trustworthy in one role and not in the other. That asymmetry already
has to be explained in seven places — `refuseIfNotParty`, both `lookup` doc
comments, `handleTransferInitiate`, the `CounterpartyID` doc comments on
`store.ConsumerNegotiation` and `store.ConsumerTransfer`, and §32.3 — and every
new reader of that column has to learn it before touching anything.

**What can verify it, and this is the finding to specify against.** The TCK is
worse than neutral here: it is a constraint on the design rather than evidence
for it. The harness authenticates as `urn:participant:tck` while hardcoding
`TCK_PARTICIPANT` as the `providerId` it sends, so a roster check written the
obvious way loses all fifteen `TP_C` results and all but one of `CN_C` — the
same 35-of-65 outcome §32 measured before writing a line. Evidence therefore
comes from unit tests plus `make demo`, the only harness where the two names
agree, and even there it agrees by fixture rather than by rule. **Settle
before starting:** what an initiate call may name when the roster does not
list it. Refusing outright is what breaks the harness, so that question is the
milestone, not a detail inside it.

What follows is the original argument, kept as written.

## Where this stood when this was written

The connector speaks the DSP 2025-1 control plane in both roles and the
official TCK reports 64 of 65 with every suite gated. That is a real result
and it is also a narrow one. Three things a dataspace needs are absent, and
none of them is a thing the TCK checks:

- **No data plane.** No route serves bytes. `dataAddress` appears only in
  comments. The connector negotiates the right to transfer data and then does
  not transfer it.
- **No connector-to-connector authentication.** `jwt` and `roster` match
  nothing in the source. Every DSP endpoint is anonymous, so any caller can
  negotiate a contract and start a transfer. `DECISIONS.md` §9 and §10 record
  the intended design — did:web plus an operator-signed static roster,
  self-signed JWTs with a five-minute expiry — as a decision, not as code.
- **No expressible policy.** `carriesConstraint` refuses any permission
  carrying a constraint. That is the honest reading of "never accept a
  constraint that is not enforced", and it means the only contract terms this
  connector can agree to are no terms at all.

A green TCK coexists with all three because the suites verify the control
plane and nothing else — the transfer suites do not move a byte, and no suite
sends a credential or an ODRL constraint that has to be evaluated.

## What can verify each remaining milestone

This is the finding that orders the work.

**Connector auth stays TCK-verifiable.** `DspSystemLauncher` reads
`dataspacetck.dsp.connector.http.headers.authorization` and registers it as an
interceptor on every request the TCK makes:

```sh
javap -p -c -classpath tckx org.eclipse.dataspacetck.dsp.system.DspSystemLauncher |
  grep -E 'headers.authorization|registerAuthorizationInterceptor'
```

So the harness can present a credential, and the full 65-test suite remains
the regression check while authentication is added. Unauthenticated rejection
is the half the TCK cannot show — it has no test that omits the header — so
that half is unit tests.

**The data plane is verifiable by nothing that exists.** No TCK test sends,
receives, or asserts a byte, in either transfer suite. Phase B is therefore
the first milestone whose correctness cannot lean on the TCK at all, and it
has to bring its own end-to-end harness. That is a cost worth naming before
starting rather than discovering halfway.

**Policy evaluation is TCK-neutral.** The CN suites negotiate unconstrained
offers, so adding constraint evaluation must leave them green; the suite is a
guard against regression, not evidence the new code works. Evidence comes
from unit tests.

**Discovery is the second milestone the TCK cannot verify, and the reason is
not the data plane's.** The data plane was out of reach because no test in
either transfer suite asserts a byte. Discovery is out of reach because the
`CAT` suite plays the consumer — that is the role this milestone implements,
and the pinned image carries no consumer-role catalog test, confirmed by
listing the runtime jar. So the suite exercises the half that was already
there and cannot see the half being added: `make tck` is a regression check,
not evidence. Unlike the data plane, discovery needs no new harness — `make
demo` already stands both connectors up, and its negotiate rounds now obtain
their offer identifiers by asking, which makes the client load-bearing rather
than demonstrated. But `make demo` is not in CI: the pipeline runs `go vet`,
the race suite, and the TCK. So the demo's evidence is only as good as
someone running it, and `go test` is what carries this milestone unattended.
That is a finding about the pipeline rather than about the milestone, and
ordered item 5 of `docs/goal-gap-analysis.md` owns it.

## The order

### 1. Connector authentication — done, `DECISIONS.md` §§9-10, 27

First, for three reasons that point the same way.

It is the only remaining milestone the TCK can still verify, and doing it
while that is true is worth more than doing it later.

The surface is at its smallest and most uniform right now: fourteen DSP
routes that all mean the same thing, "a counterparty is talking to me". After
a data plane exists there is a fifteenth that means something different — "a
counterparty is fetching bytes" — with its own credential lifetime question.
Adding auth across a uniform surface and then extending it to one new case is
smaller than designing it across two kinds of surface at once.

And the ordering is not merely convenient. Phase B makes `STARTED` the state
that authorizes a pull. A counterparty that can reach `STARTED` anonymously
would be authorizing itself, which turns the transfer state machine into
decoration. Building the data plane first means shipping, however briefly, a
server that hands out bytes to anyone who asks for them.

**Measured, and it fits.** The TCK sends a *static* string, and §10 specifies a
five-minute expiry, so the two are compatible only if a minted token outlives
the run. The suite takes **54 seconds** — first to last timestamped line of a
real run:

```sh
grep -oE "^\[[^]]+\]" tck-output.txt | sed -n '1p;$p'
```

Against a 300-second expiry that is comfortable, so §10 stands as written and
the harness needs no special credential.

One placement detail decides it, though. Mint the token **after** the
connector is up and the agreements are seeded — where `run.sh` already is at
that point — rather than before `docker compose`. A cold image build ahead of
the suite can take minutes, and a token minted before it would be spending its
expiry on the build. Minted at the seeding step, the whole five minutes is
available to a fifty-four-second suite.

> **Inverted by §35.4, and this paragraph is now the wrong instruction to
> follow.** `test/tck/run.sh` mints before `docker compose up`, because the
> same string has to reach the connector as `DSBOX_MGMT_TOKEN` before it
> starts. The build-time objection above was real and is paid for with
> `-ttl 30m` instead of §10's five minutes — a relaxation of what the harness
> mints for itself, not of `credentialTTL`. The claim in the paragraph before
> it, that "the harness needs no special credential", went the same way.

Still open: whether outbound authentication ships in the same milestone.

The TCK's mock endpoints do not verify what this connector sends, so that half
is untestable by the harness and rests on unit tests either way.

### 2. The data plane (Phase B, HTTP-PULL) — done, `DECISIONS.md` §§25-26, 31

Second, because it is the milestone that turns a protocol implementation into
something that does the thing a dataspace is for — and because it is much
harder to verify, so it should not also be carrying an unfinished auth story.

It needs an end-to-end harness this project does not have: two connectors, or
one connector and a client that negotiates, transfers, and fetches. Building
that harness is part of the milestone, not a prerequisite someone can skip.

**Settle before starting.** `docs/follow-ups.md` already records the conflict:
a `transfer_policies` sequence like `[STARTED, COMPLETED]` completes two
hundred milliseconds after starting, which is harmless while nothing is served
and cuts access before any pull once something is. Autonomous completion has
to be bounded by something the data plane knows, or terminal steps have to
stop being configurable — that is a design decision, and it belongs in this
milestone's spec rather than being discovered by a test that fetches nothing.

Also settle what a `dataAddress` contains and how long its authorization
lives, which is where milestone 1's credential design gets extended rather
than reinvented.

### 3. Policy constraints — done, `DECISIONS.md` §26

Third. It can technically be built at any point, because constraint
evaluation happens during negotiation and is observable without a data plane.
It goes here because its *value* is not: enforcing a validity period or a
spatial restriction matters when it gates access to something, and until
milestone 2 there is nothing to gate.

Doing it third also means the enforcement point already exists, so "evaluated"
and "enforced" can be the same change rather than two milestones apart — which
is what `CLAUDE.md`'s rule actually asks for.

### 4. `CN:02-07` — done, `DECISIONS.md` §29

Last, and optional. It is a single exempted test, tracked with its reason in
`cmd/tckgate/main.go` and `docs/follow-ups.md`. Closing it takes the gate to
65 of 65 and changes nothing else. It is worth doing when the 65 matters —
for an external claim, say — and not before.

## The order that was rejected

Data plane first, auth second, on the reasoning that the data plane is the
product and auth is infrastructure.

Rejected because it inverts the risk. The data plane is the milestone with no
external verification, and starting it while authentication is still absent
means the first version that serves bytes serves them to anyone — with the
transfer state machine, the thing that is supposed to authorize the serve,
carrying no weight. The window would be closed later, but "later" is exactly
when a demo happens.

## What this does not decide

Whether any of this constitutes certification. The TCK result is a
reproducible local fact about a pinned image. Whether DSP 2025-1 has a formal
certification or attestation process, and what it would require beyond a green
suite, has not been checked and is not claimed here.
