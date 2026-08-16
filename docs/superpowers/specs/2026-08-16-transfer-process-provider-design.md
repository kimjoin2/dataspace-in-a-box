# Transfer process, provider role — design

The fourth and last DSP protocol in this project's build order, provider role
(`TP`). The consumer role (`TP_C`) is a separate milestone with its own spec.

This design argues from
`docs/superpowers/specs/2026-08-16-transfer-process-tck-requirements.md`, a
survey of what the pinned TCK runtime actually verifies. Three findings there
shape everything below:

- The `TP` suite verifies the **control plane only**. No test moves a byte.
- The suite declares 16 tests but two are `@Disabled` upstream, so the gate
  takes `"TP": 15`.
- The TCK sends a **random UUID as `agreementId`**, so it asks this connector
  to transfer data under an agreement that was never negotiated.

## Scope

This milestone delivers both the control plane and a minimal HTTP-PULL data
plane. That is a deliberate choice to build past what the TCK measures, and it
carries an obligation stated once here and repeated in the documentation: **the
data plane is verified by this project's own tests and by nothing else.**

Delivered in two phases, one spec and two implementation plans:

- **Phase A — control plane.** Ends with `TP` at 15/15 in the compliance gate,
  merged. The TCK is the authority for this phase.
- **Phase B — data plane.** Serves a dataset's bytes over an authorized pull
  endpoint. Raises no TCK number at all.

Phase A is planned and merged before Phase B is planned. The phases have
different evidence bases and mixing them would make it impossible to say
afterwards what proved what.

## Phase A: control plane

### Storage

One new table, mirroring `negotiations`:

```sql
CREATE TABLE IF NOT EXISTS transfer_processes (
    provider_pid     TEXT PRIMARY KEY,
    consumer_pid     TEXT NOT NULL,
    agreement_id     TEXT NOT NULL,
    state            TEXT NOT NULL,
    callback_address TEXT NOT NULL,
    format           TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
```

State updates are compare-and-swap, as `SetConsumerState` already is, so a lost
race is distinguishable from a missing row (`ErrNotFound` vs
`ErrStateChanged`).

### State machine

The five wire states from `transfer-process-schema.json`:

```
REQUESTED -> STARTED -> { COMPLETED, SUSPENDED, TERMINATED }
               ^                        |
               +------------------------+
```

Transition legality is a pure function per inbound message, in the shape
`agreementLegalFrom` already established for negotiation, so every legal and
illegal pair is pinned by unit tests rather than discovered by the TCK.

An illegal transition is `400`, never `404` — the same rule the negotiation
milestones established, for the same reason (`HttpFunctions.postJson` throws
on `404` even where an error is expected).

### Messages

Five inbound/outbound message types plus the `TransferProcess` response
document. Only three have TCK schema validators registered
(`TransferProcess`, `TransferRequestMessage`, `TransferStartMessage`); the
suspension, termination, and completion messages are checked only by what the
TCK's pipeline reads out of them. This connector emits all of them to the same
standard regardless — every node carries `@type`, per the project constraint.

Outbound calls made while handling an inbound request dispatch with `go` and go
through `pushCallback`'s retry schedule (`DECISIONS.md` §23.7, §23.8). Nothing
about transfer changes that reasoning.

### Agreement validation

**A `TransferRequestMessage` naming an agreement this connector has no record
of is rejected with `400`.**

The alternative — accept it, record the gap — is the shape `DECISIONS.md` §24.6
took in the previous milestone and had to be reversed. Here the stakes are
higher than a policy constraint: accepting an unknown `agreementId` means a
counterparty can start a transfer by citing a contract that was never made.

An agreement is "known" if either holds:

1. A negotiation exists whose `provider_pid` equals the `agreementId` and whose
   state is one of `AGREED`, `VERIFIED`, or `FINALIZED` — the three states in
   which this connector has actually issued an agreement. `REQUESTED`,
   `OFFERED`, `ACCEPTED`, and `TERMINATED` do not qualify. This needs no new
   concept: the provider role already emits `Agreement.ID = n.ProviderPID`
   (`buildAgreementMessage`), so the agreement identifier *is* the
   negotiation's provider pid.
2. The `agreementId` appears in a new `agreements:` list in the config file.

The second exists because provider pids are generated at runtime, so no
statically-configured TCK fixture could ever name one. It is not a test
backdoor: an operator recording contracts concluded outside this connector is a
real situation, and the config file is already where datasets and policies are
declared.

It does carry weight that must be documented rather than left implicit:
**writing an id into that list creates a contract as far as this connector is
concerned.** That belongs in `DECISIONS.md` and in the config file's own
comments.

### TCK fixtures

`test/tck/config.properties` pins `agreementId` per test through the TCK's
`@ConfigParam` mechanism, and `test/tck/dsbox.yaml` lists the same ids under
`agreements:`.

