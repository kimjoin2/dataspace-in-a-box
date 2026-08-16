# Transfer process: what the TCK actually requires

A survey of the `TP` and `TP_C` suites, taken before any transfer-process
design work, so that the design argues from what the TCK verifies rather than
from what "transfer process" sounds like it should mean.

Everything below was read out of the TCK runtime the harness actually runs —
`eclipsedataspacetck/dsp-tck-runtime@sha256:45cfafa40486714d441057bc6063653a1feebb95444f721974147cb0dd7416ad`,
the digest pinned in `test/tck/compose.yaml` — not from the upstream repository's
default branch, which can drift from the pinned image. Every finding is
reproducible with the commands given.

## The headline: these suites do not move data

**The `TP`/`TP_C` suites verify the transfer protocol's control plane and
nothing else.** No test sends, receives, or asserts a single byte of the data
being "transferred".

Each test carries its own sequence diagram in the compiled class. Extracting all
32 and tallying every step gives the complete vocabulary the suites exercise:

| Step | Occurrences across the 32 tests |
|---|---|
| `200 OK` | 64 |
| `TransferRequestMessage` | 32 |
| `TransferStartMessage` | 30 |
| `TransferProcess` (response document) | 22 |
| `TransferTerminationMessage` | 18 |
| `Signal to start transfer` (the `TP_C` initiate hook) | 16 |
| `TransferSuspensionMessage` | 14 |
| `TransferCompletionMessage` | 14 |
| `4xx ERROR` | 12 |
| `TransferRequest` | 10 |

Two of those numbers double as a check on the extraction: every one of the 32
tests opens with exactly one `TransferRequestMessage`, and `Signal to start
transfer` appears once per consumer test, of which there are 16.

The same extraction searched for `bytes`, `payload`, `download`, `upload`,
`data transfer`, and `dataAddress` across all 32 diagrams and found **zero**
matches. Every test name describes a state transition — request, started,
suspended, terminated, completed — and nothing else.

This matters for more than scope. It means **passing this TCK is not evidence
that this connector can transfer data**, and no document in this repository may
imply otherwise on the strength of a green `TP`/`TP_C` run.

```sh
docker run --rm --entrypoint sh <pinned-image> -c 'cat /app/tck-runtime.jar' > tck.jar
mkdir tckx && (cd tckx && unzip -oq ../tck.jar 'org/eclipse/dataspacetck/dsp/verification/tp/*')
javap -p -v -classpath tckx org.eclipse.dataspacetck.dsp.verification.tp.TransferProcessProvider01Test |
  grep -oE 'participant TCK as.*'
```

Note when repeating the tally: `javap -v` emits each diagram string **twice**
(once in the constant pool listing, once as the annotation value), so a raw
count over its output is exactly double the real one.

## Test inventory: 32 methods, 30 gated results

Each suite declares 16 test methods, but **`TP:02-04` and `TP_C:02-04` carry
JUnit's `@Disabled`** upstream, so each suite produces 15 results.

```sh
javap -p -classpath tckx org.eclipse.dataspacetck.dsp.verification.tp.TransferProcessProvider02Test |
  grep -oE 'void tp_[0-9_]+\(\)'          # 5 methods
javap -p -v -classpath tckx org.eclipse.dataspacetck.dsp.verification.tp.TransferProcessProvider02Test |
  grep -B3 -i Disabled                     # tp_02_04 is annotated @Disabled
```

The gate's `expected` map therefore takes `"TP": 15` and `"TP_C": 15`, and the
README's denominator becomes 65 — which is already what it says, because the
CN_C milestone corrected it from an estimate to a measurement. Deriving these
counts from the method list alone would have produced 16 and failed the gate.

Both suites are tagged `dsp-tp`.

