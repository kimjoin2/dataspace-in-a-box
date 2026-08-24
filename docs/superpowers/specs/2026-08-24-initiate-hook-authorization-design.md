# Authorizing the initiate hooks

**Status:** design, agreed 2026-08-24. Supersedes the forward-looking section
of `docs/milestone-sequence.md`, which framed this milestone around a
different question — see §1.3.

**What it closes:** both consequences `docs/follow-ups.md` records under "From
the exchange-authorization milestone", including the entry that file calls its
highest-severity item.

---

## 1. The finding this milestone acts on

### 1.1 What is wrong today

`POST /2025-1/negotiations/initiate` and `POST /2025-1/transfers/initiate` sit
on the public DSP listener behind `requireParticipant`. Any roster participant
may call them; with `require_auth: false`, anyone may.

Both take `providerId` from the request body, record it as the exchange's
counterparty, and hand it to `mintOutboundCredential` as the audience of a
token this connector signs — delivered to the `connectorAddress` the same
caller chose. So a caller names a victim as `providerId`, points
`connectorAddress` at itself, and collects a credential this connector signed
naming that victim as its audience. §28 declined replay defense and `claims`
carries no `jti`, so that token can be presented to the victim repeatedly. It
needs no agreement, no negotiation state, and no second request.

The second consequence is quieter. Because a consumer-role row's counterparty
is a string the caller chose, it is not an identity, and comparing an inbound
message's verified issuer against it is not authorization. §32.3 therefore
left `handleOffers`, `handleAgreement`, and the consumer branch of
`transferHandler.lookup` deliberately unguarded — which means a roster
participant that learns one of this connector's consumer pids can push an
offer or an agreement into that negotiation.

### 1.2 The two are one defect seen from two sides

Both exist because `counterparty_id` means something different in each role.
A provider-role row takes it from a verified issuer. A consumer-role row takes
it from an initiate body. The repository has to explain that asymmetry
everywhere a reader can meet it — `refuseIfNotParty`'s doc comment, both
`lookup` doc comments, both initiate handlers, the `CounterpartyID` doc
comments on `store.ConsumerNegotiation` and `store.ConsumerTransfer`, and
§32.3 itself — a list `docs/milestone-sequence.md` counts as seven.

§32.3 named the fix and deferred it: the consumer role's inbound messages
"need `providerId` validated against the roster at initiate time — a separate
change with its own compatibility question". This milestone is that change.

### 1.3 The dispute this settles

`docs/milestone-sequence.md` frames the milestone as **what an initiate call
may name when the roster does not list it**, and calls the TCK "worse than
neutral" because a roster check written the obvious way loses 30 of 65
results.

`docs/goal-gap-analysis.md` disputes the framing: the prior question is **who
may call these endpoints at all** — that is, which listener they belong on.

The gap analysis is right, and this design adopts its framing. The reason is
not preference. Moving the hooks to the management listener makes the caller
the operator, which is what makes validating `providerId` a *decision* rather
than a compatibility crisis: an operator naming a participant this connector
cannot verify is a configuration error to refuse, not a counterparty to
accommodate. The sequence document's question survives, but as a subordinate
one, and §4.1 answers it.

The 30-result cost the sequence document measured is real and it dissolves for
a reason neither document anticipated. See §6.2.

---

## 2. The decision, in one paragraph

Both initiate hooks move to the management listener, behind its existing
shared-token check. On the DSP listener the two routes disappear. Both hooks
refuse a `providerId` the roster does not list. With the counterparty now
supplied by an authenticated operator and constrained to a roster
participant, `refuseIfNotParty` is applied to the three consumer-role
resolvers §32.3 left open. The TCK harness reaches the moved endpoints with
the one credential it can express, and its own participant identity is
corrected to the name it already uses in its message bodies.

---

## 3. The move

### 3.1 Why the management listener is the answer

