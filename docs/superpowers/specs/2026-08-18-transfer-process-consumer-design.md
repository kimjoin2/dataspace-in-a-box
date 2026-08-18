# Design: the transfer process protocol (consumer role)

This milestone implements the transfer process protocol's consumer role
(`TP_C`) and takes its suite into the compliance gate. The provider role
(`TP`, Phase A) is already gated at 15/15; this is its mirror, and the two
share every endpoint path.

Everything asserted here about the TCK was read out of the pinned runtime
image, `eclipsedataspacetck/dsp-tck-runtime@sha256:45cfafa40486714d441057bc6063653a1feebb95444f721974147cb0dd7416ad`
— the digest in `test/tck/compose.yaml` — not from upstream's default branch,
which can drift from it. Everything asserted about the protocol was read out
of the DSP 2025-1 specification at tag `2025-1`. Both are reproducible with
the commands given.

## Scope

In: the consumer role of the transfer process control plane — the initiate
hook, this connector's outbound `TransferRequestMessage`, its reactions to
the provider's four messages, its own autonomous messages, storage for
consumer-role transfers, and the gate entry that makes all of it required.

In, because the consumer role is what exposes it: a writer for
`store.Agreement` on the consumer side. `negotiation_consumer_handler.go`
moves a consumer-role negotiation to `AGREED` and writes no agreement row
today, so a real negotiation cannot be followed by a transfer under the
agreement it produced. The TCK does not catch this — its transfers cite
seeded agreements — which is exactly why it needs closing deliberately
rather than when a user finds it.

Out: the data plane. The `TP_C` suite moves no bytes, for the same reason
`TP` does not — see `2026-08-16-transfer-process-tck-requirements.md`, "The
headline: these suites do not move data". A green `TP_C` is a claim about
the control plane and must not be reported as anything else.

Out: a management-API trigger for starting a transfer as consumer. Same
reasoning as `2026-08-15-contract-negotiation-consumer-design.md`, "The
initiate endpoint is not a management feature": building the operator UX for
choosing an agreement and a provider is a separate concern from satisfying a
test harness's startup contract, and conflating them designs a product
decision as a side effect. Until that exists, `POST /transfers/initiate` is
this connector's only way to start a transfer as consumer, which is worth
naming plainly rather than folding silently into precedent.

## What the TCK actually requires

### Inventory: 16 methods, 15 gated results

`TransferProcessConsumer{01,02,03}Test` declare 5, 5, and 6 `@MandatoryTest`
methods. `tp_c_02_04` also carries JUnit's `@Disabled`, and a disabled test
produces no result, so the suite produces 15 — the same shape, and the same
trap, as `TP`.

```sh
javap -p -v -classpath tckx \
  org.eclipse.dataspacetck.dsp.verification.tp.TransferProcessConsumer02Test |
  grep -B3 -i Disabled
```

The gate therefore takes `"TP_C": 15`, and the README's required total goes
from 49 to 64 of 65.

### The initiate hook

`DspSystemLauncher.start()` has required
`dataspacetck.dsp.connector.transfer.initiate.url` since the first milestone
that ran the harness for real; `test/tck/config.properties` already points it
at `http://dsbox:8080/2025-1/transfers/initiate`, where it currently 404s.

`HttpConsumerTransferProcessClient.initiateTransferRequest` POSTs

```json
{"providerId": "...", "agreementId": "...", "format": "...", "connectorAddress": "..."}
```

as **plain JSON, not JSON-LD** — no `@context`, no `@type`. Both facts come
from the disassembly: the body is a four-entry `Map.of`, and the call is
`postJson(url, body, false, true)`, whose `false` is the JSON-LD flag. This
is the same shape and the same flags as the negotiation initiate hook, so
`handleInitiate` in `negotiation_consumer_handler.go` is the template.

```sh
javap -p -c -classpath tckx \
  org.eclipse.dataspacetck.dsp.system.client.tp.http.HttpConsumerTransferProcessClient
```

