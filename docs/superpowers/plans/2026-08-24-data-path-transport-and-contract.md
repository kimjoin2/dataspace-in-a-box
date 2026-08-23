# Data Path — Transport and Contract (Plan A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A data transfer of any size completes as long as it keeps moving, and what arrives is checked against the size the counterparty stated.

**Architecture:** Both sides stop bounding a transfer by total elapsed time and start bounding it by time without progress. The provider abandons `io.Copy` — which collapses into one blocking `sendfile` on a non-chunked response and makes a rolling deadline impossible — for an explicit loop that pushes the write deadline before each chunk. The consumer gets its own HTTP client with no overall timeout and an idle-timeout body reader. The expected size comes from `Content-Length` or `Content-Range`, is recorded on the first attempt, and is compared on every later one; where a counterparty states nothing, a byte ceiling bounds the transfer instead.

**Tech Stack:** Go 1.26, standard library only. SQLite via `modernc.org/sqlite` (already present). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-24-data-path-correctness-design.md` — this plan implements **Plan A** of that spec's §7 only: §1 entire, §2 entire, the single `expected_bytes` column, and `Sync` before rename. The remaining four columns, `GET /transfers`, and the provider audit line are Plan B and must not be built here.

## Global Constraints

- Go standard library only. Ask before adding any dependency; the default answer is no.
- English for all documentation, code comments, error strings, and commit messages.
- Every check added is verified by mutation: delete the check, confirm a **named** test fails, restore. A test that passes with the check deleted has not tested it.
- Final gates: `go vet ./...`, `go test ./...`, `make tck` holding **65 of 65**, and `make demo` still moving its file and diffing it.
- `w.Write`, never `io.Copy`, on any provider streaming path. A helper taking an `io.Writer` finds `ReadFrom` again through the interface and silently restores the bug this plan exists to fix.
- The copy buffer is **256 KB**. `net/http` buffers internally at 2048 (`response.w`) and 4096 (`conn.bufw`); a buffer near those sizes measures the buffer rather than the wire.
- Production `SetWriteDeadline` errors stay **fatal**. Never soften this to make a test pass — add the test shim instead.
- `pullTransferData`'s read path stays store-free. Five existing tests call it directly with a bare struct and no database row.

---

### Task 1: Record what the TCK's data endpoint actually sends

The spec (§6) makes this a prerequisite to writing any other code. A real TCK run performs twelve consumer-side pulls from the TCK's own data endpoint, and whether that endpoint states a length decides which branch of §2 the gate exercises. This is the only third-party counterparty available to this project.

**Files:**
- Modify (temporarily, then revert): `internal/dsp/transfer_consumer_handler.go`
- Modify: `docs/superpowers/specs/2026-08-24-data-path-correctness-design.md`

**Interfaces:**
- Consumes: nothing.
- Produces: a recorded fact in the spec's §6. Later tasks read it to know whether the no-length branch is exercised by `make tck` or only by unit tests.

- [ ] **Step 1: Add a temporary header dump**

In `internal/dsp/transfer_consumer_handler.go`, immediately after the `resp, err := callbackHTTPClient.Do(req)` error check and before `defer resp.Body.Close()`, insert:

```go
	slog.Info("TASK1 TEMPORARY response headers",
		"status", resp.StatusCode,
		"content_length_header", resp.Header.Get("Content-Length"),
		"content_range", resp.Header.Get("Content-Range"),
		"transfer_encoding", fmt.Sprintf("%v", resp.TransferEncoding),
		"resp_content_length_field", resp.ContentLength)
```

- [ ] **Step 2: Run the TCK harness and capture the lines**

```bash
make tck 2>&1 | tee /tmp/task1-tck.log
grep 'TASK1 TEMPORARY' tck-connector.txt | head -20
```

If `tck-connector.txt` is not refreshed by the run, get them from the container instead:

```bash
docker compose -f test/tck/compose.yaml logs dsbox 2>&1 | grep 'TASK1 TEMPORARY' | head -20
```

Expected: twelve or so lines. Note for each whether `content_length_header` is empty or a number, and whether `resp_content_length_field` is `-1` (unknown, i.e. chunked) or a byte count.

- [ ] **Step 3: Revert the temporary logging**

```bash
git checkout internal/dsp/transfer_consumer_handler.go
```

Confirm the file is back to its committed state:

```bash
git diff --stat internal/dsp/transfer_consumer_handler.go
```

Expected: no output.

- [ ] **Step 4: Record the finding in the spec**

In `docs/superpowers/specs/2026-08-24-data-path-correctness-design.md`, find the paragraph in §6 beginning `**Before implementing:**` and replace that whole paragraph with the answer. Write one of these two, whichever is true, and nothing vaguer:

If the TCK states a length:

```markdown
**Measured (Task 1):** the TCK's data endpoint answers with
`Content-Length: <N>` and no chunked transfer encoding. So `make tck`
exercises §2's stated-length branch on all twelve of its consumer-side
pulls, and the no-length branch is covered by unit tests alone.
```

If it does not:

```markdown
**Measured (Task 1):** the TCK's data endpoint answers chunked, with no
`Content-Length`. So `make tck` exercises §2's no-length branch on all
twelve of its consumer-side pulls — the gate would have caught a strict
publish rule, and `expected_bytes` stays 0 for every TCK transfer.
```

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/specs/2026-08-24-data-path-correctness-design.md
git commit -m "docs: record what the TCK's data endpoint sends

The spec made this a prerequisite: twelve consumer-side pulls run against
that endpoint in every gate run, and whether it states a length decides
which branch of the size contract the gate actually exercises."
```

---

### Task 2: The idle-timeout reader

A single-purpose unit with its own file and its own tests, built before anything uses it.

**Files:**
- Create: `internal/dsp/idle_reader.go`
- Test: `internal/dsp/idle_reader_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `var errIdleTimeout = errors.New("no progress within the idle timeout")`
  - `func newIdleTimeoutReader(r io.Reader, idle time.Duration, cancel context.CancelCauseFunc) *idleTimeoutReader`
  - `func (t *idleTimeoutReader) Read(p []byte) (int, error)`
  - `func (t *idleTimeoutReader) Stop()`

  Task 4 wraps a response body with it and reads the cause back with `context.Cause`.

- [ ] **Step 1: Write the failing tests**

Create `internal/dsp/idle_reader_test.go`:

```go
package dsp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// blockingReader returns one byte per call until released, then blocks
// forever. It stands in for a counterparty that stops sending.
type blockingReader struct {
	remaining int
	blocked   chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	if b.remaining > 0 {
		b.remaining--
		p[0] = 'x'
		return 1, nil
	}
	<-b.blocked
	return 0, io.EOF
}

func TestIdleTimeoutReaderCancelsWhenProgressStops(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	src := &blockingReader{remaining: 2, blocked: make(chan struct{})}
	r := newIdleTimeoutReader(src, 50*time.Millisecond, cancel)
	defer r.Stop()

	buf := make([]byte, 1)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("first read: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the context was never cancelled after progress stopped")
	}
	if got := context.Cause(ctx); !errors.Is(got, errIdleTimeout) {
		t.Errorf("cause = %v, want errIdleTimeout — a bare cancel is not a reason an operator can read", got)
	}
}

func TestIdleTimeoutReaderDoesNotCancelWhileBytesKeepArriving(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	// Far more bytes than the idle window would allow if the bound were on
	// total elapsed time rather than on time without progress.
	src := strings.NewReader(strings.Repeat("x", 400))
	r := newIdleTimeoutReader(src, 40*time.Millisecond, cancel)
	defer r.Stop()

	buf := make([]byte, 1)
	for i := 0; i < 400; i++ {
		if _, err := r.Read(buf); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		time.Sleep(time.Millisecond)
		if ctx.Err() != nil {
			t.Fatalf("cancelled after %d bytes while still making progress: %v", i, context.Cause(ctx))
		}
	}
}

func TestIdleTimeoutReaderStopReleasesTheTimer(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	src := strings.NewReader("xx")
	r := newIdleTimeoutReader(src, 30*time.Millisecond, cancel)

	buf := make([]byte, 1)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	r.Stop()

	time.Sleep(120 * time.Millisecond)
	if ctx.Err() != nil {
		t.Errorf("the context was cancelled after Stop: %v", context.Cause(ctx))
	}
}