An initiate call is not a DSP message. It carries no `@context`, no `@type`,
and no `dspace:` anything; it is a plain four-key JSON object that says
"start an exchange on my behalf". The design spec for the negotiation
consumer role called it "the TCK-shaped hook, not a management feature",
which was true as a statement about the DSP specification and misleading as a
statement about this connector: whatever the specification declines to
standardise, an endpoint that tells this connector to go negotiate with
somebody is an operator action, and this connector already has a listener for
operator actions.

Putting it there is not a mitigation of the impersonation primitive. It is
its removal: the primitive requires an untrusted caller, and after the move
there is none.

### 3.2 Route names

`POST /negotiations/initiate` and `POST /transfers/initiate` on the
management listener — the same final segments, no `/2025-1` prefix, because
the management listener carries no protocol version and nothing on it does.

The verb is kept deliberately. §25.3 bounded the management API with a rule
rather than with code — "this is not the start of a general management CRUD
surface" — and `initiate` is a trigger, which is easier to hold that line
against than a resource-creating `POST /negotiations` would be.

### 3.3 The old paths simply disappear

No 410, no explanatory body. There is no release, no tag, and `README.md`
does not mention either endpoint, so there is no reader to redirect. A route
that exists only to explain a move is dead weight from the day it ships and
has to be maintained forever.

### 3.4 Wiring

The handlers are unexported methods on `dsp.negotiationHandler` and
`dsp.transferHandler` and depend on package-private machinery
(`validateOutgoingCallback`, `writeError`, `mintOutboundCredential`, the
callback pushers). They cannot move to `internal/mgmt` as code.

`dsp.NewRouter` therefore returns the two handlers alongside what it already
returns, and `cmd/dsbox` passes them into `mgmt.NewRouter`. `internal/mgmt`
takes `http.Handler` values and never imports `internal/dsp`.

**This is a layering choice, not cycle avoidance.** Nothing imports
`internal/dsp` except `cmd/dsbox`, so `mgmt` could import it without a cycle.
The reason to route through `main` is that `main` already mediates the roster
and the signing key, and `mgmt` having no opinion about the protocol package
is worth keeping.

**The handler values must be the ones the DSP router already built, not
second instances.** Measured on 2026-08-24: from the two initiate entry
points, `transferHandler.cfg`, `.store`, and `.stepDelay` are reachable
(the last via `maybeDriveConsumerTransfer` -> `driveConsumerTransfer`, which
`consumer_transfer_policies` with `after: REQUESTED` fires straight off the
initiate path), and `.pulling`, `.pulls`, `.pullCtx` are not — those three
are touched only from `applyTransition`'s inbound-start path. So a second
instance would not panic. It would do something worse: a zero `stepDelay`
silently removes the 200 ms pause `transfer_handler.go` spends thirty lines
justifying, and a zero `cfg` makes the callback address a bare `/2025-1`.
Beyond that, `pulling` is a `sync.Map` whose entire purpose is to be shared;
handing a second one to a second listener is the kind of coupling that is
correct today and wrong after the next change.

`NewRouter` has five call sites (one production, four in-package tests),
measured 2026-08-24. Its return list is already three values with a
paragraph of doc comment explaining two of them; adding two more anonymous
`http.Handler` returns would be four handlers at a call site with nothing to
tell them apart. It returns a named struct instead.

**A guard is lost and must be replaced.** `auth_middleware_test.go` parses
`router.go` as text, extracts every `mux.HandleFunc` pattern, and asserts
that each is behind authentication. That is the repository's proof that no
DSP route is accidentally anonymous. Moving two routes out of `router.go`
moves them out of that proof with nothing on the management side to replace
it. This milestone adds the equivalent assertion for the management
listener: every route it serves except `/health` is behind `authenticated`.

---

## 4. `providerId` must name a roster participant

### 4.1 The rule

Both hooks reject a `providerId` the roster does not list, with 400 and a
reason that names the value.

