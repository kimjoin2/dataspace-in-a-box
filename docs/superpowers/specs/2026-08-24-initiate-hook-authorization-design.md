# Authorizing the initiate hooks

**Status:** design, agreed 2026-08-24, revised the same day after three
cross-checks — one of which applied this spec to a throwaway copy of the
repository and ran the suite. What that measured is recorded in §11.

**Supersedes** the forward-looking section of `docs/milestone-sequence.md`,
which framed this milestone around a different question — see §1.3.

**What it closes:** both consequences `docs/follow-ups.md` records as deferred
by §32.3, including the entry that file calls its highest-severity item.

---

## 1. The finding this milestone acts on

### 1.1 What is wrong today

`POST /2025-1/negotiations/initiate` and `POST /2025-1/transfers/initiate` sit
on the public DSP listener behind `requireParticipant`. Any roster participant
may call them; with `require_auth: false`, anyone may.

Both take `providerId` from the request body, record it as the exchange's
counterparty, and it becomes the audience of every credential this connector
mints for that exchange — delivered to the `connectorAddress` the same caller
chose. So a caller names a victim as `providerId`, points `connectorAddress`
at itself, and collects a credential this connector signed naming that victim
as its audience. §28 declined replay defense and `auth`'s claims struct
carries no `jti`, so that token can be presented to the victim repeatedly. It
needs no agreement, no negotiation state, and no second request.

The second consequence is quieter. Because a consumer-role row's counterparty
is a string the caller chose, it is not an identity, and comparing an inbound
message's verified issuer against it is not authorization. §32.3 therefore
names `handleOffers` and `handleAgreement` as deliberately unguarded, and
places `transferHandler.lookup`'s comparison after its consumer branch has
already returned. A roster participant that learns one of this connector's
consumer pids can push an offer or an agreement into that negotiation.

**§32.3's list is shorter than the set of resolvers.** §5.1 has the measured
list, which is twice as long, and closing only the resolvers §32.3 happens to
name would be the worst outcome available — see §5.4.

### 1.2 The two are one defect seen from two sides

Both exist because `counterparty_id` means something different in each role.
A provider-role row takes it from a verified issuer. A consumer-role row takes
it from an initiate body.

`docs/milestone-sequence.md` says that asymmetry has to be explained in seven
places and enumerates them: `refuseIfNotParty`, both `lookup` doc comments,
`handleTransferInitiate`, the `CounterpartyID` doc comments on
`store.ConsumerNegotiation` and `store.ConsumerTransfer`, and §32.3. That list
is accurate — `handleInitiate`'s doc comment is **not** on it and does not
belong on it, because it says nothing about listeners, ownership, or the
asymmetry. §7 depends on getting this right and states it separately.

§32.3 named the fix and deferred it: the consumer role's inbound messages
"needs `providerId` validated against the roster at initiate time — a separate
change with its own compatibility question". This milestone is that change,
plus the one §32.3 did not name.

### 1.3 The dispute this settles, and where it is not followed

`docs/milestone-sequence.md` frames the milestone as **what an initiate call
may name when the roster does not list it**, and calls the TCK "worse than
neutral" because a roster check written the obvious way costs 30 of the 65
results.

`docs/goal-gap-analysis.md` disputes the framing: the prior question is "who
may call them at all — that is, which listener they belong on".

The gap analysis is right about the framing, and this design adopts it. Moving
the hooks to the management listener makes the caller the operator, and that
is what removes the impersonation primitive — the primitive requires an
untrusted caller.

**But the gap analysis also concludes that the move makes validating
`providerId` "unnecessary rather than difficult", and this design does not
follow it there.** §4 validates anyway, for a reason that is diagnostic rather
than security: without it, an operator who names a participant this connector
cannot verify gets a successful initiate and then every inbound message on
that exchange refused by §5, with a log line as the only clue. The check turns
a failure that surfaces minutes later in someone else's subsystem into a 400
at the point of the mistake. §9 records what that costs.

The 30-result figure both prior documents measured is real, and it dissolves
for a reason neither anticipated. See §6.2.

---

## 2. The decision, in one paragraph

Both initiate hooks move to the management listener, behind its existing
shared-token check. On the DSP listener the two routes are removed. Both hooks
refuse a `providerId` the roster does not list, through a predicate the router
supplies. With the counterparty now supplied by an authenticated operator,
`refuseIfNotParty` is applied to every consumer-role resolver — six, not the
three §32.3 enumerates. The TCK harness reaches the moved endpoints with the
one credential it can express, and its own participant identity is corrected
to the name it already uses in its message bodies.

