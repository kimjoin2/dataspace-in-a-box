# Discovery: a catalog client, and an address in the roster

**Status:** design, agreed 2026-08-28. Written after two rounds of
cross-checks. The first round refuted five judgements in the design; the
second round refuted four of the five corrections. What each round measured
is in §12, and it is worth reading before §5, §6 and §7, because those
sections are shorter than the drafts they replace and the reason is always a
measurement rather than a preference.

**Ordered item 4** of `docs/goal-gap-analysis.md`.

**What it acts on:** two findings that document keeps separately. P1 — of the
four core protocols, catalog and version metadata are implemented in the
provider role only, and the practical consequence is that "the only
counterparties this connector can transact with today are another `dsbox` and
the TCK". And the residual `DECISIONS.md` §35.5 left open and `SECURITY.md`
still carries — that a roster participant is not necessarily the participant
at `connectorAddress`. Item 4 inherited the second because it is where
something finally consumes an address.

**What it does not amend:** `DECISIONS.md` §25.3's rule. §3.4 argues this
milestone's route against that rule rather than asking for an exception to it.

---

## 1. The finding this milestone acts on

### 1.1 The catalog protocol runs in one direction

`internal/dsp/` holds `negotiation_client.go` and `transfer_client.go` and no
catalog client. `CatalogRequestMessage` appears only in `catalog_handler.go`,
the receiving side. The version document is likewise served and never
requested.

The consequence is not the missing code. It is that a consumer has to learn
`datasetId` and `offerId` out of band, and the offer identifier is derived by
a convention private to this implementation — `offerIDSuffix = "#offer"`
(`internal/dsp/catalog.go`), which `demo/run.sh` hardcodes in each of its
negotiate calls.

### 1.2 An operator can point an initiate call at the wrong connector

§35.2 constrains the name an initiate call may give. Nothing binds that name
to the address the same call chooses, so an operator who names a roster
participant and points the address at a different connector still hands a
signed credential to whoever is there, and every inbound message on that
exchange is then refused with only a log line to explain it.

§35.5 recorded this and assigned it to the roster milestone. §36.9 declined it
on scope: that milestone changed the document's *lifecycle*, while an address
changes what an *entry* means. §2.4 answers the cost §36.9 named.

### 1.3 What this milestone closes, and what it leaves

It closes 1.1 for the catalog protocol and leaves version metadata served-only
(§2.2). It closes 1.2 structurally rather than by validation — see §5.

---

## 2. Scope

### 2.1 In

A catalog client; a management route that triggers it; an optional
`connector_address` on a roster entry; address derivation at the two initiate
hooks; a `demo/run.sh` whose negotiate rounds obtain what they need from
discovery; and the documentation §10 lists.

### 2.2 Out, and why each

**A version-metadata client.** Fetching a counterparty's version document
would let the roster store a connector root and let this connector discover
the path prefix. It is out because the harness settles it: the TCK's mock
serves DSP at the root and `dsbox` serves it under `VersionPath`, so an
address is the base that message paths are appended to and there is nothing
for a version document to resolve. P1's protocol table keeps its
version-metadata row, and §10 requires that to be said rather than left to be
inferred from the table's shape.

**`distribution`, and therefore `format`.** `transferInitiateBody` requires
`format`, and `format` lives in `dataset[].distribution[].format`, so leaving
it out means the transfer half of an exchange still takes a value out of band.
This is a deferral and not a design argument, and the reason it is safe to
defer is concrete: this connector advertises `unspecifiedFormat` —
`"dsbox:unspecified"` — whose own comment calls it a placeholder that "changes
when the transfer milestone makes a real one true", while `demo/run.sh` sends
`HTTP-PULL`. Decoding the advertised value today would supply a string that
`POST /transfers/initiate` cannot use. `demo/run.sh` therefore keeps
hardcoding `format`, and §8.3 says so where a reader of the demo will meet it.

**Nested catalogs.** `catalog-schema.json` permits a `catalog` array of
sub-catalogs, which is how a federated broker advertises. This milestone does
not walk them. It must not report such a document as empty, so §7.4 requires a
log line naming what was skipped.

**Expanded JSON-LD, and any context form other than the fixed compact one.**
Already out, by `DECISIONS.md` §20 and `CLAUDE.md`'s JSON-LD convention. This
spec cites that decision rather than recording a new limit, because writing it
again would imply it is new.