This answers the sequence document's question — *what may an initiate call
name when the roster does not list it?* — with **nothing**. The argument is
this repository's own rule, "never accept a constraint that is not
enforced", applied to identity: a name the roster does not carry is a name
this connector cannot authenticate a message from and cannot address a
credential to in any meaningful sense. Accepting it produces a row whose
counterparty is decorative and a signed token whose audience nobody claims.
Refusing is the honest answer, and after §3 the only caller who can be
refused is the operator, who can read the error.

### 4.2 When authentication is off, the check is absent

`cmd/dsbox` loads the roster **only** when `cfg.AuthRequired()`; the else
branch logs a warning and loads nothing. `require_auth: false` with
`dev_mode: true` and no `roster:` key is a fully valid configuration —
`config.Load` requires a roster only when authentication is on — and in it
`auth.Roster` is its zero value, whose `KeyFor` answers `false` for every id.

A check written as a plain lookup would therefore refuse **every** initiate
call on a development instance. The check is gated on `cfg.AuthRequired()`,
which is the same shape `refuseIfNotParty` already has and the same principle
`NewRouter` states when authentication is off: a disabled check is absent,
not silently true.

### 4.3 What this check does not establish

Being in the roster is not the same as being the participant at
`connectorAddress`. An operator who names a roster participant but points the
address at a different connector still gets a row whose counterparty is
wrong, and after §5 every inbound message on that exchange is refused with
only a `slog.Warn` to explain it. This narrows the hole; it does not close
it. Recorded as an accepted trade-off in §9.

---

## 5. Consumer-role inbound messages become authorizable

### 5.1 The three insertion points

With §4 in place, a consumer-role row's counterparty is supplied by an
authenticated operator and constrained to a roster participant. That is what
§32.3 required, so `refuseIfNotParty` — unchanged — is applied at:

- `handleOffers`, after the `GetConsumer` lookup and its 404, before the body
  is decoded, with `stored = n.CounterpartyID`.
- `handleAgreement`, symmetrically.
- `transferHandler.lookup`'s **consumer branch**, before it returns, with
  `stored = c.CounterpartyID`.

The third has a trap its own doc comment already warns about, and the warning
must be preserved in rewritten form rather than deleted along with the
obsolete part. `resolvedTransfer` carries `CounterpartyID` for consumer rows
too, so a check written against the value `lookup` returns — or hoisted above
the branch split — compiles, reads correctly, and applies the provider-role
comparison to consumer rows. It happens to be right after this milestone and
would have been catastrophically wrong before it, which is exactly why the
next reader needs to know the placement is deliberate.

### 5.2 No empty-counterparty clause

`counterparty_id` was added by `ALTER TABLE ... ADD COLUMN ... DEFAULT ''`,
so consumer-role rows written before §27 carry an empty string.
`refuseIfNotParty` has no empty-stored clause, deliberately, and this
milestone does not add one: an empty counterparty means the row predates
verification, and the safe direction for a row nobody can authorize is to
refuse.

The consequence is stated rather than discovered: a deployment upgraded
across this change refuses inbound messages on consumer-role exchanges that
were in flight before the upgrade, and those exchanges must be re-initiated.
Neither harness sees this — both start from an empty database — so a test
must cover the upgraded-database branch directly.

### 5.3 One test is inverted, not patched

`TestTransferLookupDoesNotCheckConsumerRows` exists to pin the absence of the
check §5.1 adds. Its fixture stores `CounterpartyID: "SOME_OTHER_NAME"`,
presents a different issuer, and asserts 200 with the message "a consumer row
must not be checked". Its premise is reversed by this milestone. It becomes
the test that a mismatched consumer row is refused, and its comment becomes
the record of why the rule changed.

Two further tests fail because their fixtures set a counterparty and present
no issuer at all, against a `config.Config{}` whose `AuthRequired()` defaults
to true: `TestHandleAgreement_RecordsTheProviderAsCounterparty` and
`TestConsumerFollowUpsAreAddressedToTheCounterparty`. They are fixture
repairs, not premise reversals.