---

## 3. The move

### 3.1 Why the management listener is the answer

An initiate call is not a DSP message. It carries no `@context`, no `@type`,
and no `dspace:` anything; it is a plain four-key JSON object that says
"start an exchange on my behalf". `handleTransferInitiate`'s doc comment calls
it "the TCK-shaped hook, not a management feature", and the negotiation
consumer design spec has a heading saying the same. That was true as a
statement about what the DSP specification standardises and misleading as a
statement about this connector: whatever the specification declines to
standardise, an endpoint that tells this connector to go negotiate with
somebody is an operator action, and this connector already has a listener for
operator actions.

Putting it there is not a mitigation of the impersonation primitive. It is
its removal.

### 3.2 Route names

`POST /negotiations/initiate` and `POST /transfers/initiate` on the
management listener — the same final segments, no `/2025-1` prefix, because
the management listener carries no protocol version on any route.

The verb is kept deliberately. §25.3 bounded the management API with a rule
rather than with code — "this is not the start of a general management CRUD
surface" — and `initiate` is a trigger, which is easier to hold that line
against than a resource-creating `POST /negotiations` would be.

**Pattern coexistence was measured, not assumed.** Against a real `ServeMux`
carrying both the new routes and the existing `GET /transfers`:
`POST /transfers/initiate` and `GET /transfers` both resolve correctly;
`POST /transfers` and `GET /transfers/initiate` answer 405 with an accurate
`Allow`; and `POST /transfers/initiate/` — with the trailing slash — answers
404, which is what the DSP mux does today for the same input.

### 3.3 The old paths are removed, and they do not answer 404

No 410, no explanatory body: there is no release, no tag, and `README.md`
mentions neither endpoint, so there is no reader to redirect.

**What they actually answer is 405, and that is worth knowing.** Removing the
two `HandleFunc` registrations leaves `POST /2025-1/negotiations/initiate`
matching `GET /2025-1/negotiations/{id}` with `id` = `initiate`, so the mux
answers 405 with `Allow: GET, HEAD`. The same holds for the transfer path.
This matters because of how the TCK treats status codes (§6.1): a 404 fails
immediately, while a 405 is retried three times first. A `config.properties`
left pointing at an old path therefore produces a slow, confusing failure
rather than a fast one. Nothing is added to change that — it is recorded so
that whoever meets it recognises it.

### 3.4 Wiring

The handlers are unexported methods on `dsp.negotiationHandler` and
`dsp.transferHandler` and use package-private machinery
(`validateOutgoingCallback`, `writeError`, and the outbound clients that
attach a minted credential). They cannot move to `internal/mgmt` as code.

Nothing needs exporting: `http.HandlerFunc(neg.handleInitiate)` is a legal
method value on an unexported type and is assignable to an exported
`http.Handler` field. `dsp.NewRouter` returns the two handlers alongside what
it already returns, and `cmd/dsbox` passes them into `mgmt.NewRouter`.

**This is a layering choice, not cycle avoidance.** Nothing imports
`internal/dsp` except `cmd/dsbox`, so `mgmt` could import it without a cycle.
The reason to route through `main` is that `main` already mediates the roster
and the signing key, and `mgmt` having no opinion about the protocol package
is worth keeping.

**`NewRouter` has two return statements, and both must populate the new
fields.** The first is the `!cfg.AuthRequired()` path. Populating only the
second ships nil handlers in development mode, and nothing catches it: the
management listener wraps them in `authenticated`, which is non-nil, so
registration succeeds and the panic arrives at request time — after the token
check passes — as a connection reset with no error document. The
route-coverage test of this section does not catch it either, because a nil
handler still answers 401 to an unauthenticated request. Neither harness runs
with authentication off, so CI would be green. This was measured on a
throwaway copy; see §11.

**The handler values must be the ones the DSP router already built, not
second instances.** From the two initiate entry points, `transferHandler`'s
`cfg`, `store`, and `stepDelay` are reachable and `pulling`, `pulls`, and
`pullCtx` are not — the last three are touched only from `applyTransition`'s
inbound-start path. So a second instance would not panic on those. It would
do something quieter: a zero `cfg` makes the callback address a bare
`/2025-1`, and a zero `stepDelay` removes the pause between configured
consumer steps that `transfer_handler.go` argues for at length. (That pause is
reachable from the initiate path in principle — a policy keyed `after:
REQUESTED` fires there — but only from its second step onward, and no fixture
in either harness has a second step, so it is not exercised there today.)
Beyond that, `pulling` is a `sync.Map` whose purpose is to be shared. Pass the
values the router built.

