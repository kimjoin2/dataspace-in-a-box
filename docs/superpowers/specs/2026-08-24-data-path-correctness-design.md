# Data path correctness: bound progress, verify size, record arrival

**Status:** design, approved 2026-08-24.
**Measures against:** `docs/goal-gap-analysis.md` P2, which this milestone is
the whole of. It is item 1 of that document's order.

## The problem, and why it goes first

`make demo` moves a real file and diffs it. That is the only evidence in the
repository that this connector moves data at all, and the files it moves are
a few kilobytes. The TCK moves no bytes in either transfer suite. So the
product's central claim is standing without a measurement, and the means to
take one does not exist here.

Underneath that gap sit six defects, all found by reading rather than by
failure, because nothing runs that could fail:

1. **The consumer's data pull is bounded by ten seconds of wall clock.**
   `pullTransferData` issues its fetch through `callbackHTTPClient`
   (`internal/dsp/transfer_consumer_handler.go:334`), whose `Timeout` is
   `10 * time.Second` (`internal/dsp/callback.go:26`). `http.Client.Timeout`
   covers the response body read. That client's doc comment describes
   callback pushes only; nothing says a data pull reuses it.
2. **The provider's response is bounded by thirty seconds.** `dspSrv` sets
   `WriteTimeout: 30 * time.Second` (`cmd/dsbox/main.go`), and
   `cmd/dsbox/main.go`'s own comment predicted this would need revisiting
   once streaming landed. Streaming landed; it was not revisited.
3. **There is no expected size.** On the 200 path the provider sets only
   `Content-Type` and streams, so Go sends the response chunked and the
   consumer never learns how many bytes to expect. On the 206 path
   `Content-Range` carries the complete length and `contentRangeFirstByte`
   parses only the first-byte-pos, discarding it.
4. **A longer replacement is appended to rather than refused.** Resumption
   validates the starting offset alone. If the counterparty's file is
   replaced between attempts with one at least as long as the offset already
   fetched, no `416` is returned, the offset checks out, and the tail of a
   different file is joined to the head of the first.
5. **Nothing is written down.** No expected size, no received size, no path,
   no completion, no failure. The only record is one `slog.Info` line, so a
   failed pull and a successful one are indistinguishable in storage and an
   operator has no way to ask whether data arrived.
6. **The provider's success path is silent.** `handleData` logs on every
   error branch and on none of the success ones. §27 went to real trouble to
   obtain a verified identity, and the moment that identity collects data is
   not recorded, so a provider cannot say who took what.

Defect 1 was the discovery that set this milestone's position in the order.
Defect 2 is why fixing 1 alone would be half a fix: a consumer freed to
stream indefinitely still meets a provider that stops writing at thirty
seconds.

## What this milestone is not

Two P2 items are deliberately excluded, and both are named here so that a
reader does not mistake the omission for an oversight.

- **An operator retry endpoint.** `pullTransferData`'s doc comment says "an
  operator can ask again", and no endpoint exists to ask with; the only
  recovery today is the provider pushing suspend then start. Recording the
  failure (§4 below) is what makes such an endpoint designable, so it comes
  after, not with.
- **Cleaning up orphaned `.partial-*` files.** Recorded in
  `docs/follow-ups.md`. It needs a retention rule, which is a decision of its
  own, and it does not block anything here.

Neither is closed by this work, and `docs/goal-gap-analysis.md` P2 stays open
until they are.

## 1. Bound progress, not total time

**Decision.** Both sides stop bounding a data transfer by total elapsed time
and start bounding it by time without progress.

This is the substance of the milestone rather than a tuning change. A total
time bound on a streaming response *is* a file size limit — expressed in
seconds, so it does not read like one, and it moves with the network rather
than with any decision anyone made. Raising ten seconds to sixty produces a
larger size limit, not a different kind of thing. An idle bound is the
different kind of thing: a transfer of any size completes as long as it keeps
moving, and a connection that stops moving is still killed.

**Consumer.** A second client, separate from `callbackHTTPClient`, whose
purpose is stated in its own doc comment so the next reader is not left to
discover the sharing:

- No `Timeout` on the `http.Client`. The overall bound is what this
  milestone is removing.