**And a fixture accident is worth naming**, because it means the unit suite
gives this change less coverage than a green run suggests: the other
consumer-role tests survive only because their shared fixture leaves
`CounterpartyID` empty, so the comparison is `"" == ""`.

---

## 6. The harness

### 6.1 One credential, two listeners

The TCK registers `dataspacetck.dsp.connector.http.headers.authorization` as
a **process-wide static interceptor**, and `HttpFunctions` is the only class
in the runtime that constructs an HTTP client. Confirmed 2026-08-24 by
disassembling the digest-pinned image: both initiate clients call the
four-argument `postJson`, which falls back to that static interceptor, and no
call site anywhere uses the overload that would override it.

So the header does reach the initiate POSTs — the premise this design needed
— and the same value reaches every other endpoint. The TCK cannot express two
credentials.

Therefore one string must satisfy both listeners: `test/tck/run.sh` mints the
participant credential **before** `docker compose up`, with a long `-ttl`,
and passes it both as the TCK's authorization header and as the connector's
`mgmt_token` through the existing `DSBOX_MGMT_TOKEN` override.

The management listener compares that string with `subtle.ConstantTimeCompare`
and never parses it. That is worth a comment where it lands: the management
listener is not verifying a credential, it is comparing a shared secret that
happens to look like one, and it will accept the string after the credential
inside it has expired. Every other TCK request still goes to the DSP listener
and still expires, so the TTL still governs the run.

**TTL.** The repository has no measured build time; it says only "minutes".
`auth.Verify` imposes no maximum lifetime. The harness uses **30 minutes**,
with the reasoning recorded where the value is: a cold `docker build` plus the
pull of the digest-pinned TCK image has to fit inside it, and the suite itself
takes under a minute.

**Two JSON-formatting notes, verified rather than assumed.** A minted token is
`base64url . base64url . base64url` — alphabet `A-Za-z0-9-_` plus two dots —
so it is safe unquoted in a Java properties value and inside a double-quoted
shell string, and `$(...)` strips the trailing newline `dsops` prints.
Separately, the management listener's bearer helper does **not** trim
whitespace while the DSP listener's does; one string now feeds both, so a
stray trailing space would produce 401 on one listener and 200 on the other.

### 6.2 The harness's identity is corrected

`test/tck/run.sh` mints the TCK's credential with
`-iss urn:participant:tck` and writes that id into the roster it generates.
The TCK hardcodes `TCK_PARTICIPANT` as the `providerId` in both initiate
bodies — confirmed 2026-08-24 from the pinned image's `ConstantValue`
attribute on `DspConstants.TCK_PARTICIPANT_ID`, and at every call site, where
javac has inlined it as `ldc`. It is not configurable.

The two names differ for no reason: `urn:participant:tck` was chosen by
`run.sh`. This milestone makes them agree — the roster carries
`TCK_PARTICIPANT` and the credential is minted with `-iss TCK_PARTICIPANT`.

**This is a fixture correction, not a harness bend**, and three independent
observations from the same disassembly say so. The TCK never verifies an
inbound credential: only two classes mention `Authorization` and both are
outbound, and the terminal inbound handler takes the request headers as a
parameter and never reads them. It has no property naming its own participant
id. And it already calls itself `TCK_PARTICIPANT` in message payloads — its
consumer negotiation pipeline passes that constant to `createAgreement` as
its own side of the agreement. Making the authenticated identity agree with
the claimed one is the correction.

**This is what dissolves the 30-result cost** `docs/milestone-sequence.md` and
§32.3 both measured. That number was never a property of roster validation;
it was the cost of two names for one harness.

