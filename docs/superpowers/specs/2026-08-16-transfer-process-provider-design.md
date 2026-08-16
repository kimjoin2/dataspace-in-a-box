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

- **Phase A — control plane**, including the provider's own autonomous
  transitions, which the TCK turned out to require (see "Autonomous provider
  behavior, keyed by agreement" — that section is an amendment written after
  implementation began, and it is the largest thing this milestone got wrong on
  the first pass). Ends with `TP` at 15/15 in the compliance gate,
  merged. The TCK is the authority for this phase.
- **Phase B — data plane.** Serves a dataset's bytes over an authorized pull
  endpoint. Raises no TCK number at all.

Phase A is planned and merged before Phase B is planned. The phases have
different evidence bases and mixing them would make it impossible to say
afterwards what proved what.

## Phase A: control plane

### Storage

Two new tables. The first makes an agreement a first-class record rather than
something inferred from a negotiation row:

```sql
CREATE TABLE IF NOT EXISTS agreements (
    agreement_id TEXT PRIMARY KEY,
    dataset_id   TEXT NOT NULL,
    consumer_pid TEXT NOT NULL DEFAULT '',
    origin       TEXT NOT NULL,          -- 'negotiated' | 'imported'
    created_at   TEXT NOT NULL
);
```

A row is written when a negotiation reaches `AGREED` — the moment this
connector actually issues an agreement document — with `agreement_id` set to
the value `buildAgreementMessage` already emits (`n.ProviderPID`). Agreements
concluded outside this connector are imported (below) and carry
`origin = 'imported'`.

This is not speculative structure: the transfer protocol has to answer "does
this agreement exist" on every request, and inferring it by scanning
`negotiations` for a matching `provider_pid` would leave externally-concluded
agreements unrepresentable.

Writing that row is a change to the negotiation flow, so Phase A touches the
provider-role negotiation handler as well as the new transfer code. No backfill
is needed for negotiations that reached `AGREED` before this table existed:
there is no release yet, so no such deployment exists.

The second table holds the transfers themselves, mirroring `negotiations`:

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

### Autonomous provider behavior, keyed by agreement

**Amended after implementation.** This section replaces an earlier claim that
the provider pushes one `TransferStartMessage` and thereafter only reacts. The
TCK falsified that, and the correction is large enough to be worth stating as a
correction rather than quietly rewriting.

In the `TP:01-xx` tests the TCK sends this connector exactly one message — the
`TransferRequestMessage` — and thereafter only polls `GET /transfers/{id}`.
There is no trigger, no control call, no out-of-band nudge. (The
`recordXxxAction` calls at the top of every provider test are a TCK self-test
path, switched off against a remote connector.) The connector is expected to
decide its own subsequent transitions, and **the only test-varying field on the
wire is `agreementId`** — the same shape the negotiation suites already use,
where `datasetId` selects a configured policy.

Two consequences, and the second is the one that is easy to miss:

1. The connector must be able to emit a provider-initiated
   `TransferSuspensionMessage`, `TransferCompletionMessage`, or
   `TransferTerminationMessage` on a transfer of its own, with nothing inbound
   prompting it.
2. **Starting must itself be conditional.** Four of the sixteen provider tests
   carry no "provider started" step at all — `TP:01-05`, `TP:02-05`,
   `TP:03-01`, `TP:03-02` — and poll for `REQUESTED`. An unconditional start
   does not merely fail to help there; it breaks them.

So `config.Config` gains `transfer_policies`, a list keyed by `agreement_id`,
each carrying the sequence of states this connector walks on its own after
accepting a request. `[STARTED]` is the default for an agreement with no entry;
`[]` means stay in `REQUESTED`; `[STARTED, SUSPENDED, STARTED, COMPLETED]` is
the longest the TCK asks for. Each step pushes the matching message to the
consumer's callback address through `pushCallback` and then advances the stored
state, which is the same machinery the initial start already uses.

