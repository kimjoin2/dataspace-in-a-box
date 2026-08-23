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
   `WriteTimeout: 30 * time.Second` (`cmd/dsbox/main.go`), and that file's own
   comment predicted this would need revisiting once streaming landed.
   Streaming landed; it was not revisited. This is worse than it looks, and
   §1.1 is where the reason is: the 206 path already sets `Content-Length`,
   so `io.Copy` there already collapses into a single blocking `sendfile` and
   a resumed transfer is already capped at thirty seconds with no way to
   observe it.
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

Three things survive this milestone. All three are named here so that a
reader does not mistake an omission for an oversight.

- **An operator retry endpoint.** `pullTransferData`'s doc comment says "an
  operator can ask again", and no endpoint exists to ask with; the only
  recovery today is the provider pushing suspend then start. Recording the
  failure (§3) is what makes such an endpoint designable, so it comes after,
  not with. This is the one P2 item left open, so P2 does not close here.
- **A same-length replacement between attempts.** §2 records why a digest is
  not in this milestone. `README.md`'s existing sentence about same-size
  replacement stays true.
- **Cleaning up orphaned `.partial-*` files.** Recorded in
  `docs/follow-ups.md`, and listed under P4 rather than P2. It needs a
  retention rule, which is a decision of its own.

**And this milestone makes the third one worse, which has to be said rather
than left to be discovered.** The ten-second bound §1 removes is also, by
accident, the only thing limiting how large an orphaned partial can get: a
stalled pull today leaks at most what the link moved in ten seconds. After §1
it leaks whatever arrived before the connection went idle, which is bounded
only by `max_download_bytes` (§2). §31 named the same shape of amplification
when it made partial filenames predictable; this is the larger version of it,
and the retention rule it argues for is now overdue rather than optional.

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

### 1.1 The provider must stop using `io.Copy`

This is the part a first reading of this design gets wrong, so it is stated
before the rest.

`io.Copy(w, f)` on an `http.ResponseWriter` does not loop. `*http.response`
implements `io.ReaderFrom`, and when the response is **not** chunked that
implementation hands the whole file to `*net.TCPConn.ReadFrom` — one
`sendfile` call that blocks until the transfer finishes or dies. A handler
parked in that call cannot roll a deadline forward. Whatever deadline was set
before the copy governs the entire transfer.

So a rolling deadline and `io.Copy` are mutually exclusive, and §2's
`Content-Length` is what makes the response non-chunked. **The 206 path
already sets `Content-Length` today** (`internal/dsp/data_handler.go:164`),
which means it already has this shape and already caps a resumed transfer at
`WriteTimeout` — a fact the problem statement above did not know.

Both streaming paths therefore become an explicit loop: set the write
deadline, read a chunk, write it, repeat. Two constraints on that loop:

- **`w.Write`, never `io.Copy`.** A helper that takes an `io.Writer` will
  find `ReadFrom` again through the interface and silently restore the
  problem. Wrapping `w` in a type that hides `ReadFrom` also works and is
  worse: it defeats the fast path by accident, and reads to the next
  maintainer as a mistake rather than a decision.
- **A large buffer — 256 KB.** `net/http` buffers the response internally
  (2048 bytes at `response.w`, 4096 at `conn.bufw`). With a buffer near those
  sizes, "the write succeeded" measures the buffer rather than the wire, and
  the provider's idle bound would never fire.

The cost is real and is accepted: this gives up `sendfile` on exactly the
large-file case the milestone exists for. A user-space copy is the price of
being able to observe progress at all, and an unbounded transfer that is
merely slower beats a fast one that stops at thirty seconds.

### 1.2 The provider's rolling deadline

`handleData` takes an `http.ResponseController` and pushes the write deadline
forward before each chunk, on the idle value.

The controller is taken **immediately before the first byte is written** —
after the 416 branch — not at handler entry. Every refusal path must stay
untouched, both because they are correct as they are and because taking it
early would drag ten passing tests into this change for no reason.

Two things about `SetWriteDeadline` decide whether this works.

- Its documentation says setting a deadline *after* it has been exceeded will
  not extend it. The push happens before each write, while the current
  deadline is still in the future — never after a stall.