**Two consequences to state.** After this change, agreements the CN suite
concludes carry `assignee: "TCK_PARTICIPANT"` rather than
`"urn:participant:tck"`, because the provider role fills `Assignee` from the
verified issuer. The TCK does not validate that field — CN passes 15 of 15
today with the other value — and `TCK_PARTICIPANT` is the more accurate
string, but it is a change to what goes on the wire and is named here rather
than discovered in a diff. And the harness's roster identity becomes a
function of a constant in a digest-pinned upstream image; if that pin ever
moves and the constant changes, the failure presents as every `CN_C` and
`TP_C` result failing on a refusal that reads like a protocol bug. The roster heredoc carries a
comment saying so.

### 6.3 Two edits that are easy to miss and expensive to omit

`test/tck/compose.yaml` passes exactly one environment variable through to
the connector today. **If `DSBOX_MGMT_TOKEN` is not added there**, the
connector keeps the token from its yaml, seeding still passes because it uses
that same literal, the run looks healthy through "seeded 12 transfer
agreements" — and then every initiate call gets 401, losing all of `CN_C` and
`TP_C`. The gate fails on counts, forty seconds after the last thing that
looked fine.

The mitigation is to make the omission loud: **`mgmt_token` is removed from
`test/tck/dsbox.yaml` entirely.** An absent token rejects every request, so a
missing passthrough fails at the first seeded agreement with an explicit
message instead of silently thirty-one tests later.

The second edit is the seeding header in `run.sh`, which hardcodes the old
literal and must become the minted token. This one already fails loudly, by
design of the block around it.

### 6.4 The demo

`demo/run.sh` calls the two hooks four times on the consumer's DSP port with
a **self-issued** operator credential — the consumer signing a token from
itself to itself — minted for exactly that purpose. Those four calls move to
the management port and the management token, which the script already uses
for `GET /agreements`, and the minting step and the comment block explaining
it are deleted.

The demo passes §4 and §5 by construction: both initiate bodies name
`urn:participant:provider`, which the demo roster lists, and the demo's
provider signs its inbound messages with `iss` equal to that same id, so the
comparison in §5.1 succeeds on every message of both rounds.

**One coverage loss, named rather than absorbed.** That self-issued
credential is the only place in either harness where a client this repository
controls presents a participant credential to the DSP listener. After this
change nothing in `make demo` exercises that shape.

---

## 7. What becomes false

This milestone's largest surface is not code. A body of in-code comments and
documents currently explains, correctly, why the thing this milestone does
must not be done. They are not stale comments to sweep — they are the
recorded reasoning, and this is the milestone they anticipated. Each is
rewritten to say what is now true and, where the argument is worth keeping,
why it changed:

- `refuseIfNotParty`'s doc comment, whose central example is the two names
  §6.2 makes one.
- `negotiationHandler.lookup` and `transferHandler.lookup`'s doc comments —
  the second including the placement warning §5.1 preserves.
- `handleInitiate`'s and `handleTransferInitiate`'s doc comments, both of
  which describe the public listener and the absence of an ownership check.
- The `CounterpartyID` doc comments on `store.ConsumerNegotiation` and
  `store.ConsumerTransfer`.
- `DECISIONS.md` §32.3, which stays as the record of what was true then, with
  its two deferred consequences marked closed and pointing here.
- `docs/follow-ups.md`: both entries are deleted, per that file's own rule.
- `docs/milestone-sequence.md`'s forward section, whose framing §1.3 settles.
- `SECURITY.md`, whose published-gaps section names the initiate hooks'
  unvalidated `providerId` as the sharpest item currently open, and which
  already says whoever closes it should correct that section too.
- The harness comments that encode the opposite decision: the mint-after-build
  rationale in `run.sh`, the management-port note in `compose.yaml`, the
  "not a secret" note in `dsbox.yaml`, the initiate-URL block in
  `config.properties`, and the self-issued-operator block in `demo/run.sh`.