*Trade-off accepted.* A configured script is a stand-in for judgement this
connector does not have. A real provider decides to suspend or complete a
transfer for operational reasons — the data ran out, the window closed, an
operator intervened — and v1 has none of those inputs, exactly as v1's consumer
role has no reason of its own to reject an offer and takes that from
`consumer_policies` instead. The honest framing is the same in both places:
this is the connector's *configured* autonomous behavior, and the configuration
is where a real reason would eventually plug in.

### Agreement validation

**A `TransferRequestMessage` naming an agreement this connector has no record
of is rejected with `400`.**

The alternative — accept it, record the gap — is the shape `DECISIONS.md` §24.6
took in the previous milestone and had to be reversed. Here the stakes are
higher than a policy constraint: accepting an unknown `agreementId` means a
counterparty can start a transfer by citing a contract that was never made.

An agreement is "known" if a row exists in `agreements`. That is the single
source of truth, and it has exactly two writers:

1. **Negotiation.** Reaching `AGREED` writes the row, because that is when this
   connector issues the agreement document.
2. **Import.** `POST /agreements` on the management API records an agreement
   concluded outside this connector, with `origin = 'imported'`.

An earlier draft of this design put externally-concluded agreements in a list
in the config file instead. That was wrong, and the way it was wrong is worth
recording: an agreement is **runtime state**, not a static declaration of what
this connector advertises, so putting it in the config file creates a second
source of truth for the same concept and makes "edit a YAML file, restart" the
way contracts come into being. The tell was that the design needed a warning
attached — *writing an id into that list creates a contract* — and a design
that needs a warning is usually the wrong design. The weight is real either
way; the management API is a defensible place to put it, because importing an
agreement is then an authenticated operator action against a running connector
rather than a line in a file.

The import endpoint is the management API's first endpoint beyond `/health`,
and it is the reason this milestone touches `internal/mgmt` at all.

It is worth being exact about what that costs, because the obvious reading is
wrong. `DECISIONS.md` §11 settled that the management API takes one static
bearer token — but only as a decision. **No authentication is implemented:**
`internal/mgmt` today is `NewRouter()` with no arguments serving `/health`
alone, and `config.Config` has no token field. So Phase A does not "add an
endpoint to an authenticated API"; it stands that API's auth model up for the
first time — a config field and its validation, a bearer check, a router that
now takes config and store, and the wiring in `cmd/dsbox`.

That was weighed against a CLI subcommand writing to the store directly, which
needs no auth, no HTTP surface, and no config field. The management API was
chosen anyway, on the grounds that §11 already committed to it and an operator
importing an agreement is a real need rather than a harness convenience. The
trade is accepted with its own guard rail: `POST /agreements` records an
agreement and nothing else. This is not the start of a general management CRUD
surface, and a later milestone that wants one argues for it on its own merits.

### TCK fixtures

`test/tck/config.properties` pins `agreementId` per test through the TCK's
`@ConfigParam` mechanism, and `test/tck/run.sh` imports those same ids through
the management API before starting the suite. The script already waits on
`/health` before handing over to the TCK, so the seeding step goes there, in
the open, as a visible part of the harness rather than a config convention
somebody has to infer.

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

The DSP listener's `WriteTimeout` is 30 seconds, which would cut off any
download slower than that mid-stream. `cmd/dsbox/main.go` already carries a
comment predicting exactly this, written during an earlier milestone. Phase B
has to resolve it — most likely by dropping `WriteTimeout` on that server and
bounding request duration another way, since a per-request deadline cannot be
set from a shared server timeout. Whatever is chosen, it is a change to a
security-relevant default and belongs in `DECISIONS.md` with its reasoning,
not a silent edit.

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

`DECISIONS.md` gains §25 recording: the agreement-validation decision; why
agreements are a table written at `AGREED` and imported through the management
API rather than declared in the config file, since that boundary — runtime
state in the database, static declarations in the config — is the kind of rule
a later milestone will otherwise re-litigate; state-as-access-control; the
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
5. **The management API gains its first real endpoint**, and with it the first
   write path into this connector that is not a DSP message. The auth model is
   already settled (`DECISIONS.md` §11), so the risk is not authentication but
   scope: `POST /agreements` must record an agreement and nothing more. It is
   not the beginning of a general management CRUD surface, and a later
   milestone wanting one should argue for it on its own merits.