- `Transport.ResponseHeaderTimeout` bounds the wait for response headers. A
  counterparty that accepts a connection and says nothing is still refused
  promptly; it is only the body that is allowed to take as long as it needs.
- The body is wrapped in an `idleTimeoutReader`: a reader that resets a
  `time.Timer` on every read returning more than zero bytes, where the timer
  cancels the request's context. Its own file, its own tests, no dependency
  on anything in this package.

`callbackHTTPClient` keeps its ten seconds. A callback push is a small JSON
body to a counterparty that should answer immediately, and that is what the
bound was chosen for.

**Provider.** `handleData` takes an `http.ResponseController` before it
streams and rolls the write deadline forward as bytes leave, on the same
idle value. `dspSrv`'s `WriteTimeout` stays as the server-wide default; the
data endpoint becomes the one documented exception, because it is the one
endpoint whose response length is not bounded by anything this connector
authored.

Two things about `SetWriteDeadline` decide whether this works, and both are
easy to get wrong. Its documentation says setting a deadline *after* it has
been exceeded will not extend it, so the deadline has to be pushed forward
while it is still in the future — the roll happens as each chunk is written,
not after a stall. And it returns an error when the underlying writer does
not support deadlines; that error is fatal to this handler rather than
ignorable, because a silent failure leaves the response back under the
thirty-second cap with nothing saying so.

**Configuration.** One field, `data_idle_timeout`, governs both roles: both
answer the same question — how long without progress before giving up on a
transfer — and two values would be two things to keep in step for no gain. It
is optional with a default, validated as strictly positive, and documented in
`config.example.yaml`. There is no environment override, matching the other
transfer-shaped configuration in that file.

## 2. Size is the contract

**Decision.** The expected size of a transfer is the complete length the
counterparty states over HTTP; it is recorded on the first attempt, compared
against on every later one, and checked before the download is published.

DSP 2025-1's `dataAddress` carries `endpointType` and `endpoint` and has
nowhere to put a size or a digest, so a size that travels in the protocol
would have to be an extension. It does not need to be: HTTP already carries
it, in a form every conforming counterparty emits.

- **Provider sets `Content-Length` on the 200 path.** It has already called
  `Stat`, so the value is in hand; not setting it is what makes Go choose
  chunked encoding and leaves the consumer with nothing to expect. The 206
  path already sets both `Content-Length` and `Content-Range`.
- **Consumer takes the complete length from whichever header carries it.**
  `Content-Length` on a 200; the complete-length field of `Content-Range` on
  a 206. `contentRangeFirstByte` grows into a parser that returns both the
  first-byte-pos and the complete length, since it is already parsing the
  header that holds them.
- **The recorded value is authoritative across attempts.** A resumed pull
  whose counterparty states a different complete length is answering about a
  different representation. The partial is discarded and the transfer starts
  over, which is the same handling `416` already gets and for the same
  reason.
- **The download is published only if the size checks out.** Total bytes on
  disk must equal the expected size before the rename. A short answer leaves
  the partial in place to be resumed; a long one leaves it in place to be
  investigated. Either way the failure is recorded (§4).

**What this catches:** a silently truncated transfer, a replacement longer
than the original, and — through the existing `416` path plus the recorded
length — a replacement shorter than it.

**What it does not catch:** a replacement of exactly the same length. Closing
that needs a digest, and a digest over a *ranged* response has to cover the
whole representation, so a 1 GB file resumed at 99% would still require
reading 1 GB to answer. That needs a digest cache keyed on path, size, and
modification time — a component, with an invalidation rule, and the only
counterparties that would send the header today are other instances of this
connector (`docs/goal-gap-analysis.md` P1). Deferred deliberately, and
`README.md`'s existing sentence about same-size replacement stays true.

**Durability.** The file is `Sync`ed before it is renamed. A rename that
outruns its data turns a crash into a success log beside a truncated file,
which is worse than a failure, because it is a failure that reports itself as
a success.

## 3. Recording arrival

**Decision.** Five columns on `consumer_transfer_processes`, not a new table.

A download is one-to-one with the consumer transfer that produced it, and the
question an operator asks — did the data arrive for this transfer? — is
answered on the row that already says what state the transfer is in. A second
table would add a join and a second identifier for one fact.