**Every documentation edit this milestone makes names the code fact it was
checked against.** Two milestones in a row were blocked by documentation
asserting things about the code that were not so, and the rule that came out
of the second one applies here: a sentence about the code is verified against
the code before it is written, and counts are left out of comments rather
than maintained in them.

---

## 8. Evidence

**`make tck` at 65 of 65** is the evidence that §4 and §5 did not break the
harness — and after §6.2 it is a meaningful gate rather than a fixture
accident, because the harness now presents the identity it claims.

**`make demo`** is the evidence that the whole path works against a
counterparty the roster actually lists, in both roles, across an interrupted
and resumed transfer.

**Neither harness can show a refusal.** No TCK test names a participant the
roster does not list, and none pushes a message about an exchange it is not
party to. So the refusal side is unit tests, which is the same situation §32
had and the same answer: the pass side is covered by a suite that already
exists, and the refusal side is covered by tests written for it —
an unlisted `providerId` on both hooks, a mismatched issuer at each of the
three insertion points in §5.1, the empty-counterparty row from §5.2, and the
authentication-off case from §4.2.

**Two harness facts must be verified by observation, not assumed**, because
both are single points of failure for a 65-of-65 result: that the connector
actually receives `TCK_PARTICIPANT` as the recorded counterparty, and that
the management listener actually receives the minted token. Both are visible
in a run — the first in the transfer rows the connector stores, the second in
whether seeding succeeds.

---

## 9. Trade-offs accepted

**The management API grows from three routes to five, and two of them write.**
§25.3's rule is not repealed — a later milestone wanting a general CRUD
surface still argues for it on its own merits — but the rule now bounds a
larger API. The argument that these two belong is §3.1: they were always
operator actions, misfiled on the protocol listener because the TCK's
harness shape put them there. This is the second time that boundary has been
renegotiated, after `GET /transfers`, and a third will have to explain why the
management API is still small.

**A roster participant is not necessarily the participant at
`connectorAddress`.** §4.3. An operator can still produce an exchange whose
counterparty is wrong, and after §5 it fails at every inbound message with a
log line as its only explanation.

**The harness stops demonstrating the five-minute credential lifetime** that
§10 decided and four places state. The TCK's token lives thirty minutes so it
can outlive a cold build. Nothing in the connector changes, and the DSP
listener still enforces expiry on every other request the suite makes.

**The harness's identity is now coupled to an upstream constant.** §6.2.
Mitigated by the digest pin and by a comment, not by anything structural.

**`make demo` loses the only self-issued operator credential in either
harness.** §6.4.

**An upgraded deployment's in-flight consumer-role exchanges stop working.**
§5.2. Accepted because there are no deployments, and because the alternative
is a permit-on-empty clause that would outlive the reason for it.

---

## 10. What this does not do

It does not add replay defense. §28 declined it and this milestone does not
reopen that; what it removes is the *delivery* of a signed token to a caller
who chose its audience, which is what made the absence of replay defense
composable into an impersonation primitive.

It does not add rate limiting, which `docs/goal-gap-analysis.md` notes is
filed nowhere at all. After this change the amplifier is behind the
management token, which is what that document predicted would make rate
limiting cheaper later.

It does not put an address in the roster. Binding `connectorAddress` to a
participant would close §4.3, and it needs a schema change to a signed
artifact — which is the next milestone in the gap analysis's order, not this
one.

It does not change the error documents the two hooks emit. They continue to
answer with `dspace:ContractNegotiationError` and `dspace:TransferError`
JSON-LD, which is inconsistent with the plain-text errors the rest of the
management listener returns. Changing them is churn with no reader: nothing
consumes those bodies, and the TCK asserts only on the status code. Recorded
so the inconsistency reads as a decision.

It does not start returning `validateOutgoingCallback`'s rejection reason to
the caller. The "name-resolution oracle" argument weakens once the caller is
the operator, but relaxing it is a separate judgment with its own reasoning,
and the comment that currently justifies the silence on the wrong grounds is
corrected without changing the behavior.