The response body is closed and discarded, but the status is asserted:
`HttpFunctions.postJson` throws immediately on `404` or `5xx` and retries any
other `4xx` twice more before failing. `200` immediately is required. The
test then proceeds by waiting for this connector's own outbound
`TransferRequestMessage` to arrive at the TCK, not by reading this response.

### The endpoints, and why they already exist

`ProviderActions` — the TCK playing provider — POSTs to

```
<callbackAddress>/transfers/<consumerPid>/start
<callbackAddress>/transfers/<consumerPid>/completion
<callbackAddress>/transfers/<consumerPid>/suspension
<callbackAddress>/transfers/<consumerPid>/termination
```

which is the DSP 2025-1 HTTPS binding's Consumer Callback Path Bindings
resolved against this connector's `callbackAddress` of
`PublicURL + VersionPath`. Those four paths are **already mounted** — the
provider role serves them at `/2025-1/transfers/{id}/...`, keyed by
`providerPid`. The consumer role adds no routes; it adds a second meaning for
`{id}`.

A fifth endpoint is required, and it is the one this design nearly missed.
`ProviderActions` contains no `GET` path, which reads at first like the
consumer suite never observes this connector's state — the opposite of the
provider suite, where the GET is real and unforgiving. That reading is wrong.
The GET lives one layer down, in `AbstractHttpTransferProcessClientImpl`:

```
getTransferProcess(base, id) -> HttpFunctions.getJson("<base>/transfers/<id>")
```

and the consumer pipeline's `thenVerifyConsumerState` calls it with
`(getCallbackAddress(), getCorrelationId())` — that is, `GET
<this connector's callback address>/transfers/<this connector's consumerPid>`
— then reads `https://w3id.org/dspace/2025/1/state` out of the response and
compares it to the state the test expects.

It is called **37 times across the three consumer test classes** (12, 5, and
20). The GET is not a nicety here; it is how nearly every assertion in the
suite is made. `GET /transfers/{id}` must therefore resolve consumer-role
rows and return the same JSON-LD `TransferProcess` document the provider role
already returns, carrying `state` and `providerPid`.

```sh
javap -p -c -classpath tckx \
  org.eclipse.dataspacetck.dsp.system.client.tp.http.AbstractHttpTransferProcessClientImpl
javap -p -c -classpath tckx \
  org.eclipse.dataspacetck.dsp.system.pipeline.tp.ConsumerTransferProcessPipelineImpl
```

That the document builder is already correct is not an assumption: the
provider suite asserts against the same builder through the same GET, and
passes 15 of 15.

### The three groups, and what each demands

The 16 tests fall into three groups whose demands are different in kind. Each
test's sequence diagram is carried in its own class as a
`@TestSequenceDiagram` annotation; all 16 are reproduced in the appendix.

**Group 01 (5 tests) — the provider drives, this connector answers.** The TCK
sends start, then some combination of suspension, start again, completion, or
termination, and requires `200` to each. This connector sends exactly one
message in these tests: the initial `TransferRequestMessage`. It must
otherwise stay passive.

**Group 02 (5 tests, one disabled — 4 results) — this connector drives.**
After the provider starts the transfer, this connector must send termination
(`02-01`), completion (`02-02`), or suspension then termination (`02-03`) on
its own. `02-05` is the exception that shapes the configuration: it sends a
termination directly from `REQUESTED`, without waiting for a start.

**Group 03 (6 tests) — illegal inbound messages must be refused with `4xx`.**
Completion from `REQUESTED` (`03-01`), suspension from `REQUESTED`
(`03-02`), completion from `SUSPENDED` (`03-03`), and start, suspension, or
completion from `TERMINATED` (`03-04`, `03-05`, `03-06`).

Every rejection group 03 demands is already produced by the legality
predicates in `internal/dsp/transfer.go`: `completionLegalFrom` is `STARTED`
only, `suspensionLegalFrom` is `STARTED` only, and nothing is legal from a
terminal state. No new refusal logic is needed. What is needed is that these
predicates be reachable from consumer-role rows, which is a routing and
storage question rather than a legality one.