func TestIdleTimeoutReaderReportsAlreadyCancelledRatherThanResetting(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	src := strings.NewReader(strings.Repeat("x", 10))
	r := newIdleTimeoutReader(src, time.Nanosecond, cancel)
	defer r.Stop()

	// The idle window is already gone by the time anything is read, so the
	// cancel is scheduled or done. Read must surface that rather than reset
	// a timer whose function has already fired.
	deadline := time.After(2 * time.Second)
	buf := make([]byte, 1)
	for {
		select {
		case <-deadline:
			t.Fatal("Read never reported the idle timeout")
		default:
		}
		if _, err := r.Read(buf); err != nil {
			if !errors.Is(err, errIdleTimeout) {
				t.Fatalf("err = %v, want errIdleTimeout", err)
			}
			return
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/dsp/ -run TestIdleTimeoutReader
```

Expected: FAIL, `undefined: newIdleTimeoutReader`.

- [ ] **Step 3: Write the implementation**

Create `internal/dsp/idle_reader.go`:

```go
package dsp

import (
	"context"
	"errors"
	"io"
	"time"
)

// errIdleTimeout is the cause a pull's context carries when it was cancelled
// for lack of progress. A bare context.Canceled is indistinguishable from
// every other cancellation, and the reason is what an operator reads.
var errIdleTimeout = errors.New("no progress within the idle timeout")

// idleTimeoutReader bounds the time a transfer may go without progress,
// rather than the time it may take. It wraps a response body: every read
// that returns bytes pushes the deadline out, and a deadline that expires
// cancels the request's context, which closes the connection underneath the
// read.
//
// This is what lets a transfer be arbitrarily large. A bound on total
// elapsed time is a file size limit wearing a clock, and one expressed in
// seconds moves with the network rather than with any decision anyone made.
type idleTimeoutReader struct {
	r     io.Reader
	idle  time.Duration
	timer *time.Timer
	// fired records that the timer's cancel has already been scheduled or
	// run. Cancellation is irreversible, so once this is set the transfer is
	// over and Read says so rather than letting the next read fail for a
	// reason the log would misattribute.
	fired bool
}

func newIdleTimeoutReader(r io.Reader, idle time.Duration, cancel context.CancelCauseFunc) *idleTimeoutReader {
	t := &idleTimeoutReader{r: r, idle: idle}
	t.timer = time.AfterFunc(idle, func() { cancel(errIdleTimeout) })
	return t
}

// Read reports errIdleTimeout rather than resetting a timer whose function
// has already fired. time.Timer documents that Reset on an AfterFunc timer
// returns false when the function is already scheduled or running, and
// gives no guarantee the prior run has finished — with cancel as that
// function, a false means this transfer is already dead.
func (t *idleTimeoutReader) Read(p []byte) (int, error) {
	if t.fired {
		return 0, errIdleTimeout
	}
	n, err := t.r.Read(p)
	if n > 0 {
		if !t.timer.Reset(t.idle) {
			t.fired = true
			return n, errIdleTimeout
		}
	}
	return n, err
}

// Stop releases the timer once the body is done with, so a completed
// transfer does not cancel its own context on the way out.
func (t *idleTimeoutReader) Stop() {
	t.timer.Stop()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/dsp/ -run TestIdleTimeoutReader -v
```

Expected: PASS, 4 tests.

- [ ] **Step 5: Mutation-check each behavior**

Run each mutation, confirm the **named** test in the right column fails, then restore with `git checkout internal/dsp/idle_reader.go`.

| Mutation | Must fail |
|---|---|
| Change `cancel(errIdleTimeout)` to `cancel(nil)` | `TestIdleTimeoutReaderCancelsWhenProgressStops` |
| Delete the `if n > 0 { ... }` reset block | `TestIdleTimeoutReaderDoesNotCancelWhileBytesKeepArriving` |
| Make `Stop()` a no-op | `TestIdleTimeoutReaderStopReleasesTheTimer` |
| Replace `if !t.timer.Reset(t.idle) {...}` with a bare `t.timer.Reset(t.idle)` | `TestIdleTimeoutReaderReportsAlreadyCancelledRatherThanResetting` |

- [ ] **Step 6: Commit**

```bash
git add internal/dsp/idle_reader.go internal/dsp/idle_reader_test.go
git commit -m "feat: an idle-timeout reader for data transfers

A bound on total elapsed time is a file size limit wearing a clock. This
bounds time without progress instead, so a transfer of any size completes
as long as it keeps moving.

Two details are load-bearing and both are tested. The cancel carries a
cause, because a bare context.Canceled is not a reason an operator can
read. And Reset's return is checked: time.Timer documents that Reset on an
AfterFunc timer returns false when the function is already scheduled, with
no guarantee it has finished — and that function is cancel, which cannot be
undone."
```

---

### Task 3: `Content-Range` parses its complete length

`contentRangeFirstByte` already parses the header that carries the total and discards it. Widening it is a prerequisite for Task 5's comparison, and it has a trap: RFC 9110 §14.4 permits an unknown total.

**Files:**
- Modify: `internal/dsp/transfer_consumer_handler.go` (the `contentRangeFirstByte` function and its one call site)
- Test: `internal/dsp/transfer_consumer_handler_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func parseContentRange(header string) (first int64, hasFirst bool, complete int64, hasComplete bool)`. Task 5 calls it. The old name `contentRangeFirstByte` is gone.

- [ ] **Step 1: Write the failing tests**

Append to `internal/dsp/transfer_consumer_handler_test.go`:

```go
func TestParseContentRange(t *testing.T) {
	tests := []struct {
		name         string
		header       string
		first        int64
		hasFirst     bool
		complete     int64
		hasComplete  bool
	}{
		{
			name:     "a complete single range yields both values",
			header:   "bytes 2000-4095/4096",
			first:    2000, hasFirst: true,
			complete: 4096, hasComplete: true,
		},
		{
			// RFC 9110 section 14.4 permits an unknown complete length. This
			// parses today because the total is discarded; a single ok would
			// make it read as failure and break a resume that works.
			name:     "an unknown complete length is unknown, not a failure",
			header:   "bytes 2000-4095/*",
			first:    2000, hasFirst: true,
			complete: 0, hasComplete: false,
		},
		{
			name:     "an absent header yields nothing",
			header:   "",
			first:    0, hasFirst: false,
			complete: 0, hasComplete: false,
		},
		{
			name:     "a unit other than bytes yields nothing",
			header:   "items 1-2/3",
			first:    0, hasFirst: false,
			complete: 0, hasComplete: false,
		},
		{
			name:     "an unsatisfied-range form carries the total and no first byte",
			header:   "bytes */4096",
			first:    0, hasFirst: false,
			complete: 4096, hasComplete: true,
		},
		{
			name:     "a negative first byte is rejected",
			header:   "bytes -1-5/10",
			first:    0, hasFirst: false,
			complete: 0, hasComplete: false,
		},
		{
			name:     "a malformed total is rejected without losing the first byte",
			header:   "bytes 0-5/abc",
			first:    0, hasFirst: true,
			complete: 0, hasComplete: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, hasFirst, complete, hasComplete := parseContentRange(tt.header)
			if hasFirst != tt.hasFirst || (hasFirst && first != tt.first) {
				t.Errorf("first = (%d, %v), want (%d, %v)", first, hasFirst, tt.first, tt.hasFirst)
			}
			if hasComplete != tt.hasComplete || (hasComplete && complete != tt.complete) {
				t.Errorf("complete = (%d, %v), want (%d, %v)", complete, hasComplete, tt.complete, tt.hasComplete)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/dsp/ -run TestParseContentRange
```

Expected: FAIL, `undefined: parseContentRange`.

- [ ] **Step 3: Replace the parser**

In `internal/dsp/transfer_consumer_handler.go`, delete the whole `contentRangeFirstByte` function together with its doc comment, and put this in its place:

```go
// parseContentRange parses a Content-Range header, reporting the
// first-byte-pos and the complete length separately because either can be
// absent independently.
//
// RFC 9110 section 14.4's shape for a satisfied range is
// "bytes <first>-<last>/<complete-length>", and the same section permits
// "*" in place of the complete length when the total is not known, and in
// place of the range itself on a 416. Each half therefore reports its own
// presence: an unknown total must reach the caller as unknown rather than as
// a parse failure, or a resume that works today starts failing.
func parseContentRange(header string) (first int64, hasFirst bool, complete int64, hasComplete bool) {
	const prefix = "bytes "
	if !strings.HasPrefix(header, prefix) {
		return 0, false, 0, false
	}
	rest := strings.TrimPrefix(header, prefix)

	slash := strings.LastIndexByte(rest, '/')
	if slash < 0 {
		return 0, false, 0, false
	}
	rangePart, completePart := rest[:slash], rest[slash+1:]

	if completePart != "*" {
		if n, err := strconv.ParseInt(completePart, 10, 64); err == nil && n >= 0 {
			complete, hasComplete = n, true
		}
	}

	if rangePart != "*" {
		if dash := strings.IndexByte(rangePart, '-'); dash > 0 {
			if n, err := strconv.ParseInt(rangePart[:dash], 10, 64); err == nil && n >= 0 {
				first, hasFirst = n, true
			}
		}
	}
	return first, hasFirst, complete, hasComplete
}
```

- [ ] **Step 4: Update the one call site**

In `pullTransferData`'s `case http.StatusPartialContent:` branch, replace:

```go
			contentRange := resp.Header.Get("Content-Range")
			first, ok := contentRangeFirstByte(contentRange)
			if !ok || first != existingSize {
```

with:

```go
			contentRange := resp.Header.Get("Content-Range")
			first, hasFirst, _, _ := parseContentRange(contentRange)
			if !hasFirst || first != existingSize {
```

The complete length is discarded here on purpose — Task 5 is what starts using it. Leaving it unused now would not compile.

- [ ] **Step 5: Run the full package**

```bash
go build ./... && go test ./internal/dsp/
```

Expected: PASS. No test should have changed behavior; this is a rename plus a widened return.

- [ ] **Step 6: Mutation-check the unknown-total path**

Change `if completePart != "*" {` to `if true {`, run `go test ./internal/dsp/ -run TestParseContentRange`, and confirm `TestParseContentRange/an_unknown_complete_length_is_unknown,_not_a_failure` fails. Restore with `git checkout internal/dsp/transfer_consumer_handler.go` — then redo Steps 3 and 4, since that reverts them. (Alternatively stash the mutation only: make the edit, test, and undo the single line by hand.)

- [ ] **Step 7: Commit**

```bash
git add internal/dsp/transfer_consumer_handler.go internal/dsp/transfer_consumer_handler_test.go
git commit -m "refactor: parse Content-Range's complete length as well

The header carrying the total was already being parsed and the total thrown
away. Each half now reports its own presence, because RFC 9110 permits an
unknown complete length and folding that into one ok would turn a resume
that works today into a parse failure."
```

---

### Task 4: The provider streams under a rolling deadline

The core of the milestone, and the part the spec was wrong about on its first draft.

**Files:**
- Modify: `internal/dsp/data_handler.go`
- Test: `internal/dsp/data_handler_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func copyUnderRollingDeadline(w http.ResponseWriter, rc *http.ResponseController, src io.Reader, idle time.Duration) (int64, error)` and the test shim `type deadlineRecorder struct{ *httptest.ResponseRecorder }`. Task 6 sets `Content-Length` in the same file.

- [ ] **Step 1: Write the failing tests**

Append to `internal/dsp/data_handler_test.go`:

```go
// deadlineRecorder is httptest.ResponseRecorder plus the one method
// http.ResponseController needs. The recorder implements neither
// SetWriteDeadline nor Unwrap, so without this every streaming test would
// get the fatal error handleData now raises when deadlines are unsupported
// — and that error is fatal on purpose, so the shim moves rather than the
// production behavior.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
}

func (deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

func TestCopyUnderRollingDeadlineOutlivesAShortWriteTimeout(t *testing.T) {
	// httptest.NewServer builds a server with no timeouts at all, so a test
	// written against it would pass with and without the rolling deadline
	// and its mutation check could never fire. The timeout has to be
	// injected into an unstarted server.
	body := strings.Repeat("x", 64<<10)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		if _, err := copyUnderRollingDeadline(w, rc, &slowReader{s: body, chunk: 4 << 10, pause: 40 * time.Millisecond}, time.Second); err != nil {
			t.Errorf("copy: %v", err)
		}
	}))
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v — the response was cut off, so the deadline was not rolled", err)
	}
	if len(got) != len(body) {
		t.Errorf("got %d bytes, want %d — the write timeout truncated a response that kept making progress", len(got), len(body))
	}
}

func TestCopyUnderRollingDeadlineFailsWhenDeadlinesAreUnsupported(t *testing.T) {
	rec := httptest.NewRecorder()
	rc := http.NewResponseController(rec)
	_, err := copyUnderRollingDeadline(rec, rc, strings.NewReader("hello"), time.Second)
	if err == nil {
		t.Fatal("copy succeeded on a writer with no deadline support; an ignored error leaves the response back under the server-wide WriteTimeout with nothing saying so")
	}
}

// slowReader delivers its string in chunks with a pause between them, so a
// response takes longer than a short WriteTimeout while never going idle.
type slowReader struct {
	s     string
	chunk int
	pause time.Duration
	off   int
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.off >= len(r.s) {
		return 0, io.EOF
	}
	if r.off > 0 {
		time.Sleep(r.pause)
	}
	n := copy(p[:min(len(p), r.chunk)], r.s[r.off:])
	r.off += n
	return n, nil
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/dsp/ -run TestCopyUnderRollingDeadline
```

Expected: FAIL, `undefined: copyUnderRollingDeadline`.

- [ ] **Step 3: Write the copy helper**

Add to `internal/dsp/data_handler.go`:

```go
// copyBufSize is large on purpose. net/http buffers a response internally —
// 2048 bytes at response.w, 4096 at conn.bufw — so with a buffer near those
// sizes "the write succeeded" measures the buffer rather than the wire, and
// the deadline below would never fire on a stalled client.
const copyBufSize = 256 << 10

// copyUnderRollingDeadline streams src to w, pushing the write deadline out
// before each chunk so a transfer is bounded by time without progress rather
// than by total elapsed time.
//
// It does not use io.Copy, and that is the whole point. *http.response
// implements io.ReaderFrom, and on a non-chunked response — which is every
// response that carries a Content-Length — that implementation hands the
// whole file to *net.TCPConn.ReadFrom, one sendfile call that blocks until
// the transfer finishes. A handler parked in that call cannot roll
// anything, so whatever deadline was set before it governs the entire
// response. Writing through w directly is what keeps the loop.
//
// For the same reason w must stay an http.ResponseWriter here rather than an
// io.Writer: a helper taking io.Writer would find ReadFrom again through the
// interface and silently restore the problem. The cost is real and accepted
// — this gives up sendfile on exactly the large-file case this exists for —
// because an unbounded transfer that is merely slower beats a fast one that
// stops at the server's write timeout.
func copyUnderRollingDeadline(w http.ResponseWriter, rc *http.ResponseController, src io.Reader, idle time.Duration) (int64, error) {
	buf := make([]byte, copyBufSize)
	var total int64
	for {
		// Before the write, while the current deadline is still in the
		// future: SetWriteDeadline documents that setting a deadline after
		// it has been exceeded will not extend it.
		if err := rc.SetWriteDeadline(time.Now().Add(idle)); err != nil {
			return total, fmt.Errorf("set write deadline: %w", err)
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := w.Write(buf[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/dsp/ -run TestCopyUnderRollingDeadline -v
```

Expected: PASS, 2 tests.

- [ ] **Step 5: Set `Content-Length` before the Range branch**

One line, and it decides whether `make demo` passes, so it comes before the
copy edits rather than after.

In `handleData`, immediately after the `stat, err := f.Stat()` error check
and **before** `if rangeStart, hasRange := parseRangeStart(...)`, insert:

```go
	// Before the Range branch, so all three response shapes carry it — the
	// full 200, the 206, and the simulated interrupt, which is also a 200
	// and returns before the plain path. Setting it only at the plain-200
	// site leaves the interrupt branch chunked, and then a demo's first
	// attempt records no expected size while its resumed attempt learns the
	// real one — which reads as a changed representation, discards the
	// partial, and leaves no third start message coming to recover it.
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
```

Leave the 206 branch's own `Content-Length` line exactly where it is. It runs
after this one and overwrites it with the range's length, which is correct: a
206's `Content-Length` is the length of the part being sent, while its
`Content-Range` carries the total.

Then add a test for the shape most likely to be missed. Append to
`internal/dsp/data_handler_test.go`:

```go
func TestSimulatedInterruptStillDeclaresTheFullLength(t *testing.T) {
	// The interrupt branch is a 200 that returns before the plain-200 path,
	// so a Content-Length set only there would leave this response chunked —
	// and a consumer would record no expected size for the first attempt of
	// exactly the transfer make demo resumes.
	rec := httptest.NewRecorder()
	pullInterrupting(t, deadlineRecorder{rec}, 5000, 2000)
	if got := rec.Result().Header.Get("Content-Length"); got != "5000" {
		t.Errorf("Content-Length = %q, want the file's real length 5000 — an interrupted response must still declare it", got)
	}
}
```

`pullInterrupting(t, w, fileSize, interruptAfter)` is a helper this test
needs. Build it beside `pullAs` in the same file, following `pullAs`'s
existing setup exactly — a temp source file of `fileSize` bytes, a dataset
carrying `SimulateInterruptAfterBytes: interruptAfter`, a seeded STARTED
transfer, and a `GET` at the data path. Do not invent a second setup shape;
read `pullAs` and mirror it.

Run it:

```bash
go test ./internal/dsp/ -run TestSimulatedInterruptStillDeclaresTheFullLength
```

Expected: PASS with the line above in place. If it was put at the plain-200
site instead, `Content-Length` comes back empty and this fails — which is the
point of the test.

- [ ] **Step 6: Use the copy helper on both streaming paths**

In `handleData`, replace the 206 branch's copy. Find:

```go
		w.WriteHeader(http.StatusPartialContent)
		if _, err := io.Copy(w, f); err != nil {
			slog.Error("stream data", "provider_pid", providerPID, "error", err)
		}
		return
```

and replace with:

```go
		w.WriteHeader(http.StatusPartialContent)
		// After the 416 branch and immediately before the first byte: every
		// refusal path above must stay untouched.
		rc := http.NewResponseController(w)
		if _, err := copyUnderRollingDeadline(w, rc, f, h.cfg.DataIdleTimeout); err != nil {
			slog.Error("stream data", "provider_pid", providerPID, "error", err)
		}
		return
```

Then replace the full-content copy. Find:

```go
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, f); err != nil {
		slog.Error("stream data", "provider_pid", providerPID, "error", err)
	}
```

and replace with:

```go
	w.Header().Set("Content-Type", "application/octet-stream")
	rc := http.NewResponseController(w)
	if _, err := copyUnderRollingDeadline(w, rc, f, h.cfg.DataIdleTimeout); err != nil {
		slog.Error("stream data", "provider_pid", providerPID, "error", err)
	}
```

Also update the stale comment above the full-content path. Replace:

```go
	// Streamed rather than buffered: memory must not scale with file size.
	// The server's write timeout still bounds how large a file can finish.
```

with:

```go
	// Streamed rather than buffered: memory must not scale with file size.
	// The write deadline rolls forward as bytes leave, so what bounds this
	// is time without progress rather than the server's write timeout —
	// which would otherwise be a file size limit expressed in seconds.
```

`h.cfg.DataIdleTimeout` does not exist yet; Task 6 adds it. This step will not compile until then, which is why Step 6 is next.

- [ ] **Step 7: Add the field so this task compiles on its own**

In `internal/config/config.go`, add to the `Config` struct, immediately after the `DataDir` field:

```go
	// DataIdleTimeout bounds how long a data transfer may go without
	// progress, on both sides: the provider rolls its write deadline by it,
	// and the consumer's pull cancels when no bytes arrive within it.
	// Optional; defaultDataIdleTimeout when unset.
	DataIdleTimeout time.Duration `yaml:"data_idle_timeout"`
```

and next to the other defaults:

```go
const defaultDataIdleTimeout = 60 * time.Second
```

In `Load`, where `cfg` is initialised with its other defaults, add `DataIdleTimeout: defaultDataIdleTimeout` to the literal. Task 6 adds the environment override, the validation, and the documentation; this step exists only so Task 4 builds and its tests run.

- [ ] **Step 8: Add the shim to the five affected tests**

`httptest.ResponseRecorder` implements neither `SetWriteDeadline` nor `Unwrap`, so `SetWriteDeadline` returns `errNotSupported` and every recorder-driven streaming test now gets the fatal error. Wrap the recorder at each of these call sites.

In `internal/dsp/data_handler_test.go`, find `pullAs` and change the line that passes the recorder to `handleData` so it passes `deadlineRecorder{rec}` instead of `rec`. Then do the same for every inline `httptest.NewRecorder()` that reaches a streaming path, which is these tests:

- `TestDataPullServesTheConfiguredFile`
- `TestDataPullServesWithinAnOpenValidityWindow`
- `TestDataPullServesAPartialRange`
- `TestDataPullUnsupportedRangeFormIsIgnored`
- `TestDataPullSimulatedInterruptDoesNotFireOnARangedRequest`

Read the response from the embedded `*httptest.ResponseRecorder` exactly as before — `deadlineRecorder` embeds it, so `rec.Result()` and `rec.Body` still work if you keep a reference to the inner recorder.

Do **not** change `handleData` to tolerate the error. The fatal behavior is the decision; the shim is what absorbs it.

- [ ] **Step 9: Run the whole package**

```bash
go build ./... && go test ./internal/dsp/
```

Expected: PASS. If any of the five tests fails with a 500, its recorder was not wrapped.

- [ ] **Step 10: Mutation-check both behaviors**

| Mutation | Must fail |
|---|---|
| Move `rc.SetWriteDeadline(...)` to after the `w.Write` | `TestCopyUnderRollingDeadlineOutlivesAShortWriteTimeout` |
| Change the `SetWriteDeadline` error return to ignore it and continue | `TestCopyUnderRollingDeadlineFailsWhenDeadlinesAreUnsupported` |

Restore after each.

- [ ] **Step 11: Commit**

```bash
git add internal/dsp/data_handler.go internal/dsp/data_handler_test.go internal/config/config.go
git commit -m "feat: stream data under a rolling write deadline

io.Copy on an http.ResponseWriter does not loop. *http.response implements
io.ReaderFrom, and on a non-chunked response that hands the whole file to
*net.TCPConn.ReadFrom — one sendfile call the handler is parked in until it
finishes. So a rolling deadline and io.Copy are mutually exclusive, and the
206 path, which already sets Content-Length, was already capped at the
server's WriteTimeout with no way to observe it.

Both streaming paths now use an explicit loop that pushes the deadline
before each chunk, with a 256 KB buffer so a successful write measures the
wire rather than net/http's internal 2 KB and 4 KB buffers.

A SetWriteDeadline error is fatal: ignoring it leaves the response back
under the server-wide timeout with nothing saying so. httptest's recorder
supports no deadlines, so a shim carries the five existing streaming tests
rather than the production behavior being softened to suit them."
```

---

### Task 5: The consumer's client, and the size contract

**Files:**
- Modify: `internal/dsp/transfer_consumer_handler.go`
- Modify: `internal/store/store.go`
- Modify: `internal/dsp/transfer_handler.go`
- Test: `internal/dsp/transfer_consumer_handler_test.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `newIdleTimeoutReader`, `errIdleTimeout` (Task 2); `parseContentRange` (Task 3); `cfg.DataIdleTimeout` (Task 4).
- Produces: `store.ConsumerTransfer.ExpectedBytes int64`; `func (s *Store) SetConsumerTransferExpectedBytes(consumerPID string, expected int64) error`; `cfg.MaxDownloadBytes int64` (declared here, documented in Task 6).

- [ ] **Step 1: Add the column and its setter**

In `internal/store/store.go`, add to the `ConsumerTransfer` struct after `CounterpartyID`:

```go
	// ExpectedBytes is the complete length the counterparty stated for this
	// transfer's data, recorded on the first attempt so a later one can tell
	// a resumption from a different representation. Zero means not known —
	// never known to be zero — because a counterparty that streams chunked
	// states no length at all and that is not an error.
	ExpectedBytes int64
```

Add the column to `consumerTransferSchema`, after `state`:

```
    expected_bytes    INTEGER NOT NULL DEFAULT 0,
```

Add it to `migrate`, after the `counterparty_id` loop:

```go
	// expected_bytes is in both the CREATE literal above and this loop.
	// counterparty_id is in the literal only on agreements (section 32.5);
	// doing both here is what that section's own reasoning argues for.
	if err := addColumnIfMissing(db, "consumer_transfer_processes", "expected_bytes",
		`ALTER TABLE consumer_transfer_processes ADD COLUMN expected_bytes INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
```

Add `expected_bytes` to `CreateConsumerTransfer`'s column list, its placeholder list, and its argument list, and to `GetConsumerTransfer`'s `SELECT` and `Scan`. Then add the setter beside the other consumer-transfer setters:

```go
// SetConsumerTransferExpectedBytes records the complete length a
// counterparty stated for a transfer's data. Unguarded, unlike the state
// setters: this is not a protocol transition, and folding it into one would
// make SetConsumerTransferState's from/to guard mean two things.
func (s *Store) SetConsumerTransferExpectedBytes(consumerPID string, expected int64) error {
	if _, err := s.db.Exec(
		`UPDATE consumer_transfer_processes SET expected_bytes = ? WHERE consumer_pid = ?`,
		expected, consumerPID,
	); err != nil {
		return fmt.Errorf("set expected bytes for consumer transfer %s: %w", consumerPID, err)
	}
	return nil
}
```

- [ ] **Step 2: Write the store test**

Append to `internal/store/store_test.go`:

```go
func TestConsumerTransferExpectedBytesRoundTrips(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	if err := st.CreateConsumerTransfer(ConsumerTransfer{
		ConsumerPID: "urn:uuid:c1", ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a1",
		Format: "HttpData-PULL", State: "REQUESTED", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, found, err := st.GetConsumerTransfer("urn:uuid:c1")
	if err != nil || !found {
		t.Fatalf("get: %v found=%v", err, found)
	}
	if got.ExpectedBytes != 0 {
		t.Errorf("a fresh row has ExpectedBytes = %d, want 0 — zero means not known", got.ExpectedBytes)
	}

	if err := st.SetConsumerTransferExpectedBytes("urn:uuid:c1", 4096); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _, err = st.GetConsumerTransfer("urn:uuid:c1")
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if got.ExpectedBytes != 4096 {
		t.Errorf("ExpectedBytes = %d, want 4096", got.ExpectedBytes)
	}
}
```

Run it:

```bash
go test ./internal/store/ -run TestConsumerTransferExpectedBytes -v
```

Expected: PASS.

- [ ] **Step 3: Carry it through the projection**

In `internal/dsp/transfer_handler.go`, add to `resolvedTransfer`:

```go
	// ExpectedBytes is set for consumer-role rows only. It reaches
	// pullTransferData through the struct the caller assembles rather than a
	// store read, so the pull's read path stays store-free and the tests
	// that call it directly keep working.
	ExpectedBytes int64
```

In `lookup`, where the consumer row is projected (just after `GetConsumerTransfer` succeeds), set `ExpectedBytes: c.ExpectedBytes` on the `resolvedTransfer` it builds.

Then at the `go h.pullTransferData(store.ConsumerTransfer{...})` call site, add one field to the literal:

```go
				ExpectedBytes:  t.ExpectedBytes,
```

- [ ] **Step 4: Write the failing pull tests**

Append to `internal/dsp/transfer_consumer_handler_test.go`:

```go
func TestPullRecordsAndPublishesAStatedLength(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("y", 3000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	st := newPullStore(t, "urn:uuid:len1")
	h := transferHandler{cfg: pullConfig(dir), store: st, pulling: &sync.Map{}}
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: "urn:uuid:len1"}, &DataAddress{Endpoint: srv.URL})

	final := filepath.Join(dir, downloadDir, "urn:uuid:len1")
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("the download was not published: %v", err)
	}
	if len(got) != len(body) {
		t.Errorf("published %d bytes, want %d", len(got), len(body))
	}
	row, _, err := st.GetConsumerTransfer("urn:uuid:len1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.ExpectedBytes != int64(len(body)) {
		t.Errorf("ExpectedBytes = %d, want %d — the stated length was not recorded", row.ExpectedBytes, len(body))
	}
}

func TestPullDoesNotPublishFewerBytesThanStated(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5000")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(strings.Repeat("z", 100)))
	}))
	defer srv.Close()

	st := newPullStore(t, "urn:uuid:short1")
	h := transferHandler{cfg: pullConfig(dir), store: st, pulling: &sync.Map{}}
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: "urn:uuid:short1"}, &DataAddress{Endpoint: srv.URL})

	if _, err := os.Stat(filepath.Join(dir, downloadDir, "urn:uuid:short1")); err == nil {
		t.Error("a download short of its stated length was published")
	}
	if _, err := os.Stat(filepath.Join(dir, downloadDir, ".partial-urn:uuid:short1")); err != nil {
		t.Errorf("the partial was not kept for a later resume: %v", err)
	}
}

func TestPullPublishesWhenNoLengthIsStated(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("q", 2000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Content-Length: net/http sends this chunked, which is what this
		// connector's own provider did before this milestone and what the
		// TCK's endpoint may still do.
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 4; i++ {
			w.Write([]byte(body[i*500 : (i+1)*500]))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	st := newPullStore(t, "urn:uuid:nolen1")
	h := transferHandler{cfg: pullConfig(dir), store: st, pulling: &sync.Map{}}
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: "urn:uuid:nolen1"}, &DataAddress{Endpoint: srv.URL})

	got, err := os.ReadFile(filepath.Join(dir, downloadDir, "urn:uuid:nolen1"))
	if err != nil {
		t.Fatalf("a transfer with no stated length was not published: %v", err)
	}
	if len(got) != len(body) {
		t.Errorf("published %d bytes, want %d", len(got), len(body))
	}
	row, _, _ := st.GetConsumerTransfer("urn:uuid:nolen1")
	if row.ExpectedBytes != 0 {
		t.Errorf("ExpectedBytes = %d, want 0 — nothing was stated", row.ExpectedBytes)
	}
}

func TestResumeDiscardsThePartialWhenTheStatedTotalChanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(dir, downloadDir, ".partial-urn:uuid:chg1")
	if err := os.WriteFile(partial, []byte(strings.Repeat("a", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Starts where the partial left off, but the representation is a
		// different length than the one recorded below.
		w.Header().Set("Content-Range", "bytes 100-599/600")
		w.Header().Set("Content-Length", "500")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte(strings.Repeat("b", 500)))
	}))
	defer srv.Close()

	st := newPullStore(t, "urn:uuid:chg1")
	h := transferHandler{cfg: pullConfig(dir), store: st, pulling: &sync.Map{}}
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: "urn:uuid:chg1", ExpectedBytes: 400}, &DataAddress{Endpoint: srv.URL})

	if _, err := os.Stat(partial); err == nil {
		t.Error("the partial survived a counterparty stating a different complete length; a different representation is not a resumption")
	}
	if _, err := os.Stat(filepath.Join(dir, downloadDir, "urn:uuid:chg1")); err == nil {
		t.Error("the mismatched response was published")
	}
}

func TestResumeAcceptsAnUnknownCompleteLength(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(dir, downloadDir, ".partial-urn:uuid:star1")
	if err := os.WriteFile(partial, []byte(strings.Repeat("a", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 100-199/*")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte(strings.Repeat("b", 100)))
	}))
	defer srv.Close()

	st := newPullStore(t, "urn:uuid:star1")
	h := transferHandler{cfg: pullConfig(dir), store: st, pulling: &sync.Map{}}
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: "urn:uuid:star1", ExpectedBytes: 200}, &DataAddress{Endpoint: srv.URL})

	got, err := os.ReadFile(filepath.Join(dir, downloadDir, "urn:uuid:star1"))
	if err != nil {
		t.Fatalf("an unknown complete length was treated as a mismatch: %v", err)
	}
	if len(got) != 200 {
		t.Errorf("published %d bytes, want 200", len(got))
	}
}

func TestPullStopsAtMaxDownloadBytes(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 50; i++ {
			w.Write([]byte(strings.Repeat("m", 1000)))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	cfg := pullConfig(dir)
	cfg.MaxDownloadBytes = 2000
	st := newPullStore(t, "urn:uuid:cap1")
	h := transferHandler{cfg: cfg, store: st, pulling: &sync.Map{}}
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: "urn:uuid:cap1"}, &DataAddress{Endpoint: srv.URL})

	if _, err := os.Stat(filepath.Join(dir, downloadDir, "urn:uuid:cap1")); err == nil {
		t.Error("a download past the ceiling was published")
	}
	info, err := os.Stat(filepath.Join(dir, downloadDir, ".partial-urn:uuid:cap1"))
	if err != nil {
		t.Fatalf("stat partial: %v", err)
	}
	if info.Size() > 2000+copyBufSize {
		t.Errorf("wrote %d bytes past a %d ceiling", info.Size(), 2000)
	}
}

func TestPullIsCutOffWhenProgressStops(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(strings.Repeat("s", 500)))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	cfg := pullConfig(dir)
	cfg.DataIdleTimeout = 150 * time.Millisecond
	st := newPullStore(t, "urn:uuid:idle1")
	h := transferHandler{cfg: cfg, store: st, pulling: &sync.Map{}}
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: "urn:uuid:idle1"}, &DataAddress{Endpoint: srv.URL})

	if _, err := os.Stat(filepath.Join(dir, downloadDir, "urn:uuid:idle1")); err == nil {
		t.Error("a pull that stalled was published")
	}
	info, err := os.Stat(filepath.Join(dir, downloadDir, ".partial-urn:uuid:idle1"))
	if err != nil {
		t.Fatalf("the partial was not kept after an idle cutoff: %v", err)
	}
	if info.Size() != 500 {
		t.Errorf("partial holds %d bytes, want the 500 that arrived", info.Size())
	}
}

func TestPullCompletesWhileBytesKeepArriving(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Ten chunks, each well inside the idle window, together far beyond
		// it. This is the difference between an idle bound and a total one.
		for i := 0; i < 10; i++ {
			w.Write([]byte(strings.Repeat("k", 100)))
			w.(http.Flusher).Flush()
			time.Sleep(30 * time.Millisecond)
		}
	}))
	defer srv.Close()

	cfg := pullConfig(dir)
	cfg.DataIdleTimeout = 150 * time.Millisecond
	st := newPullStore(t, "urn:uuid:slow1")
	h := transferHandler{cfg: cfg, store: st, pulling: &sync.Map{}}
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: "urn:uuid:slow1"}, &DataAddress{Endpoint: srv.URL})

	got, err := os.ReadFile(filepath.Join(dir, downloadDir, "urn:uuid:slow1"))
	if err != nil {
		t.Fatalf("a transfer that kept making progress was cut off: %v", err)
	}
	if len(got) != 1000 {
		t.Errorf("published %d bytes, want 1000", len(got))
	}
}

func TestPullRefusesAConnectionThatNeverSendsHeaders(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // never writes a header
	}))
	defer func() { close(release); srv.Close() }()

	cfg := pullConfig(dir)
	cfg.DataIdleTimeout = 150 * time.Millisecond
	st := newPullStore(t, "urn:uuid:nohdr1")
	h := transferHandler{cfg: cfg, store: st, pulling: &sync.Map{}}

	done := make(chan struct{})
	go func() {
		h.pullTransferData(store.ConsumerTransfer{ConsumerPID: "urn:uuid:nohdr1"}, &DataAddress{Endpoint: srv.URL})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a counterparty that never sent headers held the pull open; ResponseHeaderTimeout is not set")
	}
}

// newPullStore returns an in-memory store holding one consumer transfer row
// for the given pid, so a pull can record against it.
func newPullStore(t *testing.T, consumerPID string) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC()
	if err := st.CreateConsumerTransfer(store.ConsumerTransfer{
		ConsumerPID: consumerPID, ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a",
		Format: "HttpData-PULL", State: "STARTED", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed consumer transfer: %v", err)
	}
	return st
}

// pullConfig is the smallest config a pull needs, with bounds generous
// enough not to fire unless a test lowers them.
func pullConfig(dir string) config.Config {
	return config.Config{
		DataDir:          dir,
		DataIdleTimeout:  5 * time.Second,
		MaxDownloadBytes: 1 << 30,
	}
}
```

- [ ] **Step 5: Run to verify they fail**

```bash
go test ./internal/dsp/ -run 'TestPull|TestResume'
```

Expected: FAIL — `cfg.MaxDownloadBytes` undefined and the behaviors unimplemented.

- [ ] **Step 6: Add `MaxDownloadBytes` to the config struct**

In `internal/config/config.go`, after `DataIdleTimeout`:

```go
	// MaxDownloadBytes caps a single data pull. With no overall client
	// timeout, a counterparty that states no length is bounded by nothing
	// else — and it is also the only backstop against a transfer that never
	// goes idle because it dribbles. Optional; defaultMaxDownloadBytes when
	// unset.
	MaxDownloadBytes int64 `yaml:"max_download_bytes"`
```

and:

```go
const defaultMaxDownloadBytes = 8 << 30
```

Add `MaxDownloadBytes: defaultMaxDownloadBytes` to `Load`'s initial literal beside `DataIdleTimeout`.

- [ ] **Step 7: Add the data-pull client**

In `internal/dsp/transfer_consumer_handler.go`, add near the top:

```go
// dataPullHTTPClient fetches transfer data. It is deliberately not
// callbackHTTPClient: a callback push is a small JSON body that should be
// answered at once, and ten seconds is right for it, while a data pull's
// body is the product and may legitimately take hours.
//
// So there is no Client.Timeout here — the body is bounded by
// idleTimeoutReader instead, on time without progress. Two things that
// looks like an omission are requirements:
//
// CheckRedirect matches callbackHTTPClient's. validateOutgoingCallback
// checks the endpoint a counterparty supplied and nothing a redirect points
// at, so a client that follows redirects would let that endpoint hop to
// 127.0.0.1:8081 and reach the management listener that binds to localhost
// precisely so a firewall mistake cannot expose it (DECISIONS.md section
// 12).
//
// The transport is a clone of the default rather than a bare literal.
// ResponseHeaderTimeout starts only once the request is written, so without
// the clone's DialContext timeout and TLSHandshakeTimeout — and
// validateCallbackURL permits https — a black-holed endpoint would hold this
// goroutine for as long as the OS is willing to wait.
var dataPullHTTPClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}()
```

- [ ] **Step 8: Rewrite the fetch and the copy in `pullTransferData`**

Replace the request construction and the `callbackHTTPClient.Do(req)` call with a context-carrying request through the new client:

```go
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr.Endpoint, nil)
	if err != nil {
		slog.Error("build data pull", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	if authorization := mintOutboundCredential(t.CounterpartyID); authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if resuming {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}
	resp, err := dataPullHTTPClient.Do(req)
```

In the `case http.StatusPartialContent:` branch, after the existing first-byte check passes, add the complete-length comparison:

```go
			// A counterparty stating a different complete length is
			// answering about a different representation, not continuing
			// this one. Same handling as 416, and for the same reason.
			// An absent or unknown total is not a mismatch.
			if _, _, complete, hasComplete := parseContentRange(contentRange); hasComplete &&
				t.ExpectedBytes > 0 && complete != t.ExpectedBytes {
				slog.Warn("the provider states a different complete length than this transfer recorded; discarding the partial download",
					"consumer_pid", t.ConsumerPID, "stated", complete, "recorded", t.ExpectedBytes)
				if err := os.Remove(partial); err != nil {
					slog.Error("remove stale partial download", "path", partial, "error", err)
				}
				return
			}
```

After the status handling and before opening the file, work out the expected total for this attempt and record it if this is the first one:

```go
	// The complete length the counterparty states, from whichever header
	// carries it. Zero means not known — never known to be zero — because a
	// counterparty that streams chunked states nothing, and this
	// connector's own provider did exactly that until this milestone.
	expected := t.ExpectedBytes
	if resuming {
		if _, _, complete, hasComplete := parseContentRange(resp.Header.Get("Content-Range")); hasComplete {
			expected = complete
		}
	} else if resp.ContentLength >= 0 {
		expected = resp.ContentLength
	}
	if expected > 0 && expected != t.ExpectedBytes {
		if err := h.store.SetConsumerTransferExpectedBytes(t.ConsumerPID, expected); err != nil {
			slog.Error("record expected bytes", "consumer_pid", t.ConsumerPID, "error", err)
		}
	}
```

Replace the copy with one that is bounded, idle-aware, and synced:

```go
	body := newIdleTimeoutReader(resp.Body, h.cfg.DataIdleTimeout, cancel)
	defer body.Stop()

	remaining := h.cfg.MaxDownloadBytes - existingSize
	n, err := io.Copy(out, io.LimitReader(body, remaining+1))
	if err != nil {
		out.Close()
		if cause := context.Cause(ctx); errors.Is(cause, errIdleTimeout) || errors.Is(err, errIdleTimeout) {
			slog.Error("data pull made no progress within the idle timeout; leaving the partial download in place",
				"consumer_pid", t.ConsumerPID, "had_bytes", existingSize, "appended_bytes", n)
			return
		}
		slog.Error("write download", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	if n > remaining {
		out.Close()
		slog.Error("data pull exceeded max_download_bytes; leaving the partial download in place",
			"consumer_pid", t.ConsumerPID, "limit", h.cfg.MaxDownloadBytes)
		return
	}
	// Sync before the rename. A rename that outruns its data turns a crash
	// into a success log beside a truncated file, which is worse than a
	// failure because it reports itself as one that did not happen.
	if err := out.Sync(); err != nil {
		out.Close()
		slog.Error("sync download", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	if err := out.Close(); err != nil {
		slog.Error("close download", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}

	total := existingSize + n
	if expected > 0 && total != expected {
		slog.Error("the download does not match the length the provider stated; leaving the partial download in place",
			"consumer_pid", t.ConsumerPID, "have", total, "stated", expected)
		return
	}
```

Leave the existing `resuming` log line and the rename that follow, and change the final success line to report the total rather than this attempt's bytes:

```go
	slog.Info("pulled transfer data", "consumer_pid", t.ConsumerPID, "path", final, "bytes", total, "expected", expected)
```

Add `"context"` and `"errors"` to the file's imports.

- [ ] **Step 9: Run the tests to verify they pass**

```bash
go build ./... && go test ./internal/dsp/ ./internal/store/
```

Expected: PASS, including the five pre-existing direct-call pull tests, which pass a bare struct with `ExpectedBytes` zero and therefore skip every size check.

- [ ] **Step 10: Mutation-check each new check**

| Mutation | Must fail |
|---|---|
| Delete the `expected > 0 && total != expected` block | `TestPullDoesNotPublishFewerBytesThanStated` |
| Delete the complete-length mismatch block in the 206 branch | `TestResumeDiscardsThePartialWhenTheStatedTotalChanged` |
| Drop `hasComplete &&` from that condition | `TestResumeAcceptsAnUnknownCompleteLength` |
| Delete the `n > remaining` block | `TestPullStopsAtMaxDownloadBytes` |
| Remove `out.Sync()` | none — record this in the ledger as untested and say why (a crash cannot be staged in a unit test); it is carried by review, not by a test |
| Replace `dataPullHTTPClient` with `callbackHTTPClient` | `TestPullCompletesWhileBytesKeepArriving` |
| Remove `tr.ResponseHeaderTimeout` | `TestPullRefusesAConnectionThatNeverSendsHeaders` |

- [ ] **Step 11: Commit**

```bash
git add internal/dsp/ internal/store/ internal/config/config.go
git commit -m "feat: bound the pull by progress and check what arrives

The data pull no longer borrows the callback client, whose ten-second
timeout covers the response body and so capped a transfer at roughly what
the link moved in ten seconds. Its own client has no overall timeout; the
body is bounded by time without progress instead.

Two things the new client must not lose: redirects stay disabled, because
validateOutgoingCallback checks the endpoint and nothing a redirect points
at, and the transport is a clone of the default so the dial and TLS
handshake stay bounded once Client.Timeout is gone.

The expected size comes from Content-Length or Content-Range, is recorded
on the first attempt, and a later attempt stating a different total is
answering about a different representation. Zero means not known, never
known to be zero: a counterparty that streams chunked states nothing, and
this connector's own provider did exactly that. max_download_bytes bounds
what no stated length can."
```

---

### Task 6: Configuration, shutdown, and the documents that are now wrong

**Files:**
- Modify: `internal/config/config.go`
- Modify: `config.example.yaml`
- Modify: `cmd/dsbox/main.go`
- Modify: `internal/dsp/transfer_handler.go`
- Modify: `DECISIONS.md`
- Modify: `README.md`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `cfg.DataIdleTimeout`, `cfg.MaxDownloadBytes` (Tasks 4 and 5).
- Produces: nothing later tasks depend on. This is the last task in Plan A.

- [ ] **Step 1: Write the failing config tests**

Append to `internal/config/config_test.go`:

```go
func TestDataIdleTimeoutDefaultsAndOverrides(t *testing.T) {
	base := "public_url: http://x\nparticipant_id: urn:participant:x\ndata_dir: /tmp/x\nrequire_auth: false\ndev_mode: true\n"

	cfg, err := Load([]byte(base), func(string) string { return "" })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataIdleTimeout != defaultDataIdleTimeout {
		t.Errorf("DataIdleTimeout = %v, want the default %v", cfg.DataIdleTimeout, defaultDataIdleTimeout)
	}
	if cfg.MaxDownloadBytes != defaultMaxDownloadBytes {
		t.Errorf("MaxDownloadBytes = %d, want the default %d", cfg.MaxDownloadBytes, defaultMaxDownloadBytes)
	}

	cfg, err = Load([]byte(base+"data_idle_timeout: 90s\nmax_download_bytes: 1024\n"), func(string) string { return "" })
	if err != nil {
		t.Fatalf("load with values: %v", err)
	}
	if cfg.DataIdleTimeout != 90*time.Second {
		t.Errorf("DataIdleTimeout = %v, want 90s", cfg.DataIdleTimeout)
	}
	if cfg.MaxDownloadBytes != 1024 {
		t.Errorf("MaxDownloadBytes = %d, want 1024", cfg.MaxDownloadBytes)
	}

	cfg, err = Load([]byte(base), func(k string) string {
		switch k {
		case "DSBOX_DATA_IDLE_TIMEOUT":
			return "5s"
		case "DSBOX_MAX_DOWNLOAD_BYTES":
			return "77"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("load with env: %v", err)
	}
	if cfg.DataIdleTimeout != 5*time.Second {
		t.Errorf("DataIdleTimeout = %v, want the environment's 5s", cfg.DataIdleTimeout)
	}
	if cfg.MaxDownloadBytes != 77 {
		t.Errorf("MaxDownloadBytes = %d, want the environment's 77", cfg.MaxDownloadBytes)
	}
}

func TestNonPositiveDataBoundsAreRejected(t *testing.T) {
	base := "public_url: http://x\nparticipant_id: urn:participant:x\ndata_dir: /tmp/x\nrequire_auth: false\ndev_mode: true\n"
	for _, tc := range []struct{ name, doc string }{
		{"zero idle timeout", base + "data_idle_timeout: 0s\n"},
		{"negative idle timeout", base + "data_idle_timeout: -1s\n"},
		{"zero max download bytes", base + "max_download_bytes: 0\n"},
		{"negative max download bytes", base + "max_download_bytes: -1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load([]byte(tc.doc), func(string) string { return "" }); err == nil {
				t.Error("loaded without error; a bound that is not positive disables the thing it bounds")
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
go test ./internal/config/ -run 'TestDataIdleTimeout|TestNonPositiveDataBounds'
```

Expected: FAIL — no overrides and no validation yet.

- [ ] **Step 3: Add the overrides and the validation**

In `Load`'s environment block, beside the other `DSBOX_*` reads:

```go
	if v := getenv("DSBOX_DATA_IDLE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("DSBOX_DATA_IDLE_TIMEOUT %q: %w", v, err)
		}
		cfg.DataIdleTimeout = d
	}
	if v := getenv("DSBOX_MAX_DOWNLOAD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("DSBOX_MAX_DOWNLOAD_BYTES %q: %w", v, err)
		}
		cfg.MaxDownloadBytes = n
	}
```

In the validation function, beside the other checks:

```go
	if c.DataIdleTimeout <= 0 {
		return fmt.Errorf("data_idle_timeout must be positive, got %v: a transfer with no idle bound is one nothing can stop", c.DataIdleTimeout)
	}
	if c.MaxDownloadBytes <= 0 {
		return fmt.Errorf("max_download_bytes must be positive, got %d: it is the only bound on a counterparty that states no length", c.MaxDownloadBytes)
	}
```

Add `"strconv"` to the imports if it is not already there.

- [ ] **Step 4: Run to verify they pass**

```bash
go test ./internal/config/
```

Expected: PASS, including the existing `TestExampleConfigLoads`.

- [ ] **Step 5: Wire shutdown to wait for in-flight pulls**

In `internal/dsp/transfer_handler.go`, add a field to `transferHandler` beside `pulling`:

```go
	// pulls counts in-flight pullTransferData goroutines so the connector
	// can wait for them at shutdown. Without this, a pull's store write can
	// land after run()'s deferred st.Close() and be lost — and with the
	// overall client timeout gone, the window is unbounded rather than ten
	// seconds wide.
	pulls *sync.WaitGroup
```

In `pullTransferData`, immediately after the `h.pulling` guard:

```go
	if h.pulls != nil {
		h.pulls.Add(1)
		defer h.pulls.Done()
	}
```

In `NewRouter` (`internal/dsp/router.go`), construct the handler with a `WaitGroup` and return it so `main` can wait. Change `NewRouter`'s signature to return `(http.Handler, *sync.WaitGroup)`, build `pulls := &sync.WaitGroup{}`, pass it into `transferHandler`, and return it alongside the handler.

In `cmd/dsbox/main.go`, take the new return value:

```go
	dspHandler, pulls := dsp.NewRouter(cfg, st, roster, signKey)
```

and use `dspHandler` for `dspSrv.Handler`. Then, after the two `srv.Shutdown` calls complete and before `run` returns — that is, before the deferred `st.Close()` fires — wait for pulls with a bound:

```go
	// In-flight pulls write to the store on their way out, so they have to
	// finish before the deferred st.Close() above. Bounded: a pull that is
	// still going after this loses its record, which is better than holding
	// shutdown open indefinitely.
	pullsDone := make(chan struct{})
	go func() { pulls.Wait(); close(pullsDone) }()
	select {
	case <-pullsDone:
	case <-time.After(5 * time.Second):
		slog.Warn("shutting down with data pulls still in flight; their outcome will not be recorded")
	}
```

Update every other caller of `dsp.NewRouter` — search with `grep -rn 'dsp.NewRouter' --include='*.go' .` — and adjust each to the two-value return.

- [ ] **Step 6: Build and run everything**

```bash
go build ./... && go vet ./... && go test ./...
```

Expected: PASS.

- [ ] **Step 7: Document the two fields**

In `config.example.yaml`, after the `data_dir` block, add:

```yaml
# How long a data transfer may go without progress before it is given up on,
# on both sides: the provider rolls its response write deadline by this, and
# a consumer's pull cancels when no bytes arrive within it. Optional,
# default 60s. DSBOX_DATA_IDLE_TIMEOUT overrides it.
#
# This bounds time without progress, not total time. That distinction is the
# whole point: a bound on total time is a file size limit expressed in
# seconds, and it moves with the network rather than with any decision an
# operator made.
# data_idle_timeout: 60s

# The ceiling on a single data pull, in bytes. Optional, default 8589934592
# (8 GiB). DSBOX_MAX_DOWNLOAD_BYTES overrides it.
#
# A counterparty that states a Content-Length is bounded by it. One that
# streams chunked states nothing, and with no overall client timeout this is
# what bounds it instead — as well as being the only backstop against a
# transfer that never goes idle because it dribbles.
# max_download_bytes: 8589934592
```

In `internal/config/config.go`, extend the `SimulateInterruptAfterBytes` doc comment with:

```go
	// Note what this now does to the response's framing: the connector sets
	// Content-Length from the file's real size before it decides how much to
	// send, so an interrupted response declares a length it does not deliver
	// and a client sees an unexpected EOF. That is a more realistic
	// simulation than a truncated chunked stream, and it is why the header
	// is set before the Range branch rather than beside each write.
```

In `config.example.yaml`'s `simulate_interrupt_after_bytes` comment, add:

```
    # The response still declares the file's full Content-Length, so a
    # client sees an unexpected EOF rather than a short but well-formed
    # body — which is what a real interruption looks like.
```

- [ ] **Step 8: Correct README's Status section**

`README.md` currently names the wrong bound. Replace this sentence:

```
**And a pull is capped at ten seconds, which is not a policy.** The
```

through the end of that paragraph, with:

```
**A transfer is now bounded by progress, not by the clock.** Both sides give
up only after `data_idle_timeout` passes with no bytes moving, so a transfer
of any size finishes as long as it keeps moving. `max_download_bytes` is the
ceiling for a counterparty that states no length, and the only backstop
against one that dribbles slowly enough never to go idle. What still has no
measurement is size: `make demo` moves kilobytes and the TCK moves no bytes,
so nothing here proves how large a transfer this can actually carry.
```

Also correct the integrity sentence. Replace:

```
whole of the integrity check: a same-size content replacement between
attempts is not caught.
```

with:

```
one half of the integrity check. The other half is length: a counterparty
that states a `Content-Length` or a complete `Content-Range` has that value
recorded on the first attempt and compared on every later one, so a
replacement of a different size is refused rather than appended to. A
replacement of exactly the same size is still not caught, and a counterparty
that states no length at all is checked only by the byte ceiling.
```

- [ ] **Step 9: Add the DECISIONS section**

Append a new section to `DECISIONS.md`, numbered 33, following the shape of the sections around it — a **Decision**, a **Rationale**, and a **Trade-off accepted**. It must record:

- that both sides bound progress rather than total time, and why a total bound is a size limit;
- that `io.Copy` is banned on provider streaming paths because `*http.response`'s `ReadFrom` collapses a non-chunked response into one `sendfile` call, and that this means the 206 path was already capped before this milestone;
- that `SetWriteDeadline` errors are fatal, and that `httptest.ResponseRecorder` needs a shim as a result;
- that `expected_bytes` of 0 means not known, never known to be zero;
- the trade-offs: `sendfile` is given up on large files; a same-length replacement is still not caught; and removing the ten-second bound makes an orphaned partial unboundedly large, which makes the retention rule in `docs/follow-ups.md` overdue.

Cross-reference the spec at `docs/superpowers/specs/2026-08-24-data-path-correctness-design.md`.

- [ ] **Step 10: Run every gate**

```bash
go build ./... && go vet ./... && go test -race ./...
make tck
make demo
```

Expected: unit tests pass; `make tck` reports **65 of 65**; `make demo` moves its file, diffs it, and its resume round still logs `resumed transfer data pull`.

If `make demo` fails on the resume round, the cause is almost certainly `Content-Length` placement — confirm it is set once after `Stat` and before the Range branch, so the `SimulateInterruptAfterBytes` path declares it too.

- [ ] **Step 11: Commit**

```bash
git add -A
git commit -m "feat: configure the data bounds, wait for pulls at shutdown

data_idle_timeout and max_download_bytes both get DSBOX_* overrides, like
every other scalar in the config, and both are rejected when not positive:
a bound that is not positive disables the thing it bounds.

In-flight pulls are now waited for before the store closes. They write to
it on their way out, and with the overall client timeout gone the window
between a pull finishing and the process exiting is unbounded rather than
ten seconds wide.

README named the wrong bound as the limit on transfer size, and the
simulate_interrupt_after_bytes documentation did not mention that an
interrupted response now declares a length it does not deliver."
```

---

## Self-review

**Spec coverage.** §1.1 → Task 4. §1.2 → Task 4. §1.3 → Task 5 Steps 7-8, with the reader itself in Task 2. §1.4 → the ceiling in Task 5, documented in Task 6. §1.5 → Task 6 Step 5. §1.6 → Tasks 4 Step 6 and 6 Steps 1-3. §2's provider half → Task 4, with `Content-Length` placement as its own step (Task 4 Step 5) because that placement decides whether `make demo` passes. §2's consumer half → Tasks 3 and 5. §2's durability → Task 5 Step 8. The `expected_bytes` column → Task 5 Steps 1-3. §6's TCK measurement → Task 1.

**Placeholder scan.** No "TBD", no "add appropriate error handling", no "similar to Task N". Task 6 Step 9's DECISIONS section is described by required content rather than written out, which is the one place this plan asks the implementer to compose prose; that is deliberate, because the section must match its neighbours' voice and a pasted draft would read as foreign.

**Type consistency.** `parseContentRange` returns four values everywhere it appears (Tasks 3 and 5). `newIdleTimeoutReader(r, idle, cancel)` and `errIdleTimeout` match between Tasks 2 and 5. `copyUnderRollingDeadline(w, rc, src, idle)` matches between Task 4's helper and its two call sites. `store.ConsumerTransfer.ExpectedBytes` and `SetConsumerTransferExpectedBytes` match between Task 5's store edits and its pull edits. `cfg.DataIdleTimeout` and `cfg.MaxDownloadBytes` are declared in Tasks 4 and 5 respectively and configured in Task 6 — each is declared before first use.