**Making `connectorAddress` optional in the initiate body.** §5.5.

**Putting `make demo` in CI.** §9.1 names the consequence; the work belongs to
ordered item 5, which is the item that defines what CI measures.

### 2.3 The route is a read, and the milestone keeps it one

Nothing in this milestone caches a fetched catalog. §3.4 makes that the
concrete guard on the §25.3 boundary, so it is scope and not merely an
omission.

### 2.4 The answer to the cost §36.9 named

§36.9 declined an address because it "would make every address change a
re-signed roster and a fleet-wide restart". That cost is real and this
milestone does not deny it. It bounds it: the field is optional in the
document and mandatory only where this connector dials out (§4.2, §5.2), so an
operator pays the re-signing cost for the participants they initiate toward
and pays nothing for the ones they only ever receive from. A participant whose
signature this connector verifies and whom it never calls needs no address at
all — `public_key` serves the inbound direction, `connector_address` the
outbound one.

---

## 3. The management route

### 3.1 Shape

```
GET /catalog?providerId=urn:participant:provider
Authorization: Bearer <mgmt_token>

200 {"participantId":"urn:participant:provider",
     "connectorAddress":"http://provider:8080/2025-1",
     "datasets":[{"id":"urn:dataset:sample",
                  "offerId":"urn:dataset:sample#offer"}]}
```

`datasets` is a list of negotiable pairs, not a catalog model. A dataset
carrying several offers produces several rows, because one initiate call takes
one `(datasetId, offerId)` pair and a nested list would blur that
correspondence. A dataset carrying no offer cannot be negotiated for; it is
omitted and §7.4 requires it to be logged.

`connectorAddress` is in the response so that the response is sufficient to
build an initiate call. It is the address this connector resolved from the
roster and actually dialed, so it is a report of what happened rather than an
echo.

### 3.2 Where the handler lives

In `internal/dsp`, reaching the management listener as an `http.Handler` on
`Routers`, the route the initiate hooks already travel — §35.1's "They live in
package dsp as code and here as routes". `internal/mgmt` keeps having no
opinion about the protocol package the handler came from.

### 3.3 GET, and the error shape

`GET`, because the argument in §3.4 is that this writes nothing, and every
`POST` on the management listener so far is a write or a trigger. DSP's own
catalog request is a `POST`, but that is the message this connector sends
outward; it is not the shape of the operator's instruction to send it.

Rejections use `writeError` with this connector's catalog error type, which is
the JSON-LD error document the rest of `internal/dsp` emits. §35.5 already
ruled on this for the initiate hooks and accepted the resulting inconsistency with
the plain-text errors the rest of the management listener returns: "Changing
them would be churn with no reader." A new route must not introduce a third
shape.

### 3.4 The answer §35.5 asked the next route for

§35.5 said whoever added the next management route should be made to say why
the management API is still small, and gave two properties that made the
initiate hooks admissible: they are triggers rather than resources, and they
arrived by subtraction from another listener.

This route has the first and not the second. `GET /catalog` is a verb — "ask
that participant for their catalog" — and the thing it names is not a resource
this connector owns. But it is addition, not subtraction, and no argument
should pretend otherwise.

It stands on the property §25.3's boundary is actually drawn around: **it
writes nothing.** That is what admitted `GET /agreements` and then
`GET /transfers`, and the concrete guard here is that no catalog is stored.
Every call asks the counterparty again. A later milestone that wants to cache
one is asking for a write path and argues for it on its own merits.

The one genuinely new capability is an operator-triggered outbound request to
a third party. That is not new to this listener either: §35.1 put both
initiate hooks here, and each of them dials a counterparty.

### 3.5 With authentication off, the route refuses

The address lives in the roster, and `cmd/dsbox/main.go` loads a roster only
under `cfg.AuthRequired()`. The nil-predicate convention `internal/dsp`
follows — absence is not a check that fails, it is a check that is not there —
cannot apply, because what is absent is not a check but the value itself. The
route refuses and says so.

`config` already forbids the other half of the matrix: authentication off with
`dev_mode` off is a configuration error, and `dev_mode` on with authentication
on is legal and loads a roster. There is no combination in which a roster
exists and authentication is off.

---