- It returns an error when the underlying writer does not support deadlines.
  That error is **fatal to this handler**. Ignoring it leaves the response
  back under the server-wide `WriteTimeout` with nothing saying so, which is
  the exact silent regression this milestone exists to end.

That fatality has a consequence for the existing tests, and the resolution is
part of this decision rather than an implementation detail:
`httptest.ResponseRecorder` implements neither `SetWriteDeadline` nor
`Unwrap`, so `SetWriteDeadline` returns `errNotSupported` and every
recorder-driven streaming test would get a 500. Five tests are affected
(`TestDataPullServesTheConfiguredFile`,
`TestDataPullServesWithinAnOpenValidityWindow`,
`TestDataPullServesAPartialRange`, `TestDataPullUnsupportedRangeFormIsIgnored`,
`TestDataPullSimulatedInterruptDoesNotFireOnARangedRequest`). They encode
correct behavior and must not be rewritten. A six-line test shim embedding
`*httptest.ResponseRecorder` and satisfying `SetWriteDeadline` is what
absorbs the change. **The production error stays fatal.**

`dspSrv`'s `WriteTimeout` stays as the server-wide default; the data endpoint
becomes the one documented exception, because it is the one endpoint whose
response length is not bounded by anything this connector authored.

`ReadTimeout` is deliberately not touched, and neither is the request
context. `net/http` clears the read deadline before its background read
begins, so `ReadTimeout` does not cancel a long response; and gating the copy
on `r.Context()` would import a cancellation source this handler does not
control.

### 1.3 The consumer's client

A second client, separate from `callbackHTTPClient`, whose purpose is stated
in its own doc comment so the next reader is not left to discover the
sharing.

- **No `Timeout` on the `http.Client`.** The overall bound is what this
  milestone removes.
- **`CheckRedirect: http.ErrUseLastResponse`, the same as
  `callbackHTTPClient`.** Not an inherited detail — a requirement.
  `validateOutgoingCallback` checks `addr.Endpoint` and nothing a redirect
  points at, so a client that follows redirects lets a counterparty-supplied
  endpoint hop to `127.0.0.1:8081` and reach the management listener that
  binds to localhost precisely so a firewall mistake cannot expose it (§12).
  Today the data pull inherits this posture by borrowing the callback client;
  a new client that omits it is a security regression introduced by a
  refactor.
- **Transport is `http.DefaultTransport.(*http.Transport).Clone()` with
  `ResponseHeaderTimeout` set** — not a bare `&http.Transport{...}` literal.
  `ResponseHeaderTimeout` starts only after the request is written, so with
  `Client.Timeout` gone a bare literal leaves the TCP connect and the TLS
  handshake unbounded, and `validateCallbackURL` permits `https`. The clone
  keeps `DialContext`'s timeout, `TLSHandshakeTimeout`,
  `ExpectContinueTimeout`, and `Proxy`.
- **The body is wrapped in an idle-timeout reader**: it resets a
  `time.Timer` on every read returning more than zero bytes, and the timer
  cancels the request's context. Cancelling the context does interrupt an
  in-flight body read — `net/http`'s read loop selects on the context and
  closes the connection — so the mechanism works.

Two details of that reader are load-bearing:

- **`context.WithCancelCause`, with a sentinel for the idle timeout.** A bare
  cancel surfaces as `context.Canceled`, which is indistinguishable from any
  other cancellation and is not the sentence §3's `data_error` promises an
  operator.
- **`Timer.Reset`'s return value is checked.** Go documents that `Reset` on
  an `AfterFunc` timer returns false when the function is already scheduled
  or running — and here that function is `cancel`, which is irreversible. A
  read that returns bytes at the same instant the deadline expires would
  otherwise reset a timer that has already killed the transfer, and the
  *next* read fails for a reason the log misattributes. On `false`, stop and
  report the idle timeout.

`callbackHTTPClient` keeps its ten seconds. A callback push is a small JSON
body to a counterparty that should answer immediately, and that is what the
bound was chosen for.

### 1.4 Drip-feed: what an idle bound does not cover

