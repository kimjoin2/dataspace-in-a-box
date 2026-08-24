# Data Path — Recording and Exposure (Plan B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator can ask whether data arrived, and a provider can say who collected it.

**Architecture:** Four columns on the consumer transfer row record what a pull did, written once from a deferred recorder rather than scattered across seventeen exit points. Shutdown gains the cancellation the spec asked for, because moving the write to the end of the copy is what makes the existing cap inadequate without it. One read-only management route lists both roles, and the provider's success path stops being silent.

**Tech Stack:** Go 1.26, standard library only. SQLite via `modernc.org/sqlite` (already present). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-24-data-path-correctness-design.md` — this plan implements **Plan B** of that spec's §7: the remaining four columns of §3, §4's `GET /transfers`, and §5's audit line. Plan A (§1, §2, `expected_bytes`, `Sync`) is done and pushed; `origin/main` is at `5c428b5`.

## Global Constraints

- Go standard library only. Ask before adding any dependency; the default answer is no.
- English for all documentation, code comments, error strings, and commit messages.
- Every check is verified by mutation: delete or invert the check, confirm a **named** test fails, restore, confirm `git diff` is empty. **Each mutation in this plan carries a one-line reason it must fail its named test.** If you cannot state that reason, the mutation is wrong — four of Plan A's prescribed mutations turned out to test nothing, and each was a defect in that plan, not in the code.
- `httptest.ResponseRecorder` does not enforce `Content-Length` and supports no deadlines. Anything about response framing or write deadlines must be tested against `httptest.NewServer`.
- Existing tests build `config.Config` as struct literals, bypassing `config.Load`'s defaults. A new config field must be filled in `newTestTransferHandler` (`internal/dsp/transfer_handler_test.go:22`) and in `data_handler_test.go`'s fixtures.
- Documentation that says something untrue about the code blocked Plan A's merge twice. Every documentation step in this plan names the code fact its sentence must be checked against; check it before writing.
- Final gates: `go vet ./...`, `go test -race ./...`, `make tck` holding **65 of 65**, and `make demo` still moving its file, diffing it, and logging its resumption round.

---

### Task 1: The four columns and their store methods

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `store.ConsumerTransfer` gains `ReceivedBytes int64`, `DataPath string`, `DataCompletedAt time.Time`, `DataError string`.
  - `func (s *Store) RecordConsumerTransferOutcome(consumerPID string, received int64, path string, completedAt time.Time, failure string) error`
  - Task 2 calls that method from a deferred recorder. Tasks 3 and 4 read the fields.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/store_test.go`:

```go
func TestConsumerTransferOutcomeRoundTrips(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC().Truncate(time.Second)
	if err := st.CreateConsumerTransfer(ConsumerTransfer{
		ConsumerPID: "urn:uuid:o1", ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a1",
		Format: "HttpData-PULL", State: "STARTED", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, found, err := st.GetConsumerTransfer("urn:uuid:o1")
	if err != nil || !found {
		t.Fatalf("get: %v found=%v", err, found)
	}
	if got.ReceivedBytes != 0 || got.DataPath != "" || got.DataError != "" || !got.DataCompletedAt.IsZero() {
		t.Errorf("a fresh row is not blank: %+v — a transfer that has not fetched anything must read as one", got)
	}

	// A success: bytes, a path, a completion, and no failure.
	if err := st.RecordConsumerTransferOutcome("urn:uuid:o1", 4096, "/data/downloads/urn:uuid:o1", now, ""); err != nil {
		t.Fatalf("record success: %v", err)
	}
	got, _, err = st.GetConsumerTransfer("urn:uuid:o1")
	if err != nil {
		t.Fatalf("get after success: %v", err)
	}
	if got.ReceivedBytes != 4096 {
		t.Errorf("ReceivedBytes = %d, want 4096", got.ReceivedBytes)
	}
	if got.DataPath != "/data/downloads/urn:uuid:o1" {
		t.Errorf("DataPath = %q, want the published path", got.DataPath)
	}
	if !got.DataCompletedAt.Equal(now) {
		t.Errorf("DataCompletedAt = %v, want %v", got.DataCompletedAt, now)
	}
	if got.DataError != "" {
		t.Errorf("DataError = %q, want empty on a success", got.DataError)
	}

	// A failure on a later attempt: the reason is recorded and the
	// completion is cleared, so the row cannot read as both at once.
	if err := st.RecordConsumerTransferOutcome("urn:uuid:o1", 100, "", time.Time{}, "no progress within the idle timeout"); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	got, _, err = st.GetConsumerTransfer("urn:uuid:o1")
	if err != nil {
		t.Fatalf("get after failure: %v", err)
	}
	if got.DataError != "no progress within the idle timeout" {
		t.Errorf("DataError = %q, want the reason", got.DataError)
	}
	if !got.DataCompletedAt.IsZero() {
		t.Errorf("DataCompletedAt = %v, want zero — a failed attempt must not leave a completion behind", got.DataCompletedAt)
	}
	if got.DataPath != "" {
		t.Errorf("DataPath = %q, want empty — nothing was published", got.DataPath)
	}
}

func TestRecordConsumerTransferOutcomeOnAMissingRowIsNotAnError(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Seventeen tests call pullTransferData directly with no seeded row, and
	// the deferred recorder Task 2 adds runs on every one of them. This must
	// stay a silent no-op or all of them start failing.
	if err := st.RecordConsumerTransferOutcome("urn:uuid:absent", 1, "/x", time.Now().UTC(), ""); err != nil {
		t.Errorf("recording against a missing row returned %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/store/ -run 'TestConsumerTransferOutcome|TestRecordConsumerTransferOutcome'
```

