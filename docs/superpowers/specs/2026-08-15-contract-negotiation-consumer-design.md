# Design: the contract negotiation protocol (consumer role)

Date: 2026-08-15
Status: approved, ready for implementation planning

The fourth sub-project. It adds the DSP contract negotiation protocol in the
**consumer** role, completing `CN` end to end and making the `CN_C` suite (16
tests) join the compliance gate. The provider role (`CN`, 15 of 15 gated,
`CN:02-07` a named exemption) shipped in the previous milestone and is
untouched here except where explicitly noted.

## Scope

**In scope**

- One new public endpoint, `POST /negotiations/initiate` — a TCK-only trigger
  hook, not a DSP protocol message. Unauthenticated, on the public DSP
  listener, matching the posture `DECISIONS.md` §23.11 already accepted for
  the provider role's negotiation endpoints.
- Two new public endpoints the provider pushes to:
  `POST /negotiations/{id}/offers`, `POST /negotiations/{id}/agreement`.
- Three existing public endpoints — `POST /negotiations/{id}/events`,
  `POST /negotiations/{id}/termination`, `GET /negotiations/{id}` — gain a
  role-dispatch layer. Their provider-role behavior is unchanged.
- A second SQLite table, `consumer_negotiations`, keyed by this connector's
  own generated consumer pid.
- An outbound HTTP client path: the initial `ContractRequestMessage`, and the
  events/verification/re-request/termination this connector sends as
  consumer.
- Minimal ODRL constraint parsing (`leftOperand`/`operator`/`rightOperand`)
  on an incoming offer's `permission`, needed to decide whether this
  connector can accept what it was offered. Nothing before this milestone
  has parsed a constraint at all — `catalog.go`'s `Permission` explicitly
  deferred it "to the negotiation milestone."

**Out of scope**

Transfer process. `did:web`, the participant roster, JWT authentication —
same unauthenticated v1 posture as the provider role and the catalog
protocol. Logical constraints (`and`/`or` — the schema's
`LogicalConstraint`); only `AtomicConstraint` is parsed. A real management-API
trigger for starting a negotiation — `/negotiations/initiate` is a TCK-shaped
hook only; see "The initiate endpoint is not a management feature" below. Any
policy this connector could not itself enforce as a provider — the consumer
role holds itself to the same validity-period-or-unrestricted-only bar
`DECISIONS.md` §14 set for the provider role.

## What the TCK actually requires

