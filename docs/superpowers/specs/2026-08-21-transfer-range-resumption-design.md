# Design: transfer robustness — HTTP Range and resumption

`2026-08-19-data-plane-design.md` scoped this out on purpose: "Out: push
transfers, streaming, range requests, resumption". That milestone's own
`dspSrv` carries the marker — `WriteTimeout: 30 * time.Second`, with a
comment reading "WriteTimeout will need revisiting once transfer streaming
lands: a streaming response can legitimately run longer than 30 seconds and
would be cut off mid-stream." README states the resulting limit plainly:
"Transfers are small and one-shot... there are no range requests and no
resumption, so a failed pull refetches from zero." This milestone closes
that gap.

## Scope

In: `Range: bytes=N-` support on the provider's data endpoint; a consumer
that resumes an interrupted pull instead of refetching from zero; a
dataset-keyed way for a provider to autonomously suspend and restart a
transfer, closing a gap `DECISIONS.md` §25.7 named but left unsolved; a demo
extension that deliberately interrupts a real pull and proves the resumed
file is correct.

Out: multi-range requests and the `bytes=-N` suffix form — resumption needs
only a single open-ended range, and RFC 7233's fuller grammar has no
consumer here. Out too: any integrity check beyond file size — see
"Integrity across a resume" below for why. Out: cleaning up an orphaned
partial file left behind by a transfer that terminates instead of
restarting — a real gap, named and accepted rather than solved (see "Accepted
trade-offs").

## Provider: `Range` on the data endpoint

`data_handler.go`'s `handleData` currently ignores any `Range` header and
streams the whole file. It gains exactly one supported form: `bytes=N-`,
parsed as a single non-negative integer with nothing after the dash. Anything
else — no header, a form this connector does not parse, multiple ranges, a
suffix range — is treated as no `Range` header at all and gets today's
unconditional `200` with the whole file streamed from the start. That
is RFC 7233's own guidance for a range a server does not support: ignore it,
serve the whole thing.

A parsed `N` is checked against the file's current size, read via `Stat`
right before serving (the same "config and the file are live, not
snapshotted" rule `SourceFile`'s own doc comment already states):

- `0 <= N < size`: `206 Partial Content`, `Content-Range: bytes N-(size-1)/size`,
  body seeked to `N` and streamed to the end. Every check `handleData`
  already makes — transfer exists, is `STARTED`, belongs to the requesting
  participant, the dataset's `ValidityUntil` still holds — runs first and
  unchanged; `Range` handling only changes what happens after they pass.
- `N >= size`: `416 Range Not Satisfiable`, `Content-Range: bytes */size`.
  This is not an error path bolted on for this milestone — it is what
  "integrity across a resume" below is built out of.

## Consumer: resuming instead of refetching

`pullTransferData` currently writes to `os.CreateTemp(dir, ".partial-*")` —
a random name, unrecoverable across calls — and on any error it removes that
file, discards everything received, and does nothing further; the transfer
stays `STARTED` and nothing retries automatically. That last part is
deliberate and stays exactly as it is: "Retrying automatically would make a
slow first fetch indistinguishable from a stuck one." This milestone does
not add self-driven retry. It makes the *next* delivery attempt this
connector is externally handed — a restart — pick up where the last one
left off.

The temp file gets a deterministic name: `downloads/.partial-<consumerPID>`.
`pullTransferData` starts by checking whether that file already exists:

- **Absent** (first attempt for this transfer): today's behavior exactly —
  no `Range` header, write from byte 0.
- **Present with size `N > 0`** (a restart after an earlier attempt left
  bytes behind): send `Range: bytes=N-`.
  - `206`: append the response body to the existing partial file.
  - `416`: the provider says its file is no longer longer than what this
    connector already has — see "Integrity across a resume". Delete the
    partial file and return without writing anything. The *next* restart,
    if one arrives, finds no partial file and fetches from byte 0.
  - Anything else (a status `>= 300`, or an unexpected `200` to a request
    that carried `Range` — see "Accepted trade-offs"): log and return,
    **leaving the partial file untouched**. This is the one behavior change
    from today for ordinary failures: an ordinary network error during a
    resumed pull no longer discards what was already received.

On a full, successful receipt — whichever branch got there — the partial
file is renamed to its final path exactly as today.

## Integrity across a resume

Between one attempt and a restart, the operator could have replaced
`source_file` with different content — config is read live, not
snapshotted, the same as every other config-driven value in this connector.
The chosen check is `416`'s own semantics: a partial file whose size is at
or past the provider's *current* file size cannot be a valid prefix of that
file, so the provider refuses the range and the consumer treats that as
"this is not the file I was receiving; start over." No hash, no `ETag`, no
`Last-Modified` — those would answer "did the file change" more completely
than a size comparison does, but this connector has no field to carry one
today (`DataAddress` names an endpoint, nothing else) and adding one is a
wire-shape change to a message the TCK's schema governs, which is a cost
this milestone does not need to pay for the value it buys: **a same-size
replacement that changes only content is not caught.** That residual risk is
accepted, not solved. `SourceFile`'s doc comment already tells an operator a
swapped file is their business; this is the same posture applied to a resume
in flight.

## The demo's blocker, and the fix that also closes a real gap

Proving resumption works needs a real interrupted pull, not a mocked one —
the same reasoning that made `make demo` part of the data-plane milestone
rather than a nicety after it: "No test in either transfer suite sends,
receives, or asserts a byte, so a green suite is not evidence that data
moves." The natural way to force a real interruption
and a real recovery is `transfer_policies`, which already lets a provider
autonomously walk `[STARTED, SUSPENDED, STARTED, COMPLETED]`.

It cannot be used here. `transfer_policies` is keyed by `agreement_id`, and
`DECISIONS.md` §25.7 already recorded why that fails for exactly this case:
"A negotiated agreement's id is the issuing negotiation's provider pid...
and it does not exist until that negotiation is under way, so nobody can
write it into a configuration file beforehand... A deployment that wants
autonomous behavior on agreements it negotiated needs a different key, and
there is no obvious one on the wire: `agreementId` is the only test-varying
field, and `datasetId` is not carried on a `TransferRequestMessage` at all."
The demo negotiates for real — its
agreement id is a UUID minted mid-run, confirmed by running it
(`agreement 4d6e6582-0e8c-4414-a65d-bb63404678f2`, different on every run) —
which is precisely the case §25.7 named as unreachable.

**The different key §25.7 said did not exist on the wire does exist off
it: `dataset_id`.** `hasSourceFor` already resolves an agreement's dataset
id via `store.GetAgreement`, regardless of whether that agreement was
negotiated or imported — the row carries `DatasetID` either way.
`config.Dataset` gains `TransferSequence []string`
(`yaml:"transfer_sequence"`), the same shape and the same legality rules as
`TransferPolicy.Sequence`. `resolveTransferSequence` gains a `datasetID`
parameter and, when no `agreement_id` entry matches, falls back to the
matching dataset's `TransferSequence` before defaulting to `[STARTED]`. The
one call site, `driveTransfer`, resolves `datasetID` via
`h.store.GetAgreement(t.AgreementID)` — the same lookup `hasSourceFor`
already performs — and passes it down; `resolveTransferSequence` itself
stays a pure function, store-free, exactly as testable as it is today.

This is not a demo-only convenience bolted in. It closes the gap §25.7 named
and left open: a real deployment that negotiates its own agreements, not
only one seeded by a harness, now has a way to configure autonomous
transfer behavior at all.

## Demo-only fault injection

`config.Dataset` gains `SimulateInterruptAfterBytes int64`
(`yaml:"simulate_interrupt_after_bytes"`, default `0` = disabled), the same
"a real deployment has no reason to configure this" category as
`TerminateOnVerify`. When set and `handleData` is serving a request with
**no** `Range` header, it writes exactly that many bytes and then hijacks
the connection (`http.Hijacker`) and closes it directly — not a clean
response, a real severed connection, so the consumer's `io.Copy` fails the
way an actual network interruption fails it. A request that **does** carry
`Range` is never truncated, regardless of this setting: the first pull gets
cut short, the resumed pull completes normally, which is what lets the
sequence terminate instead of truncating forever.

The demo's own HTTP server already runs over plain HTTP/1.1
(`http.Server.ListenAndServe`, no TLS, no h2c) and no middleware in the
request path wraps `http.ResponseWriter` (`auth_middleware.go` passes the
original one through), so `Hijack` works without any change elsewhere.

## The demo scenario

`demo/provider.yaml`'s dataset gains `simulate_interrupt_after_bytes` (some
value smaller than the sample file) and `transfer_sequence: [STARTED,
SUSPENDED, STARTED, COMPLETED]`. Nothing in `demo/run.sh` needs to manually
trigger anything — the provider walks that sequence on its own, exactly as
it already does for the TCK's own fixtures, `transferStepDelay` apart. The
first `STARTED` triggers a pull that gets truncated; well before the next
`transferStepDelay` elapses, `SUSPENDED` arrives; then a second `STARTED`
carrying the same `dataAddress` arrives, and `pullTransferData` finds the
partial file and resumes it.

`run.sh` keeps its existing byte-for-byte diff of the received file against
the source — that proves correctness, but not that resumption specifically
happened, since a silent full refetch would pass the same diff. It also
greps the consumer's log for a line this milestone adds to the resume path
(reporting the partial size found and the range requested), so the demo
fails if the file happened to match without the resume path ever running.

## Accepted trade-offs

- **A `200` in answer to a ranged request is treated as an error, not
  handled.** Both roles here are this same connector, so a provider that
  understands `Range` well enough to answer `416` correctly but answers
  `200` to a `Range` request should not happen. If it ever did, appending
  that body to an existing partial would silently corrupt the file — the
  chosen response is to abort without writing rather than guess.
- **An orphaned partial file is not cleaned up.** A transfer that terminates
  instead of restarting after being interrupted leaves
  `downloads/.partial-<consumerPID>` on disk forever. Pre-existing risk in a
  smaller form (a stray temp file could already leak on an unclean process
  exit); this milestone makes the leaked file larger and its name
  predictable. Solving it needs a retention or garbage-collection policy
  this project has none of yet, and inventing one here would be scope this
  milestone does not need.
- **Same-size content changes across a resume are not caught.** Stated
  above, repeated here because it is the sharpest edge: the integrity check
  this milestone ships answers "did the file get shorter or disappear," not
  "is this still the same file."