## 4. `connector_address` in the roster

### 4.1 Shape

```json
{"id": "urn:participant:provider",
 "public_key": "...",
 "connector_address": "http://provider:8080/2025-1"}
```

The field carries the same string `connectorAddress` names in an initiate
body: the base that DSP message paths are appended to. The name says so,
because a different name would invite a reader to think it is a different
value.

### 4.2 Optional in the document

The Go field carries `omitempty`, and that tag is load-bearing.
`canonicalRosterBytes` re-marshals the parsed document, so with `omitempty` an
entry that carries no address produces canonical bytes byte-identical to the
bytes produced before the field existed, and every signature made before this
change still verifies. Without it, every existing roster stops verifying and
the error an operator meets names the signer key rather than the upgrade. Both
halves were measured (§12.1).

Optional is not a weakness here, because the signature covers the field in
both directions: stripping an address out of a signed roster breaks
verification, adding one to an entry that had none breaks verification, and
rewriting one breaks verification. All three were measured. "Optional" means
the operator chose, not that an attacker may choose.

An entry whose `connector_address` is an explicit empty string is an entry
with no address, and every outbound site refuses it (§5.2). It cannot fall
back to anything, because there is nothing to fall back to.

### 4.3 What `LoadRoster` checks, and what it does not

For an address that is present: it must parse, carry the `http` or `https`
scheme, carry a host, carry no query and no fragment, and not end in `/`.
Query and fragment are checked on the raw string, because a bare `?` leaves
`RawQuery` empty and a bare `#` leaves `Fragment` empty, and `url.String()`
then silently drops the `#` so the stored string and the parsed value
disagree. The host check carries the "absolute" rule; `url.IsAbs()` means only
that a scheme is present and returns true for `provider:8080/2025-1`.

The no-trailing-slash rule is load-bearing rather than tidiness: every path
constant this connector appends begins with `/`, so a trailing slash produces
`//negotiations/...`.

There is no case rule and no normalization. An earlier draft had both, and §5
is why they are gone.

**No name resolution, and no comparison against `dev_mode`.**
`validateOutgoingCallback` resolves a hostname and refuses loopback and
link-local addresses; it cannot run here. `internal/dsp` imports
`internal/auth`, so the reverse is an import cycle, and the boot ordering
settles it independently: a counterparty's container does not exist when this
connector boots — in the TCK harness the TCK is started by `compose run` after
`dsbox` is already up, and in the demo `provider` boots first under
`depends_on`. That guard runs where it already runs, on the address this
connector is about to dial (§5.1).

### 4.4 The per-entry boundary is unchanged

`SignRoster` does not walk `doc.Participants`, so `dsops roster sign` prints a
signature for a roster whose address is malformed, and the connector refuses
that roster at boot naming the entry. That is exactly what already happens for
a missing or malformed `public_key`, and the boundary is stated in
`SignRoster`'s own doc comment. This milestone follows it rather than moving
it, and `config.example.yaml`'s paragraph enumerating which per-entry faults
sign cleanly gains the address (§10).

### 4.5 Storage and accessor

`Roster` keeps a second map from participant id to address, and exposes
`AddressFor(id) (string, bool)` beside `KeyFor`. Without it the validated
value is parsed and discarded.

---

## 5. Address derivation at the initiate hooks

### 5.1 The roster's address is used, not compared

Both hooks run `validateOutgoingCallback` on an address and then store that
same string as `ProviderBaseURL`. This milestone changes which string that is:
when the roster carries an address for `providerId`, **that** value becomes
`ProviderBaseURL` and is what `validateOutgoingCallback` runs on.

The request's `connectorAddress` is then not consulted. When it differs from
the roster's, that is logged at warning level with both values. The comparison
exists only for the log line, so it needs no normalization to be sound.

### 5.2 The rule

| state | behaviour |
|---|---|
| the roster carries an address for `providerId` | it becomes `ProviderBaseURL`; a differing request value is logged |
| the participant is in the roster with no address | refused |
| the predicate is nil, authentication off | the request's value is used, as today |

### 5.3 Why derivation rather than comparison

The reasons, and the first is this repository's own.

**§35.1 answered this shape of question already.** Facing "validate what an
untrusted caller may name, or remove their ability to name it", it removed,
and recorded that the move "removes the primitive rather than mitigating it".
Comparison is mitigation. Derivation is removal, and it is the same argument.

