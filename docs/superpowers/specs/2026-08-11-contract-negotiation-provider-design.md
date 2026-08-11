# Design: the contract negotiation protocol (provider role)

Date: 2026-08-11
Status: approved, ready for implementation planning

The third sub-project. It adds the DSP contract negotiation protocol in the
**provider** role only and makes the `CN` suite (15 tests) join the compliance
gate. The consumer role (`CN_C`, 16 tests) is a separate, later sub-project: it
needs a webhook the TCK uses to tell the connector to *initiate* a negotiation,
which nothing in this milestone builds.

## Scope

**In scope**

- Six DSP endpoints on the public listener, all under `/negotiations`
- A `negotiations` table in SQLite — the first runtime state this project
  persists, and the point at which `DECISIONS.md` §8's justification for
  SQLite stops being aspirational
- A minimal migration mechanism: `CREATE TABLE IF NOT EXISTS` run at startup,
  closing the "migration approach" item `DECISIONS.md` has carried as
  undecided since the concept design
- A validity-period constraint on advertised offers — the second of the two
  policy shapes §14 already permits, given its first real use here
- Provider-side contract negotiation state machine: the eight DSP states,
  transitions, and the synchronous/asynchronous split the protocol requires
- A `modernc.org/sqlite` dependency (pure Go, no CGO) and a hand-rolled
  provider-pid generator (`crypto/rand`, no new dependency for that)

**Out of scope**

The consumer role and `CN_C`. Transfer process. `did:web`, the participant
roster, JWT authentication — negotiation messages are unauthenticated in v1,
same posture as the catalog protocol. Any policy constraint other than
unrestricted use and validity period. Retrying a failed asynchronous push to a
consumer's callback address. A general "provider decides to terminate for
business reasons" mechanism — the only reason implemented is an expired
validity period.

## What the TCK actually requires