## The sender dimension lands here

`2026-08-17`'s fix split start legality in two, because DSP 2025-1 gives
`TransferStartMessage` a single permitted sender: its message table reads
"Sent by: Provider", and the HTTPS binding admits the consumer's copy only as
a resume — "The Consumer can POST a Transfer Start Message to attempt to
start a Transfer Process after it has been suspended". That produced
`startLegalFrom` (`REQUESTED` or `SUSPENDED`) for the outbound direction and
`inboundStartLegalFrom` (`SUSPENDED` alone) for the inbound one.

Group 01 shows why that split was load-bearing rather than pedantic. Tests
`01-01` through `01-04` all reach `STARTED` by *receiving* a start from
`REQUESTED`. In the consumer role that is legal, because the sender is the
provider. In the provider role the identical message from `REQUESTED` must be
refused, because the sender would be the consumer.

The wider set turns out to serve two callers rather than one:

| Predicate | Legal from | Used by |
|---|---|---|
| `startLegalFrom` | `REQUESTED`, `SUSPENDED` | the provider's outbound start, and a start received as consumer |
| `providerInboundStartLegalFrom` | `SUSPENDED` | a start received as provider |

Only the second name carries a role, and that asymmetry is the point.
`{REQUESTED, SUSPENDED}` describes the *transfer* — one that has not run yet
or has been paused — which is why the same set answers both "may a provider
send a start now" and "may a consumer accept one now". The narrower set
exists solely because of who is sending, so it is the one that names a role.
Naming both after roles would suggest they are a matched pair of role
lookups, and then the provider's outbound driver would be calling something
called `consumerInbound…`.

So `inboundStartLegalFrom` is renamed to `providerInboundStartLegalFrom`, and
`startLegalFrom` keeps its name and gains a second caller.

The other three messages need no split. DSP 2025-1's Sent by rows for
suspension, completion, and termination name both parties, so one predicate
serves both roles and both directions. The model gains a sender dimension in
exactly one place.

## Configuration: `consumer_transfer_policies`

### Why configuration at all

Group 02 requires this connector to send messages nobody asked it for. Which
message, and when, varies per test, and the only test-varying field on the
wire is the agreement id — the same situation the provider role met, and the
same answer: a policy table keyed by `agreement_id`, selected by the
`agreementId` the initiate call supplies.

A separate block from `transfer_policies`, not a reuse of it. The two roles
answer different questions — "what do I do after accepting a request" versus
"what do I do after making one" — and a single table keyed by agreement id
would make an entry's meaning depend on which role happened to see that id
first. `CN` already split provider policy from `consumer_policies` for this
reason.

### The shape

```yaml
consumer_transfer_policies:
  - agreement_id: urn:uuid:tck-tpc-02-01
    after: STARTED
    sequence: [TERMINATED]
```

`sequence` is the list of states this connector drives to on its own, pushing
the matching message to the provider at each step, spaced by the same
`transferStepDelay` the provider role uses. `after` is the state whose
arrival releases the sequence.

**The default is an empty sequence** — an agreement with no entry makes this
connector passive after its initial request. This is the opposite of
`transfer_policies`, whose default is `[STARTED]`, and it is forced by the
evidence: groups 01 and 03 are eleven of the fifteen tests, and every one of
them fails if this connector volunteers a message.

### Why `after` is a field and not an inference

`02-01` and `02-05` send the same message, `TransferTerminationMessage`, and
differ only in when. `02-01` sends it after the provider's start; `02-05`
sends it from `REQUESTED`, before any start arrives.

Legality cannot tell them apart: `terminationLegalFrom` admits `REQUESTED`,
`STARTED`, and `SUSPENDED`, so a driver that fired as soon as its step became
legal would send `02-01`'s termination immediately — and then the TCK's start
would land on a terminated transfer, take a `4xx`, and fail a test that was
supposed to pass. `after` is the smallest field that distinguishes them, and
it is a state this connector observes rather than a delay, so the test does
not become a race.