**Comparison had a hole in the property this milestone exists to add.** A
byte comparison needs normalization, and normalization was measured to fail in
both directions (§12.2). The direction that matters is the false acceptance:
with the scheme and host lowercased before comparing, a host written with
U+212A KELVIN SIGN folds onto an ASCII host, passes the comparison, and the
raw non-ASCII string is what gets stored and dialed. The comparison approves
one string and the connector uses another. Percent-encoding, an explicit
default port, and a leading-zero port each produce false refusals by the same
mechanism.

Derivation removes all of it: the approved string and the used string are the
same string by construction.

**It keeps the TCK decoupled from a constant it need not depend on.** A
comparison requires the harness roster to hold, byte for byte, the
`connectorAddress` the pinned TCK image emits. Derivation does not read that
value at all, so a future pin that changes it costs nothing. `test/tck/run.sh`
already carries one coupling of that class for the participant id, and its
comment names the symptom: every consumer-role result failing on a refusal
that reads like a protocol bug. One is enough.

### 5.4 The counter-argument, and why it does not apply

§35.2 added a `providerId` check after §35.1 had already closed the hole
structurally, and gave a reason: "an unverifiable name accepted at initiate
time surfaces later as blanket refusals in someone else's subsystem". That
reason does not transfer. A wrong `providerId` is stored and every later
inbound message on that exchange is compared against it. A wrong
`connectorAddress` is not stored at all under §5.1 — the roster's value is —
so nothing downstream is left holding a value that will confuse it.

### 5.5 The request body does not change

`connectorAddress` stays required. Two reasons. The TCK hardcodes the
four-field body in a pinned image and cannot be configured, so leaving the
shape alone keeps 65/65 safe structurally rather than by argument. And a field
that is required only when authentication is off is a rule with no reader
today. Making it optional is a follow-up this milestone does not take.

### 5.6 Order, and the two handlers

The derivation replaces the address the handler was going to use, so it runs
where the roster membership check already runs — after it, since an unknown
participant has no address to look up. The existing ordering rules are
untouched: the negotiation handler's address guard still precedes its roster
check, and the transfer handler still refuses an unknown agreement before its
roster check. The two handlers do not have the same order, and each is pinned
by its own test; §9.3 names both.

---

## 6. The catalog client

### 6.1 The call

A new `internal/dsp/catalog_client.go`, carrying the header
`negotiation_client.go` and `transfer_client.go` already share: outbound calls
this connector makes as consumer live in their own file. The file holds
functions; the types live in `catalog.go` beside the ones they neighbour,
which is where `ConsumerRequestMessage` and `TransferRequestMessage` live
relative to their own clients.

The request is a `CatalogRequestMessage` with `@context` and `@type` and no
filter, POSTed to the derived address plus the catalog request path. No
filter, because this connector's own provider side refuses one — DSP leaves
the filter expression implementation-defined, so returning a full catalog to a
consumer that asked for a subset is a worse failure than a rejection — and
that argument holds for what this connector sends as much as for what it
receives.

The audience of the minted credential is the `providerId` the route was given.
It is never empty, because the route requires it.

No retry. `sendInitialRequest` records the reasoning and it is stronger here:
an operator asked, and a failure they are told about beats a silent retry.

### 6.2 It reuses `callbackHTTPClient`

An earlier draft gave the catalog request its own client. That was wrong, and
the reason is in the code: `sendInitialRequest` and `sendTransferRequest`
already use `callbackHTTPClient`, and both are structurally what a catalog
request is — one POST, bounded, response decoded, no retry. A third client
needs an argument this milestone does not have.

The inherited behaviours that have to be said rather than left to be discovered:

- **The response body is bounded** by an `io.LimitReader`, at the bound the
  negotiation request body already uses. The ten-second client timeout covers
  the body, so a hostile provider is bounded in time — but a streamed response
  can allocate a great deal inside that window (§12.3), and the catalog is the
  one DSP body whose size scales with the counterparty's holdings. Every other
  inbound read in this connector is bounded; this one is too. A catalog larger
  than the bound is refused, and that is the deliberate answer.