Expected: FAIL, `got.ReceivedBytes undefined` and `st.RecordConsumerTransferOutcome undefined`.

- [ ] **Step 3: Add the fields**

In `internal/store/store.go`, add to `ConsumerTransfer` immediately after `ExpectedBytes`:

```go
	// ReceivedBytes, DataPath, DataCompletedAt, and DataError record what a
	// pull did. They are the answer to the only question an operator can ask
	// about a transfer that the protocol state does not answer: did the data
	// arrive? A completed download has DataCompletedAt set and DataError
	// empty; a failed one is the reverse, and the two can never both hold,
	// because RecordConsumerTransferOutcome writes all four together.
	//
	// DataError holds the reason a pull stopped rather than a code. The
	// reasons are already distinct sentences in the log, and the sentence is
	// what an operator reading this field needs.
	ReceivedBytes   int64
	DataPath        string
	DataCompletedAt time.Time
	DataError       string
```

- [ ] **Step 4: Add the columns to the schema and the migration**

In `consumerTransferSchema`, after `expected_bytes`:

```
    received_bytes    INTEGER NOT NULL DEFAULT 0,
    data_path         TEXT NOT NULL DEFAULT '',
    data_completed_at TEXT NOT NULL DEFAULT '',
    data_error        TEXT NOT NULL DEFAULT '',
```

In `migrate`, beside the `expected_bytes` call:

```go
	// In the CREATE literal above and in this loop both, the same way
	// expected_bytes is, for the reason section 32.5 gives.
	for _, col := range []struct{ name, stmt string }{
		{"received_bytes", `ALTER TABLE consumer_transfer_processes ADD COLUMN received_bytes INTEGER NOT NULL DEFAULT 0`},
		{"data_path", `ALTER TABLE consumer_transfer_processes ADD COLUMN data_path TEXT NOT NULL DEFAULT ''`},
		{"data_completed_at", `ALTER TABLE consumer_transfer_processes ADD COLUMN data_completed_at TEXT NOT NULL DEFAULT ''`},
		{"data_error", `ALTER TABLE consumer_transfer_processes ADD COLUMN data_error TEXT NOT NULL DEFAULT ''`},
	} {
		if err := addColumnIfMissing(db, "consumer_transfer_processes", col.name, col.stmt); err != nil {
			return err
		}
	}
```

- [ ] **Step 5: Extend the existing read and write**

Add the four columns to `CreateConsumerTransfer`'s column list, placeholders, and arguments — passing `t.ReceivedBytes`, `t.DataPath`, the formatted `DataCompletedAt`, and `t.DataError`. A zero `DataCompletedAt` must store `''`, not a formatted zero time, so use the same shape as elsewhere in this file:

```go
	completedAt := ""
	if !t.DataCompletedAt.IsZero() {
		completedAt = t.DataCompletedAt.UTC().Format(timeFormat)
	}
```

Add them to `GetConsumerTransfer`'s `SELECT` and `Scan`, scanning `data_completed_at` into a `string` and parsing it only when non-empty:

```go
	if completedAt != "" {
		if t.DataCompletedAt, err = time.Parse(timeFormat, completedAt); err != nil {
			return ConsumerTransfer{}, false, fmt.Errorf("get consumer transfer %s: parse data_completed_at: %w", consumerPID, err)
		}
	}
```

- [ ] **Step 6: Add the recorder**

Beside the other consumer-transfer setters:

```go
// RecordConsumerTransferOutcome writes what a pull did, all four columns at
// once. Together rather than individually so a row can never read as both
// completed and failed: a success passes a completion and an empty failure,
// a failure passes the reason and a zero time, and each overwrites whatever
// the last attempt left.
//
// A missing row is not an error. Seventeen tests drive pullTransferData
// directly with no row behind it, and in production there is always one —
// lookup found it, and there is no delete path. Surfacing "not found" here
// would add noise to the tests and tell production nothing it could act on.
func (s *Store) RecordConsumerTransferOutcome(consumerPID string, received int64, path string, completedAt time.Time, failure string) error {
	stamp := ""
	if !completedAt.IsZero() {
		stamp = completedAt.UTC().Format(timeFormat)
	}
	if _, err := s.db.Exec(
		`UPDATE consumer_transfer_processes
		 SET received_bytes = ?, data_path = ?, data_completed_at = ?, data_error = ?
		 WHERE consumer_pid = ?`,
		received, path, stamp, failure, consumerPID,
	); err != nil {
		return fmt.Errorf("record outcome for consumer transfer %s: %w", consumerPID, err)
	}
	return nil
}
```

- [ ] **Step 7: Run the tests to verify they pass**

```bash
go test ./internal/store/ && go build ./...
```

Expected: PASS.

- [ ] **Step 8: Mutation-check**

| Mutation | Must fail | Why it fails |
|---|---|---|
| Drop `data_completed_at = ?` from the UPDATE's SET list (and its argument) | `TestConsumerTransferOutcomeRoundTrips` | The failure branch asserts the completion is cleared; without the column in the SET, the success's stamp survives the failure write. |
| Return an error instead of nil when `RowsAffected` is 0 — add `if n, _ := res.RowsAffected(); n == 0 { return fmt.Errorf("no such transfer") }` | `TestRecordConsumerTransferOutcomeOnAMissingRowIsNotAnError` | That test records against a pid with no row and asserts nil. |
| Store the zero time as a formatted stamp instead of `""` | `TestConsumerTransferOutcomeRoundTrips` | `DataCompletedAt.IsZero()` is asserted after the failure write; a formatted zero parses back as a non-zero year-1 time. |

Restore after each; confirm `git diff` empty.

- [ ] **Step 9: Commit**

