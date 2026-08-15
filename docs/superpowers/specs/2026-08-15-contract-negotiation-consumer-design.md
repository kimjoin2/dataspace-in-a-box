# Design: the contract negotiation protocol (consumer role)

Date: 2026-08-15 (revised after independent cross-check against the TCK's
full `CN_C` test source — see "Revision note" below)
Status: approved, ready for implementation planning

The fourth sub-project. It adds the DSP contract negotiation protocol in the
**consumer** role, completing `CN` end to end and making the `CN_C` suite (16
tests) join the compliance gate. The provider role (`CN`, 14 of 15 gated,
`CN:02-07` a named exemption) shipped in the previous milestone and is
untouched here except where explicitly noted.

## Revision note

The first draft of this spec inferred the offer-reaction decision rule
(accept / counter / reject) from comparing an incoming offer's content
against what this connector originally requested, and treated it as the
design's shakiest, TCK-to-be-confirmed part. An independent cross-check
fetched all 16 `CN_C` test files (the first draft had only read the 4-test
`01` group) and found that inference **false**, not merely unconfirmed: the
TCK's mock provider always echoes the CUT's own `datasetId`/`offerId` back
verbatim with an empty constraint list — there is no wire content to compare
against. Every one of the 16 tests requires the *same* wire input to produce
a *different* connector reaction. The only lever the TCK leaves is which
`datasetId` this project chooses to send in each numbered test's fixture
configuration — the same mechanism the provider milestone already used, now
applied to a decision this connector makes about itself rather than about an
advertised catalog entry. This revision rebuilds the reaction design, the
state machine, and the harness plan on that finding. The endpoint contract,
the routing-collision analysis, and the storage/callback-reuse plan were all
independently confirmed accurate and are carried over largely unchanged.

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
  own generated consumer pid, with a durably-persisted `OFFERED` state
  (unlike the first draft's assumption, `GET /negotiations/{id}` must be able
  to report it).
- An outbound HTTP client path: the initial `ContractRequestMessage`, and the
  events/verification/re-request/termination this connector sends as
  consumer.
- A small `consumer_policies` configuration list — see "Why a policy
  configuration, not a content rule" — that selects this connector's
  autonomous reaction (on an offer, on an agreement, on an idle request) per
  requested `dataset_id`. Absent an entry, the default is the sane
  production behavior: accept a matching offer, verify any agreement, never
  self-abandon.

**Out of scope**

Transfer process. `did:web`, the participant roster, JWT authentication —
same unauthenticated v1 posture as the provider role and the catalog
protocol. ODRL constraint parsing — the first draft planned this to decide
offer acceptance; the revision found the TCK never sends a non-empty
constraint list to an external CUT, so there is nothing to parse against in
this milestone. A real management-API trigger for starting a negotiation —
`/negotiations/initiate` is a TCK-shaped hook only; see "The initiate
endpoint is not a management feature" below.

## What the TCK actually requires