An idle bound kills a connection that stops moving. It does not kill one that
moves arbitrarily slowly: one byte per idle interval resets the timer
forever. That connection holds a goroutine, a file descriptor, a socket, and
— because `h.pulling` claims the `ConsumerPID` for the pull's lifetime —
makes every later start message for that transfer a no-op. Today
`callbackHTTPClient`'s ten seconds guarantees release; after §1 nothing does,
and the retry endpoint that would let an operator clear it is excluded above.

`callbackHTTPClient`'s own doc comment already reasons about this hazard for
callbacks — "a goroutine that never returns is never collected either". This
design must carry that reasoning across rather than drop it.

`max_download_bytes` (§2) is the backstop: a drip feed still terminates, at
the byte ceiling rather than at a time. That is a partial answer and is
recorded as one. A minimum-throughput floor would be the complete answer and
is not in this milestone.

### 1.5 Shutdown

`pullTransferData` runs in a goroutine spawned from a handler, so
`srv.Shutdown` has never known about it — `Shutdown` waits on connections and
handlers, not on goroutines a handler launched. Today that is harmless
because a pull writes nothing to the database. **§3 changes that**: the
pull's last act becomes a store write, which would land on a `*sql.DB` that
`run()`'s `defer st.Close()` has already closed, producing an error and a row
that never records its outcome. §1 makes the window unbounded rather than ten
seconds wide.

So in-flight pulls are tracked in a `sync.WaitGroup` beside `h.pulling`, and
given a context derived from a connector-lifetime context that `run()`
cancels before waiting — bounded — ahead of `st.Close()`. This is the
difference between §3's `data_error` being trustworthy and not.

### 1.6 Configuration

`data_idle_timeout` governs both roles: both answer the same question — how
long without progress before giving up — and two values would be two things
to keep in step for no gain. Optional with a default, validated as strictly
positive, documented in `config.example.yaml`, and carrying a
`DSBOX_DATA_IDLE_TIMEOUT` override. Every scalar in this config has an
environment override; the values that lack one are all lists, for the stated
reason that a list has no sensible environment representation.

## 2. Size is the contract, where there is one

**Decision.** The expected size of a transfer is the complete length the
counterparty states over HTTP. It is recorded on the first attempt, compared
against on every later one, and checked before the download is published —
*when the counterparty states one at all*. When none is stated the transfer
still runs, under a byte ceiling instead.

DSP 2025-1's `dataAddress` carries `endpointType` and `endpoint` and has
nowhere to put a size, so a size that travels in the protocol would have to
be an extension. It does not need to be: HTTP already carries it.

**Provider sets `Content-Length` once, after `Stat`, before the Range
branch.** The placement is the decision, not a detail. `handleData` has three
response shapes — 200 full, 206 ranged, and the `SimulateInterruptAfterBytes`
branch, which is also a 200 and returns before the plain-200 code. Setting
the header at the plain-200 site alone leaves the interrupt branch chunked,
so a demo's first attempt records no expected size while its resumed attempt
learns the real one, and the rule below would read that as a changed
representation and discard the partial. `demo/provider.yaml`'s sequence sends
no third start, so `make demo` — a declared gate — would fail. Setting it
once before the branch covers all three.

The interrupt branch then declares a length it does not deliver. That is
what it is for, and it is a more realistic simulation than truncated chunked
framing: a client sees `io.ErrUnexpectedEOF` rather than a short chunked
stream. `internal/config/config.go`, `config.example.yaml`, and
`DECISIONS.md` §31.4 each describe that knob and each need a sentence saying
so.

**Consumer takes the complete length from whichever header carries it.**
`Content-Length` on a 200; the complete-length field of `Content-Range` on a
206. `contentRangeFirstByte` grows into a parser returning the first-byte-pos
and the complete length *with a separate presence flag for each*, because
RFC 9110 §14.4 permits `bytes 2000-4095/*` when the total is unknown. A
single `ok` would make `*` parse as failure and abort a resume that works
today — a regression introduced by the fix.

**A stated length is authoritative across attempts.** A resumed pull whose
counterparty states a *different* complete length is answering about a
different representation: discard the partial and start over, the same
handling `416` already gets and for the same reason. An *absent* length on a
later attempt is not a mismatch — see below.