Evidence sources, both permitted under `CLAUDE.md`'s "published, publicly
obtainable sources" rule as the official TCK: the TCK's own public source
repository, [`eclipse-dataspacetck/dsp-tck`](https://github.com/eclipse-dataspacetck/dsp-tck),
fetched via the GitHub API — specifically `HttpConsumerNegotiationClientImpl`
(the exact path constants this connector must serve), `ConsumerActions` (the
TCK's own local-connector reference implementation — informative, not
literally what runs against an external CUT), `ContractNegotiationConsumer01Test`
(the `@TestSequenceDiagram`-annotated scenarios), and
`contract-schema.json` (the ODRL constraint shape).

### The initiate endpoint is not a management feature

`DspSystemLauncher.start()` requires
`dataspacetck.dsp.connector.negotiation.initiate.url` unconditionally once
`dataspacetck.dsp.local.connector=false` — this project already set it to
`http://dsbox:8080/2025-1/negotiations/initiate` in the previous milestone,
before any consumer-role code existed, purely to unblock system startup. The
TCK's `HttpConsumerNegotiationClientImpl.initiateRequest` POSTs
`{providerId, offerId, datasetId, connectorAddress}` to that URL and discards
the response body — only the HTTP status is observed, and the test proceeds
by polling `GET /negotiations/{consumerPid}` afterward, not by reading
anything this endpoint returns.

This project's own `CLAUDE.md` lists "a management API" as in scope
eventually, and `internal/mgmt/router.go` exists today with exactly one
route, `GET /health` — no auth middleware, no negotiation trigger. Building a
real "start a negotiation" management feature is a separate concern from
satisfying this TCK contract, and conflating them would mean designing a
production UX decision (how does an operator choose a provider and an offer
to request?) as a side effect of a test harness requirement. This milestone
builds only the TCK-shaped hook, on the public listener, unauthenticated —
consistent with `DECISIONS.md` §23.11's already-accepted reasoning that this
connector's negotiation surface is unauthenticated in v1 regardless of which
role receives the request. A real management-API trigger is future work,
out of scope here.

### The endpoints

| Path | Direction | Purpose |
|---|---|---|
| `POST /negotiations/initiate` | TCK → CUT | Trigger hook. Not a DSP message. |
| `POST {provider}/negotiations/request` | CUT → provider | Initial `ContractRequestMessage`, sent by this connector after `initiate` fires. |
| `POST /negotiations/{consumerPid}/offers` | provider → CUT | `ContractOfferMessage`. |
| `POST {provider}/negotiations/{providerPid}/request` | CUT → provider | A counter/re-request repeating this connector's original ask — the consumer-role mirror of the provider milestone's `handleReRequest`. |
| `POST {provider}/negotiations/{providerPid}/events`, type `ACCEPTED` | CUT → provider | Sent once this connector decides to accept an offer. |
| `POST /negotiations/{consumerPid}/agreement` | provider → CUT | `ContractAgreementMessage`. |
| `POST {provider}/negotiations/{providerPid}/agreement/verification` | CUT → provider | Sent once this connector reaches `AGREED`. |
| `POST /negotiations/{consumerPid}/events`, type `FINALIZED` | provider → CUT | Terminal success. |
| `POST .../negotiations/{id}/termination` | either direction | Same path shape both ways — see "Routing" below. |
| `GET /negotiations/{consumerPid}` | provider/TCK → CUT | State document — same path shape as the provider role's status endpoint. |

`{id}` in every provider-pushed path is `consumerPid` — this connector's own
generated identifier, known the instant `/negotiations/initiate` fires,
before the outbound request is even sent. This is the mirror image of the
provider milestone, where `{id}` was the provider's own pid; each role's
table is keyed by its own generated identifier, not the counterparty's.

### Routing: three endpoints already exist, for the other role

`POST /negotiations/{id}/events`, `POST /negotiations/{id}/termination`, and
`GET /negotiations/{id}` are registered today in `router.go` for the
provider role. Go's `http.ServeMux` rejects a second registration of an
identical pattern, and the TCK's consumer-role client uses the exact same
three path shapes (`HttpConsumerNegotiationClientImpl`'s `EVENT_PATH`,
`TERMINATE_PATH`, and `GET_PATH` — well, `FINALIZE_PATH` there, same
literal shape as `EVENT_PATH`). These three handlers become dispatchers:
look the path parameter up in the provider store first (by `provider_pid`,
today's behavior, unchanged); if not found, look it up in the new consumer
store (by `consumer_pid`); if found there, run the new consumer-role logic;
if found in neither, 404, same as today. Collision is structural, not
incidental — see Risks for the one case this can get wrong.

The two genuinely new paths, `.../offers` and `.../agreement`, have no
provider-role equivalent and need no dispatch.

## Architecture

```
internal/store/store.go                consumer_negotiations CRUD, alongside the existing table
internal/store/store_test.go
internal/dsp/negotiation.go            + consumer-role message builders, + Constraint parsing
internal/dsp/negotiation_test.go
internal/dsp/negotiation_handler.go    + initiate/offers/agreement handlers, dispatch added to 3 existing ones
internal/dsp/negotiation_handler_test.go
internal/dsp/negotiation_client.go     new: the outbound calls this connector makes as consumer
internal/dsp/negotiation_client_test.go
internal/dsp/callback.go               reused as-is: validateCallbackURL, pushCallback, callbackHTTPClient
internal/dsp/router.go                 + 3 new routes, 3 existing ones rebound to dispatchers
cmd/dsbox/main.go                      unchanged — already passes the one *store.Store into dsp.NewRouter
```

`negotiation_client.go` is a new file, not new functions bolted onto
`negotiation_handler.go`: everything in `negotiation_handler.go` today
answers an inbound HTTP request; the outbound calls this milestone adds
(the initial request, and every message this connector sends as consumer)
are a different responsibility — initiating HTTP calls, not answering them —
and the file is a natural unit a reader can understand in isolation, per
this project's own design-for-isolation convention.

### Storage

```sql
CREATE TABLE IF NOT EXISTS consumer_negotiations (
    consumer_pid      TEXT PRIMARY KEY,
    provider_pid      TEXT NOT NULL DEFAULT '',
    provider_base_url TEXT NOT NULL,
    state             TEXT NOT NULL,
    dataset_id        TEXT NOT NULL,
    offer_id          TEXT NOT NULL,
    rerequested       INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);
```

A second table, not a `role` column on the existing `negotiations` table.
The provider table is 14 of 15 TCK tests deep; a shared table would mean
every row carries columns that mean different things depending on role
(whose pid is the primary key, what `callback_address` even is), and every
provider-role query would need a `WHERE role = 'provider'` this milestone
did not need to add. A second table costs one more `CREATE TABLE IF NOT
EXISTS` and a handful of CRUD functions shaped exactly like the ones
`store.go` already has for `negotiations` — `CreateConsumer`,
`GetConsumer(consumerPID)`, `SetConsumerState(consumerPID, from, to,
updatedAt)`, `SetConsumerRerequested(consumerPID)` — the same
compare-and-swap shape as `SetState`/`SetRerequested`, for the same reason:
consumer-role pushes also run in goroutines (see below) and can outlive a
termination that arrived in the meantime.

`provider_base_url` is `connectorAddress` from the initiate call, stored
because every subsequent outbound call this connector makes (re-request,
accepted event, verification) needs it and the initiate call is the only
place it is ever supplied.

No `callback_address` column: unlike the provider role, this connector's own
callback address is not per-negotiation data — it is always
`config.Config.PublicURL + VersionPath`, already computed at startup and
available to every handler through `cfg`, same as `buildAgreementMessage`
already reads it today.

### The initial request: goroutine dispatch, no retry

`handleInitiate` validates the four required fields, runs `connectorAddress`
through `validateCallbackURL` (the same SSRF guard the provider role already
applies to a consumer-supplied `callbackAddress` — `/negotiations/initiate`
is unauthenticated, so an anonymous caller fully controls where this
connector's outbound POSTs go, exactly the concern `DECISIONS.md` §23.11
already accepted for the provider's negotiation surface), generates a
`consumer_pid`, persists a row with `state = REQUESTED` and `provider_pid =
""` (mirroring the provider role's own table, whose first observable state
is also `REQUESTED`, never a wire-visible `INITIALIZED`), and responds `200`
immediately — then dispatches the outbound request in a goroutine.

The goroutine requirement is `DECISIONS.md` §23.8's lesson, carried forward
unchanged: `net/http` does not put a response on the wire until the handler
returns, so any outbound call made inline would hold the `200` back behind
it. Nothing about the trigger being TCK-only changes that mechanic.

Unlike `pushCallback`, this call is not retried. `pushCallback`'s backoff
schedule exists because the TCK's own callback *listener* registration is a
sequential stage that can still be running when this connector's push
arrives (`DECISIONS.md` §23.7) — a race on the *receiving* side. Here, this
connector is the one initiating, against a provider mock that is already a
live, already-listening server by the time `/negotiations/initiate` fires
(the TCK would have nothing to send `providerId`/`connectorAddress` from
otherwise). There is no equivalent registration race on this side. A single
attempt with `callbackHTTPClient`'s existing timeout is the design; the
first real TCK run is what confirms or refutes that, the same way every
other timing assumption in this project has been settled.

The outbound `ContractRequestMessage` carries `callbackAddress:
config.Config.PublicURL + VersionPath` — this connector's own address,
exactly the pattern `buildAgreementMessage` already uses for the provider
role's outbound `AgreementMessage.CallbackAddress`. The response is a
synchronous `ContractNegotiation` state document; its `providerPid` is
parsed out and written to the row with a plain (non-CAS) update — nothing
else ever writes this field, and by the time this goroutine is parsing the
provider's synchronous response, the provider has not yet had the chance to
start its own asynchronous push (which, on this project's own precedent,
only begins after its handler has already returned that same response).

This is genuinely new code, not a `pushCallback` reuse: `pushCallback`
discards the response body entirely, correct for a fire-and-forget push, but
this call's entire purpose is reading `providerPid` back out of a
synchronous response.

### Reacting to what the provider sends

Three inbound pushes need a reaction, each dispatched as its own goroutine
for the same §23.8 reason, then reusing `pushCallback` for the outbound leg
(fire-and-forget, retried, status-code-only — correct here because these are
acknowledgment-style sends, matching the provider role's own outbound
pushes):

**Receiving an offer (`POST /negotiations/{consumerPid}/offers`).** Three
outcomes, decided by comparing the received offer against what this
connector originally requested (`dataset_id`/`offer_id`, read from its own
row — nothing here is inferred from a catalog, since a consumer has none):

1. The offer's `@id` matches the original ask, and its `permission` carries
   no constraint outside what this connector can enforce (empty, or a
   validity-period `AtomicConstraint` — the same bar `DECISIONS.md` §14 set
   for the provider role) → send `ACCEPTED`
   (`CN_C:01-01`).
2. The offer's `@id` does not match, constraints are within bounds, and this
   negotiation has not yet re-requested → send exactly one counter,
   repeating the original `dataset_id`/`offer_id` (`SetConsumerRerequested`,
   the same one-shot CAS shape `DECISIONS.md` §23.9 used for the provider
   role's re-request rule) → stays `REQUESTED` (`CN_C:01-02`; the provider's
   own subsequent termination is out of this connector's control).
3. The offer carries a constraint this connector cannot enforce, or a
   mismatch after the one re-request has already been spent → terminate
   (`CN_C:01-03`).

Rule 3's constraint clause is the least confirmed part of this design — see
Risks. It is the best account available from published sources: `CLAUDE.md`
already states "any other constraint parses, then the negotiation is
rejected. Never accept a constraint that is not enforced" as a project-wide
policy convention, and nothing before this milestone has had a constraint on
the wire to apply it to. If the real TCK's `CN_C:01-03` fixture turns out
not to be constraint-shaped, the fallback account is a second re-request
budget check, structurally identical to rule 2 with no re-request remaining
— the code path is the same `SetConsumerRerequested` CAS either way, only
the *trigger* differs, so this does not change the shape of the
implementation, only which condition selects it.

**Receiving an agreement (`POST /negotiations/{consumerPid}/agreement`).**
No decision: this connector does not evaluate agreement terms, the same
choice the provider role made for the request it receives. Send
verification, unconditionally. `CN_C:01-01` and `CN_C:01-04` (the direct
`AGREED` path, no offer ever pushed) both terminate here identically.

**Receiving a `FINALIZED` event (`POST /negotiations/{consumerPid}/events`,
dispatched to the consumer branch).** Terminal. `SetConsumerState` to
`FINALIZED`, `200` ack, nothing sent back.

**Receiving a termination (`POST /negotiations/{id}/termination`, dispatched
to the consumer branch).** `SetConsumerState` to `TERMINATED`, `200` ack.

### State machine

```
(initiate) → REQUESTED → OFFERED (transient, resolved same-goroutine) → ACCEPTED → AGREED → VERIFIED → FINALIZED
                  ↓                    ↓                                    ↓
              TERMINATED           TERMINATED                          (no validity check — mirrors
                                  (rule 2 or 3)                          the provider role's own
                                                                         VERIFIED→FINALIZED, §23's
                                                                         "no check" precedent)
```

`OFFERED` is not durably written as an intermediate state the way the
provider role's `OFFERED` is: receiving an offer and deciding
accept/counter/terminate happens within the same handler invocation, so the
row moves directly from `REQUESTED` to whatever the decision produces. This
mirrors `handleContractRequest`'s own shape in the provider role — decide,
then transition once — rather than the provider's own `OFFERED`, which is
durable there only because it is a real waiting point for an external
party's next move (the consumer's) between messages. Here, this connector
*is* the one deciding, synchronously with the message that requires the
decision.

## Testing

| Layer | Cases |
|---|---|
| Store | `consumer_negotiations` create/read/CAS-update, `rerequested` CAS, table creation is idempotent across two opens (alongside the existing `negotiations` suite, unchanged) |
| Negotiation documents | request/offer/agreement/event/verification/termination/state-document shapes for the consumer-sent messages; `Constraint` decoding (`AtomicConstraint` shape, and that a `LogicalConstraint` — `and`/`or` — counts as unsupported) |
| Decision logic | the three offer-reaction outcomes above, both success and rejection; the agreement→verification path takes no decision |
| Handlers (`httptest`) | `/negotiations/initiate` (happy path, missing-field 400, `connectorAddress` rejected by `validateCallbackURL`), `/negotiations/{id}/offers`, `/negotiations/{id}/agreement`; the three dispatch handlers, both branches (an existing provider-role negotiation and a new consumer-role one, same `{id}` space, asserting no cross-talk) |
| Outbound client (`negotiation_client.go`) | initial request against a fake provider server (success, and the provider's synchronous 4xx path), the three acknowledgment-style sends against a fake provider using `pushCallback`'s existing retry test patterns |
| Goroutine-dispatch regression | the same shape as the provider milestone's `TestSynchronousResponseDoesNotWaitForTheCallbackPush` — `/negotiations/initiate` must return before the outbound request to a slow fake provider completes |
| TCK | `make tck` green with `CN_C` in the gate, `CN`'s existing 14-of-15 unaffected |

## Gate

```go
var expected = map[string]int{"MET": 1, "CAT": 3, "CN": 15, "CN_C": 16}
```

`exempt` is not extended unless a real TCK run shows a `CN_C` test this
design cannot account for, the same honesty bar `DECISIONS.md` §23.5 set for
`CN:02-07` — no exemption is assumed in advance for this suite. If one turns
out to be needed, it is added the same way, named and documented, not
silently dropped.

## TCK harness

`test/tck/config.properties` already has
`dataspacetck.dsp.connector.negotiation.initiate.url` from the previous
milestone. This milestone adds the `CN_C_xx_yy_DATASETID`/`OFFERID` keys the
TCK's `@ConfigParam`-injected test fields read (same mechanism the `CN`
suite already uses, confirmed working), left at their random defaults except
where a specific test needs a deterministic value the design above depends
on — the exact set is determined once the full `CN_C` test file (all
sub-suites, not only the `01` group read for this design) is read during
implementation.

No `test/tck/dsbox.yaml` changes are anticipated: this connector's own
`datasets` config describes what it *advertises* as provider, which the
consumer role never consults.

## Documentation

- `README.md`: status table's `CN_C` row moves from "not started" to "gated
  in CI, N of 16" (N determined by the real run); total pass count updated.
- `docs/follow-ups.md`: an entry for any `CN_C` exemption this milestone
  turns out to need, following the `CN:02-07` precedent — only if one is
  needed.
- `DECISIONS.md`: a new section recording the second-table-over-shared-table
  choice, the initiate endpoint's unauthenticated TCK-only scope (and that a
  real management trigger is deliberately not built here), the
  no-retry-on-the-initial-request choice, and the constraint-based
  offer-reaction rule with its trade-off.

## Done criteria

1. `make tck` passes with `CN_C` in the gate's expected map, `CN` still 14 of
   15 with `CN:02-07` exempted, green in CI
2. `go test ./...` passes and covers every case in the testing table
3. `README.md` reflects the real `CN_C` pass count and the new total
4. `DECISIONS.md` records the new decisions with their trade-offs
5. A fresh clone can negotiate as consumer against a manually-run second
   `dsbox` instance playing provider — a manual end-to-end walkthrough, the
   same bar the provider milestone's done criteria set

## Risks

| Risk | Mitigation |
|---|---|
| The offer-reaction rule's constraint clause (rule 3 above) is the least confirmed part of this design — inferred from a project-wide policy convention (`CLAUDE.md`) that nothing has implemented yet, not from a targeted TCK assertion read during brainstorming | Implement as designed; the first real run of `CN_C:01-02` and `CN_C:01-03` in the same suite either confirms the constraint-shaped account or shows the actual fixture, the same way `CN:01-02` vs `CN:03-04` was settled for the provider role |
| Only `CN_C`'s `01` group (4 tests) was read in full during brainstorming; `02` and `03` groups (12 more tests) may require additional decision branches this design does not yet name | The task brief for whichever implementation task reaches this fetches and reads the full `ContractNegotiationConsumer02Test`/`03Test` source before writing the decision function, the same evidence-gathering step this design itself used for the `01` group |
| The three-endpoint routing dispatch (events/termination/GET) could silently favor the wrong role if a `consumer_pid` and a `provider_pid` ever collided | Both are generated by the same `store.NewUUID` (122 bits of randomness); a collision is the same order of risk this project already accepts for every other generated pid, not a new one this milestone introduces |
| No retry on the initial outbound request (unlike every other outbound call in this codebase, which either retries via `pushCallback` or is provider-authoritative) could mean a transient failure strands a negotiation with no consumer-side recovery | Acceptable for v1 under the same "no fallback for scenarios that can't happen" principle the provider role's un-retried async pushes already rely on; revisit if a real TCK run shows the provider mock genuinely racy from this side too |

## What this unlocks

The transfer process protocol (`TP`, `TP_C`), the first consumer of a
`FINALIZED` agreement from either role. A real management-API trigger for
starting a negotiation, deliberately deferred here, becomes the natural next
use of `negotiation_client.go`'s outbound path once the management API
itself is a real milestone rather than a `GET /health` stub.