The exact property key spelling is **unconfirmed**. `agreementId` and `format`
are declared `@ConfigParam` with no explicit key on
`AbstractTransferProcessProviderTest`, so the key is derived by the TCK core
from the concrete test id and field name — by the `CN_C_01_02_DATASETID`
precedent that would be `TP_01_01_AGREEMENTID`, but the previous milestone
found a related assumption wrong (there is no `CN_C_xx_yy_OFFERID` key at all).
Confirm it from a real run before relying on it; if the derivation differs, the
fixture changes but nothing in this design does.

`format` defaults to `HTTP-PULL`, which is the format Phase B implements.

## Phase B: data plane (HTTP-PULL)

### What a dataset's bytes are

`config.Dataset` today is an id and an optional validity period — pure catalog
metadata with no content behind it. It gains `source_file`, a path resolved
**once at config load**, where the file's existence, regular-file-ness, and
readability are checked. A path never arrives from the wire, so there is no
traversal surface to defend.

### Access is the state machine

The provider mints a per-transfer token (`crypto/rand`, 256 bits) on the
`REQUESTED -> STARTED` transition, stores it on the transfer row, and sends it
in the `TransferStartMessage`'s `dataAddress` as an `EndpointProperty`
alongside the endpoint URL.

The pull endpoint lives on the public DSP listener at
`GET /2025-1/transfers/{providerPid}/data`. It is **not a DSP protocol
endpoint** — the specification defines no such path, and it exists only because
this connector chose to be the data source. It sits on the public listener
because the counterparty must reach it, and under the transfer's own path
because its lifetime is exactly that transfer's. That distinction is worth
keeping visible: a reader must not mistake it for part of the protocol this
project claims conformance to.

It requires two things at once:

1. The token matches, compared with `crypto/subtle.ConstantTimeCompare`.
2. **The transfer is in `STARTED` right now.**

The second condition is the design's centre. It means no separate revocation
mechanism exists or is needed: suspension, termination, and completion each
cut data access as a side effect of being themselves. It is also a property
this project can prove without the TCK, which matters because the TCK proves
nothing here.

Serving uses the standard library only (`os.Open` plus `http.ServeContent`),
which brings range requests along for free and adds no dependency.

### A terminated transfer stops mid-stream

Checking state only at request entry would mean a `TERMINATED` transfer keeps
shipping bytes until the file ends — a state that does not enforce what it
claims, which is the same defect class this project reversed in §24.6. So the
copy loop re-checks state and aborts the response when the transfer leaves
`STARTED`.

An aborted response is a well-defined failure: with `Content-Length` set, a
short body is detectable as failure by any correct client. There is no resume
support in v1 — the consumer requests again from the beginning.

**Trade-off accepted.** The state check is throttled to at most once per second
rather than run per chunk, because the store holds a single connection
(`SetMaxOpenConns(1)`) and a read per 32 KiB chunk would serialize the whole
connector behind one download. So cut-off is bounded by roughly a second, not
immediate. An in-memory per-transfer cancellation signal would make it
immediate, at the cost of new shared mutable state; that trade is available
later and is safer to make now that `go test -race` runs in CI, but it is not
worth taking in the same milestone that introduces the endpoint.

### `endpointType` is unresolved

The value `dataAddress.endpointType` should carry is not determined by the TCK,
which never inspects it. It must come from the DSP 2025-1 specification. If the
specification names no normative value, that finding itself gets recorded in
`DECISIONS.md` rather than a plausible-looking string being invented.

## Testing

Phase A follows the established shape: pure decision functions pinned by unit
tests, handler tests for each inbound message, and the real `make tck` as the
final authority. The gate takes `"TP": 15` and the README's pass rate becomes
49 of 65.

Phase B has no external authority, so its tests are the whole of its evidence.
At minimum:

- a wrong token is rejected
- a missing token is rejected
- transfer A's token cannot pull transfer B
- bytes are served in `STARTED` and refused in each of `REQUESTED`,
  `SUSPENDED`, `TERMINATED`, and `COMPLETED`
- the served bytes equal the file's bytes
- terminating a transfer mid-download aborts the response, within the
  documented delay
- an end-to-end test that drives the control plane to `STARTED` and then
  actually pulls the bytes over HTTP

## Documentation obligations

`README.md` gains a `TP` row and the pass rate moves to 49 of 65. It must also
say that data transfer exists and is **not** covered by the TCK. Phase B raises
no number in that table, and a repository whose stated premise is "a compliance
claim anyone can verify" has to be equally plain about the part nobody can
verify from a CI artifact.

`DECISIONS.md` gains §25 recording: the agreement-validation decision and the
weight the `agreements:` config list carries; state-as-access-control; the
`source_file` choice; the token model; the mid-stream abort and its bounded
delay; the `endpointType` finding; and the scope statement that the data plane
is outside TCK coverage.

## Risks

1. **The `@ConfigParam` key derivation is unconfirmed.** Mitigated by the fact
   that only the fixture changes if it differs. Discovered on the first real
   `make tck`.
2. **Phase B has no external verification.** Mitigated only by the test list
   above being treated as a floor rather than a target.
3. **The bounded cut-off delay is a real, if small, gap** between a transfer
   being terminated and its data being refused. Recorded rather than hidden.
4. **`endpointType` may have no normative value in the spec**, in which case
   interoperability with a real consumer is unproven for that field.
