# Design: the HTTP-PULL data plane

Everything so far negotiates the right to move data and then does not move it.
This milestone serves bytes, which is what makes the thing a dataspace rather
than a protocol implementation. It is also the first milestone the TCK does
not verify at all — no test in either transfer suite sends, receives, or
asserts a byte — so it brings its own end-to-end evidence.

The deliverable is not only code. It is a command that stands two connectors
up, has them authenticate, negotiate, transfer, and land a file, and then
checks the file is the one that was sent.

## Scope

In: a provider endpoint that serves a dataset's bytes; a `dataAddress` on the
`TransferStartMessage` that says where; a consumer that pulls when a transfer
starts and writes what it gets; a demo that runs the whole exchange between
two real connectors and verifies the result.

Out: push transfers, streaming, range requests, resumption, and any format
other than HTTP-PULL. Out too: making the data plane a general file server —
it serves exactly what a live agreement entitles a counterparty to, and
nothing else is reachable through it.

## The two questions this milestone had to settle first

### Authorization: no new credential

The obvious design gives the `dataAddress` its own bearer token with its own
lifetime and its own verification path. This one does not. The data endpoint
sits behind the **same** roster credential milestone 1 already enforces, and
adds three checks of its own:

1. the path names a transfer this connector is providing,
2. that transfer is in `STARTED`,
3. the authenticated issuer is the counterparty that transfer is with.

That is the whole authorization rule. It needs no new token type, no second
expiry to reason about, and no way for a leaked URL to be useful on its own —
a `dataAddress` is not a capability here, it is an address, and possessing it
grants nothing. `endpointProperties.authorization` is therefore **absent**
from what this connector emits: including a token there would be inventing a
second credential to sit beside one that already answers the question.

The consumer needs no new machinery either. It already mints a credential for
every outbound call; a data pull is one more.

### The terminal step: documented, not defended

`docs/follow-ups.md` records the conflict: a `transfer_policies` sequence like
`[STARTED, COMPLETED]` completes two hundred milliseconds after starting, and
once `STARTED` gates a pull that configuration cuts access before a consumer
can fetch anything. The follow-up offered two ways out — bound autonomous
completion by something the data plane observes, or state plainly that a
terminal step and real bytes are mutually exclusive.

**Taking the second.** The first sounds safer and is worse: "complete once a
pull has been observed" makes the state machine depend on data-plane traffic,
which turns a missing GET into a transfer stuck in `STARTED` forever, and
turns a retried GET into an ambiguity about whether the transfer already
finished. It also invents a rule DSP 2025-1 does not have.

The honest framing is that `transfer_policies` is a **test affordance** — it
exists so the TCK's provider suite can drive a connector through state
sequences it would otherwise need a human for. Its default is `[STARTED]`,
which is exactly the configuration that serves data. A sequence naming
`COMPLETED` or `TERMINATED` is an operator saying "end this transfer on a
timer", and ending a transfer ends access to its bytes. That is not a defect
to defend against; it is the option doing what it says.

So: the config documentation says it, the `source_file` documentation says it,
and the follow-up entry is closed by being answered rather than by being
fixed.

## What a dataset is made of

`config.Dataset` gains one optional field:

```yaml
datasets:
  - id: urn:dataset:sample
    source_file: /var/lib/dsbox/data/sample.csv
```

Optional, because a dataset without one is exactly what every dataset is
today: advertisable, negotiable, transferable as a control-plane exercise, and
unable to serve bytes. A transfer for such a dataset starts normally and its
data endpoint answers `409` — the agreement is real, the transfer is real, and
there is simply nothing configured behind it. That is a different answer from
`404` (no such transfer) and from `403` (not yours), and an operator reading a
log should be able to tell the three apart.

Read at request time, not at startup. A file swapped underneath the connector
is the operator's business, and holding an open handle for the life of a
transfer would be a lock with no purpose. The path is resolved and checked
once at load, so a typo fails at boot rather than on the first pull.

## The endpoint

```
GET {public_url}/{version}/data/{providerPid}
```

Under the versioned path, behind the auth middleware, alongside the protocol
routes rather than on the management listener — it is a counterparty-facing
endpoint and it is reached by the same credential as everything else the
counterparty does.

`{providerPid}` and not a random opaque token, deliberately: the identifier is
not the secret. The three checks above are, and an identifier that carries no
authority can be logged, correlated, and pasted into a bug report without
leaking anything.

The response streams the file with `Content-Type: application/octet-stream`
and no `Content-Disposition` — this is an API, not a browser download. Errors
are the protocol's `TransferError` document, so a consumer parses one shape.

`WriteTimeout` on the DSP server is thirty seconds today, which is a real
limit on file size. Raised for the data listener specifically rather than
globally, and named as a limit in the README rather than left for someone to
discover with a large file.

## The consumer side

When a `TransferStartMessage` arrives carrying a `dataAddress`, the consumer
stores it and pulls, in a goroutine, exactly once.

Where the bytes go: `{data_dir}/downloads/{consumerPid}`. Under `data_dir`
because that is already the one directory this connector owns and the operator
has already granted; a second configurable path would be a second thing to get
wrong for no gain in this milestone.

A pull that fails is logged and not retried. The transfer stays `STARTED`,
which is the honest state — the provider is still willing to serve, and this
connector can be asked again by an operator. Retrying automatically would make
a slow first fetch indistinguishable from a stuck one, and would double-write
the file on a race.

The download is written to a temporary file and renamed into place, so a
partial fetch never appears as a complete download. A reader that finds the
file finds all of it.

## Testing

Unit tests for the three authorization checks, each proved by the case that
fails it: a transfer that is not `STARTED` is refused; a transfer belonging to
a different counterparty is refused; an unknown transfer is `404`. And the
positive case, that a `STARTED` transfer with the right issuer streams exactly
the configured file's bytes.

The refusals must be checked with everything else valid, or they prove nothing
— a test that omits the credential entirely would pass against a handler that
never checks state at all.

Consumer-side: a `TransferStartMessage` carrying a `dataAddress` results in a
file on disk with the served content, and one carrying none results in no
fetch attempt.

**And the demo, which is the milestone's real test.** `make demo` brings up
two connectors with distinct identities and a shared roster, gives the
provider a file, has the consumer negotiate an agreement and initiate a
transfer, waits for the download, and diffs it against the original. It exits
non-zero if the bytes differ or never arrive. That is the end-to-end evidence
the TCK cannot supply, and it is what turns "the code compiles and the units
pass" into "two connectors moved a file".

The TCK stays exactly where it is: 64 of 65, 0 outside the gate, as a
regression gate rather than a goal. The data plane must not change it, and if
it does, something in the control plane broke.

## Accepted trade-offs

**No range requests or resumption.** A failed pull refetches from zero. Files
this milestone targets are small enough that this is cheaper than the
bookkeeping.

**One pull per transfer.** The consumer fetches once on start. A second fetch
is an operator action, not an automatic one.

**Whole-file buffering is avoided but timeouts still bound size.** The
response streams, so memory is not the limit; the write timeout is.

## Done criteria

- `make demo` exits zero, having moved a real file between two authenticated
  connectors, and non-zero if the bytes differ.
- The data endpoint refuses a transfer that is not `STARTED`, one belonging to
  another counterparty, and an unknown id, with three distinguishable answers.
- `make tck` still reports 64 of 65 with 0 outside the gate.
- `go test -race -count=2 ./...` is green.