`NewRouter` has five call sites, one production and four in-package tests.
Its return list is already three values with a paragraph of doc comment
explaining two of them; two more bare `http.Handler` returns would leave a
reader nothing to tell them apart by. It returns a named struct instead.
`mgmt.NewRouter` has three call sites, all in its own test file, and every one
must be updated when its signature changes.

**A guard is lost and must be replaced.** `auth_middleware_test.go` parses
`router.go` as text, extracts every `mux.HandleFunc` pattern, and asserts each
is behind authentication. That is the repository's proof that no DSP route is
accidentally anonymous. Removing two routes still satisfies every assertion in
that test — its floor and its calls-versus-routes equality both still hold —
so **nothing in it needs editing**, and the two routes simply leave its reach.
This milestone adds the equivalent assertion on the management side: every
route that listener serves except `/health` refuses an unauthenticated
request with 401 and a `WWW-Authenticate` challenge. The management router
registers with a mix of `mux.HandleFunc` and `mux.Handle`, so the new test
parses both forms or it proves less than it appears to.

---

## 4. `providerId` must name a roster participant

### 4.1 The rule

Both hooks reject a `providerId` the roster does not list, with 400.

This answers the sequence document's question — *what may an initiate call
name when the roster does not list it?* — with **nothing**. The argument is
this repository's own rule, "never accept a constraint that is not enforced",
applied to identity: a name the roster does not carry is a name this connector
cannot authenticate a message from and cannot meaningfully address a
credential to. After §3 the only caller who can be refused is the operator,
who can read the error.

### 4.2 How the check reaches the handlers: a predicate, not a roster

Neither handler struct carries a roster, and the roster exists in `dsp` only
as a `NewRouter` parameter. The check therefore needs new plumbing, and the
shape of it decides how much of this milestone is fixture repair.

**Both structs gain a `knownParticipant func(string) bool` field**, set by
`NewRouter` from the roster on the authenticated path and left nil on the
other. The handlers skip the check when it is nil.

A `roster auth.Roster` field would work and is worse in three measurable ways.
A zero `auth.Roster` answers "not a participant" for every id, so it fails
**closed** where the default must be open: every one of the handler-struct
literals in the test files leaves new fields zero, and those tests construct
`config.Config{}`, whose `AuthRequired()` is true by default — so gating the
check on `AuthRequired()` does not spare them. Four initiate tests would start
failing for a reason unrelated to what they test. A nil predicate, by
contrast, is the check being *absent*, which is both the correct default and
the same thing `NewRouter` already says about a disabled check: "a disabled
check is absent, not silently true." Second, a test that wants the check must
otherwise build a signed roster file on disk, because `LoadRoster` is
`auth.Roster`'s only constructor — roughly thirty lines per test, against a
two-line closure. Third, it needs no new exported API on `internal/auth`,
where the only lookup method returns a key rather than a boolean.

A package-level variable armed in `NewRouter` — the shape
`mintOutboundCredential` uses — is rejected: several tests in this package run
in parallel and CI runs with `-race`, so a test that swapped a global would
race with one that did not.

### 4.3 The check goes last

In both handlers the roster check goes **after** every existing validation:
required fields, `validateOutgoingCallback`, and — in the transfer hook — the
agreement lookup.

This is a unit-test constraint, not a protocol one. Every branch involved
answers 400, so no ordering changes anything the TCK sees. But two existing
tests assert only a status code: one pins that an unknown agreement is
refused, the other that an unsendable address is. Placed earlier, the roster
check answers those requests first, both tests stay green, and neither tests
its own rule any more. Placing the check last preserves them — and this
milestone also strengthens both to assert the reason they are about, so the
next reordering cannot silently void them.

### 4.4 What the refusal says

The 400 names the rejected `providerId` and says it is not a participant the
roster lists.

**This is a deliberate departure from the endpoint's other refusal**, and the
difference has to be stated or it reads as an oversight.
`validateOutgoingCallback`'s reason is withheld — logged, never echoed —
because it reports which address a hostname resolved to, which would make the
endpoint a name-resolution oracle for the network this connector sits on. That
argument is about what the *connector* learned and the caller did not. A
roster refusal echoes back only a string the caller just sent, and tells them
one bit they could learn by trying any other name. There is nothing to
disclose, and an operator debugging a typo needs to see which name was
rejected.