One ordering constraint rides along with it. `after: REQUESTED` cannot mean
"as soon as the row is written", because every message this connector sends
as consumer is addressed to `<provider_base_url>/transfers/<providerPid>/...`
and `providerPid` does not exist until the ACK to the initial request
arrives. So the trigger is the state *after the request has been
acknowledged*, and a policy whose `after` is `REQUESTED` fires on that ACK,
not on the row's creation. Firing earlier would build a URL with an empty
path segment and fail `02-05` in a way that looks like a policy bug rather
than an ordering one.

### Deriving all 15 results from the policy

| Test | `after` | `sequence` | What carries it |
|---|---|---|---|
| `01-01` … `01-05` | — | — | no entry: passive, inbound legality answers everything |
| `02-01` | `STARTED` | `[TERMINATED]` | policy |
| `02-02` | `STARTED` | `[COMPLETED]` | policy |
| `02-03` | `STARTED` | `[SUSPENDED, TERMINATED]` | policy |
| `02-04` | — | — | `@Disabled` upstream, no result |
| `02-05` | `REQUESTED` | `[TERMINATED]` | policy |
| `03-01` … `03-06` | — | — | no entry: passive, inbound legality answers everything |

Eleven of the fifteen need no configuration at all. That is the check on the
design: a policy table that had to carry every test would mean the legality
model was doing no work.

## Architecture

### Storage

```sql
CREATE TABLE IF NOT EXISTS consumer_transfers (
    consumer_pid      TEXT PRIMARY KEY,
    provider_pid      TEXT NOT NULL DEFAULT '',
    provider_base_url TEXT NOT NULL,
    agreement_id      TEXT NOT NULL,
    format            TEXT NOT NULL,
    state             TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);
```

A second table, not a `role` column on `transfers` — the same judgment
`consumer_negotiations` already made, for the same reasons. The provider
table is 15 TCK tests deep, a shared table would give every column a meaning
that depends on the row's role, and every existing provider-role query would
need a predicate it does not need today.

Keyed by this connector's own generated consumer pid, because that is the
identifier the provider puts in the callback path. `provider_pid` starts
empty and is filled from the ACK to the initial request.
`provider_base_url` is `connectorAddress` from the initiate call, stored
because every later outbound message needs it and the initiate call is the
only place it is supplied. No `callback_address` column: this connector's own
callback address is `PublicURL + VersionPath`, computed at startup, not
per-transfer data.

CRUD mirrors the negotiation consumer table: `CreateConsumerTransfer`,
`GetConsumerTransfer(consumerPID)`, `SetConsumerTransferState(consumerPID,
from, to, updatedAt)` — compare-and-swap for the same reason the other three
setters use it, since consumer-role reactions run in goroutines and can
outlive a termination that arrived meanwhile.

### Routing: one path, two tables

`handleTransferStart`, `handleTransferCompletion`, `handleTransferSuspension`,
`handleTransferTermination`, and `handleGetTransfer` each serve both roles.
They resolve `{id}` by trying `GetConsumerTransfer` first and falling back to
the provider table — exactly what `handleEvent`, `handleTermination`, and
`handleGetNegotiation` already do for negotiations. A row found in the
consumer table has its inbound start checked with `startLegalFrom`; one found
in the provider table with `providerInboundStartLegalFrom`.

The two id spaces are UUIDs generated independently, so a collision is not a
practical concern; consumer-first is the order the negotiation handlers
established and there is no reason to differ.

`POST /transfers/request` stays provider-only: it is where a consumer asks
*this* connector to provide, and the consumer role never receives it.

### The initial request

`handleInitiate` validates that `providerId`, `agreementId`, `format`, and
`connectorAddress` are all present, runs `connectorAddress` through
`validateOutgoingCallback` — the same SSRF guard both existing roles use,
logging rather than echoing the reason, for the reason `handleInitiate`
already documents for negotiations — and checks that `agreementId` names an
agreement this connector holds.