```bash
git add internal/store/
git commit -m "feat: record what a pull did on the transfer row

Four columns, written together rather than one at a time, so a row cannot
read as both completed and failed. data_error holds the reason rather than a
code: the reasons are already distinct sentences in the log, and the sentence
is what an operator needs.

A missing row is not an error. Seventeen tests drive the pull directly with
no row behind it, and production always has one."
```

---

### Task 2: One deferred recorder, not seventeen write sites

`pullTransferData` has sixteen failure exits and one success. Writing the outcome at each is sixteen chances to miss one, and nothing would catch the miss.

**Files:**
- Modify: `internal/dsp/transfer_consumer_handler.go`
- Test: `internal/dsp/transfer_consumer_handler_test.go`

**Interfaces:**
- Consumes: `store.RecordConsumerTransferOutcome` (Task 1).
- Produces: nothing later tasks call directly. Task 4 reads the columns this fills.

- [ ] **Step 1: Write the failing tests**

Append to `internal/dsp/transfer_consumer_handler_test.go`:

```go
// TestPullRecordsItsOutcomeOnEveryPath is the reason the recorder is
// deferred rather than written at each exit: it pins that a representative
// failure, a representative success, and a refusal before any request all
// leave the row describing what happened.
func TestPullRecordsItsOutcomeOnEveryPath(t *testing.T) {
	t.Run("a success records the bytes, the path, and no failure", func(t *testing.T) {
		dir := t.TempDir()
		body := strings.Repeat("y", 2048)
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
		defer provider.Close()

		h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
		pid := seedConsumerTransfer(t, st, "STARTED")
		h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: provider.URL})

		row, _, err := st.GetConsumerTransfer(pid)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if row.ReceivedBytes != int64(len(body)) {
			t.Errorf("ReceivedBytes = %d, want %d", row.ReceivedBytes, len(body))
		}
		if row.DataPath == "" {
			t.Error("DataPath is empty after a published download")
		}
		if row.DataCompletedAt.IsZero() {
			t.Error("DataCompletedAt is zero after a published download")
		}
		if row.DataError != "" {
			t.Errorf("DataError = %q, want empty on a success", row.DataError)
		}
	})

	t.Run("an idle cutoff records the reason and no completion", func(t *testing.T) {
		dir := t.TempDir()
		release := make(chan struct{})
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(strings.Repeat("s", 500)))
			w.(http.Flusher).Flush()
			<-release
		}))
		defer func() { close(release); provider.Close() }()

		h, st := newTestTransferHandler(t, config.Config{DataDir: dir, DataIdleTimeout: 150 * time.Millisecond})
		pid := seedConsumerTransfer(t, st, "STARTED")
		h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: provider.URL})

		row, _, err := st.GetConsumerTransfer(pid)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if row.DataError == "" {
			t.Fatal("DataError is empty after a pull that was cut off — a failed pull and a successful one must be distinguishable in storage")
		}
		if !strings.Contains(row.DataError, "idle") {
			t.Errorf("DataError = %q, want the sentence naming the idle timeout", row.DataError)
		}
		if !row.DataCompletedAt.IsZero() {
			t.Error("DataCompletedAt is set after a failure")
		}
		if row.DataPath != "" {
			t.Errorf("DataPath = %q, want empty — nothing was published", row.DataPath)
		}
	})

	t.Run("a refusal before any request still records", func(t *testing.T) {
		dir := t.TempDir()
		h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
		pid := seedConsumerTransfer(t, st, "STARTED")

		// An endpoint the outgoing-callback guard rejects, so the pull
		// returns before it ever builds a request. This is the exit a
		// per-site recorder is most likely to miss, because it is nowhere
		// near the copy.
		orig := validateOutgoingCallback
		validateOutgoingCallback = func(string) error { return errors.New("refused by the guard") }
		t.Cleanup(func() { validateOutgoingCallback = orig })

		h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: "http://example.invalid/x"})

		row, _, err := st.GetConsumerTransfer(pid)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if row.DataError == "" {
			t.Error("DataError is empty after a pull refused before it started")
		}
	})
}
```

`seedConsumerTransfer(t, st, state string) string` already exists at `internal/dsp/transfer_handler_test.go:1069`. It takes a **state** and **returns** the generated pid — it does not take one. Use its return value as the pid, as the tests above do; do not pass a pid into it and do not change it.

- [ ] **Step 2: Run to verify they fail**

```bash
go test ./internal/dsp/ -run TestPullRecordsItsOutcomeOnEveryPath
```

Expected: FAIL — `DataError is empty` on the failure subtests, since nothing records yet.

- [ ] **Step 3: Add the outcome value and its deferred write**

At the top of `internal/dsp/transfer_consumer_handler.go`, above `pullTransferData`:

```go
// pullOutcome is what a pull leaves behind on its transfer's row. It exists
// so there is exactly one write site: pullTransferData has sixteen failure
// exits and one success, and a recorder called at each would be sixteen
// chances to miss one with nothing to catch the miss. Every exit sets a
// field or two on this value; the deferred write in pullTransferData turns
// it into a row.
//
// The zero value is a failure with no reason, which is the right default for
// a function that can return from anywhere: an exit that forgets to say why
// still records that the pull did not finish, rather than silently leaving
// the row describing a previous attempt.
type pullOutcome struct {
	received  int64
	path      string
	completed time.Time
	failure   string
}

// fail records why a pull stopped. The sentence is the one that goes to the
// log, because DECISIONS.md section 34 records that this column holds a
// reason rather than a code and an operator reading it needs the sentence.
func (o *pullOutcome) fail(reason string) {
	o.failure = reason
}

// succeed records a published download and clears any reason a previous
// attempt left.
func (o *pullOutcome) succeed(received int64, path string, at time.Time) {
	o.received, o.path, o.completed, o.failure = received, path, at, ""
}
```