The two roles are near mirror images: the same `01`/`02`/`03` grouping, the same
5/5/6 split, the same disabled test. `TP` drives the connector as the transfer
provider; `TP_C` drives it as the consumer and needs an initiate hook, exactly
as `CN_C` did — `test/tck/config.properties` already carries
`dataspacetck.dsp.connector.transfer.initiate.url`.

## State machine

`TransferProcess$State` declares six states:

```
INITIALIZED -> REQUESTED -> STARTED -> { COMPLETED, SUSPENDED, TERMINATED }
                              ^                          |
                              +--------------------------+
```

`INITIALIZED` is internal to the TCK's own model. The wire enum in
`transfer-process-schema.json` has five values and omits it:
`REQUESTED`, `STARTED`, `TERMINATED`, `COMPLETED`, `SUSPENDED`.

`TransferProcess$TransferKind` is `Consumer | Provider` — which party is the
data sink — not a transfer format.

## Messages and schemas

`AbstractTransferProcessTest` registers validators for exactly three schemas:

- `transfer/transfer-process-schema.json`
- `transfer/transfer-request-message-schema.json`
- `transfer/transfer-start-message-schema.json`

`TransferSuspensionMessage`, `TransferTerminationMessage`, and
`TransferCompletionMessage` have **no registered validator**, so their shape is
checked only by whatever the pipeline reads out of them.

Required fields, from the pinned image's own schemas:

| Message | Required |
|---|---|
| `TransferRequestMessage` | `@context`, `@type`, `agreementId`, `format`, `callbackAddress`, `consumerPid` |
| `TransferStartMessage` | `@context`, `@type`, `providerPid`, `consumerPid` |
| `TransferProcess` | `@context`, `@type`, `providerPid`, `consumerPid`, `state` |
| `DataAddress` (when present) | `@type`, `endpointType` |

**`dataAddress` is optional in both messages that can carry it.** Its schema
exists and is referenced, but neither `TransferRequestMessage` nor
`TransferStartMessage` lists it as required, and no test asserts on one. A
`DataAddress` that is sent needs only `@type` and `endpointType`;
`endpointProperties`, if present, is `minItems: 1` — the same empty-array trap
that `MessageOffer`'s `permission`/`prohibition` set in `DECISIONS.md` §24.7.

## The finding that needs a decision: `agreementId` is a random UUID

Both `AbstractTransferProcessProviderTest` and
`AbstractTransferProcessConsumerTest` declare `agreementId` as a `@ConfigParam`
defaulting to `UUID.randomUUID().toString()`, and `format` as a `@ConfigParam`
defaulting to `HTTP-PULL`.

So by default **the TCK asks this connector to transfer data under an agreement
that was never negotiated and does not exist in its store.** The `TP` suites do
not run a negotiation first; they are independent of `CN`/`CN_C` state.

That puts the design straight into the shape `CN_C` already hit once:

- Validate that the agreement exists, and all 30 tests fail at the first message.
- Skip the validation, and the connector starts a transfer for a contract it
  never agreed to — which is the transfer-protocol form of `CLAUDE.md`'s
  "Never accept a constraint that is not enforced", and `DECISIONS.md` §14's
  "silent non-enforcement is a security bug wearing a feature costume".

`agreementId` being a `@ConfigParam` means a fixed value can be supplied from
`config.properties`, which may allow a seeded agreement instead of a random one.
Whether that is workable — and what a real, non-TCK provider should do with a
transfer request naming an unknown agreement — is a design decision, not a
fixture detail, and it should be settled before implementation rather than
discovered by a failing suite.

## What this leaves open for the design

1. Whether `TP` and `TP_C` are one milestone or two. The mirror structure and
   the `CN` -> `CN_C` precedent both point at two.
2. What the connector does with a `TransferRequestMessage` naming an agreement
   it has no record of (above).
3. Whether v1 emits a `dataAddress` at all, given nothing requires or inspects
   one.
4. Whether a data plane is in scope for this project at all, and if so, that it
   is tracked as work the TCK does not verify — because, per the top of this
   document, it does not.