Evidence: the TCK's own public source repository,
[`eclipse-dataspacetck/dsp-tck`](https://github.com/eclipse-dataspacetck/dsp-tck),
fetched via the GitHub API — `HttpConsumerNegotiationClientImpl` (path
constants), `ConsumerNegotiationPipelineImpl` and
`ProviderNegotiationManagerImpl` (how the TCK's own mock provider builds and
reacts to messages when playing the provider role against an external CUT),
and all three `ContractNegotiationConsumer0{1,2,3}Test.java` files — 16
`@TestSequenceDiagram`-annotated tests read in full for this revision, not
only the 4-test `01` group the first draft used.

### The initiate endpoint is not a management feature

`DspSystemLauncher.start()` requires
`dataspacetck.dsp.connector.negotiation.initiate.url` unconditionally once
`dataspacetck.dsp.local.connector=false` — this project already set it to
`http://dsbox:8080/2025-1/negotiations/initiate` in the previous milestone,
before any consumer-role code existed, purely to unblock system startup. The
TCK's `HttpConsumerNegotiationClientImpl.initiateRequest` POSTs
`{providerId, offerId, datasetId, connectorAddress}` as **plain JSON, not
JSON-LD** (no `@context`/`@type` — confirmed from the `postJson(url, body,
false, true)` call site) to that URL and discards the response body — but it
does assert the HTTP status: `HttpFunctions.postJson` throws on `404` or a
`5xx`, and retries other `4xx` up to three times before failing. `200`
immediately is required. The test then proceeds by polling
`GET /negotiations/{consumerPid}`, never by reading anything this endpoint
itself returns.

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
out of scope here — and until it exists, `/negotiations/initiate` is this
connector's *only* way to start a negotiation as consumer, which is worth
naming plainly rather than folding silently into precedent (see
"Documentation").

### The endpoints

| Path | Direction | Purpose |
|---|---|---|
| `POST /negotiations/initiate` | TCK → CUT | Trigger hook. Not a DSP message. |
| `POST {provider}/negotiations/request` | CUT → provider | Initial `ContractRequestMessage`, sent by this connector after `initiate` fires. |
| `POST /negotiations/{consumerPid}/offers` | provider → CUT | `ContractOfferMessage`. |
| `POST {provider}/negotiations/{providerPid}/request` | CUT → provider | A counter/re-request repeating this connector's original ask. |
| `POST {provider}/negotiations/{providerPid}/events`, type `ACCEPTED` | CUT → provider | Sent when this connector's policy accepts an offer. |
| `POST /negotiations/{consumerPid}/agreement` | provider → CUT | `ContractAgreementMessage`. |
| `POST {provider}/negotiations/{providerPid}/agreement/verification` | CUT → provider | Sent when this connector's policy accepts an agreement. |
| `POST /negotiations/{consumerPid}/events`, type `FINALIZED` | provider → CUT | Terminal success, legal only from `VERIFIED`. |
| `POST .../negotiations/{id}/termination` | either direction | Same path shape both ways — see "Routing" below. |
| `GET /negotiations/{consumerPid}` | provider/TCK → CUT | State document — same path shape as the provider role's status endpoint. |

`{id}` in every provider-pushed path is `consumerPid` — this connector's own
generated identifier, known the instant `/negotiations/initiate` fires,
before the outbound request is even sent. This is the mirror image of the
provider milestone, where `{id}` was the provider's own pid; each role's
table is keyed by its own generated identifier, not the counterparty's.

`offerId`/`datasetId` in the outbound `ContractRequestMessage` are the exact
strings `/negotiations/initiate` received — echoed verbatim, never
regenerated. The TCK's own mock provider
(`ProviderNegotiationManagerImpl.handleInitialRequest`) recovers `datasetId`
from the offer id it receives via `datasetIdFromOfferId`, which assumes the
TCK's own `offerIdFromDatasetId` convention (`"offer" + datasetId`, no
separator) — a different shape from this connector's own provider-role
convention (`catalog.go`'s `offerIDSuffix`, `datasetID + "#offer"`). The two
must never be conflated: this connector's own offer-id convention governs
what it *advertises*; the consumer role only ever relays what it was told to
ask for.

### Routing: three endpoints already exist, for the other role

`POST /negotiations/{id}/events`, `POST /negotiations/{id}/termination`, and
`GET /negotiations/{id}` are registered today in `router.go` for the
provider role. Confirmed against a real build: Go's `http.ServeMux` panics
at startup on a second registration of an identical pattern — including a
version with the wildcard renamed (`{id}` vs `{cid}`), which is not an
escape hatch. These three handlers become dispatchers: look the path
parameter up in the provider store first (by `provider_pid`, today's
behavior, unchanged); if not found, look it up in the new consumer store (by
`consumer_pid`); if found there, run the new consumer-role logic; if found
in neither, 404, same as today. The two genuinely new paths, `.../offers`
and `.../agreement`, have no provider-role equivalent and register cleanly
alongside the existing six.

## Why a policy configuration, not a content rule

All 16 `CN_C` tests send this connector the identical shape of message for a
given juncture — the same offer, the same agreement, the same termination —
and yet require different reactions across tests. Concretely: `CN_C:01-01`
requires an offer to be auto-accepted; `CN_C:02-04` requires the identical
kind of offer to produce *no* autonomous action at all, just a durable
`OFFERED` wait state; `CN_C:01-03` requires the same shape of offer to
produce an immediate, unprompted termination; `CN_C:01-02` requires it to
produce exactly one counter-request repeating the original ask. Nothing in
the offer itself distinguishes these cases — `ConsumerNegotiationPipelineImpl.sendOfferMessage`
builds it from `providerNegotiation.getOfferId()`/`getDatasetId()`, which are
in turn read straight from the exact `ContractRequestMessage` this connector
sent, and `NegotiationFunctions.createOfferPolicy` hard-codes an empty
`constraints` list. There is no content-based signal a real DSP consumer
could use here even in principle, for these specific tests.

`AbstractContractNegotiationConsumerTest` confirms the only per-test lever
this project controls is `datasetId` (`@ConfigParam`, defaulting to a random
UUID unless overridden in `test/tck/config.properties`) — the same mechanism
`CN_01_04_DATASETID` already uses for the provider role. So this connector's
own reaction must be a function of *which dataset it itself chose to
request*, not of anything it receives back. This is not a TCK-only
contrivance: it is a reasonable shape for a real automated consumer too — a
connector configured in advance to negotiate for specific datasets under
specific acceptance rules, without a human approving every message, is a
sensible "minimum operational dataspace" feature. It has no real trigger
yet (see "The initiate endpoint is not a management feature"), so today it
is exercised only by the TCK harness — worth being honest about in
`DECISIONS.md` rather than overselling it as already a product feature.

### The policy shape

Three independent choices, each defaulting to the happy-path behavior a real
consumer should have with no configuration at all:

```yaml
consumer_policies:
  - dataset_id: urn:dataset:cn-c-passive-offer
    on_offer: passive       # accept (default) | passive | reject | counter
  - dataset_id: urn:dataset:cn-c-reject-agreement
    on_agreement: reject    # verify (default) | reject
  - dataset_id: urn:dataset:cn-c-abandon
    on_idle: abandon        # wait (default) | abandon
```

Matched against the `datasetId` this connector itself sent in its initial
request (never against a catalog — the consumer role has none). An
unmatched `dataset_id` gets every field's default: accept, verify, wait.

| Field | Value | Behavior |
|---|---|---|
| `on_offer` | `accept` (default) | Send `ACCEPTED` immediately on receiving an offer. |
| `on_offer` | `passive` | Take no action; the negotiation durably holds `OFFERED`. |
| `on_offer` | `reject` | Send `TERMINATED` immediately on receiving an offer. |
| `on_offer` | `counter` | Send exactly one re-request repeating the original `dataset_id`/`offer_id` (a real CAS-guarded one-shot, mirroring `DECISIONS.md` §23.9's provider-role rule). |
| `on_agreement` | `verify` (default) | Send `ContractAgreementVerificationMessage` immediately on receiving an agreement. |
| `on_agreement` | `reject` | Send `TERMINATED` immediately on receiving an agreement. |
| `on_idle` | `wait` (default) | Take no action after the initial request; wait indefinitely (bounded only by the TCK's own patience window) for the provider's next move. |
| `on_idle` | `abandon` | Send `TERMINATED` immediately after the initial request's synchronous acknowledgement — no delay, no timer. Confirmed safe: `CN_C:02-02`'s sequence has no offer or agreement message at all before the CUT's termination, so there is nothing to race against. |

### Deriving all 16 tests from this policy plus two universal rules

Two rules apply regardless of policy and need no configuration:

- **Termination is always legal from any non-terminal state** and always
  moves the negotiation to `TERMINATED` — the same rule `DECISIONS.md`'s
  provider-role transition table already states for the provider side.
  Covers `CN_C:02-01`, `02-04`, `02-05`, `02-06` (all: reach some state via
  the default policy, then the provider terminates unprompted; this
  connector's only job is to accept it).
- **A message that arrives in a state where it is not a legal transition is
  a synchronous 4xx**, state unchanged — the consumer-role mirror of
  `DECISIONS.md`'s `CN:03` table for the provider role. See "Structural
  guards" below.

| Test | `dataset_id` policy | Reaction sequence |
|---|---|---|
| `CN_C:01-01` | default | offer→accept, agreement→verify, finalized→ack |
| `CN_C:01-02` | `on_offer: counter` | offer→counter; provider terminates (out of this connector's control) |
| `CN_C:01-03` | `on_offer: reject` | offer→terminate |
| `CN_C:01-04` | default | (no offer) agreement→verify, finalized→ack |
| `CN_C:02-01` | default | (nothing received) provider terminates → ack |
| `CN_C:02-02` | `on_idle: abandon` | (nothing received) this connector terminates unprompted |
| `CN_C:02-03` | `on_agreement: reject` | (no offer) agreement→terminate |
| `CN_C:02-04` | `on_offer: passive` | offer→(no action, durable `OFFERED`); provider terminates → ack |
| `CN_C:02-05` | default | offer→accept; provider terminates instead of agreeing → ack |
| `CN_C:02-06` | default | (no offer) agreement→verify; provider terminates instead of finalizing → ack |
| `CN_C:03-01` | default | finalized while `REQUESTED` → 4xx (structural guard) |
| `CN_C:03-02` | `on_offer: passive` (same dataset as `02-04`) | offer→(no action); agreement while `OFFERED` → 4xx (structural guard) |
| `CN_C:03-03` | `on_offer: passive` (same dataset as `02-04`/`03-02`) | offer→(no action); finalized while `OFFERED` → 4xx (structural guard) |
| `CN_C:03-04` | default | offer→accept; finalized while `ACCEPTED` → 4xx (structural guard) |
| `CN_C:03-05` | default | offer→accept; a second offer while `ACCEPTED` → 4xx (structural guard) |
| `CN_C:03-06` | default | offer→accept→agreement→(verifying); finalized arrives before this connector's own verification is acknowledged → 4xx — see "The `03-06` timing question" |

Six distinct `dataset_id` fixtures cover all 16 tests: default (9 tests),
`on_idle: abandon` (1), `on_agreement: reject` (1), `on_offer: passive` (3),
`on_offer: reject` (1), `on_offer: counter` (1).

### Structural guards

The consumer-role mirror of the provider milestone's `CN:03` table:

| Message | Legal from | Illegal from |
|---|---|---|
| Offer | `REQUESTED` | `OFFERED`, `ACCEPTED`, `AGREED`, `VERIFIED` (`CN_C:03-05`) |
| Agreement | `REQUESTED`, `ACCEPTED` (§"the direct-agreement path", `CN_C:01-04`) | `OFFERED` (`CN_C:03-02`) |
| Finalized event | `VERIFIED` only | `REQUESTED` (`03-01`), `OFFERED` (`03-03`), `ACCEPTED` (`03-04`), `AGREED` before this connector's own verification lands (`03-06`) |
| Termination | any non-terminal state | (none — always legal, per the universal rule above) |

### The `03-06` timing question

`CN_C:03-06` polls until `AGREED` is observed, then immediately sends an
illegal `FINALIZED` event and asserts the negotiation is still `AGREED`, not
`VERIFIED`. If this connector set its local state to `VERIFIED` the instant
it *sent* its verification message, a fast enough round trip could make the
illegal `FINALIZED` event arrive after that local write — at which point
`FINALIZED` from `VERIFIED` is legal, and the test would see a `200`
where it expects a `4xx`. The provider milestone hit the identical shape of
problem for `CN:03-03` and settled it by pushing before storing
(`DECISIONS.md` §23.12: the provider does not become `AGREED` and then
announce it, it becomes `AGREED` *by delivering* the agreement). The
consumer-role mirror: this connector does not become `VERIFIED` until its
own verification POST has been synchronously acknowledged by the provider —
state moves to `VERIFIED` only after that response returns success, not when
the request is sent. This keeps "not yet `VERIFIED`" and "the provider does
not yet know this connector verified" the same fact, exactly as §23.12
reasons for the provider role. Flagged as the design's one remaining timing
assumption — see Risks.

## Architecture

```
internal/store/store.go                consumer_negotiations CRUD, alongside the existing table
internal/store/store_test.go
internal/config/config.go              + ConsumerPolicy, + Config.ConsumerPolicies
internal/dsp/negotiation.go            + consumer-role message builders
internal/dsp/negotiation_test.go
internal/dsp/negotiation_handler.go    + initiate/offers/agreement handlers, dispatch added to 3 existing ones
internal/dsp/negotiation_handler_test.go
internal/dsp/negotiation_client.go     new: the outbound calls this connector makes as consumer
internal/dsp/negotiation_client_test.go
internal/dsp/callback.go               reused as-is: validateOutgoingCallback, pushCallback, callbackHTTPClient
internal/dsp/router.go                 + 3 new routes, 3 existing ones rebound to dispatchers
cmd/dsbox/main.go                      unchanged — already passes the one *store.Store into dsp.NewRouter
```

`negotiation_client.go` is a new file, not new functions bolted onto
`negotiation_handler.go`: everything in `negotiation_handler.go` today
answers an inbound HTTP request; the outbound calls this milestone adds
are a different responsibility, and the file is a natural unit a reader can
understand in isolation, per this project's own design-for-isolation
convention.

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
every row carries columns that mean different things depending on role, and
every provider-role query would need a `WHERE role = 'provider'` this
milestone did not need to add. A second table costs one more `CREATE TABLE
IF NOT EXISTS` and a handful of CRUD functions shaped exactly like the ones
`store.go` already has: `CreateConsumer`, `GetConsumer(consumerPID)`,
`SetConsumerState(consumerPID, from, to, updatedAt)`,
`SetConsumerRerequested(consumerPID)` — the same compare-and-swap shape as
`SetState`/`SetRerequested`, for the same reason: consumer-role reactions
also run in goroutines and can outlive a termination that arrived in the
meantime. `explainNoUpdate` is provider-table-specific (it hard-codes a
`Get` against `negotiations`); the consumer table needs its own equivalent
rather than reusing that one, or the error message would name the wrong
table's state.

`migrate`'s multi-statement handling needs one more check during
implementation: today's `schema` const is a single `CREATE TABLE`, and
`db.Exec` on `modernc.org/sqlite` has not yet been exercised with two
statements in one call. Adding `consumer_negotiations` as a second statement
in the same `schema` string should be verified to execute both, or split
into two separate `db.Exec` calls if it does not — a five-minute check, not
a design fork.

`provider_base_url` is `connectorAddress` from the initiate call, stored
because every subsequent outbound call this connector makes (re-request,
accepted event, verification) needs it and the initiate call is the only
place it is ever supplied. No `callback_address` column: unlike the provider
role, this connector's own callback address is not per-negotiation data — it
is always `config.Config.PublicURL + VersionPath`, already computed at
startup, same as `buildAgreementMessage` already reads it today.

### The initial request: goroutine dispatch, no retry

`handleInitiate` validates the four required fields, runs `connectorAddress`
through `validateOutgoingCallback` (the same SSRF guard the provider role
already applies to a consumer-supplied `callbackAddress`, held as a
package-level `var` specifically so tests can relax it for `httptest` —
`/negotiations/initiate` is unauthenticated, so an anonymous caller fully
controls where this connector's outbound POSTs go, exactly the concern
`DECISIONS.md` §23.11 already accepted for the provider's negotiation
surface), generates a `consumer_pid`, persists a row with `state =
REQUESTED` and `provider_pid = ""` (mirroring the provider role's own table,
whose first observable state is also `REQUESTED`, never a wire-visible
`INITIALIZED`), and responds `200` immediately — then dispatches the
outbound request in a goroutine.

The goroutine requirement is `DECISIONS.md` §23.8's lesson, carried forward
unchanged: `net/http` does not put a response on the wire until the handler
returns, so any outbound call made inline would hold the `200` back behind
it.

Unlike `pushCallback`, this call is not retried. `pushCallback`'s backoff
schedule exists because the TCK's own callback *listener* registration is a
sequential stage that can still be running when this connector's push
arrives (`DECISIONS.md` §23.7) — a race on the *receiving* side. Here, this
connector is the one initiating, against a provider mock that is already a
live, already-listening server by the time `/negotiations/initiate` fires.
There is no equivalent registration race on this side. A single attempt
with `callbackHTTPClient`'s existing timeout is the design; the first real
TCK run confirms or refutes that.

The outbound `ContractRequestMessage` carries `callbackAddress:
config.Config.PublicURL + VersionPath` and echoes `datasetId`/`offerId`
verbatim from the initiate call (see "The endpoints" above — never
regenerated). The response is a synchronous `ContractNegotiation` state
document; its `providerPid` is parsed out and written to the row with a
plain (non-CAS) update — nothing else ever writes this field. This write
races against the provider's own async pushes only in principle; in
practice `CN_C:02-01`'s own termination is what arrives soonest after
`REQUESTED`, and its `sendTermination()` call is what actually bounds that
window (not, as an earlier draft of this section claimed, some general
"the provider hasn't had the chance yet" reasoning — the provider's mock
handler returns and could in principle push immediately).

This is genuinely new code, not a `pushCallback` reuse: `pushCallback`
discards the response body entirely, correct for a fire-and-forget push, but
this call's entire purpose is reading `providerPid` back out of a
synchronous response.

### Reacting to what the provider sends

Each of the three inbound pushes (`offers`, `agreement`, and the
consumer-branch of `events`/`termination`) is dispatched as its own
goroutine for the same §23.8 reason, then:

- **Structural guard first.** If the current state does not permit this
  message (see "Structural guards" above), respond `4xx` synchronously and
  make no state change. This check runs before any policy lookup.
- **Policy lookup**, keyed by the row's own `dataset_id` against
  `config.Config.ConsumerPolicies` (see "Why a policy configuration, not a
  content rule").
- **Outbound leg reuses `pushCallback`** (fire-and-forget, retried,
  status-code-only) for every message this connector sends in reaction —
  correct here because these are acknowledgment-style sends, matching the
  provider role's own outbound pushes.

## Testing

| Layer | Cases |
|---|---|
| Store | `consumer_negotiations` create/read/CAS-update, `rerequested` CAS, its own `explainNoUpdate`, table creation idempotent across two opens (alongside the existing `negotiations` suite, unchanged) |
| Config | `consumer_policies` parses, an unmatched `dataset_id` gets every field's default, an invalid enum value on `on_offer`/`on_agreement`/`on_idle` is rejected at load |
| Negotiation documents | request/offer/agreement/event/verification/termination/state-document shapes for the consumer-sent messages |
| Policy resolution | all four `on_offer` values, both `on_agreement` values, both `on_idle` values, and the unmatched-default case |
| Structural guards | all four rows of the guard table, both success and rejection |
| Handlers (`httptest`) | `/negotiations/initiate` (happy path, missing-field 400, `connectorAddress` rejected by `validateOutgoingCallback`), `/negotiations/{id}/offers`, `/negotiations/{id}/agreement`; the three dispatch handlers, both branches (an existing provider-role negotiation and a new consumer-role one, same `{id}` space, asserting no cross-talk) |
| Outbound client (`negotiation_client.go`) | initial request against a fake provider server (success, and the provider's synchronous 4xx path), the four reaction sends against a fake provider using `pushCallback`'s existing retry test patterns |
| Goroutine-dispatch regression | the same shape as the provider milestone's `TestSynchronousResponseDoesNotWaitForTheCallbackPush` — `/negotiations/initiate` must return before the outbound request to a slow fake provider completes |
| Verification-ordering regression | the consumer-role mirror of `TestVerificationIsRejectedWhileTheAgreementIsStillInFlight` — a `FINALIZED` event arriving while this connector's own verification POST is still in flight must be rejected, state stays `AGREED` |
| TCK | `make tck` green with `CN_C` in the gate, `CN`'s existing 14-of-15 unaffected |

## Gate

```go
var expected = map[string]int{"MET": 1, "CAT": 3, "CN": 15, "CN_C": 16}
```

`exempt` is not extended unless a real TCK run shows a `CN_C` test this
design cannot account for — no exemption is assumed in advance for this
suite, the same honesty bar `DECISIONS.md` §23.5 set for `CN:02-07`. Given
this revision derives all 16 tests from a concrete mechanism rather than an
inferred content rule, 16 of 16 is the actual target, not a best effort.

## TCK harness

`test/tck/config.properties` already has
`dataspacetck.dsp.connector.negotiation.initiate.url` from the previous
milestone. This milestone adds `CN_C_xx_yy_DATASETID` keys — confirmed to be
the *only* `@ConfigParam` field `AbstractContractNegotiationConsumerTest`
declares; an `OFFERID` counterpart does not exist for the consumer suite
(unlike the provider suite's `CN_xx_yy_OFFERID`, which is real because
`AbstractContractNegotiationProviderTest` declares both fields) — mapped per
the six-fixture table above: the nine default-policy tests need no override
(a random `datasetId` with no matching `consumer_policies` entry already
gets the default policy); the other seven point at one of five distinct
non-default `dataset_id` values (three of `03`'s tests share the `on_offer:
passive` fixture with `02-04`).

`test/tck/dsbox.yaml` gains a `consumer_policies` section with the five
non-default fixtures. No changes to its existing `datasets` list — that
describes what this connector *advertises* as provider, which the consumer
role never consults.

## Documentation

- `README.md`: status table's `CN_C` row moves from "not started" to "gated
  in CI, 16 of 16"; total pass count updated.
- `docs/follow-ups.md`: an entry for any `CN_C` exemption this milestone
  turns out to need, following the `CN:02-07` precedent — only if one is
  needed.
- `DECISIONS.md`: a new section recording the second-table-over-shared-table
  choice, the initiate endpoint's unauthenticated TCK-only scope (and that a
  real management trigger is deliberately not built here — today
  `/negotiations/initiate` is this connector's only way to start a
  negotiation as consumer), the no-retry-on-the-initial-request choice, the
  policy-configuration mechanism (named honestly as TCK-driven today, with
  no real trigger yet to make it a product feature), and the
  verify-before-store ordering for `CN_C:03-06`.

## Done criteria

1. `make tck` passes with `CN_C` in the gate's expected map at 16, `CN` still
   14 of 15 with `CN:02-07` exempted, green in CI
2. `go test ./...` passes and covers every case in the testing table
3. `README.md` reflects the real `CN_C` pass count and the new total
4. `DECISIONS.md` records the new decisions with their trade-offs
5. A fresh clone can negotiate as consumer against a manually-run second
   `dsbox` instance playing provider — a manual end-to-end walkthrough, the
   same bar the provider milestone's done criteria set

## Risks

| Risk | Mitigation |
|---|---|
| `CN_C:03-06`'s illegal-`FINALIZED`-while-verifying case depends on this connector not marking itself `VERIFIED` until its own verification POST is acknowledged (see "The `03-06` timing question") — the same class of timing dependency `DECISIONS.md` §23.12 accepted for the provider role, not a new kind of risk this milestone introduces | Implement verify-then-store as designed; the first real TCK run against `CN_C:03-06` confirms or refutes it, the same way §23.12 was settled |
| The `on_idle: abandon` policy fires immediately with no delay — correct per `CN_C:02-02`'s sequence diagram (no offer/agreement ever sent), but if a future `CN_C` revision or a real deployment scenario ever needs a genuine idle timeout instead of an immediate abandon, this design has no timer mechanism to extend | Not needed for the 16 tests read for this design; if it becomes needed, `on_idle: abandon` is one policy value among several and gains a duration field without disturbing the other three |
| No retry on the initial outbound request (unlike every other outbound call in this codebase) could mean a transient failure strands a negotiation with no consumer-side recovery | Acceptable for v1 under the same "no fallback for scenarios that can't happen" principle the provider role's un-retried async pushes already rely on |
| `modernc.org/sqlite`'s multi-statement `Exec` behavior for the new table is unverified (see Storage) | A five-minute check during implementation, with a two-`Exec`-calls fallback already named |

## What this unlocks

The transfer process protocol (`TP`, `TP_C`), the first consumer of a
`FINALIZED` agreement from either role. A real management-API trigger for
starting a negotiation, deliberately deferred here, becomes the natural next
use of `negotiation_client.go`'s outbound path once the management API
itself is a real milestone rather than a `GET /health` stub — at which point
`consumer_policies` stops being TCK-only and becomes a real operator-facing
feature.