**Zero means "not known", never "known to be zero".** This is the rule that
keeps the design usable. A counterparty that states no length is not an
error: this connector's own provider emits exactly that today, and a real TCK
run performs twelve consumer-side pulls from the TCK's own data endpoint,
whose framing this repository does not control and has never inspected. A
strict publish rule would leave twelve partials per run on disk and would
refuse every conforming provider that streams.

So: with no stated length, the download publishes on a **clean EOF**, and
`expected_bytes` stays 0. Less is lost than it sounds — HTTP/1.1 chunked
framing carries its own end-of-message marker, so a truncated chunked
response still surfaces as an error, which is precisely what the existing
`SimulateInterruptAfterBytes` demo relies on. What is lost is the a-priori
size, not truncation detection.

**`max_download_bytes` bounds what no length can bound.** With
`Client.Timeout` gone, a 200 *with* `Content-Length` is bounded by
`net/http`'s own limited reader on the body; a 200 *without* one is bounded
by nothing at all, and the bytes land in `data_dir` from a
counterparty-supplied endpoint. Ten seconds is what caps that today. A
configured ceiling replaces it — and is also the only backstop against the
drip feed of §1.4. Exceeding it aborts the pull and records the reason.

**The download is published only if what was stated checks out.** Where a
length was stated, total bytes on disk must equal it before the rename. A
short answer leaves the partial to be resumed; a long one leaves it to be
investigated. Either way the failure is recorded (§3).

**What this catches:** a silently truncated transfer, and — for a
counterparty that states a length — a replacement longer than the original,
plus, through the existing `416` path, one shorter than it. Defect 4 of the
problem statement closes only for counterparties that state a length. That is
narrower than "closed" and is the honest description.

**What it does not catch:** a replacement of exactly the same length. Closing
that needs a digest, and a digest over a *ranged* response has to cover the
whole representation, so a 1 GB file resumed at 99% would still require
reading 1 GB to answer. That needs a digest cache keyed on path, size, and
modification time — a component with an invalidation rule — and the only
counterparties that would send the header today are other instances of this
connector (`docs/goal-gap-analysis.md` P1). Deferred deliberately.

**Durability.** The file is `Sync`ed before it is renamed. A rename that
outruns its data turns a crash into a success log beside a truncated file,
which is worse than a failure because it is a failure that reports itself as
a success. This buys the file's contents, not the directory entry: a crash
between the rename and the directory reaching disk can leave neither name.
Fsyncing the parent directory is what would buy that, and on the filesystems
this runs on the file fsync usually carries it — which is why this is named
rather than built.

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
`consumerTransferSchema` for fresh databases. Both places — which is *not*
what `counterparty_id` does on this table: §32.5 records that it sits in the
`CREATE TABLE` literal only on `agreements`, and in the migrate loop alone on
the other four. Doing both here is what §32.5's own reasoning argues for.
Defaults are the zero values, so rows written before this milestone read as a
transfer that has not fetched anything, which is what they are.

**`expected_bytes` reaches the pull through the transfer struct, not a store
read.** `pullTransferData` is called with a `store.ConsumerTransfer` the
caller assembles from `lookup`'s projection, and five existing tests call it
directly with a bare struct and no row in the database. So `ExpectedBytes`
becomes a field on `store.ConsumerTransfer` and `lookup`'s projection widens
to carry it; the pull's *read* path stays store-free and those five tests
keep passing unchanged. Only the outcome *write* touches the store, and a
write against a missing row logs and moves on.

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

Behind the same `authenticated(cfg.MgmtToken, ...)` middleware as
`GET /agreements` and `POST /agreements`. Read-only and unpaginated, matching
`GET /agreements`, and for the reason that route records: a list that
outgrows one response is a problem worth having first.

One thing to watch, recorded because this repository records things like
this. §25.3 narrowed its own boundary to writes when it added
`GET /agreements`; this is the second read route admitted by that same
argument, and the argument has no stated stopping point. A third and fourth
are now free rides. Whoever adds the third should be made to say why the
management API is still small.

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

Required cases. Each fails before this milestone, and each is verified by
mutation as this repository does elsewhere: delete the check, confirm a
*named* test fails, restore.