- **Redirects are not followed.** `CheckRedirect` returns
  `ErrUseLastResponse`, and a status at or above 300 is an error, so a
  load balancer's `308` is reported to the operator as a refusal rather than
  followed. That is a real deployment shape and the operator has to be able to
  read what happened.
- **The connection pool is shared** with the callback pushes, because the
  client carries no `Transport` of its own.

### 6.3 The client's doc comment becomes true

`callbackHTTPClient`'s comment describes callback pushes, while two of its
existing call sites are synchronous protocol requests whose responses are
decoded — the same staleness `docs/goal-gap-analysis.md` P2 identified as the
mechanism behind the data-path defect. This milestone corrects it.

The correction is written by kind and not by census: naming how many call
sites there are would be false on the day this milestone lands.

### 6.4 What the route checks, in order

Roster expiry first, before the request is read, because that refusal is about
this connector rather than about the request — the order `handleInitiate`
already uses. Then a missing `providerId`; then a participant the roster does
not list, echoing the name back as the initiate hooks do; then a participant
with no address. Then the call, and then §7.

---

## 7. The decode type

### 7.1 Strict, and separate

A decode-only type, unexported, in `catalog.go`. It carries what discovery
needs and nothing else: the catalog's `participantId`, each dataset's `@id`,
and each offer's `@id`. It does not decode `@context`, `@type`, or
`distribution`.

Not decoding those is what makes the type work: measured against realistic
catalog documents, the existing encoder structs failed most of them, and
simply not reading those three fields removed all but two of the failures
(§12.4).

**It is strict, like every other decode site in this connector.** An earlier
draft proposed tolerating a single object where the schema requires an array.
Two measurements killed it. The TCK's own `catalog-schema.json` and
`dataset-schema.json` — read out of the image `test/tck/compose.yaml` pins by
digest — declare `dataset` and `hasPolicy` to be arrays with at least one
item, and `hasPolicy` to be required; so tolerance would buy interoperability
only with documents the TCK itself rejects. And every inbound decode site in
`internal/dsp` is strict today, which is not an accident: `DECISIONS.md` §20
accepted "arbitrary JSON-LD input — different context forms, expanded form —
is not handled", and `CLAUDE.md` states the fixed compact form as the
convention. Tolerating on the outbound-request side alone would make this
connector stricter about what it receives than about what it reads, with no
test able to say which.

Tolerance is also worse than strictness on `null`: a `null` dataset list
decodes to one phantom dataset, and a `null` policy list to one phantom offer
whose `@id` is empty — which is a value the operator would paste into an
initiate call.

### 7.2 Why a separate type rather than the encoder structs

Not because reusing an encoder for decoding is forbidden — this connector
reuses several. The rule is `DECISIONS.md` §24.7's: split when the two
directions carry different obligations, and say what differs. Here the
emitting side owes a complete catalog document and the reading side owes three
identifiers and a refusal (§7.3); `OfferRef` is the existing precedent for a
lean decode-only sibling.

### 7.3 An empty catalog and a non-catalog must not look alike

A type error must be fatal — `encoding/json` populates what it can before
returning one, so a document with a malformed policy list decodes into a
structurally valid value with offers missing.

That is necessary and it is not sufficient. An empty JSON object, a DSP error
document, an unrelated JSON document, and a bare `null` all decode without
error into "no datasets" (§12.5). What the operator would then be told is that
the counterparty advertises nothing — the worst available failure for a
discovery tool, because it reports a conclusion about the counterparty instead
of a failure of the request.

The connector already has the answer. `sendInitialRequest` refuses a response
that carries no `providerPid`. `participantId` is required by the catalog
schema and is the value a catalog cannot omit, so **an empty `participantId`
is fatal**, and that single check rejects all four documents above.

### 7.4 What is skipped is logged

A dataset with no offer is omitted from the response, and a `catalog` array of
sub-catalogs is not walked (§2.2). Neither may be silent: both are logged,
naming what was skipped, so a caller who receives fewer rows than the
counterparty advertises can find out why.

### 7.5 The declared participant must be the one asked for

If the fetched catalog declares a `participantId` other than the one the route
was given, the request is refused.

The declared value is an unauthenticated claim — this connector authenticates
to the provider, not the response to itself. Refusing on it is still right:
`LoadRoster`'s own comment draws the line this sits on the safe side of —
"Rejecting on unauthenticated input is fail-closed, which is a different thing
from acting on unauthenticated claims." And it is the one place where evidence
about what an address actually serves can contradict the roster, which is the
shape §35.5 named.