Immediately after `pullTransferData`'s in-flight guard — after the `h.pulling` block and its `defer`, and before the `addr == nil` check — insert:

```go
	outcome := pullOutcome{failure: "the pull ended without recording a reason"}
	defer func() {
		if h.store == nil {
			return
		}
		if err := h.store.RecordConsumerTransferOutcome(
			t.ConsumerPID, outcome.received, outcome.path, outcome.completed, outcome.failure,
		); err != nil {
			slog.Error("record pull outcome", "consumer_pid", t.ConsumerPID, "error", err)
		}
	}()
```

The `h.store == nil` guard is for the handful of tests that build a bare `transferHandler`; production always has a store.

- [ ] **Step 4: Set the outcome at every exit**

Go through `pullTransferData` from top to bottom. At each `return` that follows a `slog.Error` or `slog.Warn`, add an `outcome.fail(...)` carrying **the same sentence as that log line's message**, before the return. At the success path, replace nothing — add `outcome.succeed(total, final, time.Now().UTC())` immediately before the final `slog.Info`.

The sentences to use, in the order they appear:

| Log message | `outcome.fail` argument |
|---|---|
| `refuse data endpoint` | `"the data endpoint was refused by the outgoing-address guard"` |
| `create download directory` | `"the download directory could not be created"` |
| `build data pull` | `"the data pull request could not be built"` |
| `data endpoint sent no response within the idle timeout` | `"the data endpoint sent no response within the idle timeout"` |
| `data pull` (transport error) | `"the data pull failed before any bytes arrived"` |
| `206 response's Content-Range does not start where…` | `"the provider's 206 did not start where this connector's partial download left off"` |
| `the provider states a different complete length…` | `"the provider states a different complete length than this transfer recorded"` |
| `provider's file is no longer past what this connector already has` | `"the provider's file is no longer past what this connector already has"` |
| `data endpoint gave an unexpected answer to a resumed pull` | `"the data endpoint gave an unexpected answer to a resumed pull"` |
| `data endpoint refused the pull` | `"the data endpoint refused the pull"` |
| `open download file` | `"the download file could not be opened"` |
| `data pull made no progress within the idle timeout` | `"the data pull made no progress within the idle timeout"` |
| `write download` | `"the download could not be written"` |
| `data pull exceeded max_download_bytes` | `"the data pull exceeded max_download_bytes"` |
| `sync download` | `"the download could not be synced"` |
| `close download` | `"the download could not be closed"` |
| `the download does not match the length the provider stated` | `"the download does not match the length the provider stated"` |
| `place download` | `"the download could not be placed"` |

The two `remove stale partial download` logs are not exits — they sit inside branches that then return, and those returns already have their own row above. Do not add a second `fail` for them.

The in-flight guard's early return (`a pull for this transfer is already in flight`) is **before** the `outcome` declaration and must stay that way: that path is not an attempt, it is a duplicate trigger for an attempt already running, and recording it would overwrite the running attempt's row.

- [ ] **Step 5: Run to verify they pass**

```bash
go test ./internal/dsp/ -run TestPullRecordsItsOutcomeOnEveryPath -v
go test ./internal/dsp/
```

Expected: PASS, all three subtests, and the rest of the package unchanged.

- [ ] **Step 6: Mutation-check**

| Mutation | Must fail | Why it fails |
|---|---|---|
| Delete the whole `defer` block | `TestPullRecordsItsOutcomeOnEveryPath/a_success_records_the_bytes,_the_path,_and_no_failure` | Nothing writes, so `ReceivedBytes` stays 0. |
| Delete `outcome.succeed(...)` from the success path | same subtest | The zero-value failure survives, so `DataError` is non-empty and `DataCompletedAt` zero. |
| Delete the `outcome.fail(...)` on the idle-cutoff exit | `TestPullRecordsItsOutcomeOnEveryPath/an_idle_cutoff_records_the_reason_and_no_completion` | The default sentence replaces it, which does not contain "idle". |
| Delete the `outcome.fail(...)` on the guard-refusal exit | `TestPullRecordsItsOutcomeOnEveryPath/a_refusal_before_any_request_still_records` | That subtest only asserts `DataError != ""`, so it survives — **this mutation does not fail, and that is expected**: the default sentence covers it. Record it as such rather than inventing a test; the default is what makes a missed exit safe. |

The fourth row is deliberately a mutation that does not kill. Report it that way — it demonstrates the default's purpose.

- [ ] **Step 7: Commit**

```bash
git add internal/dsp/transfer_consumer_handler.go internal/dsp/transfer_consumer_handler_test.go
git commit -m "feat: record a pull's outcome from one deferred site

Sixteen failure exits and one success. A recorder at each would be sixteen
chances to miss one, with nothing to catch the miss; a deferred write from a
value each exit fills makes a missed exit structurally impossible.

The zero value is a failure with no reason, so an exit that forgets to say
why still records that the pull did not finish rather than leaving the row
describing a previous attempt."
```

---

### Task 3: Shutdown cancels pulls, and the cap becomes adequate again

Task 2 moved the outcome write to the end of the copy. `DECISIONS.md` §33.6 predicted exactly this and said the five-second cap "will be worth re-examining then". This is that re-examination, and it has an answer: the spec asked for cancellation in §1.5, Plan A did not implement it, and cancellation is what makes the cap adequate for a write that now lands at the end.

Without it: a pull mid-copy when shutdown starts keeps copying, the cap expires, the pull is abandoned, and its outcome is never written — the exact loss Task 2's column exists to prevent. With it: the pull's context is cancelled, the copy returns promptly with a known cause, the deferred recorder writes "abandoned at shutdown", and all of that fits inside the cap with room to spare.