**The idle bound**

- A body that stalls mid-transfer is cut off at the idle bound.
- A cut-off pull leaves the partial file intact.
- A body that keeps producing bytes past the idle bound completes. This is
  the whole difference between an idle bound and a total one; without it the
  change is untested where it matters.
- The recorded failure names the idle timeout, not `context canceled` — the
  cancel-cause sentinel of §1.3.
- A counterparty that accepts the connection and never sends headers is
  refused at `ResponseHeaderTimeout`. Every other stall case here stalls
  *after* headers; this is the one that tests the other bound.

**The provider**

- A provider response that takes longer than `WriteTimeout` is not cut off.
  **This case is the most mis-writable in the list**: `httptest.NewServer`
  builds a server with no timeouts at all, so a test written against it
  passes identically with and without the fix and its mutation check can
  never fire. It must use `httptest.NewUnstartedServer`, set
  `srv.Config.WriteTimeout` to a small injected value before `Start`, and
  serve a body that outlives it.
- `SetWriteDeadline` returning an error fails the request rather than
  proceeding. §1.2 makes this fatal on purpose; an ignored error is the
  silent regression this milestone exists to end, so it gets its own test.
- The provider sets `Content-Length` on all three 200-shaped paths, the
  `SimulateInterruptAfterBytes` branch included.
- The success path logs the verified issuer, the dataset, the transfer, and
  the bytes served. §5 is one of the six defects; without this it ships
  unverified by this milestone's own standard.

**The size contract**

- Fewer bytes than a stated size does not publish the download.
- A resumed pull whose counterparty states a different complete length
  discards the partial rather than appending.
- A `Content-Range` complete-length of `*` is read as unknown, not as a
  mismatch, and the resume proceeds.
- A response stating no length at all publishes on a clean EOF with
  `expected_bytes` left at 0.
- A response with no stated length that exceeds `max_download_bytes` is
  aborted and the reason recorded.

**Recording**

- A real failure path — an idle cutoff, not a synthetic call — leaves
  `data_error` populated and `data_completed_at` empty. A store round-trip
  test proves the column exists; it does not prove anything writes it, and
  defect 5 is precisely that nothing writes anything.
- `GET /transfers` returns both roles, with download fields on consumer rows,
  and refuses without the management token.

**Measured (Task 1):** the TCK's data endpoint answers chunked, with no
`Content-Length`. So `make tck` exercises §2's no-length branch on all
twelve of its consumer-side pulls — the gate would have caught a strict
publish rule, and `expected_bytes` stays 0 for every TCK transfer.

The gates are unchanged: `go vet ./...`, `go test ./...`, `make tck` holding
65 of 65, and `make demo` still moving its file and diffing it.

## 7. Sequencing: this is two plans

The work above is one design and more than one plan. §1 and §2 are novel
`net/http` mechanics that this spec has already been wrong about once — the
`io.Copy` finding of §1.1 invalidated the original provider design outright —
and they deserve isolated review and their own green `make tck` and
`make demo`. §3 through §5 are comparatively mechanical: a column addition, a
list route, a log line.

**Plan A — the transport and the contract.** §1 entire, §2 entire, and the
single `expected_bytes` column §2 needs to compare across attempts, plus
`Sync` before rename. Ends with a transfer of measured size completing over
a bound that does not cap it.

**Plan B — recording and exposure.** The remaining four columns, the store
methods, `GET /transfers`, and the provider's audit line. Ends with an
operator able to ask whether data arrived.

The seam is where it is because §2's cross-attempt comparison needs storage
and nothing else in §3 does. Splitting anywhere earlier would put a
half-built column in Plan A; splitting later would put the riskiest code in a
plan that also touches the management API.

## What this does not decide

Whether the same-size replacement hole is worth a digest, and what would pay
for the cache it needs. §2 records why it is not here; it does not argue that
it should never be.

Whether `data_idle_timeout`'s and `max_download_bytes`'s defaults are right.
Defaults are chosen and documented; the first transfer of real size over a
real network is what will say whether they were.

Whether the drip-feed hazard of §1.4 needs a throughput floor. The byte
ceiling bounds it; it does not answer it.