### 4.5 What this check does not establish

Being in the roster is not the same as being the participant at
`connectorAddress`. An operator who names a roster participant but points the
address at a different connector still gets a row whose counterparty is
wrong, and after §5 every inbound message on that exchange is refused with
only a log line to explain it. This narrows the hole; it does not close it.
Recorded as an accepted trade-off in this document's §9.

---

## 5. Every consumer-role resolver is guarded

### 5.1 There are six, and §32.3 names three

With §3 and §4 in place, a consumer-role row's counterparty is supplied by an
authenticated operator and constrained to a roster participant. That is what
§32.3 required, so `refuseIfNotParty` — unchanged — is applied at every point
that resolves a consumer-role row from an inbound DSP request. Measured
2026-08-24, there are six:

| resolver | file | `stored` |
|---|---|---|
| `handleOffers` | `negotiation_consumer_handler.go` | `n.CounterpartyID` |
| `handleAgreement` | `negotiation_consumer_handler.go` | `n.CounterpartyID` |
| `handleConsumerFinalizedEvent` | `negotiation_consumer_handler.go` | `n.CounterpartyID` |
| `handleConsumerTermination` | `negotiation_consumer_handler.go` | `n.CounterpartyID` |
| `handleGetNegotiation`'s consumer branch | `negotiation_handler.go` | `cn.CounterpartyID` |
| `transferHandler.lookup`'s consumer branch | `transfer_handler.go` | `c.CounterpartyID` |

The first two are the ones §32.3 enumerates. The next three were missed by
every prior reading because they are reached by role dispatch rather than by a
`lookup` helper: `handleEvent` and `handleTermination` each try the provider
table, then the consumer table, and hand off to a consumer-role branch —
whose provider-role sibling *does* carry the check. `handleGetNegotiation` has
the check on its provider branch and not on its consumer branch, in the same
function.

The error type is `ContractNegotiationErrorType` at the five negotiation
points and `TransferErrorType` at the transfer one, matching what each
function already emits.

### 5.2 The placement trap in `transferHandler.lookup`

Its doc comment already warns about this and the warning must be preserved in
rewritten form rather than deleted with the obsolete part.
`resolvedTransfer` carries `CounterpartyID` for consumer rows too, so a check
written against the value `lookup` returns — or hoisted above the branch
split — applies the provider-role comparison to consumer rows. That happens to
be right after this milestone and would have been catastrophically wrong
before it, which is exactly why the next reader needs to know the placement is
deliberate rather than incidental.

### 5.3 No empty-counterparty clause

`counterparty_id` was added by `ALTER TABLE ... ADD COLUMN ... DEFAULT ''`, so
consumer-role rows written before §27 carry an empty string.
`refuseIfNotParty` has no empty-stored clause, deliberately, and this
milestone does not add one: an empty counterparty means the row predates
verification, and the safe direction for a row nobody can authorize is to
refuse.

The consequence is stated rather than discovered: a deployment upgraded across
this change refuses inbound messages on consumer-role exchanges that were in
flight before the upgrade, and those exchanges must be re-initiated.

**Testing this needs no migration fixture.** Because the column appears in no
`CREATE` literal for the consumer tables, every fresh database already runs
that `ALTER`, and a row's counterparty is empty simply by leaving the field
unset. The test is an ordinary `internal/dsp` test — seed a consumer row with
an empty counterparty, present a non-empty issuer, assert 403 — not a copy of
the previous milestone's raw-SQL old-schema fixture, which exists for a
different situation and would test the wrong package here.

### 5.4 Closing three of six would be worse than closing none

If only the resolvers §32.3 names were guarded, this milestone would rewrite
`refuseIfNotParty`'s doc comment to say a consumer-role counterparty is now an
authorization anchor while three consumer-role resolvers silently did not
compare against it. Today's state is safer than that, because today the
asymmetry is documented where a reader meets it. Measured on a throwaway copy
with only the three named resolvers guarded, a roster participant that is not
the counterparty could still read a consumer-role negotiation's state,
finalize it, and terminate it. §7's rule exists for exactly this: the
documentation must not claim a property the code does not have.

### 5.5 Tests: one inversion, two repairs, and an accident worth naming

`TestTransferLookupDoesNotCheckConsumerRows` exists to pin the absence of the
check §5.1 adds. Its fixture stores a counterparty, presents a different
issuer, and asserts 200 with the message "a consumer row must not be checked".
Its premise is reversed by this milestone. It becomes the test that a
mismatched consumer row is refused, and its comment becomes the record of why
the rule changed.