**Files:**
- Modify: `internal/dsp/transfer_consumer_handler.go`
- Modify: `internal/dsp/transfer_handler.go`
- Modify: `internal/dsp/router.go`
- Modify: `cmd/dsbox/main.go`
- Test: `internal/dsp/transfer_consumer_handler_test.go`

**Interfaces:**
- Consumes: `pullOutcome` (Task 2).
- Produces: `dsp.NewRouter` returns `(http.Handler, *sync.WaitGroup, context.CancelFunc)` — a third value `main` calls to cancel in-flight pulls before waiting.

- [ ] **Step 1: Write the failing test**

Append to `internal/dsp/transfer_consumer_handler_test.go`:

```go
func TestCancellingPullsRecordsAnAbandonedOutcome(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	started := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("c", 500)))
		w.(http.Flusher).Flush()
		close(started)
		<-release // never sends the rest
	}))
	defer func() { close(release); provider.Close() }()

	// A generous idle timeout, so nothing but the cancellation can end this.
	h, st := newTestTransferHandler(t, config.Config{DataDir: dir, DataIdleTimeout: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	h.pullCtx = ctx
	pid := seedConsumerTransfer(t, st, "STARTED")

	done := make(chan struct{})
	go func() {
		h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: provider.URL})
		close(done)
	}()

	<-started
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the connector context did not end the pull; shutdown would have to wait out its cap")
	}

	row, _, err := st.GetConsumerTransfer(pid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(row.DataError, "shut") {
		t.Errorf("DataError = %q, want the sentence naming shutdown — an abandoned pull must say why", row.DataError)
	}
	if !row.DataCompletedAt.IsZero() {
		t.Error("DataCompletedAt is set on a pull that was abandoned")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/dsp/ -run TestCancellingPullsRecordsAnAbandonedOutcome
```

Expected: FAIL, `h.pullCtx undefined`.

- [ ] **Step 3: Give the handler a connector-lifetime context**

In `internal/dsp/transfer_handler.go`, add to `transferHandler` beside `pulls`:

```go
	// pullCtx is the connector's lifetime. A pull derives its own cancellable
	// context from this one, so shutdown can end an in-flight copy rather
	// than wait out its cap and abandon it — which, now that a pull records
	// its outcome after the copy, would lose exactly the row the wait exists
	// to protect. Nil in tests that do not exercise shutdown.
	pullCtx context.Context
```

In `internal/dsp/transfer_consumer_handler.go`, add a sentinel beside `errIdleTimeout`:

```go
// errConnectorShuttingDown is the cause a pull's context carries when the
// connector is going down. It reaches the row as the reason, so an operator
// reading a half-finished transfer sees why it stopped rather than a bare
// cancellation.
var errConnectorShuttingDown = errors.New("the connector shut down while the pull was running")
```

Replace the pull's context construction. Find:

```go
	ctx, cancel := context.WithCancelCause(context.Background())
```

and replace with:

```go
	parent := h.pullCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
```

- [ ] **Step 4: Attribute the cancellation**

In the copy's error branch, where `context.Cause(ctx)` is already checked for `errIdleTimeout`, add the shutdown case before the generic one. Find the branch that reports the idle timeout and add, immediately after it:

```go
		if errors.Is(context.Cause(ctx), context.Canceled) || parent.Err() != nil {
			outcome.fail(errConnectorShuttingDown.Error())
			slog.Warn("the connector shut down while the pull was running; leaving the partial download in place",
				"consumer_pid", t.ConsumerPID, "had_bytes", existingSize, "appended_bytes", n)
			return
		}
```

Do the same in the `Do` error branch, which already distinguishes an idle timeout — a cancellation that lands before the response arrives must read the same way.

- [ ] **Step 5: Wire it through `NewRouter` and `main`**

In `internal/dsp/router.go`, change `NewRouter` to build the context and return its cancel:

```go
func NewRouter(cfg config.Config, st *store.Store, roster auth.Roster, signKey ed25519.PrivateKey) (http.Handler, *sync.WaitGroup, context.CancelFunc) {
	pullCtx, cancelPulls := context.WithCancel(context.Background())
	...
	tr := transferHandler{cfg: cfg, store: st, stepDelay: transferStepDelay, pulling: &sync.Map{}, pulls: pulls, pullCtx: pullCtx}
	...
	return outer, pulls, cancelPulls
}
```

Update all four call sites — `cmd/dsbox/main.go`, `internal/dsp/auth_middleware_test.go`, `internal/dsp/catalog_handler_test.go`, and `internal/dsp/negotiation_handler_test.go`. Find them with:

```bash
grep -rn 'NewRouter(cfg\|NewRouter(config\|dsp.NewRouter' --include='*.go' .
```

Note the in-package callers have no `dsp.` prefix — a grep for `dsp.NewRouter` alone finds only `main.go`.

In `cmd/dsbox/main.go`, take the third value and call it before waiting:

```go
	dspHandler, pulls, cancelPulls := dsp.NewRouter(cfg, st, roster, signKey)
```

and replace the comment and wait block with:

```go
	// In-flight pulls write their outcome to the store when they finish, so
	// they have to finish before the deferred st.Close() above. Cancelling
	// first is what makes the cap below adequate: a cancelled pull stops
	// copying at once and its deferred write lands immediately, where an
	// uncancelled one would copy until the cap expired and lose the row.
	// DECISIONS.md section 34.3 has the argument.
	cancelPulls()
	pullsDone := make(chan struct{})
	go func() { pulls.Wait(); close(pullsDone) }()
	select {
	case <-pullsDone:
	case <-time.After(5 * time.Second):
		slog.Warn("shutting down with data pulls still in flight; their outcome will not be recorded")
	}
```