---

## 8. The harness and the demo

### 8.1 Each harness writes its roster twice

`test/tck/run.sh` and `demo/run.sh` each write the participants block twice —
once for `dsops roster sign` to read, once with the signature pasted in.
Editing one copy and not the other produces a signature that does not verify,
the connector refuses to start, and the readiness loop times out. Every one of
the four gains whatever addresses that harness needs.

### 8.2 The TCK harness

The TCK's participant entry gains `http://tck:8083`. That value was measured
rather than assumed, and the evidence has to be the right evidence: the
outbound URLs a run logs for a termination or an agreement are also the paths
the provider role formats against a callback address the TCK supplied, so they
settle nothing. The verification path settles it, because it exists as an
outbound template only, formatted against `ProviderBaseURL`, which is set from
the initiate body and never updated afterwards. The transfer hook was
confirmed separately by correlating a logged process id against the TCK's own
output.

Under §5.1 this value is what the connector dials rather than something it
compares, so the harness does not become coupled to the string the image
emits.

This connector's own entry stays address-free: it never dials itself. That is
the optional-field rule demonstrating itself in the harness.

### 8.3 The demo

The existing negotiate rounds are rewritten to obtain what they need from
`GET /catalog` rather than from hardcoded constants, so the catalog client
becomes load-bearing: delete it and `make demo` fails. Both rounds, since each
carries its own offer id.

What that removes is the offer ids and the connector address. What it does not
remove is `format` (§2.2), the management token, the ports, and the agreement
id extraction whose `sed` depends on the field order of the agreements
response. The demo's own comments should not imply otherwise.

The demo cannot demonstrate the derivation in §5, because the address flows
from the roster through the catalog response into the initiate call and
therefore always agrees. What it demonstrates is that the path is connected.
§9.3 owns the rest.

---

## 9. Evidence

### 9.1 `go test` is the whole gate

The TCK cannot see any of this. Its catalog suite plays the consumer, and the
pinned image carries no consumer-role catalog test — confirmed by listing the
runtime jar. `make tck` is a regression check for this milestone and not
evidence for it.

`make demo` is not in CI either: the pipeline runs `go vet`, the race suite,
and the TCK. *(2026-08-29: `DECISIONS.md` §39 and §41 added a quickstart job
and a demo job, so the pipeline now runs both.)* So the demo's evidence is only as good as someone running it, and
`go test` carries everything that must not regress unattended. That is a
finding about the pipeline rather than about this milestone; ordered item 5
owns it.

### 9.2 The roster

The regression that matters is the `omitempty` mutation, and the natural test
does not catch it: every existing roster fixture signs at runtime, re-marshals
the parsed participants, and is therefore self-consistent under any struct
shape. With `omitempty` removed, the whole existing roster suite stays green
(§12.1).

The test is therefore an assertion on `canonicalRosterBytes` itself: an entry
with no address must marshal to the bytes it marshalled to before the field
existed. It needs no key, no signature, and no clock, and when it fails it
names the defect — where a pinned signature constant fails with the same
string a forged roster produces, sending the next reader to the wrong place.

A companion fixture carries a non-empty address, because an entry with no
address serializes identically under a field reordering and the first fixture
alone would not see one.

Beside those: the signature covering the address in all three directions
(stripped, added, rewritten), one refusal per syntactic rule in §4.3, and
`AddressFor` for a participant with and without an address.

### 9.3 The initiate hooks

No existing test constrains this. With the predicate wired and refusing every
participant, the suite passes; with the predicate replaced by a panic, the
suite passes; with the check moved above the address guard, the suite passes
(§12.6). Both ordering tests build their handlers as struct literals, so the
new field is nil and the code never runs. Every mutation below is killed by a
test this milestone writes, and by nothing it inherits.

| mutation | the test that fails |
|---|---|
| the derivation is deleted and the body's value used | a test whose roster address differs from the body's and which asserts the roster's is what the outbound request reaches |
| a participant with no address is allowed through | a test asserting an initiate call toward an address-free participant is refused |
| the nil predicate is read as "refuse" | a test asserting an authentication-off router still accepts the body's address |
| the derivation is moved before the roster membership check | a test asserting an unknown participant is refused as unknown |