`TestHandleAgreement_RecordsTheProviderAsCounterparty` and
`TestConsumerFollowUpsAreAddressedToTheCounterparty` fail because their
fixtures set a counterparty and present no issuer at all, against a config
whose `AuthRequired()` is true. They are fixture repairs, not premise
reversals.

**And a fixture accident is worth naming**, because it means the unit suite
gives this change less coverage than a green run suggests: the other
consumer-role tests survive only because their fixtures leave `CounterpartyID`
empty, so the comparison is `"" == ""`. The three new resolvers from §5.1
therefore need tests written for them; nothing existing would catch their
omission.

---

## 6. The harness

### 6.1 One credential, two listeners

The TCK registers `dataspacetck.dsp.connector.http.headers.authorization` as
a **process-wide static interceptor**, and `HttpFunctions` is the only class
in the runtime that constructs an HTTP client. Confirmed 2026-08-24 by
disassembling the digest-pinned image: both initiate clients call the
four-argument `postJson`, which falls back to that static interceptor, and no
call site anywhere uses the overload that would override it.

So the header does reach the initiate POSTs — the premise this design needed —
and the same value reaches every other endpoint. The TCK cannot express two
credentials.

Therefore one string must satisfy both listeners: `test/tck/run.sh` mints the
participant credential **before** `docker compose up`, with a long `-ttl`, and
passes it both as the TCK's authorization header and as the connector's
`mgmt_token` through the existing `DSBOX_MGMT_TOKEN` override.

**Status codes govern the harness's failure modes**, from the same
disassembly: the initiate POST asserts a 2xx, a 404 throws immediately, and
any other non-2xx is retried three times with backoff before it throws. So the
management listener must answer 2xx on both routes, and a misrouted URL
produces three retries and a confusing assertion rather than a clean failure.

*(2026-08-25: "any other non-2xx" is over-broad, and `DECISIONS.md` §35 was
written from this sentence.
`docs/superpowers/specs/2026-08-16-transfer-process-tck-wire-contract.md`
records the measured behaviour of `HttpFunctions.postJson`: retry applies to
4xx-non-404 only and only when `expectError` is false; and on the negative
paths, which pass `expectError=true`, 2xx, 3xx, 5xx, and 404 all raise while
400 and 409 pass. This paragraph's conclusion is unaffected — the misrouted-URL case
it describes answers 405, which is a 4xx — but a 5xx would raise at once
rather than being retried. `DECISIONS.md` §36.3 turns on that distinction.)*

The management listener compares that string with `subtle.ConstantTimeCompare`
and never parses it. That deserves a comment in `internal/mgmt`'s
`authenticated`, because the harness now makes the token look like a
credential: the management listener is not verifying one, it is comparing a
shared secret, and it will keep accepting the string after the credential
inside it has expired. Every other TCK request goes to the DSP listener and
does expire, so the TTL still governs the run.

**TTL.** The repository has no measured build time; it says only "minutes".
`auth.Verify` imposes no maximum lifetime. The harness uses **30 minutes**,
with the reasoning recorded where the value is: a cold `docker build` plus the
pull of the digest-pinned TCK image has to fit inside it, and the suite itself
takes under a minute.

**Two formatting facts, verified rather than assumed.** A minted token is
three base64url segments separated by dots — alphabet `A-Za-z0-9-_` — so it is
safe unquoted in a Java properties value and inside a double-quoted shell
string, and `$(...)` strips the trailing newline `dsops` prints. Separately,
the management listener's bearer helper does **not** trim whitespace while the
DSP listener's does; one string now feeds both, so a stray trailing space
would produce 401 on one listener and 200 on the other. The two helpers are
left as they are — they are deliberately unshared — and the hazard is recorded
where the harness writes the header.

### 6.2 The harness's identity is corrected

`test/tck/run.sh` mints the TCK's credential with `-iss urn:participant:tck`
and writes that id into the roster it generates. The TCK hardcodes
`TCK_PARTICIPANT` as the `providerId` in both initiate bodies — confirmed
2026-08-24 from the pinned image's `ConstantValue` attribute on
`DspConstants.TCK_PARTICIPANT_ID`, and at every call site, where javac has
inlined it. It is not configurable.

The two names differ for no reason: `urn:participant:tck` was chosen by
`run.sh`. This milestone makes them agree — the roster carries
`TCK_PARTICIPANT` and the credential is minted with `-iss TCK_PARTICIPANT`.
Both roster heredocs in `run.sh` must change together, or the signature check
fails and the connector refuses to start.