- [ ] **Step 6: Run everything**

```bash
go build ./... && go vet ./... && go test -race ./internal/dsp/ ./internal/config/
```

Expected: PASS.

- [ ] **Step 7: Mutation-check**

| Mutation | Must fail | Why it fails |
|---|---|---|
| Revert the parent to `context.Background()` | `TestCancellingPullsRecordsAnAbandonedOutcome` | Cancelling the connector context no longer reaches the pull, so the copy blocks until the test's 5-second guard fires. |
| Delete the shutdown branch in the copy's error handling | same test | The cause falls through to the generic write-error sentence, which does not contain "shut". |
| Delete `cancelPulls()` from `main.go` | none — **expected** | No test drives `main`'s shutdown path. Record it: this wiring is compiler-enforced only, and `TestShutdownWaitCoversAnInFlightPull` covers the wait, not the cancel. |

- [ ] **Step 8: Commit**

```bash
git add internal/dsp/ cmd/dsbox/main.go
git commit -m "feat: shutdown cancels in-flight pulls

Section 33.6 said the five-second cap would be worth re-examining once a
pull recorded its outcome at the end of the copy. It is, and the answer is
the cancellation the spec asked for and the previous milestone did not
implement.

Uncancelled, a pull caught mid-copy keeps copying until the cap expires and
is abandoned without writing — losing exactly the row the wait exists to
protect. Cancelled, it stops at once and its deferred write lands well
inside the cap. The reason reaches the row, so a half-finished transfer says
why it stopped."
```

---

### Task 4: `GET /transfers`

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/mgmt/router.go`
- Test: `internal/store/store_test.go`
- Test: `internal/mgmt/router_test.go`

**Interfaces:**
- Consumes: the four columns (Task 1).
- Produces: `func (s *Store) ListConsumerTransfers() ([]ConsumerTransfer, error)` and `func (s *Store) ListTransfers() ([]TransferProcess, error)`.

- [ ] **Step 1: Write the failing store test**

Append to `internal/store/store_test.go`:

```go
func TestListTransfersReturnsBothRolesInOrder(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	base := time.Now().UTC().Truncate(time.Second)
	if err := st.CreateConsumerTransfer(ConsumerTransfer{
		ConsumerPID: "urn:uuid:c-2", ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a",
		Format: "HttpData-PULL", State: "STARTED", CreatedAt: base.Add(time.Second), UpdatedAt: base,
	}); err != nil {
		t.Fatalf("create consumer 2: %v", err)
	}
	if err := st.CreateConsumerTransfer(ConsumerTransfer{
		ConsumerPID: "urn:uuid:c-1", ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a",
		Format: "HttpData-PULL", State: "COMPLETED", CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatalf("create consumer 1: %v", err)
	}

	consumers, err := st.ListConsumerTransfers()
	if err != nil {
		t.Fatalf("list consumer transfers: %v", err)
	}
	if len(consumers) != 2 {
		t.Fatalf("got %d consumer transfers, want 2", len(consumers))
	}
	if consumers[0].ConsumerPID != "urn:uuid:c-1" {
		t.Errorf("first is %q, want the oldest — the list is ordered by creation like ListAgreements", consumers[0].ConsumerPID)
	}

	providers, err := st.ListTransfers()
	if err != nil {
		t.Fatalf("list transfers: %v", err)
	}
	if len(providers) != 0 {
		t.Errorf("got %d provider transfers, want 0 — none were created", len(providers))
	}
}
```

- [ ] **Step 2: Run to verify it fails, then add the two list methods**

```bash
go test ./internal/store/ -run TestListTransfersReturnsBothRoles
```

Expected: FAIL, `st.ListConsumerTransfers undefined`.

Add both beside `ListAgreements`, following its shape exactly — `Query`, `defer rows.Close()`, scan into the struct, parse the timestamps, `rows.Err()` at the end. `ListConsumerTransfers` selects every column `GetConsumerTransfer` selects, ordered by `created_at`; `ListTransfers` does the same for `transfer_processes` and its own columns. Parse `data_completed_at` only when non-empty, the same way `GetConsumerTransfer` now does.

- [ ] **Step 3: Run to verify it passes**

```bash
go test ./internal/store/
```

Expected: PASS.

- [ ] **Step 4: Write the failing route test**

Append to `internal/mgmt/router_test.go`, following the file's existing helper shape for building an authenticated request:

```go
func TestListTransfersReturnsBothRolesWithARoleField(t *testing.T) {
	h, st := newTestRouter(t)
	now := time.Now().UTC()
	if err := st.CreateConsumerTransfer(store.ConsumerTransfer{
		ConsumerPID: "urn:uuid:c-1", ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a",
		Format: "HttpData-PULL", State: "COMPLETED", CreatedAt: now, UpdatedAt: now,
		ExpectedBytes: 4096, ReceivedBytes: 4096, DataPath: "/d/urn:uuid:c-1", DataCompletedAt: now,
	}); err != nil {
		t.Fatalf("seed consumer transfer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/transfers", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /transfers = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"role":"consumer"`, `"consumerPid":"urn:uuid:c-1"`, `"receivedBytes":4096`, `"dataPath":"/d/urn:uuid:c-1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %s\nbody: %s", want, body)
		}
	}
}

func TestListTransfersRequiresTheToken(t *testing.T) {
	h, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/transfers", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /transfers with no token = %d, want 401", rec.Code)
	}
}
```

`newTestRouter(t) (http.Handler, *store.Store)` and `const testToken` both already exist at the top of `internal/mgmt/router_test.go`. Use them; do not add a second way to build a router.

- [ ] **Step 5: Add the route**

In `internal/mgmt/router.go`, register beside the agreement routes:

```go
	mux.Handle("GET /transfers", authenticated(cfg.MgmtToken, http.HandlerFunc(h.listTransfers)))