The existing ordering tests stay as they are, and both are named in the plan:
the negotiation handler's address-guard-before-roster-check test, and the
transfer handler's unknown-agreement-before-roster-check test.

### 9.4 The catalog client

Against an `httptest` provider: the happy path producing the pairs; a request
that carries no filter, asserted on the received body; a credential whose
audience is the requested participant; several offers producing several rows;
a dataset with no offer omitted; a mismatched `participantId` refused; a
provider status of 401, 404 and 500 each reaching the operator; a body over
the bound refused; and an empty `participantId` refused for each of an empty
object, a DSP error document, and `null`.

And one assertion about order rather than about results: under an expired
roster the route answers before the fake provider is contacted at all, which
is what makes the guard's position observable.

### 9.5 The management route

`GET /catalog` must be refused without the token. The existing coverage test
picks that up from source automatically, and that is the only one of that
package's guards that does — the pattern-shadowing table and the
initiate-hook map are hand-written and each needs its entry.

The source-parsing guard also has a measured hole: a route registered from any
file other than `router.go` is invisible to it and ships anonymous. This
milestone replaces the parser with a registration table that `NewRouter`
builds through and the guard reads. That closes the hole, drops the existing
"write the pattern as a string literal" constraint, and avoids the two false
positives that widening the parser was measured to produce — an ordinary test
helper's own mux, and a route pattern quoted in a comment.

On the DSP side, the guard asserting the initiate hooks are not registered on
the protocol listener gains a sibling for the catalog lookup handler. It must
key on the handler's own identifier: the DSP listener legitimately registers
catalog routes, so a guard matching the word would report them.

### 9.6 `cmd/dsbox`

`mgmt.NewRouter` gains the handler as a further positional parameter, after
the roster predicate that an existing source-parsing guard in
`cmd/dsbox/roster_version_test.go` pins.

An earlier draft replaced the positional parameters with a named struct. That
was withdrawn: transposing two named fields was measured to leave the build,
the vet, and the whole suite green, so the struct buys legibility and no
detection, while `DECISIONS.md` and two test comments explain their own
existence with the word "positional" and would become false.

What is added instead is a third source-parsing guard in that same file,
beside the two that already live there for exactly this class of defect — a
call site Go does not require and no test observes — pinning which handler is
passed in which position.

---

## 10. What becomes false

Each edit below names the code fact it is checked against.

- `README.md`'s protocol table stops marking the catalog protocol served-only.
  Version metadata stays marked, and the section says so in words, because the
  table's shape is what P1 identified as reading symmetric at the one place it
  is not.
- `SECURITY.md`'s entry for the §35.5 residual: closed by §5.
- `docs/goal-gap-analysis.md`'s P1 paragraph and its ordered item 4 get dated
  bracket annotations. They are not rewritten. That is this repository's
  convention for a dated artifact and the last milestone broke it twice.
- `config.example.yaml`: the roster example gains the field; the paragraph
  enumerating which per-entry faults `dsops roster sign` passes cleanly gains
  the address (§4.4); and the upgrade note gains this field as the contrasting
  case, since the previous roster change had no compatibility path and this one
  does.
- `DECISIONS.md` gains a section recording the roster field and the bound on
  §36.9's cost (§2.4), the answer to §25.3 for the new route (§3.4),
  derivation rather than comparison and why (§5.3), strict decoding and its
  citation of §20 and §24.7 (§7), and the empty-`participantId` refusal
  (§7.3). Its existing sections that describe the roster entry shape and the
  positional management handlers are checked against the code and amended only
  where this milestone made them false.
- `docs/milestone-sequence.md`'s "What can verify each remaining milestone"
  gains an entry: discovery is the second milestone the TCK cannot verify,
  after the data plane, and for a different reason — not that no test asserts
  it, but that the suite plays the role this milestone implements.

---

## 11. Trade-offs accepted

**An operator's `connectorAddress` is ignored when the roster carries one.**
It is logged, not refused. §5.3 and §5.4 are the argument; the cost is that an
operator who typed a different address is told so only in the log.

**The transfer half of an exchange still takes `format` out of band.** §2.2.
The ten-minute claim this milestone serves is therefore improved rather than
satisfied, and ordered item 5 will measure what is left.