**This is a fixture correction, not a harness bend**, and three independent
observations from the same disassembly say so. The TCK never verifies an
inbound credential: only two classes mention `Authorization` and both are
outbound, and the terminal inbound handler takes the request headers as a
parameter and never reads them. It has no property naming its own participant
id. And it already calls itself `TCK_PARTICIPANT` in message payloads — its
consumer negotiation pipeline passes that constant to `createAgreement` as its
own side of the agreement.

**This is what dissolves the 30-result cost** both prior documents measured.
That number was never a property of roster validation; it was the cost of two
names for one harness.

**Two consequences to state.** After this change, agreements the CN suite
concludes carry `assignee: "TCK_PARTICIPANT"` rather than
`"urn:participant:tck"`, because the provider role fills `Assignee` from the
verified issuer. That is safe on evidence already in the repository: the
exchange-authorization design records that CN passed with a bare UUID in that
field, which is not even an IRI, so nothing asserts its value. And the
harness's roster identity becomes a function of a constant in a digest-pinned
upstream image; if that pin ever moves and the constant changes, the failure
presents as every `CN_C` and `TP_C` result failing on a refusal that reads
like a protocol bug. The roster heredoc carries a comment saying so.

### 6.3 Two edits that are easy to miss and expensive to omit

`test/tck/compose.yaml` passes exactly one environment variable through to the
connector today. **If `DSBOX_MGMT_TOKEN` is not added there**, the connector
keeps the token from its yaml, seeding still passes because it uses that same
literal, the run looks healthy through "seeded 12 transfer agreements" — and
then every initiate call gets 401, losing all of `CN_C` and `TP_C`.

The mitigation is to make the omission loud: **`mgmt_token` is removed from
`test/tck/dsbox.yaml`**, and the comment that stood above it is replaced by
one saying where the value now comes from and why — not deleted, because the
next reader will ask. An absent token rejects every authenticated request, so
a missing passthrough fails at the first seeded agreement with an explicit
message instead of silently, much later, as a suite result. This was measured:
an unset host variable lands on empty rather than on a stale literal, because
the environment override ignores an empty value.

The second edit is the seeding header in `run.sh`, which hardcodes the old
literal and must become the minted token. This one already fails loudly.

### 6.4 The demo

`demo/run.sh` calls the two hooks four times on the consumer's DSP port with a
**self-issued** operator credential — the consumer signing a token from itself
to itself — minted for exactly that purpose. Those four calls move to the
management port and the management token, which the script already uses for
`GET /agreements`, and the minting step and the comment block explaining it
are deleted.

The demo passes §4 and §5 by construction: both initiate bodies name
`urn:participant:provider`, which the demo roster lists, and the demo's
provider signs its inbound messages with `iss` equal to that same id, so every
comparison in §5.1 succeeds across both rounds.

**One coverage loss, named rather than absorbed.** That self-issued credential
is the only place in either harness where a client this repository controls
presents a participant credential to the DSP listener. After this change
nothing in `make demo` exercises that shape.

---

## 7. What becomes false

This milestone's largest surface is not code. A body of comments and documents
currently explains, correctly, why the thing this milestone does must not be
done. They are not stale comments to sweep — they are the recorded reasoning,
and this is the milestone they anticipated.

**The governing rule for this section**, adopted from the previous milestone's
experience rather than quoted from any existing document: every documentation
edit names the code fact it was checked against, and counts are left out of
comments rather than maintained in them. This spec's own first draft violated
the second half in five places, which is the evidence for the rule.

In code:

- `refuseIfNotParty`'s doc comment, whose central example is the two names
  §6.2 makes one.
- `negotiationHandler.lookup` and `transferHandler.lookup`'s doc comments —
  the second including the placement warning §5.2 preserves.
- `handleTransferInitiate`'s doc comment, which describes the public listener
  and the absence of an ownership check. **`handleInitiate`'s doc comment says
  neither of those things**; what needs rewriting in that file is the inline
  comment about the endpoint being reachable by any roster participant.
- The `CounterpartyID` doc comments on `store.ConsumerNegotiation` and
  `store.ConsumerTransfer`, and `ConsumerNegotiation.ProviderBaseURL`'s and
  `store.Agreement`'s, which name the initiate paths.
- `config.Config.MgmtToken`'s doc comment and `ConsumerPolicies`', the first
  because an absent token now disables a protocol capability rather than an
  import endpoint, the second because it names an initiate path.