```
expected_bytes    INTEGER NOT NULL DEFAULT 0
received_bytes    INTEGER NOT NULL DEFAULT 0
data_path         TEXT    NOT NULL DEFAULT ''
data_completed_at TEXT    NOT NULL DEFAULT ''
data_error        TEXT    NOT NULL DEFAULT ''
```

Added through `addColumnIfMissing` in `migrate`, and to
`consumerTransferSchema` for fresh databases — both, as `counterparty_id`
already does. Defaults are the zero values, so rows written before this
milestone read as a transfer that has not fetched anything, which is what
they are.

`data_error` is what closes defect 5. A failed pull and a successful one stop
being the same row: a completed download has `data_completed_at` set and
`data_error` empty, and a failed one is the reverse. It holds the reason a
pull stopped rather than a code, because the reasons are already distinct
sentences in the log and an operator reading this field needs the sentence.

A new store method records the outcome; the existing state setters are not
extended, because a download's progress is not a protocol state transition
and folding it into one would make `SetConsumerTransferState`'s guard mean
two things.

## 4. `GET /transfers`

**Decision.** One read-only route on the management listener, listing both
roles, with a `role` field distinguishing them.

`DECISIONS.md` §25.3 set a boundary — the management API "is not the
beginning of a general management CRUD surface" — and this route is inside
it. That boundary is about writing: a surface that creates, updates, and
deletes is what invites a general CRUD API. `GET /agreements` already exists
for exactly the reason this route does, stated in its own doc comment: an
operator otherwise has no way to see what a negotiation concluded. An
operator has no way to see whether data arrived. This is the same principle
applied a second time, not the boundary moving.

Both roles are listed because a route named `/transfers` that shows half the
transfers is a trap for whoever reads it next. Provider-role rows carry no
download fields — they never fetch anything — and the `role` field says so
rather than leaving a reader to infer it from empty values.

Read-only and unpaginated, matching `GET /agreements`, and for the reason
that route records: a list that outgrows one response is a problem worth
having first.

Two new store methods list the two tables. `agreementView`'s field-ordering
constraint does not apply here — `demo/run.sh`'s `sed` extraction reads the
agreements response, not this one — but the wire shape stays a view type
separate from the store structs, so the API does not leak whichever columns
storage happens to carry.

## 5. The provider's audit line

**Decision.** `handleData` logs a success line carrying the verified issuer,
the dataset, the transfer, and the bytes served.

The identity is already on the request context from §27's work and is already
used to refuse the wrong caller. Not recording it on the path that succeeds
means the connector can say who it turned away and not who it served, which
is the wrong half to keep for a component whose product is data.

A log line rather than a row: the provider has no per-download state to
maintain, and a table here would be an audit store, which is a larger
decision than this milestone should make on its way past.

## 6. Evidence

**Decision.** Unit tests with an injected idle value carry this milestone.
`make demo` is not extended.

The bound being tested is a duration, and a test that waits out a real
timeout pays that duration on every CI run forever. The idle value is
injectable, so a slow `httptest` server and a value in the hundreds of
milliseconds prove the same behavior in the same shape.

Required cases, each of which fails before this milestone:

- A body that stalls mid-transfer is cut off at the idle bound.
- A cut-off pull leaves the partial file intact.
- A body that keeps producing bytes past the idle bound completes — this is
  the difference between an idle bound and a total one, and without it the
  change is untested where it matters.
- A resumed pull continues from what the partial held.
- Fewer bytes than the expected size does not publish the download.
- A resumed pull whose counterparty states a different complete length
  discards the partial rather than appending.
- The provider sets `Content-Length` on a 200.
- A provider response that takes longer than `WriteTimeout` is not cut off.
- The recorded columns round-trip through the store.
- `GET /transfers` returns both roles, with download fields on consumer rows.

Each check is verified by mutation, as this repository does elsewhere:
delete the check, confirm a *named* test fails, restore.

The gates are unchanged: `go vet ./...`, `go test ./...`, `make tck` holding
65 of 65, and `make demo` still moving its file and diffing it.

## What this does not decide

Whether the same-size replacement hole is worth a digest, and what would pay
for the cache it needs. §2 records why it is not here; it does not argue that
it should never be.

Whether `data_idle_timeout`'s default is right. A default is chosen and
documented; the first transfer of real size over a real network is what will
say whether it was.