That last check is the decision this milestone takes deliberately. The TCK
supplies agreement ids that were never negotiated, so the alternative is to
start a transfer under a contract this connector has no record of, which is
the transfer-protocol form of `CLAUDE.md`'s "Never accept a constraint that
is not enforced". The provider role already rejects an unknown agreement;
the consumer role rejects it too, and the harness seeds the ids the suite
uses, exactly as `run.sh` already seeds seven for `TP`. One rule, both roles.

It then generates a consumer pid, writes the row in `REQUESTED`, answers
`200`, and dispatches the outbound `TransferRequestMessage` in a goroutine —
the negotiation consumer role's shape, including its no-retry decision. The
ACK supplies `providerPid`, which is written to the row.

### Reacting

Inbound messages take the same path they already do: envelope check,
legality check against the row's role, compare-and-swap, `200`. On reaching
the state named by the policy's `after`, the driver walks `sequence`,
pushing each message to `provider_base_url` and stopping at the first refusal
— `pushTransferStep`'s existing behavior, which the provider role's driver
already proves stops rather than skips.

## Testing

Unit tests for the new legality mapping (a start received as consumer from
`REQUESTED` is `200`; as provider it is `400` — the pair that pins the
rename), for the initiate handler's four validations, for the policy
resolver including the empty default and the `after` gate, and for the
consumer driver's stop-on-refusal. Handler tests walk the inbound matrix for
consumer-role rows the way `TestTransferTransitionsOverHTTP` already does for
provider-role ones.

The `after` gate needs a test that would fail without it: configure
`02-01`'s policy, deliver a start, and assert the termination is sent after
the start rather than before. A test that only asserts "a termination was
sent" passes with the field removed.

`GET /transfers/{id}` needs a test against a consumer-role row — the suite
makes 37 assertions through it, so a GET that resolved only provider rows
would fail most of the suite while every inbound handler behaved perfectly.
That failure mode is worth pinning directly rather than discovering through
the TCK.

## Gate, harness, and documentation

`cmd/tckgate`'s `expected` gains `"TP_C": 15`, taking the required total to
64 of 65. The single exemption, `CN:02-07`, is unaffected.

`test/tck/config.properties` gains one `TP_C_xx_yy_AGREEMENTID` key per test
method — including `02_04`'s, which costs nothing and stops the list looking
like it has a hole, exactly as `TP_02_04`'s does. `run.sh` seeds those
agreements alongside the existing seven. `test/tck/dsbox.yaml` gains the four
`consumer_transfer_policies` entries.

`README.md`'s pass rate becomes 64 of 65. The claim it makes must stay a
control-plane claim.

## Done criteria

- `TP_C` reports 15 of 15, and the gate requires it.
- `TP`, `CN`, `CN_C`, `CAT`, `MET` are unchanged: 64 required tests pass with
  one known exemption.
- `go test -race -count=2 ./...` is clean.
- A consumer-role negotiation reaching `AGREED` writes a `store.Agreement`
  row, and a transfer can be initiated under it without seeding.
- `GET /transfers/{id}` resolves a consumer-role row and reports its state.

## Risks

The `after` field is a new concept in this codebase's configuration
vocabulary, and it exists because four tests need it. If a later reading of
the suite shows the timing can be derived from state alone, the field should
go — it earns its place only as long as `02-01` and `02-05` remain
indistinguishable without it.

The consumer-role agreement writer changes behavior no TCK test exercises,
which is precisely why it is being done here rather than later, but it also
means the suite cannot confirm it. Its test has to be a unit test that
asserts the row, not a green suite.

## Appendix: the 16 sequence diagrams

Read from the pinned image's `@TestSequenceDiagram` annotation values. `TCK`
is the provider; `CUT` is this connector as consumer. Reproduce with:

