# Design: the contract negotiation protocol (provider role)

Date: 2026-08-11
Status: approved, ready for implementation planning

The third sub-project. It adds the DSP contract negotiation protocol in the
**provider** role only and makes the `CN` suite (15 tests, 14 required) join
the compliance gate. The consumer role (`CN_C`, 16 tests) is a separate, later
sub-project: it needs a webhook the TCK uses to tell the connector to
*initiate* a negotiation, which nothing in this milestone builds.

`CN:02-07` is explicitly excluded from the passing set — see "The
autonomous-termination scenarios" below for why, and "Gate" for how the gate
still runs all 15 `CN` tests honestly while tracking that one as a known,
named gap rather than silently dropping it.

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
`REQUESTED` state); whatever the provider decides — offer, agree, or
terminate — happens afterward, as an asynchronous push. The only synchronous
400 path is a **structurally** malformed message — not JSON, wrong `@type`,
missing `@context` — the same class of check the catalog protocol already
makes.

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
wire beyond comparing two strings and one timestamp:

- dataset not advertised at all → the negotiation stays `REQUESTED`; the
  provider takes no autonomous action, since it has nothing coherent to say
  about a dataset it has never heard of (`CN_02_02`'s default random
  `datasetId`/`offerId` land here — the test only needs the provider to sit
  still until the consumer's own termination arrives)
- dataset advertised, offer `@id` matches, currently valid → skip straight to
  `AGREED` (`CN_01_04`, `CN_02_03`)
- dataset advertised, offer `@id` matches, expired → skip straight to
  `TERMINATED`, no offer or agreement ever pushed (`CN_02_01`)
- dataset advertised, offer `@id` does not match, currently valid →
  counter-offer with the real one, `OFFERED` (`CN_01_01`/`02`/`03`, `CN_02_04`)
- dataset advertised, offer `@id` does not match, expired → counter-offer with
  the real one (telling a consumer the true terms is never gated on validity),
  then immediately follow with an unprompted `TERMINATED` (`CN_02_05`,
  `CN_02_06` — see the autonomous-termination section below)

The first two rules were already established for `CN_01_04` before validity
entered the picture; the third and fifth are validity's actual contribution.

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

Three tests require the provider to push a termination the consumer never
explicitly asked for — after offering with no further consumer action
(`CN:02-05`), after receiving `ACCEPTED` (`CN:02-06`), and after receiving
verification (`CN:02-07`). The TCK's own source scripts this via
`negotiationMock.recordXAction(ProviderActions::postTerminate)`, but that
mechanism is TCK-internal test-double wiring for the TCK's local self-test
mode (`dataspacetck.dsp.local.connector=true`); a `NoOpProviderNegotiationMock`
is injected instead when testing a real external connector
(`dataspacetck.dsp.local.connector=false`, this project's setting since the
catalog milestone). Nothing in the TCK tells an external connector *why* to
terminate at these three points — that is a genuine design decision this
project makes, not something extracted from the TCK.

**Decision:** the trigger is an expired validity-period policy, checked at two
points — when an unmatched request would otherwise only earn an informational
counter-offer, and when an `ACCEPTED` event would otherwise advance the
negotiation to `AGREED`. `DECISIONS.md` §14 already permits a validity-period
constraint as one of exactly two enforceable v1 policy shapes; this milestone
is its first real use rather than new scope. One dataset in the TCK harness
configuration (`test/tck/dsbox.yaml`) is given a `validity_until` in the past:

- `CN_02_05_DATASETID`/`OFFERID` — offer mismatched, dataset expired. The
  provider still pushes its canonical counter-offer (telling a consumer the
  true terms is never gated on validity), then immediately follows with an
  unprompted termination, since there is nothing left to agree to.
- `CN_02_06_DATASETID`/`OFFERID` — the same expired, mismatched pair. The
  counter-offer is pushed the same way; this time the consumer's `ACCEPTED`
  event arrives before the provider's own follow-up termination, so the
  re-check that would normally advance `ACCEPTED → AGREED` finds the offer
  expired and terminates instead. `CN_02_05` and `CN_02_06` are therefore
  driven by the identical connector behavior — offer, then check-and-terminate
  — differing only in whether the consumer's accept happens to arrive first.

**`CN:02-07` does not fit this account, and is deliberately left failing.**
Its sequence shows a *clean* `AGREED` — meaning the offer matched and passed
the validity check — followed by an unprompted termination only after
`VERIFIED`. A check performed once at accept-time cannot explain a rejection
that surfaces later, on a negotiation that already passed that check; making
it fit would mean a `validity_until` timestamp tuned to fall between the
agreement and the verification during a running test, which is a wall-clock
race this project will not build a test fixture around. No connector-side
mechanism in this milestone produces `CN:02-07`'s behavior. It is tracked as a
named, honest gap — see "Gate" and `docs/follow-ups.md` — for whichever future
milestone finds the actual trigger DSP intends here.

*Trade-off accepted:* "the provider may terminate for other business reasons"
is not built beyond these two checks. `CN:02-07` stays failing until a real
trigger is found. If a future requirement needs a broader mechanism, this is
where it attaches.

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

| From | Event | To | Push |
|---|---|---|---|
| — | `POST /negotiations/request`, dataset not advertised | `REQUESTED` (no further autonomous action) | none |
| — | `POST /negotiations/request`, dataset advertised, offer matches, valid | `REQUESTED` then async `AGREED` | agreement |
| — | `POST /negotiations/request`, dataset advertised, offer matches, expired | `REQUESTED` then async `TERMINATED` | termination |
| — | `POST /negotiations/request`, dataset advertised, offer mismatched, valid | `REQUESTED` then async `OFFERED` | offer |
| — | `POST /negotiations/request`, dataset advertised, offer mismatched, expired | `REQUESTED` then async `OFFERED`, immediately followed by async `TERMINATED` | offer, then termination |
| `OFFERED` | `POST .../request`, same offer resent | unchanged | 400 (sync) — see Risks |
| `OFFERED` | `POST .../request`, different offer | `TERMINATED` | termination |
| `OFFERED` | `POST .../events` `ACCEPTED`, dataset valid | `ACCEPTED` then async `AGREED` | agreement |
| `OFFERED` | `POST .../events` `ACCEPTED`, dataset expired | `ACCEPTED` then async `TERMINATED` | termination |
| `AGREED` | `POST .../agreement/verification` | `VERIFIED` then async `FINALIZED` event | event |
| any non-terminal | `POST .../termination` | `TERMINATED` | — |
| any state | `POST .../events`, `.../verification`, or `.../termination` for the wrong state | unchanged | 400 `ContractNegotiationError` |

`VERIFIED → FINALIZED` has no validity check — this is deliberate, see
"`CN:02-07` does not fit this account" above. A negotiation that reached
`AGREED` always finalizes on verification.

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
| State machine | every transition in the table above, both success and rejection; the validity check at the initial-request decision and at the `ACCEPTED → AGREED` transition |
| Negotiation documents | request/offer/event/agreement/verification/termination/state-document shapes, matching the field table above |
| Handlers (`httptest`) | all six endpoints, happy path and the four `CN:03` rejection shapes, using a fake callback server to assert what gets pushed |
| TCK | `make tck` green with `CN` in the gate |

## Gate

The count-per-suite model alone cannot express "this suite's 15 results are
all expected to arrive, but one specific, named test is known to fail." Adding
`CN` to `expected` at 15 would demand `CN:02-07` pass; adding it at 14 would
report a shortfall the moment the TCK (correctly) produces all 15 results.
Neither is honest.

**Gate extension: named exemptions.** A second map,
`exempt = map[string]bool{"CN:02-07": true}`, holds individual test IDs
excused from the failure gate. `evaluate` still requires `CN` to produce
exactly 15 results — the suite ran to completion, nothing was truncated — but
a `FAILED` result whose ID is in `exempt` is counted separately
(`Report.Exempted`) instead of joining `Report.Failed`. `OK()` is unchanged in
spirit: every gated result must either pass or be a named, tracked exemption.

The exemption is self-cleaning in one direction: if `CN:02-07` ever
**passes** — the TCK updated, or a later milestone accidentally fixes it — the
gate must fail loudly, because a stale exemption hiding a real pass is worse
than a stale exemption hiding a real failure. `evaluate` checks this: a
`SUCCESSFUL` result for an exempted ID is an error, reported the same way a
count shortfall is.

```go
var expected = map[string]int{"MET": 1, "CAT": 3, "CN": 15}
var exempt = map[string]bool{"CN:02-07": true}
```

`README.md` reports the count this produces: 18 of 59 pass, with `CN:02-07`
named as a tracked gap rather than folded silently into "not implemented."

## TCK harness

`test/tck/dsbox.yaml` gains `data_dir` and three datasets:

| Dataset | `validity_until` | Used for |
|---|---|---|
| a plain unrestricted dataset | absent | `CN_01_01`/`02`/`03`, `CN_02_02`/`04` — anything that needs a *recognized* dataset with a mismatched offer |
| the dataset `CN_01_04` requests, offer matching | absent | `CN_01_04`, `CN_02_03` — the immediate-`AGREED` path |
| an otherwise-identical dataset | already past | `CN_02_01` (paired with its own matching offer id) and `CN_02_05`/`06` (paired with a mismatched offer id) |

`test/tck/config.properties` gains the `CN_xx_yy_DATASETID`/`OFFERID` keys:
left at their random defaults for `CN_02_02` (needs an entirely unadvertised
identifier, not one of the three above); pointed at the plain unrestricted
dataset (offer id left mismatched) for `CN_01_01`/`02`/`03` and `CN_02_04`;
pointed at the matching pair for `CN_01_04` and `CN_02_03`; pointed at the
expired dataset with its matching offer for `CN_02_01`; and pointed at the
expired dataset with a mismatched offer for `CN_02_05`/`06`. `CN_02_07` gets
no override — it is expected to fail, and its default random values are as
good as any other for that.

## Documentation

- `README.md`: status table gains contract negotiation (provider only, `CN`
  suite, 14 of 15 required); honest total becomes 18 of 59. `CN:02-07`'s gap
  is named in the same sentence, not left implicit.
- `config.example.yaml`: `data_dir` and a `validity_until` example, commented
- `docs/follow-ups.md`: an entry for `CN:02-07`, following the pattern already
  established there by the catalog milestone
- `DECISIONS.md`: a new section recording the migration-approach decision, the
  SQLite driver choice, the provider-pid generation choice, the
  validity-period-as-autonomous-termination-trigger decision, and the named-
  exemption gate extension, each with its trade-off. §8's SQLite section gets
  a note that this milestone is where its justification stops being
  aspirational. The "Deferred to implementation" list loses the
  migration-approach line.

## Done criteria

1. `make tck` passes with `CN` in the gate's expected map (15) and `CN:02-07`
   in the exemption map, green in CI
2. `go test ./...` passes and covers every case in the testing table,
   including the exemption mechanism itself (an exempted failure passes the
   gate; an exempted ID that unexpectedly succeeds fails it)
3. A fresh clone with `config.example.yaml` and an empty `data_dir` starts,
   creates the SQLite file, and negotiates successfully against a manual
   `curl` walkthrough of the happy path
4. `README.md` states 18 of 59, names `CN:02-07` as a tracked gap, and marks
   transfer process and the consumer negotiation role as not implemented
5. `DECISIONS.md` records all five new decisions with their trade-offs
6. `docs/follow-ups.md` has an entry for `CN:02-07`

## Risks

| Risk | Mitigation |
|---|---|
| The exact rule separating `CN:01-02` (async terminate) from `CN:03-04` (sync 4xx) for what looks like the same trigger — "another request while `OFFERED`" — is inferred from one field difference (same offer vs. different offer) across two test files, not confirmed by an assertion on the connector's own decision logic | Implement the same-offer → 400, different-offer → async-terminate rule as designed; the first real TCK run against both `CN_01_02` and `CN_03_04` in the same run confirms or refutes it immediately, the same way the `@ConfigParam` key format was confirmed in the catalog milestone |
| `ContractAgreementMessage.callbackAddress` may or may not be a real wire requirement — the only evidence is a shared TCK message-builder utility, not a targeted assertion | Emit it; a first TCK run failure on `CN_01_03`/`04` with no other explanation would point here |
| Async pushes have no retry; a slow or dropped push could leave a negotiation stuck from the consumer's perspective in a real deployment | Acceptable for v1 per the "no fallback for scenarios that can't happen" principle — TCK's own default wait window is generous (10–60s), and `GET /negotiations/{id}` lets a real consumer recover state without relying on the push arriving |
| `modernc.org/sqlite` is less mature than `mattn/go-sqlite3` for exotic SQLite features | Only `CREATE TABLE`, `INSERT`, `SELECT`, `UPDATE` are used — no feature this project needs is exotic |
| `CN:02-05` and `CN:02-06` need to be distinguishable, but both start from the identical (expired, mismatched) configuration and differ only in whether the consumer's `ACCEPTED` event beats the provider's own follow-up termination. If the follow-up termination fires immediately after the offer push, it likely wins that race every time — collapsing `CN_02_06` into `CN_02_05`'s behavior and never exercising the `ACCEPTED`-time check | Give the follow-up termination a small deliberate delay (on the order of a few hundred milliseconds) after the offer push, long enough for the TCK's own synchronous test code to call `acceptLastOffer()` first when it is going to. This is inherently a timing assumption, not a guarantee; the first real TCK run against both tests in the same suite is what confirms the window is wide enough |

## What this unlocks

The consumer negotiation role (`CN_C`), which reuses this milestone's state
machine and storage but needs an initiation webhook nothing here builds; the
transfer process protocol, which is the first consumer of a `FINALIZED`
agreement; and `CN:02-07`, left open for whichever future milestone finds the
actual trigger DSP intends for an unprompted termination after verification.