```

Add the handler and its view type:

```go
// listTransfers returns every transfer this connector holds, in both roles.
// It exists for the same reason GET /agreements does — an operator otherwise
// has no way to see whether the data a transfer was for actually arrived —
// and it is read-only for the same reason. DECISIONS.md section 34.4 records
// why this does not move the boundary section 25.3 drew.
//
// Both roles, with a role field, because a route named /transfers that
// showed half the transfers would be a trap for whoever read it next.
// Provider-role rows carry no download fields: they never fetch anything.
func (h agreementHandler) listTransfers(w http.ResponseWriter, r *http.Request) {
	consumers, err := h.store.ListConsumerTransfers()
	if err != nil {
		slog.Error("list consumer transfers", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	providers, err := h.store.ListTransfers()
	if err != nil {
		slog.Error("list transfers", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	out := make([]transferView, 0, len(consumers)+len(providers))
	for _, c := range consumers {
		v := transferView{
			Role: "consumer", ConsumerPID: c.ConsumerPID, ProviderPID: c.ProviderPID,
			AgreementID: c.AgreementID, State: c.State, CounterpartyID: c.CounterpartyID,
			CreatedAt:     c.CreatedAt.UTC().Format(time.RFC3339Nano),
			ExpectedBytes: c.ExpectedBytes, ReceivedBytes: c.ReceivedBytes,
			DataPath:      c.DataPath, DataError: c.DataError,
		}
		if !c.DataCompletedAt.IsZero() {
			v.DataCompletedAt = c.DataCompletedAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, v)
	}
	for _, p := range providers {
		out = append(out, transferView{
			Role: "provider", ConsumerPID: p.ConsumerPID, ProviderPID: p.ProviderPID,
			AgreementID: p.AgreementID, State: p.State, CounterpartyID: p.CounterpartyID,
			CreatedAt:   p.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"transfers": out}); err != nil {
		slog.Error("encode transfers", "error", err)
	}
}

// transferView is the wire shape, kept separate from the store structs for
// the reason agreementView records: the management API does not leak
// whichever columns storage happens to carry. The download fields are
// omitted when empty so a provider-role row does not carry four blanks that
// mean nothing for it.
type transferView struct {
	Role            string `json:"role"`
	ConsumerPID     string `json:"consumerPid"`
	ProviderPID     string `json:"providerPid"`
	AgreementID     string `json:"agreementId"`
	State           string `json:"state"`
	CounterpartyID  string `json:"counterpartyId"`
	CreatedAt       string `json:"createdAt"`
	ExpectedBytes   int64  `json:"expectedBytes,omitempty"`
	ReceivedBytes   int64  `json:"receivedBytes,omitempty"`
	DataPath        string `json:"dataPath,omitempty"`
	DataCompletedAt string `json:"dataCompletedAt,omitempty"`
	DataError       string `json:"dataError,omitempty"`
}
```

- [ ] **Step 6: Run to verify they pass**

```bash
go test ./internal/mgmt/ ./internal/store/ && go build ./...
```

Expected: PASS.

- [ ] **Step 7: Mutation-check**

| Mutation | Must fail | Why it fails |
|---|---|---|
| Drop `authenticated(cfg.MgmtToken, …)` from the route registration | `TestListTransfersRequiresTheToken` | The unauthenticated request would get 200 instead of 401. |
| Return only the consumer rows | `TestListTransfersReturnsBothRolesWithARoleField` | That test seeds only a consumer row, so it survives — **expected**; note it and add a provider row to the test so the mutation does kill. Do that rather than leaving the gap. |
| Remove the `Role` field from the view | `TestListTransfersReturnsBothRolesWithARoleField` | The body no longer contains `"role":"consumer"`. |

- [ ] **Step 8: Commit**

```bash
git add internal/mgmt/ internal/store/
git commit -m "feat: GET /transfers

An operator could see what a negotiation concluded and not whether the data
it was for arrived. This is the same principle GET /agreements applies, a
second time: read-only, unpaginated, behind the same token.

Both roles with a role field, because a route named /transfers that showed
half the transfers would be a trap for whoever read it next."
```

---

### Task 5: The provider's audit line, and the documents

**Files:**
- Modify: `internal/dsp/data_handler.go`
- Modify: `DECISIONS.md`
- Modify: `README.md`
- Test: `internal/dsp/data_handler_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: nothing. This is the last task.

- [ ] **Step 1: Write the failing test**

`handleData` logs on every error branch and none of the success ones. Append to `internal/dsp/data_handler_test.go`, using whatever log-capture helper the package already has; if there is none, capture with `slog.SetDefault` around the call and restore in a `t.Cleanup`:

```go
func TestDataPullLogsWhoCollectedTheData(t *testing.T) {
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	h, id := dataFixture(t, TransferStarted, testPeer, true)
	rec := pullAs(t, h, id, testPeer)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull = %d, want 200", rec.Code)
	}

	line := buf.String()
	if !strings.Contains(line, "served transfer data") {
		t.Fatalf("the success path logged nothing; a provider that cannot say who took its data has the wrong half of section 27's identity\nlog: %s", line)
	}
	for _, want := range []string{testPeer, id} {
		if !strings.Contains(line, want) {
			t.Errorf("the audit line does not name %q\nlog: %s", want, line)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/dsp/ -run TestDataPullLogsWhoCollectedTheData
```

Expected: FAIL, "the success path logged nothing".

- [ ] **Step 3: Add the line**

In `handleData`, after each of the two streaming copies returns without error, log the identity and the bytes. The issuer is already on the request context and already used to refuse the wrong caller; the variable holding it is in scope at both sites. For the plain-200 path:

```go
	slog.Info("served transfer data",
		"issuer", issuerFrom(r), "provider_pid", providerPID, "dataset_id", ds.ID, "bytes", n)
```

and for the 206 path the same line with `"range_start", rangeStart` added. Capture the byte count `copyUnderRollingDeadline` already returns rather than discarding it.

A log line rather than a row: the provider has no per-download state to keep, and a table here would be an audit store, which is a larger decision than this milestone should make on its way past.

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/dsp/ -run TestDataPullLogsWhoCollectedTheData -v && go test ./internal/dsp/
```

Expected: PASS.

- [ ] **Step 5: Mutation-check**

| Mutation | Must fail | Why it fails |
|---|---|---|
| Delete the plain-200 success log | `TestDataPullLogsWhoCollectedTheData` | The fixture serves a full file, so that is the path it takes and the buffer holds no `served transfer data`. |
| Log without the issuer | same test | The test asserts the line names `testPeer`. |

- [ ] **Step 6: Write `DECISIONS.md` §34**

Read §32 and §33 first for the shape — **Decision**, numbered sub-sections, **Trade-off accepted**. Record:

- **34.1** The four columns, written together from one deferred site, and why: sixteen failure exits mean a per-site recorder is sixteen chances to miss one. The zero value is a failure with no reason, so a missed exit still records that the pull did not finish.
- **34.2** `data_error` holds a sentence, not a code, and it is the same sentence the log carries.
- **34.3** Shutdown cancels pulls. State plainly that §33.6 predicted this re-examination and that the answer is the cancellation spec §1.5 asked for and the previous milestone deviated from. Say what it buys: a cancelled pull stops at once and its deferred write lands inside the cap, where an uncancelled one copies until the cap expires and loses the row the wait exists to protect. **Check before writing:** confirm `cancelPulls()` is called before `pulls.Wait()` in `cmd/dsbox/main.go`, and that the cap is still five seconds.
- **34.4** `GET /transfers`, read-only, both roles, behind the token. Carry across the sentence spec §4 wrote about itself: this argument has no stated stopping point, this is its second use, and whoever adds a third should be made to say why the management API is still small.
- **34.5** The provider's audit line, and why a line rather than a table.
- **Trade-off accepted.** At least: a five-second cap can still lose an outcome if a cancelled pull's own cleanup outruns it; `GET /transfers` is unpaginated; the audit line is a log rather than a queryable record, so it is subject to whatever retention the operator's log stack has.

Then **update §33.6's closing paragraph**, which says the cap "will be worth re-examining then" — it has been, and §34.3 is where. Do not delete the paragraph; make it point forward correctly.

- [ ] **Step 7: Update `README.md`**

The Status section says the connector "moves data" and describes what it does not close. Add what this milestone changed, and check each sentence against the code before writing it:

- An operator can now ask whether data arrived (`GET /transfers`) — **check** the route is registered behind `authenticated` in `internal/mgmt/router.go`.
- A failed pull and a successful one are distinguishable, and the failure carries its reason — **check** `data_error` and `data_completed_at` are written together in `RecordConsumerTransferOutcome`.
- The provider records who collected data — **check** the log line exists on both streaming paths.
- What is still open: `docs/goal-gap-analysis.md` P2's operator-retry endpoint and the orphaned-partial retention rule, neither of which this milestone closes.

- [ ] **Step 8: Run every gate**

```bash
go build ./... && go vet ./... && go test -race ./...
make tck
make demo
```

Expected: unit tests pass; `make tck` reports **65 of 65**; `make demo` moves its file, diffs it, and its resume round still logs `resumed transfer data pull`.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "feat: the provider says who collected its data, and section 34 records the milestone

Section 27 obtained a verified identity and the success path did not record
it, so this connector could say who it turned away and not who it served.

Section 34 records the four columns and their single deferred write site,
the sentence-not-a-code rule for data_error, the shutdown cancellation that
section 33.6 asked to be re-examined, GET /transfers and the boundary
argument it is the second use of, and this line."
```

---

## Self-review

**Spec coverage.** §3's four remaining columns → Task 1; its "new store method records the outcome" → Task 1 Step 6, with Task 2 supplying the single call site. §3's "a failed pull and a successful one stop being the same row" → Task 1's test asserts the two cannot both hold. §4 → Task 4, including the role field, the auth, and the view type kept separate from the store structs. §5 → Task 5. §1.5's connector-lifetime context, deviated from in Plan A → Task 3, which is also §33.6's promised re-examination.

**Placeholder scan.** No "TBD", no "add appropriate error handling". Three places deliberately hand the implementer judgement rather than text: Task 4 Step 2's list methods ("following `ListAgreements`'s shape exactly", with the deviations named), Task 4 Step 4's test helpers ("read the file first and mirror it"), and Task 5 Step 6's §34 prose. Each names what to read and what to check; a pasted draft of §34 would read as foreign beside §32 and §33.

**Type consistency.** `RecordConsumerTransferOutcome(consumerPID string, received int64, path string, completedAt time.Time, failure string) error` is declared in Task 1 and called in Task 2. `pullOutcome`'s `fail`/`succeed` are declared and used within Task 2, then extended in Task 3. `NewRouter`'s new three-value signature is introduced in Task 3 and its four call sites named there; Task 4 does not touch it. `transferView`'s JSON tags in Task 4 match the strings Task 4's own test asserts.

**Two mutations in this plan are expected not to kill, and both say so.** Task 2's fourth row demonstrates that the zero-value default covers a missed exit; Task 3's third row records that `main`'s shutdown path has no test driving it. Stating a mutation that does not kill is the point — Plan A prescribed four that silently did not, and each was a defect in the plan.