**A federated catalog is reported without its sub-catalogs.** §2.2, logged per
§7.4.

**Strict decoding refuses documents some connectors may emit.** By
`DECISIONS.md` §20, already accepted, and the measurement in §12.4 says the
shapes refused are the shapes the TCK's own schema rejects.

**The catalog response is bounded, so a catalog larger than the bound cannot
be discovered.** §6.2. The alternative is an unbounded read of a counterparty's
document, which nothing else in this connector permits.

*(2026-08-28: the second sentence is false, and it was written into
`DECISIONS.md` §38 verbatim before a review caught it. The other outbound
clients permit exactly that read: `fetchCatalog` is the only one that bounds
a response body, and this milestone is what gave it one, while
`sendInitialRequest` in `internal/dsp/negotiation_client.go` and
`sendTransferRequest` in `internal/dsp/transfer_client.go` each call
`json.NewDecoder(resp.Body).Decode(&doc)` with no `io.LimitReader`. What the
bound buys stands; the claim that it matches the rest of the connector does
not. `docs/follow-ups.md` records the residual, and §38's trade-off
paragraph was corrected rather than left to repeat this.)*

**This milestone's evidence lives where the pipeline is weakest.** `go test`
carries it and `make demo` demonstrates it, and only the first runs in CI
(§9.1).

---

## 12. What the cross-checks measured

Two rounds. The first checked the design; the second checked the corrections
the first produced, and refuted most of them. Every number below is a
measurement taken on 2026-08-27 or 2026-08-28 against the tree at `d78b8cd`.

**12.1 The roster.** With `omitempty`, an address-free entry's canonical bytes
are identical to the pre-field bytes and a pre-field signature verifies;
without it, they differ and it does not. The signature covers the address when
it is stripped, added, or rewritten. And the mutation that removes `omitempty`
leaves every existing roster test green, because the fixtures re-marshal the
participants before signing — which is why §9.2 asserts on canonical bytes
instead.

**12.2 Address comparison.** Lowercasing the scheme is dead code, since
`url.Parse` already folds it. With scheme and host lowercased before
comparing, a host written with U+212A KELVIN SIGN passes the comparison
against an ASCII host while the raw string is what gets stored — a false
acceptance in the property the milestone adds. `%7E` against `~`, an explicit
`:80`, and a leading-zero port each produce false refusals. Comparing a
normalized value while using the raw one was measured to change the string for
whitespace and for any uppercase host. These findings are why §5 derives.

**12.3 The HTTP client.** A provider streaming an unterminated dataset array
is cut off at the client's ten-second timeout, having transferred a great deal
inside it — which is why §6.2 bounds the body rather than relying on the
timeout. A redirect is surfaced as a status at or above 300 rather than
followed.

**12.4 Decoding.** Against a set of realistic catalog documents, the existing
encoder structs failed most of them; a minimal type that skips `@context`,
`@type`, and `distribution` failed only those supplying a single object
where the schema requires an array; tolerating those failed none. The TCK's
`catalog-schema.json` and `dataset-schema.json`, extracted from the digest
pinned by `test/tck/compose.yaml`, declare both to be arrays with at least one
item. Every inbound decode site in `internal/dsp` is strict.

**12.5 Silent emptiness.** An empty object, a DSP error document, an unrelated
JSON document, and `null` all decode without error into a catalog with no
datasets. An empty `participantId` treated as fatal rejects all of them.

**12.6 The initiate predicate.** Wired and refusing everything: the suite
passes. Replaced with a panic: the suite passes. Moved above the address
guard: the suite passes. No inherited test observes it.

**12.7 The management route guards.** A route registered from a sibling file
ships anonymous with nothing failing. Widening the source parser to the whole
package catches it, and also fails on an ordinary test helper's own mux and on
a route pattern quoted in a comment. A registration table catches it with
neither false positive.

**12.8 The management signature.** Transposing two named fields of a handler
struct leaves the build, the vet, and the whole suite green — the same state
already recorded for the positional form, which is why §9.6 adds a guard
rather than a struct.

**12.9 What the first round got wrong about the TCK.** The address
`http://tck:8083` is correct, but the termination and agreement URLs first
cited to establish it prove nothing, because those paths are also the
provider role's callback paths. §8.2 carries the evidence that does settle it.