As with the catalog milestone, this section is evidence, not inference. Two
independent public sources were used, both permitted under `CLAUDE.md`'s
"published, publicly obtainable sources" rule because both are the official
TCK: the pinned Docker image's bytecode (`javap` against
`eclipsedataspacetck/dsp-tck-runtime@sha256:45cfafa4…`, the same digest already
in `test/tck/compose.yaml`), and the TCK's own public source repository,
[`eclipse-dataspacetck/dsp-tck`](https://github.com/eclipse-dataspacetck/dsp-tck),
fetched via the GitHub API at the commit the pinned image was built from. The
source repository carries `@TestSequenceDiagram` annotations on every test
method — a literal sequence diagram of what the TCK sends and expects — which
made most of what follows a matter of reading rather than guessing.

### The endpoints — six, not five

Bytecode inspection of `HttpProviderNegotiationClientImpl` found five path
constants: `GET_PATH`, `REQUEST_PATH`, `EVENT_PATH`, `VERIFICATION_PATH`,
`REQUEST_OFFER_PATH`. A sixth exists but lives in the shared base class
`AbstractHttpNegotiationClient`, which the first bytecode pass did not
decompile deeply enough to surface — reading the real source found it
immediately:

| Path | Method | Purpose |
|---|---|---|
| `POST /negotiations/request` | `contractRequest` | Initial request. Always the entry point. |
| `POST /negotiations/{id}/request` | `contractOfferRequest` | A consumer counter-offer, or a resend. |
| `POST /negotiations/{id}/events` | `accept` | `ContractNegotiationEventMessage`, `eventType: ACCEPTED`. |
| `POST /negotiations/{id}/agreement/verification` | `verify` | `ContractAgreementVerificationMessage`. |
| `POST /negotiations/{id}/termination` | `terminate` (in `AbstractHttpNegotiationClient`) | `ContractNegotiationTerminationMessage`, from either party. |
| `GET /negotiations/{id}` | `getNegotiation` | The `ContractNegotiation` state document. |

`{id}` throughout is the **provider's own generated pid** — confirmed by
`ProviderNegotiationPipelineImpl.sendRequestMessage`, which reads
`providerPid` out of the connector's synchronous response to the initial
request and uses that value as `{id}` for every subsequent call. The consumer
never gets to choose this identifier.

Three async pushes go the other way, from provider to the consumer's
`callbackAddress` (confirmed in `HttpConsumerNegotiationClientImpl`):
`{callback}/negotiations/{pid}/offers`, `{callback}/negotiations/{pid}/agreement`,
and `{callback}/negotiations/{pid}/events` (for the `FINALIZED` event).
Termination can be pushed by either side, to the same
`{callback}/negotiations/{pid}/termination` shape.

### The protocol is asynchronous, not synchronously validated

The first attempt at this design assumed the initial request would be
synchronously rejected — 400 with a `ContractNegotiationError` — for a dataset
or offer the connector does not recognize, mirroring how the catalog protocol
rejects a malformed request. `CN:02-01`'s sequence diagram rules this out:

```
TCK->>CUT: ContractRequestMessage
CUT-->>TCK: ContractNegotiation
CUT->>TCK: ContractNegotiationTerminationMessage
```

The synchronous response is a plain success (the negotiation resource in
`REQUESTED` state) even when the request names a dataset the connector has
never heard of. Rejection is asynchronous: the provider decides afterward and
pushes a termination message. The only synchronous 400 path is a **structurally**
malformed message — not JSON, wrong `@type`, missing `@context` — the same
class of check the catalog protocol already makes.

### The offer/agreement divergence is a configuration fixture, not a runtime inference

`ContractNegotiationProvider01Test.cn_01_03` and `cn_01_04` call the identical
line, `sendRequestMessage(datasetId, offerId)` — same method, same arguments in
shape. The only thing that differs between them is the **value** of
`datasetId`/`offerId`, which is `@ConfigParam`-injected per test method exactly
as `CAT_01_01_DATASETID` was in the catalog milestone
(`AbstractContractNegotiationProviderTest`, verified from source):

```java
@ConfigParam
protected String offerId = OFFER + randomUUID();

@ConfigParam
protected String datasetId = randomUUID().toString();
```

So the rule the connector must implement is a plain comparison, and which path
each TCK test exercises is decided by **what this project puts in
`test/tck/config.properties`** — not by anything the connector infers from the
wire beyond comparing two strings:

- offer `@id` equals this connector's advertised offer for a known dataset,
  and the offer is currently valid → skip straight to `AGREED`
  (`CN_01_04_DATASETID`/`OFFERID` will be set to a real advertised pair)
- dataset known, offer `@id` does not match → counter-offer with the real one,
  `OFFERED` (`CN_01_01`/`02`/`03` keep the default random values, which by
  construction never match)
- dataset entirely unadvertised → `TERMINATED` (`CN_02_01`)

### Message shapes, read from `NegotiationFunctions.java` and `DspConstants.java`

Property names below are DSP's compact-form field names (matching this
project's existing JSON-LD handling — fixed compact form, no RDF processing,
per §20).