- `internal/dsp/negotiation.go` and `transfer_handler.go`, where doc comments
  name the initiate paths.

In documents:

- `DECISIONS.md` §24.2, whose headline says the hook is on the public
  listener and whose "Corrected by §32" block ends by saying what §32 does not
  change is this endpoint's lack of an ownership check. Both become false.
- `DECISIONS.md` §32.3, which stays as the record of what was true then, with
  its two deferred consequences marked closed and pointing at the new section.
- **A new `DECISIONS.md` §35**, which is where this milestone's decisions land.
  §21 through §34 are all per-milestone appends and this is not an exception.
  §34.4 set a tripwire — whoever adds the third management route has to say
  why the management API is still small — and §9 is the answer that section
  demands.
- `docs/follow-ups.md`: the two entries §32.3 deferred are deleted, per that
  file's own rule. The third entry in that section survives, but the bullet
  claiming `handleTransferInitiate`'s agreement gate is decorative pending
  verified-`providerId` work becomes false and is amended.
- `docs/milestone-sequence.md`: both the forward section and the earlier note
  saying the dispute should be settled by whoever starts the work.
- `docs/goal-gap-analysis.md`, which is the only place in the repository that
  counts management routes and already carries a dated correction of that
  count.
- `SECURITY.md`, whose published-gaps section names the initiate hooks as the
  sharpest item currently open and says whoever closes it should correct that
  section. The replacement has to name what is now sharpest; the candidates
  are the forged-row follow-up, §4.5's address gap, and the absent rate
  limiting.
- `README.md`, which states the consumer-role gap as open and calls it the
  sharpest of the open items, describes the management API as two read routes
  behind a token, and reports a measurement taken with both hooks on the DSP
  listener.
- `config.example.yaml`, which documents `mgmt_token` as optional and names an
  initiate path. This file is load-guarded by a test.
- The harness comments that encode the opposite decision: the mint-after-build
  rationale in `run.sh`, the roster comment in `dsbox.yaml` naming the TCK's
  old identity, the management-port note in `compose.yaml` — whose count of
  seeding calls is already stale — the initiate-URL block in
  `config.properties`, the published-ports note in `demo/compose.yaml`, and
  the self-issued-operator block in `demo/run.sh`.

---

## 8. Evidence

**`make tck` at 65 of 65** is the evidence that §4 and §5 did not break the
harness — and after §6.2 it is a meaningful gate rather than a fixture
accident, because the harness now presents the identity it claims.

**`make demo`** is the evidence that the whole path works against a
counterparty the roster actually lists, in both roles, across an interrupted
and resumed transfer.

**Neither harness can show a refusal.** So the refusal side is unit tests,
which is the same situation §32 had: an unlisted `providerId` on both hooks,
a mismatched issuer at each of the six insertion points in §5.1, the
empty-counterparty row from §5.3, and the authentication-off case from §4.2.

**One harness fact must be made observable rather than assumed.** That the
connector receives `TCK_PARTICIPANT` as the recorded counterparty is not
visible in a run today: the initiate handlers log nothing on success,
`run.sh` never queries the transfers, and the container's database dies with
it. Since §6.2 depends on that value and a silent mismatch would present as a
protocol bug, `run.sh` gains a step that reads it back through the management
API's `GET /transfers` and fails if it is not what §6.2 expects. That endpoint
exists, and the harness is already authenticated to that listener.

---

## 9. Trade-offs accepted

**The management API takes on two write routes, and the boundary is
renegotiated for the third time.** §25.3's rule stands — a later milestone
wanting a general CRUD surface still argues for it on its own merits — but
§34.4 asked whoever came third to say why the management API is still small,
and this is that milestone. The answer: these two routes are not new
capability. They are an existing capability moved to the listener it always
belonged on, and the management API grows by nothing an operator could not
already do. What is new is that it can no longer be done by anyone else.

**The gap analysis said validating `providerId` would be unnecessary, and this
design validates it anyway.** §1.3. That single choice is the source of the
plumbing in §4.2, the ordering constraint in §4.3, and the tests in §5.5.
Accepted because the alternative failure — a wrong name accepted at initiate
and surfacing as blanket refusals later — is the kind this repository has
twice paid to diagnose.

**A roster participant is not necessarily the participant at
`connectorAddress`.** §4.5.