```sh
javap -p -v -classpath tckx \
  org.eclipse.dataspacetck.dsp.verification.tp.TransferProcessConsumer01Test
```

Note that `javap -v` emits each annotation string twice — once in the
constant pool listing and once as the annotation value — so a raw count over
its output is double the real one.

### TP_C:01-01: Verify transfer request, provider started, provider terminated

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferRequest

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferTerminationMessage
CUT-->>TCK: 200 OK
```

### TP_C:01-02: Verify transfer request, provider started, provider completed

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferRequest

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferCompletionMessage
CUT-->>TCK: 200 OK
```

### TP_C:01-03: Verify transfer request, provider started, provider suspended, provider terminated

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferRequest

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferSuspensionMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferTerminationMessage
CUT-->>TCK: 200 OK
```

### TP_C:01-04: Verify transfer request, provider started, provider suspended, provider started, provider completed

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferRequest

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferSuspensionMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferCompletionMessage
CUT-->>TCK: 200 OK
```

### TP_C:01-05: Verify transfer request, provider terminated

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferRequest

TCK->>CUT: TransferTerminationMessage
CUT-->>TCK: 200 OK
```

### TP_C:02-01: Verify transfer request, provider started, consumer terminated

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferRequest

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

CUT->>TCK: TransferTerminationMessage
TCK-->>CUT: 200 OK
```

### TP_C:02-02: Verify transfer request, provider started, consumer completed

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferRequest

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

CUT->>TCK: TransferCompletionMessage
TCK-->>CUT: 200 OK
```

### TP_C:02-03: Verify transfer request, provider started, consumer suspended, consumer terminated

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferRequest

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

CUT->>TCK: TransferSuspensionMessage
TCK-->>CUT: 200 OK

CUT->>TCK: TransferTerminationMessage
TCK-->>CUT: 200 OK
```

### TP_C:02-04: Verify transfer request, provider started, consumer suspended, consumer started, consumer completed  

**@Disabled upstream — produces no result.**

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferRequest

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

CUT->>TCK: TransferSuspensionMessage
TCK-->>CUT: 200 OK

CUT->>TCK: TransferStartMessage
TCK-->>CUT: 200 OK

CUT->>TCK: TransferCompletionMessage
TCK-->>CUT: 200 OK
```

### TP_C:02-05: Verify transfer request, consumer terminated

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferRequest

CUT->>TCK: TransferTerminationMessage
TCK-->>CUT: 200 OK
```

### TP_C:03-01: Verify transfer request, provider completed

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferProcess

TCK->>CUT: TransferCompletionMessage
CUT-->>TCK: 4xx ERROR
```

### TP_C:03-02: Verify transfer request, provider suspended

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferProcess

TCK->>CUT: TransferSuspensionMessage
CUT-->>TCK: 4xx ERROR
```

### TP_C:03-03: Verify transfer request, provider started, provider suspended, provider completed

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferProcess

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferSuspensionMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferCompletionMessage
CUT-->>TCK: 4xx ERROR
```

### TP_C:03-04: Verify transfer request, provider started, provider terminated, provider started

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferProcess

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferTerminationMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 4xx ERROR
```

### TP_C:03-05: Verify transfer request, provider started, provider terminated, provider suspended

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferProcess

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferTerminationMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferSuspensionMessage
CUT-->>TCK: 4xx ERROR
```

### TP_C:03-06: Verify transfer request, provider started, provider terminated, provider completed

```
participant TCK as Technology Compatibility Kit (provider)
participant CUT as Connector Under Test (consumer)

TCK->>CUT: Signal to start transfer

CUT->>TCK: TransferRequestMessage
TCK-->>CUT: TransferProcess

TCK->>CUT: TransferStartMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferTerminationMessage
CUT-->>TCK: 200 OK

TCK->>CUT: TransferCompletionMessage
CUT-->>TCK: 4xx ERROR
```