| Message | `@type` | Fields |
|---|---|---|
| Request | `ContractRequestMessage` | `consumerPid`, `offer` (nested: `@id`, `target` = dataset id, `permission: [{action: "use", constraints: []}]`), `callbackAddress` (initial request only) |
| Offer (push) | `ContractOfferMessage` | `providerPid`, `consumerPid`, `offer` (same nested shape) |
| Event | `ContractNegotiationEventMessage` | `providerPid`, `consumerPid`, `eventType`: `ACCEPTED` \| `FINALIZED` |
| Agreement (push) | `ContractAgreementMessage` | `providerPid`, `consumerPid`, `agreement` (`@id`, `target`, `permission`, `timestamp`), `callbackAddress` |
| Verification | `ContractAgreementVerificationMessage` | `providerPid`, `consumerPid` |
| Termination | `ContractNegotiationTerminationMessage` | `providerPid`, `consumerPid`, `code`, `reason` (array of `{message: string}` objects — **not** the array-of-plain-strings shape the catalog protocol's `CatalogError.reason` uses; the two are different DSP fields) |
| State document | `ContractNegotiation` | `providerPid`, `consumerPid`, `state` |

The nested offer's `@type` is marked in the TCK's own source as
`@DspTestingWorkaround("Remove @type")` — a note from the TCK's own authors
that this field is scheduled for removal. Parsing must not depend on its
presence.

`ContractAgreementMessage`'s top-level `callbackAddress` is carried in the
TCK's message-builder utility but that utility is shared code, not a
per-assertion check this project's tests were read against. Treated as
probable but unconfirmed; the plan should include it and the first real TCK
run will say if it was unnecessary.

### The synchronous-4xx scenarios (`CN:03`)

All four are state-machine violations, not business decisions:

| Test | Violation |
|---|---|
| `CN:03-01` | Terminating a `FINALIZED` negotiation |
| `CN:03-02` | Verifying while still `OFFERED` (verification is only legal from `AGREED`) |
| `CN:03-03` | Verifying immediately after `ACCEPTED`, before the agreement push has been acknowledged — i.e. still not `AGREED` |
| `CN:03-04` | A second `POST /negotiations/{id}/request` carrying the **same** offer as the first, sent while already `OFFERED` |

`CN:03-04`'s trigger and `CN:01-02`'s trigger are both "another request arrives
while `OFFERED`", yet one produces a synchronous 4xx and the other produces an
asynchronous termination. The distinguishing detail found in source
(`Provider03Test.cn_03_04` sends the *identical* offer id as the original
request; `Provider01Test.cn_01_02` sends a *different* one) is recorded as the
current best account, not a confirmed rule — see Risks.

### The autonomous-termination scenarios (`CN:02-05`, `06`, `07`) and the validity-period decision

Three tests require the provider to push a termination with **no consumer
action in between** — after offering, after receiving `ACCEPTED`, and after
receiving verification, respectively. The TCK's own source scripts this via
`negotiationMock.recordXAction(ProviderActions::postTerminate)`, but that
mechanism is TCK-internal test-double wiring for the TCK's local self-test
mode (`dataspacetck.dsp.local.connector=true`); a `NoOpProviderNegotiationMock`
is injected instead when testing a real external connector
(`dataspacetck.dsp.local.connector=false`, this project's setting since the
catalog milestone). Nothing in the TCK tells an external connector *why* to
terminate at these three points — that is a genuine design decision this
project makes, not something extracted from the TCK.

**Decision:** the trigger is an expired validity-period policy, re-evaluated
before every forward transition. `DECISIONS.md` §14 already permits a
validity-period constraint as one of exactly two enforceable v1 policy shapes;
this milestone is its first real use rather than new scope. One dataset in the
TCK harness configuration (`test/tck/dsbox.yaml`) is given a `validity_until`
in the past. Because the connector re-checks validity at each forward
transition (offer → agree, accept → agree, verify → finalize) rather than only
once at the start, the same expired-policy dataset naturally produces
termination at whichever of the three points that dataset's test drives it to
— `CN_02_05_DATASETID` stops after the offer, `CN_02_06` after acceptance,
`CN_02_07` after verification, purely because each test's pipeline advances the
negotiation to a different point before the next re-check fires.

*Trade-off accepted:* "the provider may terminate for other business reasons"
is not built. If a future requirement needs that, this is where it attaches.

## Architecture

```
internal/store/store.go           SQLite open, CREATE TABLE IF NOT EXISTS, negotiation CRUD
internal/store/store_test.go
internal/dsp/negotiation.go       state machine, transition rules, message documents
internal/dsp/negotiation_test.go
internal/dsp/negotiation_handler.go   the six handlers
internal/dsp/negotiation_handler_test.go
internal/dsp/callback.go          async push to a consumer's callbackAddress
internal/dsp/callback_test.go
internal/dsp/router.go            mounts the negotiation routes
internal/config/config.go         + data_dir, + datasets[].validity_until
cmd/dsbox/main.go                 opens the store, passes it into the DSP router
go.mod                            + modernc.org/sqlite
```

`internal/store` is a new package because it is genuinely a new
responsibility — nothing before this milestone touched a filesystem beyond
reading the config file once at startup. `internal/dsp/negotiation.go` stays
in the existing package, following the same reasoning `catalog.go` used: it is
a set of functions operating on types from the same package, and splitting
further would not buy anything today.

### Storage

```sql
CREATE TABLE IF NOT EXISTS negotiations (
    provider_pid     TEXT PRIMARY KEY,
    consumer_pid     TEXT NOT NULL,
    state            TEXT NOT NULL,
    dataset_id       TEXT NOT NULL,
    offer_id         TEXT NOT NULL,
    callback_address TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
```

No migration framework. `DECISIONS.md`'s "Deferred to implementation" list has
carried "migration approach for the SQLite schema" since the concept design
explicitly on the basis of deciding when it is first needed — this table is
that moment. A single idempotent `CREATE TABLE IF NOT EXISTS`, run once at
startup, is the whole mechanism. A second schema change is what decides
whether a real migration tool earns its place; one table does not.

`provider_pid` is generated with `crypto/rand` — 16 random bytes formatted per
RFC 4122 UUID v4 — rather than a UUID package, matching `CLAUDE.md`'s default
answer of "the standard library" for a value this project fully controls the
shape of.

`modernc.org/sqlite` is the driver: pure Go, no CGO. `DECISIONS.md` §7 commits
to a single static binary and §16 to `goreleaser` cross-compiling
linux/macOS × amd64/arm64; a CGO-based driver would need a C toolchain per
target and would break that promise the first time someone builds from source
on a machine without one.

### Configuration

```yaml
data_dir: ./data

datasets:
  - id: urn:dataset:sample
    validity_until: 2027-01-01T00:00:00Z   # optional; absent = no expiry
```

- `data_dir` is required once negotiation is implemented (it was not needed by
  the catalog milestone). The SQLite file lives at `{data_dir}/dsbox.db`.
- `validity_until` is optional, RFC 3339. Absent means the offer never
  expires — the catalog milestone's existing unrestricted-use behavior is
  unchanged for every dataset that does not set it.

### State machine

The eight DSP states, unchanged from the spec:
`INITIALIZED → REQUESTED → OFFERED → ACCEPTED → AGREED → VERIFIED → FINALIZED`,
`TERMINATED` reachable from every non-terminal state.

Transition table (provider-driven only — the consumer-role transitions
belong to the `CN_C` milestone):

| From | Event | Validity check | To | Push |
|---|---|---|---|---|
| — | `POST /negotiations/request`, offer unmatched or dataset unknown | — | `REQUESTED` then async `OFFERED` or `TERMINATED` | offer or termination |
| — | `POST /negotiations/request`, offer matches and valid | pass | `REQUESTED` then async `AGREED` | agreement |
| `REQUESTED`/`OFFERED` | validity re-check finds expired | fail | `TERMINATED` | termination |
| `OFFERED` | `POST .../request`, same offer resent | — | `TERMINATED` (async) or 400 (sync) — see Risks | termination or none |
| `OFFERED` | `POST .../request`, different offer | — | `TERMINATED` | termination |
| `OFFERED` | `POST .../events` `ACCEPTED` | — | `ACCEPTED` then async `AGREED` (if valid) or `TERMINATED` (if expired) | agreement or termination |
| `AGREED` | `POST .../agreement/verification` | — | `VERIFIED` then async `FINALIZED` event (if valid) or `TERMINATED` (if expired) | event or termination |
| any non-terminal | `POST .../termination` | — | `TERMINATED` | — |
| any state | `POST .../events`, `.../verification`, or `.../termination` for the wrong state | — | unchanged | 400 `ContractNegotiationError` |

### Handlers

Six handlers on `catalogHandler`'s sibling, `negotiationHandler`, holding the
store and the config. Structural validation (malformed JSON, wrong `@type`,
missing `@context`) follows the same direct-field-check approach `DECISIONS.md`
§22.5 already established for the catalog protocol — no schema library here
either, for the same reason: message count stays well under the "past a
dozen" threshold that would justify one.

Async pushes (`internal/dsp/callback.go`) are a single best-effort
`http.Post` to the stored `callback_address`, made synchronously within the
handler goroutine before it returns 200. No retry, no queue: a failed push is
logged and the local state transition stands, matching the provider's
authoritative role in DSP — the consumer can always recover state via
`GET /negotiations/{id}`.

### Error handling

`ContractNegotiationError` reuses `errorDocument`/`writeError` from the
catalog milestone (`internal/dsp/error.go`), passing a different `dspType`
exactly as that function was designed to allow. No new error-document code.

## Testing

| Layer | Cases |
|---|---|
| Store | create, read, update state, `CREATE TABLE IF NOT EXISTS` is idempotent across two opens |
| Config | `data_dir` required once negotiation is wired in; `validity_until` parses, is optional, rejects a malformed timestamp |
| State machine | every transition in the table above, both success and rejection; validity re-check at each forward transition |
| Negotiation documents | request/offer/event/agreement/verification/termination/state-document shapes, matching the field table above |
| Handlers (`httptest`) | all six endpoints, happy path and the four `CN:03` rejection shapes, using a fake callback server to assert what gets pushed |
| TCK | `make tck` green with `CN` in the gate |

## Gate

```go
var expected = map[string]int{"MET": 1, "CAT": 3, "CN": 15}
```

## TCK harness

`test/tck/dsbox.yaml` gains `data_dir` and enough datasets to cover every
scenario: a normal unrestricted dataset (for `CN_01_04`'s match case) and one
with an already-past `validity_until` (for `CN_02_05`/`06`/`07`).
`test/tck/config.properties` gains the `CN_xx_yy_DATASETID`/`OFFERID` keys —
left at their random defaults for every test that needs a mismatch, pointed at
the matching dataset only for `CN_01_04`, and pointed at the expired dataset
for `CN_02_05`/`06`/`07`. Exact values are worked out during planning once the
advertised catalog for the harness is finalized.

## Documentation

- `README.md`: status table gains contract negotiation (provider only,
  `CN` suite); honest total becomes 19 of 59
- `config.example.yaml`: `data_dir` and a `validity_until` example, commented
- `DECISIONS.md`: a new section recording the migration-approach decision, the
  SQLite driver choice, the provider-pid generation choice, and the
  validity-period-as-autonomous-termination-trigger decision, each with its
  trade-off. §8's SQLite section gets a note that this milestone is where its
  justification stops being aspirational. The "Deferred to implementation"
  list loses the migration-approach line.

## Done criteria

1. `make tck` passes with `CN` in the gate's expected map (15), green in CI
2. `go test ./...` passes and covers every case in the testing table
3. A fresh clone with `config.example.yaml` and an empty `data_dir` starts,
   creates the SQLite file, and negotiates successfully against a manual
   `curl` walkthrough of the happy path
4. `README.md` states 19 of 59 and marks transfer process and the consumer
   negotiation role as not implemented
5. `DECISIONS.md` records all four new decisions with their trade-offs

## Risks

| Risk | Mitigation |
|---|---|
| The exact rule separating `CN:01-02` (async terminate) from `CN:03-04` (sync 4xx) for what looks like the same trigger — "another request while `OFFERED`" — is inferred from one field difference (same offer vs. different offer) across two test files, not confirmed by an assertion on the connector's own decision logic | Implement the same-offer → 400, different-offer → async-terminate rule as designed; the first real TCK run against both `CN_01_02` and `CN_03_04` in the same run confirms or refutes it immediately, the same way the `@ConfigParam` key format was confirmed in the catalog milestone |
| `ContractAgreementMessage.callbackAddress` may or may not be a real wire requirement — the only evidence is a shared TCK message-builder utility, not a targeted assertion | Emit it; a first TCK run failure on `CN_01_03`/`04` with no other explanation would point here |
| Async pushes have no retry; a slow or dropped push could leave a negotiation stuck from the consumer's perspective in a real deployment | Acceptable for v1 per the "no fallback for scenarios that can't happen" principle — TCK's own default wait window is generous (10–60s), and `GET /negotiations/{id}` lets a real consumer recover state without relying on the push arriving |
| `modernc.org/sqlite` is less mature than `mattn/go-sqlite3` for exotic SQLite features | Only `CREATE TABLE`, `INSERT`, `SELECT`, `UPDATE` are used — no feature this project needs is exotic |

## What this unlocks

The consumer negotiation role (`CN_C`), which reuses this milestone's state
machine and storage but needs an initiation webhook nothing here builds, and
the transfer process protocol, which is the first consumer of a `FINALIZED`
agreement.