**The harness stops demonstrating the five-minute credential lifetime** that
§10 decided. Nothing in the connector changes — `credentialTTL` is untouched —
and the DSP listener still enforces expiry on every other request the suite
makes. What is lost is that the harness no longer exercises the value §10 set.

**The management token becomes a string the DSP listener also accepts.** In
the harness, the connector's administrative secret is exactly the credential
the TCK presents to the protocol listener. It is contained by the harness
being a closed network with one counterparty, and it is an inversion of this
milestone's own premise worth naming rather than leaving for someone to
notice.

**The harness's identity is now coupled to an upstream constant.** §6.2.

**`make demo` loses the only self-issued operator credential in either
harness.** §6.4.

**An upgraded deployment's in-flight consumer-role exchanges stop working.**
§5.3. Accepted because there are no deployments, and because the alternative
is a permit-on-empty clause that would outlive the reason for it.

---

## 10. What this does not do

It does not add replay defense. §28 declined it and this milestone does not
reopen that; what it removes is the *delivery* of a signed token to a caller
who chose its audience, which is what made the absence of replay defense
composable into an impersonation primitive.

It does not add rate limiting, which `docs/goal-gap-analysis.md` notes is
filed nowhere at all. After this change the amplifier is behind the management
token, which is what that document predicted would make rate limiting cheaper
later.

It does not put an address in the roster. Binding `connectorAddress` to a
participant would close §4.5, and it needs a schema change to a signed
artifact — which belongs with the roster milestone the gap analysis puts next,
not here.

*(2026-08-25: the roster milestone shipped as `DECISIONS.md` §36 and
deliberately did not take this. Its scope is the document's lifecycle — a
revision and an expiry, both properties of the roster as a whole — while an
address changes what an *entry* means and would make every address change a
re-signed roster and a fleet-wide restart. It moves to
`docs/goal-gap-analysis.md`'s ordered item 4, discovery, which is where
something actually consumes an address. §4.5 stays open until then.)*

It does not change the error documents the two hooks emit. They continue to
answer with this connector's existing negotiation and transfer error types,
which are **unprefixed** — `internal/dsp/transfer.go` records that a
`dspace:`-prefixed `@type` skips validation silently and then breaks JSON-LD
expansion, so nothing here goes near that. The inconsistency being accepted is
a different one: these two routes answer with JSON-LD error documents while
the rest of the management listener answers with plain text. Changing them is
churn with no reader — nothing consumes those bodies and the TCK asserts only
on the status code — so the inconsistency is recorded as a decision.

It does not start returning `validateOutgoingCallback`'s rejection reason.
§4.4 explains why the roster refusal is different rather than relaxing that
rule, and the comment that currently justifies the silence on grounds §3
invalidates is corrected without changing the behavior.

---

## 11. What the cross-checks measured

Three reviews ran against this design: one verifying every factual claim
against the tree, one attacking the design by applying it to a throwaway copy
and running the suite, and one checking it for completeness. The design
survived; this section records what did not, because the next reader should
know which parts of this document are load-bearing.

**Confirmed by disassembling the pinned TCK image**, not by grepping strings —
which matters, because macOS `strings` misreads a class file's magic number
and had silently truncated an earlier attempt: that the authorization header
reaches the initiate POSTs, that `TCK_PARTICIPANT` is the constant's value,
that it is inlined at every call site and not configurable, and that the
harness verifies no inbound credential and reads back no identity of its own.

**Found by attacking the first draft**, and fixed in this one: that §4 named
no source for the roster and that a roster-shaped field would fail closed
against every existing fixture (§4.2); that the failing-test list was short by
four, all of them §4's rather than §5's (§5.5); that the check's position
decides whether two existing tests keep testing anything (§4.3); that
`NewRouter`'s authentication-off return path would ship nil handlers whose
panic no test or harness would catch (§3.4); that the removed routes answer
405 rather than 404, which the TCK retries (§3.3); and — the finding that
changed the design rather than its details — that three consumer-role
resolvers were missing from the first draft's list of three, so the milestone
as first written would have documented a property the code did not have
(§5.1, §5.4).

**Found by checking for completeness**: that the first draft contradicted
itself twice, requiring both that `mgmt_token` be deleted from a file and that
the comment attached to it be rewritten, and that a caller-supplied string be
echoed in one refusal while the rule forbidding exactly that was preserved for
another. §6.3 and §4.4 settle them.

**And the first draft asserted numbers it could not support** — a route count
that a document in this repository had already corrected, a line count, a
count of restatements, an interval nothing measures. They are gone rather than
corrected, which is the rule §7 states.
