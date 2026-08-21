# Transfer Range and Resumption Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The provider's data endpoint supports a single-form HTTP `Range` request and the consumer resumes an interrupted pull from where it left off, instead of refetching from byte zero — verified against a real interrupted transfer in `make demo`, not a mock.

**Architecture:** `data_handler.go` gains `Range: bytes=N-` parsing with `206`/`416` responses; `transfer_consumer_handler.go`'s `pullTransferData` gains a deterministic partial-file path it can find and resume across a restart, guarded against two concurrent pulls for the same transfer; `config.Dataset` gains a dataset-keyed autonomous transfer-sequence fallback (closing a gap `DECISIONS.md` §25.7 already named) and a demo-only fault-injection field; `demo/run.sh` gains a second, dedicated negotiate-and-transfer round that deliberately interrupts a real pull and proves the recovered file is both byte-correct and actually resumed.

**Tech Stack:** Go standard library only (`net/http`, `io`, `os`, `strconv`, `sync`). No new dependency.

**Spec:** `docs/superpowers/specs/2026-08-21-transfer-range-resumption-design.md` — read it alongside this plan; the plan argues from it and does not repeat its reasoning in full.

## Global Constraints

- Go standard library only. Ask before adding a dependency — the default answer is the standard library (`CLAUDE.md`).
- Mutation-test every new conditional this plan adds: temporarily break it, confirm the specific test that should catch the break actually fails, then restore. Do this inline in each task, not as a separate pass.
- Any new concurrency-safety claim (Task 6) is verified with `go test -race`, not only `go test`.
- The TCK gate must remain 65 of 65, 0 exemptions, unaffected — confirmed with `make tck` in Task 9. Nothing in this plan touches a TCK-gated route's request-handling for an unranged request; only new behavior is added.
- English for all code, comments, and documentation, including everything under `docs/` (`CLAUDE.md`).
- No `Co-Authored-By` trailer on commits — this project carries no organizational affiliation.
- `404` is never emitted by a DSP protocol endpoint for anything but "this `{id}` names nothing" (`DECISIONS.md` §25.1) — the new `416` response is not a `404` and does not change this rule.

---

## Task 1: Config schema — `TransferSequence` and `SimulateInterruptAfterBytes`

**Files:**
- Modify: `internal/config/config.go:133-171` (the `Dataset` struct) and its validation function (the `Load`/validate body around `internal/config/config.go:342-413`)
- Modify: `internal/config/config_test.go`
- Modify: `config.example.yaml`

**Interfaces:**
- Produces: `config.Dataset.TransferSequence []string` (`yaml:"transfer_sequence"`), `config.Dataset.SimulateInterruptAfterBytes int64` (`yaml:"simulate_interrupt_after_bytes"`). Task 2 reads `TransferSequence`; Task 4 reads `SimulateInterruptAfterBytes`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/config/config_test.go` (near the existing `TerminateOnVerify` tests — search the file for `TerminateOnVerify` to place these beside them):

```go
func TestDatasetTransferSequenceRejectsAnUnknownState(t *testing.T) {
	data := []byte(
		"public_url: https://connector.example.org\n" +
			"dev_mode: true\n" +
			"require_auth: false\n" +
			"participant_id: urn:participant:example\n" +
			"data_dir: ./data\n" +
			"datasets:\n" +
			"  - id: urn:dataset:a\n" +
			"    transfer_sequence: [BOGUS]\n")
	if _, err := Load(data, func(string) string { return "" }); err == nil {
		t.Fatal("Load: expected an error for an unknown transfer_sequence state")
	}
}

func TestDatasetTransferSequenceAcceptsAKnownState(t *testing.T) {
	data := []byte(
		"public_url: https://connector.example.org\n" +
			"dev_mode: true\n" +
			"require_auth: false\n" +
			"participant_id: urn:participant:example\n" +
			"data_dir: ./data\n" +
			"datasets:\n" +
			"  - id: urn:dataset:a\n" +
			"    transfer_sequence: [STARTED, SUSPENDED, STARTED, COMPLETED]\n")
	cfg, err := Load(data, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"STARTED", "SUSPENDED", "STARTED", "COMPLETED"}
	got := cfg.Datasets[0].TransferSequence
	if len(got) != len(want) {
		t.Fatalf("TransferSequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("TransferSequence = %v, want %v", got, want)
		}
	}
}

func TestDatasetSimulateInterruptAfterBytesRejectsNegative(t *testing.T) {
	data := []byte(
		"public_url: https://connector.example.org\n" +
			"dev_mode: true\n" +
			"require_auth: false\n" +
			"participant_id: urn:participant:example\n" +
			"data_dir: ./data\n" +
			"datasets:\n" +
			"  - id: urn:dataset:a\n" +
			"    simulate_interrupt_after_bytes: -1\n")
	if _, err := Load(data, func(string) string { return "" }); err == nil {
		t.Fatal("Load: expected an error for a negative simulate_interrupt_after_bytes")
	}
}

func TestDatasetSimulateInterruptAfterBytesAcceptsAPositiveValue(t *testing.T) {
	data := []byte(
		"public_url: https://connector.example.org\n" +
			"dev_mode: true\n" +
			"require_auth: false\n" +
			"participant_id: urn:participant:example\n" +
			"data_dir: ./data\n" +
			"datasets:\n" +
			"  - id: urn:dataset:a\n" +
			"    simulate_interrupt_after_bytes: 2000\n")
	cfg, err := Load(data, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Datasets[0].SimulateInterruptAfterBytes != 2000 {
		t.Errorf("SimulateInterruptAfterBytes = %d, want 2000", cfg.Datasets[0].SimulateInterruptAfterBytes)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/... -run 'TestDatasetTransferSequence|TestDatasetSimulateInterruptAfterBytes' -v`
Expected: FAIL — `cfg.Datasets[0].TransferSequence` and `.SimulateInterruptAfterBytes` do not exist yet (compile error), and the two "rejects" tests currently succeed loading a document with an unrecognized field, since YAML ignores unknown keys by default.

- [ ] **Step 3: Add the two fields to `Dataset`**

In `internal/config/config.go`, immediately after the existing `TerminateOnVerify bool` field (the last field in the `Dataset` struct, `internal/config/config.go:170`, right before its closing `}`):

```go
	// TransferSequence configures this dataset's provider-role autonomous
	// transfer behavior when no transfer_policies entry names the specific
	// agreement — the fallback DECISIONS.md section 25.7 said did not exist:
	// an agreement's dataset_id, unlike its own id, is known before it is
	// negotiated. Same shape and same legality rules as
	// TransferPolicy.Sequence: nil means no fallback (every dataset's
	// behavior before this field existed), an agreement_id-keyed entry in
	// transfer_policies always wins when both match, and an explicit empty
	// sequence means accept and stay in REQUESTED, distinct from nil.
	TransferSequence []string `yaml:"transfer_sequence"`

	// SimulateInterruptAfterBytes truncates a non-Range data pull at this
	// many bytes and severs the connection, for exercising resumption
	// against a real interrupted transfer rather than a mocked one. Zero
	// (default) disables it. A real deployment has no reason to configure
	// this — the same "test affordance" category TerminateOnVerify and
	// transfer_policies are already in. A request that carries Range is
	// never truncated, regardless of this value.
	SimulateInterruptAfterBytes int64 `yaml:"simulate_interrupt_after_bytes"`
```

- [ ] **Step 4: Add validation**

In `internal/config/config.go`, find the existing block that validates `c.TransferPolicies` against `validTransferState` (it starts with the comment `// The state names are literals rather than the dsp package's constants` and ends right before the `validAfterState` comment for `c.ConsumerTransferPolicies`). Immediately after that `for i, p := range c.TransferPolicies { ... }` loop closes, insert:

```go
	for i, d := range c.Datasets {
		if d.SimulateInterruptAfterBytes < 0 {
			return fmt.Errorf("datasets[%d]: simulate_interrupt_after_bytes must not be negative", i)
		}
		for j, s := range d.TransferSequence {
			if !validTransferState[s] {
				return fmt.Errorf("datasets[%d]: transfer_sequence[%d] %q is not one of STARTED, SUSPENDED, COMPLETED, TERMINATED", i, j, s)
			}
		}
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/config/... -v -run 'TestDatasetTransferSequence|TestDatasetSimulateInterruptAfterBytes'`
Expected: PASS, all four.

- [ ] **Step 6: Mutation-test the validation**

Temporarily change `d.SimulateInterruptAfterBytes < 0` to `d.SimulateInterruptAfterBytes < -1` and re-run `TestDatasetSimulateInterruptAfterBytesRejectsNegative` — it must now FAIL (a value of `-1` is no longer rejected). Restore the line. Temporarily change `!validTransferState[s]` to `false` and re-run `TestDatasetTransferSequenceRejectsAnUnknownState` — it must now FAIL. Restore the line.

- [ ] **Step 7: Run the full config test suite**

Run: `go test ./internal/config/...`
Expected: PASS, no regressions.

- [ ] **Step 8: Document both fields in `config.example.yaml`**

In `config.example.yaml`, immediately after the existing `# terminate_on_verify: false` line and its preceding comment block (inside the `datasets:` list's first entry, right before the entry's own closing and the top-level `# Optional. Configures this connector's autonomous behavior when it is negotiating as consumer` comment for `consumer_policies`), add:

```yaml
    # Optional. Keyed by this dataset, not by an agreement — the fallback
    # for when no transfer_policies entry below names the specific
    # agreement, because a negotiated agreement's id does not exist until
    # the negotiation that produces it is already under way and so cannot
    # be written into this file ahead of time (DECISIONS.md section 25.7).
    # Same rules as transfer_policies' own sequence: an agreement_id match
    # there always wins over this; an explicit empty list here means accept
    # and stay in REQUESTED, a different thing from leaving this commented
    # out. Uncomment to try it:
    # transfer_sequence: [STARTED, SUSPENDED, STARTED, COMPLETED]

    # Optional. Not a policy shape either — a demo/test affordance that
    # truncates a non-Range pull at this many bytes and severs the
    # connection, so make demo can prove resumption against a real
    # interrupted transfer. A request carrying Range is never truncated. A
    # real deployment has no reason to set this. Uncomment to try it:
    # simulate_interrupt_after_bytes: 2000
```

- [ ] **Step 9: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go config.example.yaml
git commit -m "feat: add dataset-keyed TransferSequence and SimulateInterruptAfterBytes config"
```

---

## Task 2: `resolveTransferSequence` gains a dataset-keyed fallback

**Files:**
- Modify: `internal/dsp/transfer_handler.go:186-193` (`resolveTransferSequence`) and `:235-236` (`driveTransfer`'s call site)
- Modify: `internal/dsp/transfer_handler_test.go:577,588,608,636,639` (four existing call sites) and add new tests near them

**Interfaces:**
- Consumes: `config.Dataset.TransferSequence` (Task 1).
- Produces: `resolveTransferSequence(cfg config.Config, agreementID, datasetID string) []string` — the new three-argument signature every caller and every test must use from this task onward. `hasSourceFor`'s existing `h.store.GetAgreement(agreementID)` lookup pattern (`internal/dsp/transfer_handler.go:615-626`) is the one `driveTransfer` now repeats to obtain `datasetID`.

- [ ] **Step 1: Update the four existing call sites to the (soon-to-exist) three-argument form**

In `internal/dsp/transfer_handler_test.go`:

Line 577, inside `TestResolveTransferSequenceDefaultsToStarted`:
```go
	got := resolveTransferSequence(config.Config{}, "urn:uuid:unconfigured", "")
```

Line 588, inside `TestResolveTransferSequenceUsesTheMatchingEntry`:
```go
	got := resolveTransferSequence(cfg, "urn:uuid:agreement-1", "")
```

Line 608, inside `TestResolveTransferSequenceEmptyEntryIsNotTheDefault`:
```go
	if got := resolveTransferSequence(cfg, "urn:uuid:agreement-1", ""); len(got) != 0 {
```

Lines 636 and 639, inside `TestResolveTransferSequenceFromALoadedConfig`:
```go
	if got := resolveTransferSequence(cfg, "urn:uuid:agreement-1", ""); len(got) != 0 {
		t.Errorf("resolveTransferSequence(loaded empty sequence) = %v, want no steps at all", got)
	}
	if got := resolveTransferSequence(cfg, "urn:uuid:unconfigured", ""); len(got) != 1 || got[0] != TransferStarted {
		t.Errorf("resolveTransferSequence(agreement absent from the same document) = %v, want [%s]",
			got, TransferStarted)
	}
```

- [ ] **Step 2: Write the new failing tests for the dataset fallback**

Add to `internal/dsp/transfer_handler_test.go`, near the tests just updated:

```go
// TestResolveTransferSequenceFallsBackToTheDataset pins the fallback this
// milestone adds: with no agreement_id match, a dataset whose TransferSequence
// is set drives the sequence instead of the [STARTED] default.
func TestResolveTransferSequenceFallsBackToTheDataset(t *testing.T) {
	cfg := config.Config{Datasets: []config.Dataset{
		{ID: "urn:dataset:a", TransferSequence: []string{TransferStarted, TransferSuspended, TransferStarted, TransferCompleted}},
	}}
	got := resolveTransferSequence(cfg, "urn:uuid:unconfigured", "urn:dataset:a")
	want := []string{TransferStarted, TransferSuspended, TransferStarted, TransferCompleted}
	if len(got) != len(want) {
		t.Fatalf("resolveTransferSequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("resolveTransferSequence = %v, want %v", got, want)
		}
	}
}

// TestResolveTransferSequenceAgreementEntryWinsOverDatasetFallback pins the
// precedence: an agreement_id match in transfer_policies always overrides
// the dataset's own fallback, even when both are configured for the same
// transfer.
func TestResolveTransferSequenceAgreementEntryWinsOverDatasetFallback(t *testing.T) {
	cfg := config.Config{
		TransferPolicies: []config.TransferPolicy{
			{AgreementID: "urn:uuid:agreement-1", Sequence: []string{TransferTerminated}},
		},
		Datasets: []config.Dataset{
			{ID: "urn:dataset:a", TransferSequence: []string{TransferCompleted}},
		},
	}
	got := resolveTransferSequence(cfg, "urn:uuid:agreement-1", "urn:dataset:a")
	if len(got) != 1 || got[0] != TransferTerminated {
		t.Errorf("resolveTransferSequence = %v, want [%s] (the agreement_id entry, not the dataset fallback)", got, TransferTerminated)
	}
}

// TestResolveTransferSequenceNilDatasetFallbackStillDefaults pins that a
// dataset with no TransferSequence configured (the field's zero value, nil)
// is not distinguishable from "no dataset matched" — both still default to
// [STARTED], the same way an unconfigured agreement always has.
func TestResolveTransferSequenceNilDatasetFallbackStillDefaults(t *testing.T) {
	cfg := config.Config{Datasets: []config.Dataset{{ID: "urn:dataset:a"}}}
	got := resolveTransferSequence(cfg, "urn:uuid:unconfigured", "urn:dataset:a")
	if len(got) != 1 || got[0] != TransferStarted {
		t.Errorf("resolveTransferSequence = %v, want [%s]", got, TransferStarted)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run TestResolveTransferSequence -v`
Expected: FAIL to compile — `resolveTransferSequence` still takes two arguments.

- [ ] **Step 4: Implement the new signature and fallback**

Replace the whole function in `internal/dsp/transfer_handler.go:186-193`:

```go
// resolveTransferSequence resolves the autonomous sequence a provider-role
// transfer's agreement drives to. An agreement_id-keyed entry in
// cfg.TransferPolicies always wins when present. Failing that, datasetID's
// own TransferSequence is used if non-nil — the fallback DECISIONS.md
// section 25.7 named as missing: transfer_policies cannot be keyed by an
// agreement this connector negotiated itself, because that id does not
// exist until the negotiation that produces it is already under way, but
// the agreement's dataset_id is known regardless of how the agreement came
// to be (see hasSourceFor, which resolves it the same way). Neither
// matching leaves [STARTED], today's default.
func resolveTransferSequence(cfg config.Config, agreementID, datasetID string) []string {
	for _, p := range cfg.TransferPolicies {
		if p.AgreementID == agreementID {
			return p.Sequence
		}
	}
	for _, d := range cfg.Datasets {
		if d.ID == datasetID && d.TransferSequence != nil {
			return d.TransferSequence
		}
	}
	return []string{TransferStarted}
}
```

- [ ] **Step 5: Update `driveTransfer`'s call site**

In `internal/dsp/transfer_handler.go`, `driveTransfer` currently opens:

```go
func (h transferHandler) driveTransfer(t store.TransferProcess) {
	for _, state := range resolveTransferSequence(h.cfg, t.AgreementID) {
```

Change to:

```go
func (h transferHandler) driveTransfer(t store.TransferProcess) {
	datasetID := ""
	if a, ok, err := h.store.GetAgreement(t.AgreementID); err != nil {
		slog.Error("get agreement for transfer sequence", "agreement_id", t.AgreementID, "error", err)
	} else if ok {
		datasetID = a.DatasetID
	}
	for _, state := range resolveTransferSequence(h.cfg, t.AgreementID, datasetID) {
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/dsp/... -run TestResolveTransferSequence -v`
Expected: PASS, all seven (four updated, three new).

- [ ] **Step 7: Mutation-test the fallback and the precedence**

Temporarily delete the second `for _, d := range cfg.Datasets { ... }` loop entirely (leaving only the `TransferPolicies` loop and the final `return`) and re-run `TestResolveTransferSequenceFallsBackToTheDataset` — it must FAIL. Restore. Temporarily swap the two loops' order (dataset loop first, agreement loop second) and re-run `TestResolveTransferSequenceAgreementEntryWinsOverDatasetFallback` — it must FAIL (the dataset's `[COMPLETED]` would win instead of the agreement's `[TERMINATED]`). Restore.

- [ ] **Step 8: Run the whole package's tests**

Run: `go test ./internal/dsp/...`
Expected: PASS. (`driveTransfer`'s existing tests construct `store.TransferProcess` rows whose `AgreementID` may not resolve via `GetAgreement` in every fixture — that is fine: a not-found agreement leaves `datasetID` at `""`, which cannot match any real dataset id, so behavior for every existing test is unchanged.)

- [ ] **Step 9: Commit**

```bash
git add internal/dsp/transfer_handler.go internal/dsp/transfer_handler_test.go
git commit -m "feat: resolveTransferSequence falls back to a dataset-keyed sequence"
```

---

## Task 3: Provider `Range` support — `206`/`416`

**Files:**
- Modify: `internal/dsp/data_handler.go`
- Modify: `internal/dsp/data_handler_test.go`

**Interfaces:**
- Produces: `parseRangeStart(header string) (int64, bool)`, a pure function. `handleData` now serves `206 Partial Content` for a valid `bytes=N-` request and `416 Range Not Satisfiable` for one starting at or past the file's current size. Task 4 extends the same code region (the "no Range" branch) with the interrupt-simulation knob.

- [ ] **Step 1: Write the failing tests**

Add to `internal/dsp/data_handler_test.go`:

```go
func TestParseRangeStart(t *testing.T) {
	cases := []struct {
		header string
		want   int64
		wantOK bool
	}{
		{"", 0, false},
		{"bytes=0-", 0, true},
		{"bytes=42-", 42, true},
		{"bytes=-5", 0, false},     // the excluded suffix form
		{"bytes=5-10", 0, false},   // the excluded closed form
		{"bytes=0-10,20-30", 0, false}, // the excluded multi-range form
		{"bytes=abc-", 0, false},
		{"bytes=-1-", 0, false},
		{"not-bytes=5-", 0, false},
	}
	for _, c := range cases {
		got, gotOK := parseRangeStart(c.header)
		if got != c.want || gotOK != c.wantOK {
			t.Errorf("parseRangeStart(%q) = (%d, %v), want (%d, %v)", c.header, got, gotOK, c.want, c.wantOK)
		}
	}
}

// TestDataPullServesAPartialRange pins the 206 path: a valid open-ended
// range seeks and streams only the requested suffix, with a correct
// Content-Range.
func TestDataPullServesAPartialRange(t *testing.T) {
	h, id := dataFixture(t, TransferStarted, testPeer, true)
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	req.Header.Set("Range", "bytes=3-")
	rec := httptest.NewRecorder()
	h.handleData(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("got %d, want 206: %s", rec.Code, rec.Body)
	}
	want := servedBytes[3:]
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	wantRange := fmt.Sprintf("bytes 3-%d/%d", len(servedBytes)-1, len(servedBytes))
	if got := rec.Header().Get("Content-Range"); got != wantRange {
		t.Errorf("Content-Range = %q, want %q", got, wantRange)
	}
}

// TestDataPullRangeAtOrPastTheEndIs416 pins the integrity check the spec's
// "Integrity across a resume" section is built on: a range that starts at or
// after the file's current size is refused, not silently served empty or
// served from zero.
func TestDataPullRangeAtOrPastTheEndIs416(t *testing.T) {
	h, id := dataFixture(t, TransferStarted, testPeer, true)
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", len(servedBytes)))
	rec := httptest.NewRecorder()
	h.handleData(rec, req)

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("got %d, want 416: %s", rec.Code, rec.Body)
	}
	wantRange := fmt.Sprintf("bytes */%d", len(servedBytes))
	if got := rec.Header().Get("Content-Range"); got != wantRange {
		t.Errorf("Content-Range = %q, want %q", got, wantRange)
	}
	if rec.Body.String() == servedBytes {
		t.Error("served the file anyway")
	}
}

// TestDataPullUnsupportedRangeFormIsIgnored pins RFC 7233's own guidance for
// a range form this connector does not implement: ignore it and serve the
// whole thing, exactly as if no Range header had been sent at all.
func TestDataPullUnsupportedRangeFormIsIgnored(t *testing.T) {
	h, id := dataFixture(t, TransferStarted, testPeer, true)
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	req.Header.Set("Range", "bytes=0-5")
	rec := httptest.NewRecorder()
	h.handleData(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (the unsupported closed form is ignored): %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != servedBytes {
		t.Errorf("body = %q, want the whole file %q", got, servedBytes)
	}
}
```

Add `"context"` and `"fmt"` to that test file's imports if not already present (`context` already is; add `"fmt"`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run 'TestParseRangeStart|TestDataPullServesAPartialRange|TestDataPullRangeAtOrPastTheEndIs416|TestDataPullUnsupportedRangeFormIsIgnored' -v`
Expected: FAIL to compile — `parseRangeStart` does not exist yet.

- [ ] **Step 3: Implement `parseRangeStart` and the `Range`-handling branch**

In `internal/dsp/data_handler.go`, add `"fmt"` and `"strconv"` to the imports, then add this function (near the top of the file, after the `dataHandler` struct or just above `handleData`):

```go
// parseRangeStart reads this connector's one supported Range form,
// "bytes=N-": a single open-ended offset. Anything else — absent,
// unparseable, a closed or multi-range form, or the "bytes=-N" suffix form —
// is reported as absent, which is RFC 7233's own guidance for a range a
// server does not support: ignore it and serve the whole thing.
func parseRangeStart(header string) (int64, bool) {
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(header, prefix)
	if !strings.HasSuffix(rest, "-") {
		return 0, false
	}
	rest = strings.TrimSuffix(rest, "-")
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
```

This also needs `"strings"` imported in `data_handler.go` — add it alongside `"strconv"` and `"fmt"`.

Now replace the tail of `handleData` — everything from `f, err := os.Open(ds.SourceFile)` through the end of the function — with:

```go
	f, err := os.Open(ds.SourceFile)
	if err != nil {
		// Validated at load, so reaching here means it moved underneath the
		// connector while running.
		slog.Error("open source_file", "path", ds.SourceFile, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		slog.Error("stat source_file", "path", ds.SourceFile, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if rangeStart, hasRange := parseRangeStart(r.Header.Get("Range")); hasRange {
		if rangeStart >= stat.Size() {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", stat.Size()))
			writeError(w, TransferErrorType, http.StatusRequestedRangeNotSatisfiable,
				"the requested range starts at or after the end of this dataset's current content")
			return
		}
		if _, err := f.Seek(rangeStart, io.SeekStart); err != nil {
			slog.Error("seek source_file for range request", "path", ds.SourceFile, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rangeStart, stat.Size()-1, stat.Size()))
		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size()-rangeStart, 10))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusPartialContent)
		if _, err := io.Copy(w, f); err != nil {
			slog.Error("stream data", "provider_pid", providerPID, "error", err)
		}
		return
	}

	// Streamed rather than buffered: memory must not scale with file size.
	// The server's write timeout still bounds how large a file can finish.
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, f); err != nil {
		slog.Error("stream data", "provider_pid", providerPID, "error", err)
	}
}
```

(Note: the "Streamed rather than buffered" comment moves down from its old position above the old, single `io.Copy` call — it now documents the unranged, un-truncated path specifically, since Task 4 inserts the interrupt-simulation branch between the `Range` branch above and this final one.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/... -run 'TestParseRangeStart|TestDataPull' -v`
Expected: PASS, including every pre-existing `TestDataPull*` test — the unranged path is byte-for-byte what it was before.

- [ ] **Step 5: Mutation-test the 416 boundary**

Temporarily change `rangeStart >= stat.Size()` to `rangeStart > stat.Size()` and re-run `TestDataPullRangeAtOrPastTheEndIs416` — it must FAIL (a range starting exactly at the file's size would now incorrectly return 206 with zero bytes instead of 416). Restore.

- [ ] **Step 6: Run the whole package's tests**

Run: `go test ./internal/dsp/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/dsp/data_handler.go internal/dsp/data_handler_test.go
git commit -m "feat: provider serves 206/416 for a supported Range request"
```

---

## Task 4: Provider demo-only interrupt simulation

**Files:**
- Modify: `internal/dsp/data_handler.go` (continues Task 3's `handleData`)
- Modify: `internal/dsp/data_handler_test.go`

**Interfaces:**
- Consumes: `config.Dataset.SimulateInterruptAfterBytes` (Task 1).
- Produces: no new exported behavior — `handleData` truncates and hijacks the connection when configured and the request carries no `Range`.

- [ ] **Step 1: Write the failing test**

This needs a real network connection — `httptest.NewRecorder()` does not implement `http.Hijacker`, so this test spins up `httptest.NewServer` directly around `handleData`, the same pattern `TestConsumerPullsWhenTheStartCarriesAnAddress` already uses for the *other* side of a pull. Add to `internal/dsp/data_handler_test.go`:

```go
// TestDataPullSimulatedInterruptTruncatesAndSeversTheConnection pins the
// demo-only fault-injection knob: a non-Range request is cut short at the
// configured byte count and the connection is severed, not cleanly closed —
// the client must see a real error, the same as it would against a genuine
// network interruption.
func TestDataPullSimulatedInterruptTruncatesAndSeversTheConnection(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	content := strings.Repeat("x", 100)
	path := filepath.Join(t.TempDir(), "a.csv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ds := config.Dataset{ID: "urn:dataset:a", SourceFile: path, SimulateInterruptAfterBytes: 20}
	cfg := config.Config{ParticipantID: testSelf, Datasets: []config.Dataset{ds}}
	now := time.Now()
	if err := st.CreateAgreement(store.Agreement{AgreementID: "urn:uuid:a", DatasetID: "urn:dataset:a",
		Origin: store.OriginImported, CreatedAt: now}); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	if err := st.CreateTransfer(store.TransferProcess{ProviderPID: "p1", ConsumerPID: "c1",
		AgreementID: "urn:uuid:a", State: TransferStarted, CallbackAddress: "http://x", Format: "HTTP-PULL",
		CounterpartyID: testPeer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	h := dataHandler{cfg: cfg, store: st}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", "p1")
		r = r.WithContext(context.WithValue(r.Context(), issuerContextKey{}, testPeer))
		h.handleData(w, r)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + VersionPath + "/data/p1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	got, readErr := io.ReadAll(resp.Body)
	if readErr == nil {
		t.Fatalf("read: got no error, and %d bytes — the connection should have been severed before the full 100 arrived", len(got))
	}
	if len(got) != 20 {
		t.Errorf("read %d bytes before the error, want exactly the configured 20", len(got))
	}
}

// TestDataPullSimulatedInterruptDoesNotFireOnARangedRequest pins the other
// half: a resumed (ranged) request always completes, which is what lets a
// demo's interrupt-then-resume sequence terminate instead of truncating
// forever.
func TestDataPullSimulatedInterruptDoesNotFireOnARangedRequest(t *testing.T) {
	h, id := dataFixtureWithSimulatedInterrupt(t, 3)
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	req.Header.Set("Range", "bytes=3-")
	rec := httptest.NewRecorder()
	h.handleData(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("got %d, want 206 — a ranged request must never be truncated: %s", rec.Code, rec.Body)
	}
	want := servedBytes[3:]
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// dataFixtureWithSimulatedInterrupt is dataFixture plus
// SimulateInterruptAfterBytes, for the one test above that needs the field
// set but exercises it through httptest.NewRecorder (which is fine for the
// ranged case: the knob must not fire at all there, so Hijack is never
// reached).
func dataFixtureWithSimulatedInterrupt(t *testing.T, afterBytes int64) (dataHandler, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	path := filepath.Join(t.TempDir(), "a.csv")
	if err := os.WriteFile(path, []byte(servedBytes), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ds := config.Dataset{ID: "urn:dataset:a", SourceFile: path, SimulateInterruptAfterBytes: afterBytes}
	cfg := config.Config{ParticipantID: testSelf, Datasets: []config.Dataset{ds}}

	now := time.Now()
	if err := st.CreateAgreement(store.Agreement{AgreementID: "urn:uuid:a", DatasetID: "urn:dataset:a",
		Origin: store.OriginImported, CreatedAt: now}); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	if err := st.CreateTransfer(store.TransferProcess{ProviderPID: "p1", ConsumerPID: "c1",
		AgreementID: "urn:uuid:a", State: TransferStarted, CallbackAddress: "http://x", Format: "HTTP-PULL",
		CounterpartyID: testPeer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	return dataHandler{cfg: cfg, store: st}, "p1"
}
```

Add `"io"` to `internal/dsp/data_handler_test.go`'s imports — the first test above calls `io.ReadAll`, and it is not otherwise imported there yet.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run TestDataPullSimulatedInterrupt -v`
Expected: FAIL — the first test currently reads all 100 bytes with no error (the knob does not exist yet); the second currently passes already (harmless), which is fine — the point of writing it now is that it must *keep* passing after Step 3.

- [ ] **Step 3: Implement the truncation branch**

In `internal/dsp/data_handler.go`, insert this between the `Range`-handling `if` block (Task 3, ending in its second `return`) and the final unranged-streaming block, inside `handleData`:

```go
	if ds.SimulateInterruptAfterBytes > 0 {
		n := ds.SimulateInterruptAfterBytes
		if n > stat.Size() {
			n = stat.Size()
		}
		io.CopyN(w, f, n) //nolint:errcheck // the connection is about to be severed regardless
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
			}
		}
		return
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/... -run TestDataPullSimulatedInterrupt -v`
Expected: PASS, both.

- [ ] **Step 5: Mutation-test the "never on a ranged request" rule**

Temporarily move the truncation `if` block above the `Range`-handling block (so it runs unconditionally on `SimulateInterruptAfterBytes > 0`, before checking for `Range`), and re-run `TestDataPullSimulatedInterruptDoesNotFireOnARangedRequest` — it must FAIL. Restore the original order.

- [ ] **Step 6: Run the whole package's tests**

Run: `go test ./internal/dsp/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/dsp/data_handler.go internal/dsp/data_handler_test.go
git commit -m "feat: demo-only interrupt simulation on the data endpoint"
```

---

## Task 5: Consumer resume — deterministic partial file

**Files:**
- Modify: `internal/dsp/transfer_consumer_handler.go:256-316` (`pullTransferData`)
- Modify: `internal/dsp/transfer_consumer_handler_test.go`

**Interfaces:**
- Consumes: the provider's `206`/`416` behavior (Task 3).
- Produces: `pullTransferData` writes to a deterministic path `downloads/.partial-<consumerPID>` instead of a random temp name, and resumes from it across a second call for the same transfer. Task 6 adds a concurrency guard around this same method.

- [ ] **Step 1: Write the failing tests**

Add to `internal/dsp/transfer_consumer_handler_test.go`:

```go
// pullPartialPath is the deterministic name pullTransferData now uses,
// exposed here so tests can seed and inspect it directly.
func pullPartialPath(dir, consumerPID string) string {
	return filepath.Join(dir, downloadDir, ".partial-"+consumerPID)
}

// TestPullTransferData_ResumesFromAnExistingPartialFile seeds a partial
// download by hand and points the mock provider at a server that asserts
// the resulting request carries the matching Range and answers 206 with
// only the remaining bytes — then checks the two pieces landed concatenated
// in the final file.
func TestPullTransferData_ResumesFromAnExistingPartialFile(t *testing.T) {
	const already = "id,value\n1,hel"
	const rest = "lo\n"
	dir := t.TempDir()
	h, _ := newTestTransferHandler(t, config.Config{DataDir: dir})
	consumerPID := "urn:uuid:resume-1"

	partialDir := filepath.Join(dir, downloadDir)
	if err := os.MkdirAll(partialDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(pullPartialPath(dir, consumerPID), []byte(already), 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := fmt.Sprintf("bytes=%d-", len(already))
		if got := r.Header.Get("Range"); got != want {
			t.Errorf("Range = %q, want %q", got, want)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", len(already), len(already)+len(rest)-1, len(already)+len(rest)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(rest))
	}))
	defer provider.Close()

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: consumerPID}, &DataAddress{Endpoint: provider.URL})

	final := filepath.Join(dir, downloadDir, consumerPID)
	waitForFile(t, final)
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if string(got) != already+rest {
		t.Errorf("final content = %q, want %q", got, already+rest)
	}
	if _, err := os.Stat(pullPartialPath(dir, consumerPID)); !os.IsNotExist(err) {
		t.Error("the partial file was not renamed away")
	}
}

// TestPullTransferData_416DiscardsThePartialFile pins the integrity check:
// a 416 answer to a resumed pull means the provider's file is no longer
// long enough to be a valid continuation, so the partial is deleted rather
// than kept or appended to.
func TestPullTransferData_416DiscardsThePartialFile(t *testing.T) {
	dir := t.TempDir()
	h, _ := newTestTransferHandler(t, config.Config{DataDir: dir})
	consumerPID := "urn:uuid:resume-416"

	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(pullPartialPath(dir, consumerPID), []byte("stale-bytes"), 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer provider.Close()

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: consumerPID}, &DataAddress{Endpoint: provider.URL})

	if _, err := os.Stat(pullPartialPath(dir, consumerPID)); !os.IsNotExist(err) {
		t.Error("the stale partial file was not removed after a 416")
	}
	if _, err := os.Stat(filepath.Join(dir, downloadDir, consumerPID)); !os.IsNotExist(err) {
		t.Error("a final file appeared, but nothing was ever successfully downloaded")
	}
}

// TestPullTransferData_OrdinaryFailureDuringAResumeKeepsThePartialFile pins
// the one behavior change from before this milestone: an ordinary failure
// (here, a 500) on a resumed pull must not discard bytes already received.
func TestPullTransferData_OrdinaryFailureDuringAResumeKeepsThePartialFile(t *testing.T) {
	dir := t.TempDir()
	h, _ := newTestTransferHandler(t, config.Config{DataDir: dir})
	consumerPID := "urn:uuid:resume-500"
	const already = "id,value\n"

	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(pullPartialPath(dir, consumerPID), []byte(already), 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: consumerPID}, &DataAddress{Endpoint: provider.URL})

	got, err := os.ReadFile(pullPartialPath(dir, consumerPID))
	if err != nil {
		t.Fatalf("the partial file was removed after an ordinary failure: %v", err)
	}
	if string(got) != already {
		t.Errorf("partial content = %q, want it untouched at %q", got, already)
	}
}
```

Add `"fmt"` and `"strings"` to `internal/dsp/transfer_consumer_handler_test.go`'s imports if not already present (check the existing import block first).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run TestPullTransferData -v`
Expected: FAIL — today's `pullTransferData` always writes to a random `os.CreateTemp` name and never sends `Range`, so none of the three assertions hold.

- [ ] **Step 3: Rewrite `pullTransferData`**

Replace the whole function body in `internal/dsp/transfer_consumer_handler.go:256-316`:

```go
// pullTransferData fetches what a dataAddress points at and writes it under
// data_dir, resuming from a previous attempt when one left bytes behind.
// Called whenever a start message carrying an address arrives — the first
// time for a transfer, and again on every restart after a suspension.
//
// Not self-retried. A failed pull leaves the transfer in STARTED, which is
// the honest state — the provider is still willing to serve and an operator
// can ask again. What changes with resumption is what "ask again" costs: the
// next externally triggered attempt continues from wherever the last one
// left off, rather than starting over.
func (h transferHandler) pullTransferData(t store.ConsumerTransfer, addr *DataAddress) {
	if addr == nil || addr.Endpoint == "" {
		return
	}
	// The endpoint came from a counterparty, so it goes through the same
	// guard as every other address this connector is told to contact.
	if err := validateOutgoingCallback(addr.Endpoint); err != nil {
		slog.Error("refuse data endpoint", "consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "error", err)
		return
	}

	dir := filepath.Join(h.cfg.DataDir, downloadDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Error("create download directory", "dir", dir, "error", err)
		return
	}
	// A fixed name rather than os.CreateTemp's random one, so a later
	// restart of the same transfer can find what an earlier attempt left
	// behind and continue it.
	partial := filepath.Join(dir, ".partial-"+t.ConsumerPID)
	var existingSize int64
	if info, err := os.Stat(partial); err == nil {
		existingSize = info.Size()
	}
	resuming := existingSize > 0

	req, err := http.NewRequest(http.MethodGet, addr.Endpoint, nil)
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
	resp, err := callbackHTTPClient.Do(req)
	if err != nil {
		slog.Error("data pull", "consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "error", err)
		return
	}
	defer resp.Body.Close()

	if resuming {
		switch resp.StatusCode {
		case http.StatusPartialContent:
			// fall through to the append below
		case http.StatusRequestedRangeNotSatisfiable:
			// The provider's file is no longer at least as long as what this
			// connector already has — it was replaced or shrank between
			// attempts. Not a valid prefix of anything; start over next time.
			slog.Warn("provider's file is no longer past what this connector already has; discarding the partial download",
				"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "had_bytes", existingSize)
			if err := os.Remove(partial); err != nil {
				slog.Error("remove stale partial download", "path", partial, "error", err)
			}
			return
		default:
			// Any other answer to a resumed pull, including an unexpected
			// 200 — this connector's own provider role always answers a
			// Range request with 206 or 416, so a 200 here would mean the
			// counterparty does not honor Range at all, and appending its
			// full-content body to an existing partial would corrupt the
			// file. Safer to abort and leave the partial exactly as it was.
			slog.Error("data endpoint gave an unexpected answer to a resumed pull; leaving the partial download in place",
				"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "status", resp.StatusCode, "had_bytes", existingSize)
			return
		}
	} else if resp.StatusCode >= 300 {
		slog.Error("data endpoint refused the pull",
			"consumer_pid", t.ConsumerPID, "endpoint", addr.Endpoint, "status", resp.StatusCode)
		return
	}

	flag := os.O_CREATE | os.O_WRONLY
	if resuming {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	out, err := os.OpenFile(partial, flag, 0o600)
	if err != nil {
		slog.Error("open download file", "path", partial, "error", err)
		return
	}
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		out.Close()
		slog.Error("write download", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	if err := out.Close(); err != nil {
		slog.Error("close download", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	if resuming {
		slog.Info("resumed transfer data pull", "consumer_pid", t.ConsumerPID, "had_bytes", existingSize, "appended_bytes", n)
	}
	final := filepath.Join(dir, t.ConsumerPID)
	if err := os.Rename(partial, final); err != nil {
		slog.Error("place download", "path", final, "error", err)
		return
	}
	slog.Info("pulled transfer data", "consumer_pid", t.ConsumerPID, "path", final, "bytes", n)
}
```

`internal/dsp/transfer_consumer_handler.go` needs `"fmt"` added to its imports (`"encoding/json", "errors", "fmt", "io", "log/slog", "net/http", "os", "path/filepath", "time", store`).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/... -run TestPullTransferData -v`
Expected: PASS, all three.

- [ ] **Step 5: Mutation-test the 416 and the "leave untouched" branches**

Temporarily change `case http.StatusRequestedRangeNotSatisfiable:` to `case http.StatusTeapot:` (an unreachable status for this test) and re-run `TestPullTransferData_416DiscardsThePartialFile` — it must FAIL (416 now falls into the `default` branch and the partial is *not* removed). Restore. Temporarily delete the `default:` branch's early `return` (letting execution fall through to the write) and re-run `TestPullTransferData_OrdinaryFailureDuringAResumeKeepsThePartialFile` — it must FAIL (the 500 response's empty body would now overwrite-or-append nothing meaningful, but the point is the branch no longer returns early — confirm the test actually catches this by checking it fails for the right reason, i.e. the file gets touched). Restore.

- [ ] **Step 6: Run the full package's tests, including the pre-existing ones this rewrite must not break**

Run: `go test ./internal/dsp/... -run 'TestConsumerPullsWhenTheStartCarriesAnAddress|TestConsumerDoesNotPullWithoutAnAddress|TestPullTransferData' -v`
Expected: PASS, all — `TestConsumerPullsWhenTheStartCarriesAnAddress`'s "nothing partial is left behind" assertion still holds, since a successful fresh pull still renames the partial away.

Run: `go test ./internal/dsp/...`
Expected: PASS, no regressions anywhere else in the package.

- [ ] **Step 7: Commit**

```bash
git add internal/dsp/transfer_consumer_handler.go internal/dsp/transfer_consumer_handler_test.go
git commit -m "feat: consumer resumes an interrupted pull instead of refetching"
```

---

## Task 6: Guard against two concurrent pulls for the same transfer

**Files:**
- Modify: `internal/dsp/transfer_handler.go:67-71` (the `transferHandler` struct)
- Modify: `internal/dsp/router.go:40`
- Modify: `internal/dsp/transfer_handler_test.go:33` (`newTestTransferHandler`)
- Modify: `internal/dsp/transfer_consumer_handler.go` (`pullTransferData`, continuing Task 5)
- Modify: `internal/dsp/transfer_consumer_handler_test.go`

**Why this task exists:** switching to a deterministic partial-file path (Task 5) introduces a new risk the old random-named temp file did not have. The provider's own autonomous sequence walk (`driveTransfer`) suspends and restarts a transfer purely on a timer, with no awareness of whether the consumer's own pull for the *previous* start has finished — DSP's legality table only forbids a second `TransferStartMessage` while the transfer is `STARTED` (`startLegalFrom` accepts only `REQUESTED` or `SUSPENDED`), so a fast enough suspend-then-restart cycle can trigger a second `pullTransferData` call while the first is still running, mid-`io.Copy`, against the same `.partial-<consumerPID>` file — a real byte-interleaving corruption risk that the old random-named file did not have.

**Interfaces:**
- Produces: `transferHandler.pulling *sync.Map`, tracking in-flight `pullTransferData` calls by `ConsumerPID`. A second call for a `ConsumerPID` already in flight is dropped (logged, not run) rather than racing. A `nil` `pulling` field (any `transferHandler` value that does not set it) disables the guard rather than panicking, so existing tests and call sites that construct a bare `transferHandler{...}` literal without this field keep working unchanged.

- [ ] **Step 1: Write the failing test**

Add to `internal/dsp/transfer_consumer_handler_test.go`:

```go
// TestPullTransferData_ConcurrentCallsForTheSameTransferDoNotRace pins the
// guard this task adds: a second pullTransferData call for a transfer whose
// first call is still in flight must be dropped, not run alongside it —
// two goroutines writing the same deterministic partial file at once would
// corrupt it.
func TestPullTransferData_ConcurrentCallsForTheSameTransferDoNotRace(t *testing.T) {
	// started is buffered and signaled with a non-blocking send rather than
	// closed: while this test is verifying Step 2's expected failure (the
	// guard not yet in place), a missing guard lets the second call reach
	// this same handler too, and a second close would panic.
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		_, _ = w.Write([]byte(servedBytes))
	}))
	defer provider.Close()

	dir := t.TempDir()
	h, _ := newTestTransferHandler(t, config.Config{DataDir: dir})
	consumerTransfer := store.ConsumerTransfer{ConsumerPID: "urn:uuid:race-consumer"}
	addr := &DataAddress{Endpoint: provider.URL}

	go h.pullTransferData(consumerTransfer, addr)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first call never reached the provider")
	}

	done := make(chan struct{})
	go func() {
		h.pullTransferData(consumerTransfer, addr)
		close(done)
	}()
	select {
	case <-done:
		// Correct: the second call saw the guard and returned immediately,
		// without waiting on `release`.
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("the second call for the same transfer did not return promptly — it ran a second real pull instead of being dropped")
	}

	close(release)
	waitForFile(t, filepath.Join(dir, downloadDir, consumerTransfer.ConsumerPID))
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dsp/... -run TestPullTransferData_ConcurrentCallsForTheSameTransferDoNotRace -v -timeout 10s`
Expected: FAIL (times out or reports the second call did not return promptly) — nothing today prevents the second call from also blocking on `<-release` via the same `provider` server.

- [ ] **Step 3: Add the `pulling` field**

In `internal/dsp/transfer_handler.go`, add `"sync"` to the imports, then change:

```go
type transferHandler struct {
	cfg       config.Config
	store     *store.Store
	stepDelay time.Duration
}
```

to:

```go
type transferHandler struct {
	cfg       config.Config
	store     *store.Store
	stepDelay time.Duration
	// pulling tracks in-flight pullTransferData calls by ConsumerPID, so a
	// restart that arrives while a previous pull for the same transfer is
	// still running is dropped instead of racing it onto the same
	// deterministic partial file. nil disables the guard rather than
	// panicking — every construction site that does not set this field
	// (most existing tests, which never exercise pullTransferData) is
	// unaffected.
	pulling *sync.Map
}
```

- [ ] **Step 4: Wire it in production**

In `internal/dsp/router.go`, add `"sync"` to the imports, then change:

```go
	tr := transferHandler{cfg: cfg, store: st, stepDelay: transferStepDelay}
```

to:

```go
	tr := transferHandler{cfg: cfg, store: st, stepDelay: transferStepDelay, pulling: &sync.Map{}}
```

- [ ] **Step 5: Wire it into the shared test helper**

In `internal/dsp/transfer_handler_test.go`, `"sync"` is already imported. Change:

```go
	return transferHandler{cfg: cfg, store: st, stepDelay: transferStepDelay}, st
```

to:

```go
	return transferHandler{cfg: cfg, store: st, stepDelay: transferStepDelay, pulling: &sync.Map{}}, st
```

- [ ] **Step 6: Add the guard inside `pullTransferData`**

In `internal/dsp/transfer_consumer_handler.go`, at the very top of `pullTransferData` (before the `if addr == nil` check), add:

```go
	if h.pulling != nil {
		if _, alreadyRunning := h.pulling.LoadOrStore(t.ConsumerPID, struct{}{}); alreadyRunning {
			slog.Warn("a pull for this transfer is already in flight; dropping this restart's trigger",
				"consumer_pid", t.ConsumerPID)
			return
		}
		defer h.pulling.Delete(t.ConsumerPID)
	}
```

- [ ] **Step 7: Run the test to verify it passes**

Run: `go test ./internal/dsp/... -run TestPullTransferData_ConcurrentCallsForTheSameTransferDoNotRace -v -race -timeout 10s`
Expected: PASS, and clean under `-race`.

- [ ] **Step 8: Mutation-test the guard**

Temporarily wrap the guard's body in `if false && h.pulling != nil {` (disabling it without deleting it) and re-run the concurrency test — it must FAIL or hang (confirm it fails within the test's own timeout, not an infinite hang — if it hangs past `go test`'s default timeout, that is still a correctly-failing mutation test, just note it in the task and move on rather than waiting it out). Restore.

- [ ] **Step 9: Run the whole package under `-race`**

Run: `go test ./internal/dsp/... -race`
Expected: PASS, no data races reported anywhere in the package (this exercises the `pulling: &sync.Map{}` now wired into `newTestTransferHandler`, across every test that uses it).

- [ ] **Step 10: Commit**

```bash
git add internal/dsp/transfer_handler.go internal/dsp/router.go internal/dsp/transfer_handler_test.go internal/dsp/transfer_consumer_handler.go internal/dsp/transfer_consumer_handler_test.go
git commit -m "fix: guard pullTransferData against two concurrent pulls for one transfer"
```

---

## Task 7: Documentation — `DECISIONS.md`, `docs/follow-ups.md`, `README.md`

**Files:**
- Modify: `DECISIONS.md`
- Modify: `docs/follow-ups.md`
- Modify: `README.md`

**Interfaces:** None — this task changes no code.

- [ ] **Step 1: Add a new `DECISIONS.md` section**

Append after the last existing section (currently `## 30. Three data-plane integrity gaps closed from docs/follow-ups.md` — confirm the number by checking the last `^## [0-9]+\.` heading before writing, since another task landing first in a shared branch could change it):

```markdown
## 31. Transfer robustness: `Range` support and resumption

**Decision.** The provider's data endpoint supports one `Range` form,
`bytes=N-`: `206 Partial Content` when `0 <= N < size`, `416 Range Not
Satisfiable` when `N >= size`, and today's unconditional `200` for anything
else — no header, an unparseable one, or a form this connector does not
implement (multi-range, a closed range, the `bytes=-N` suffix form), per RFC
7233's own guidance for a range a server does not support. The consumer's
`pullTransferData` writes to a deterministic path,
`downloads/.partial-<consumerPID>`, instead of a random temp name, and on a
restart sends `Range: bytes=N-` for whatever it already has. `206` appends;
`416` discards the partial and starts fresh on the next restart; any other
answer to a resumed pull is logged and leaves the partial untouched — the
one behavior change from before this milestone, where any failure discarded
everything received.

**31.1 Integrity across a resume is a size check, not a content check.** A
partial file at or past the provider's current file size cannot be a valid
prefix of it, so `416` is what tells the consumer "this is not the file I
was receiving." No hash, no `ETag`. A same-size content replacement between
attempts is not caught — accepted, not solved, the same posture
`SourceFile`'s own doc comment already states for a file swapped
underneath the connector.

**31.2 `resolveTransferSequence` gains a dataset-keyed fallback, closing a
gap §25.7 named but left open.** §25.7 recorded that `transfer_policies`
cannot key a negotiated agreement, because that id does not exist until the
negotiation producing it is already under way, and named the absence of any
other wire-observable key as the reason. `config.Dataset.TransferSequence`
supplies the key that was missing off the wire: an agreement's `dataset_id`
is known regardless of whether the agreement was negotiated or imported (the
same lookup `hasSourceFor` already performs). An `agreement_id` match in
`transfer_policies` always takes precedence when both exist. This exists to
let the demo (below) prove resumption against a real negotiated agreement,
but it is not demo-only: it is the general answer to §25.7's open question.

**31.3 A concurrency guard was needed that the old design did not require.**
The deterministic partial-file path (31 above) is a new appendable target
two `pullTransferData` calls could race on, unlike the old random-named
temp file. DSP's own legality table allows a restart to arrive while a
previous pull is still running — nothing ties the provider's autonomous
suspend/restart timer to how long the consumer's own fetch takes.
`transferHandler.pulling`, a `*sync.Map` shared across every copy of the
handler, drops a restart's trigger if a pull for the same `ConsumerPID` is
already in flight, logged the same way a stale state-update elsewhere in
this connector already is, rather than run two writers against one file.

**31.4 Demo-only fault injection: `simulate_interrupt_after_bytes`, a new
dataset, not a reused one.** `config.Dataset.SimulateInterruptAfterBytes`
truncates a non-`Range` request at that many bytes and severs the
connection via `http.Hijacker`, so `make demo` can force a real
interruption rather than assert against a mock. It never fires on a `Range`
request, which is what lets the interrupt-then-resume sequence terminate.
The scenario runs against a dedicated dataset,
`urn:dataset:sample-resume`, rather than the existing `urn:dataset:sample`
— the same call as `CN:02-07`'s (§29.1): a shared fixture would make every
future demo run pay for a two-phase cycle permanently and collapse two
different failures (the basic pull broke; the resume broke) into one
ambiguous signal.

*Trade-off accepted.* An orphaned `.partial-<consumerPID>` file — a
transfer that terminates instead of restarting after being interrupted
leaves one behind forever — is not cleaned up. Pre-existing risk in a
smaller form (a stray random-named temp file could already leak on an
unclean process exit); this milestone makes the leaked file larger and its
name predictable. Tracked in `docs/follow-ups.md` rather than solved here:
it needs a retention policy this project has none of yet.
```

- [ ] **Step 2: Add a `docs/follow-ups.md` entry**

Add a new section at the end of the file (after the existing `## From the transfer process (provider, Phase A) milestone (2026-08)` section's last entry):

```markdown
## From the transfer range/resumption milestone (2026-08)

**An orphaned `.partial-<consumerPID>` file is never cleaned up.** A
transfer that is interrupted and then terminates instead of restarting
leaves that file on disk forever — the deterministic name this milestone
introduced for resumption makes what was already a smaller, random-named
leak risk into a larger, predictable one. Solving it needs a retention or
garbage-collection policy this project has none of yet: an obvious rule
("delete a partial file once its transfer reaches a terminal state") needs
the partial-file cleanup to happen somewhere that already knows the
transfer terminated, which today is a different code path (`handleTransfer
Termination`/`handleTransferCompletion`) than the one writing the file
(`pullTransferData`), and wiring the two together is a design decision, not
a cleanup.
```

- [ ] **Step 3: Update `README.md`**

Find the paragraph beginning `**Transfers are small and one-shot.**` and replace it:

```markdown
**A resumed transfer trusts a size check, not a content check.** A pull
that gets interrupted resumes from where it left off on the next restart —
the provider answers `Range: bytes=N-` with `206` and the remaining bytes,
or `416` if its file is no longer at least that long, which is also the
whole of the integrity check: a same-size content replacement between
attempts is not caught. An orphaned partial download from a transfer that
never restarts is not cleaned up either. The write timeout still bounds how
large a single uninterrupted fetch can finish before this connector's own
role in the interruption starts to matter.
```

- [ ] **Step 4: Verify the doc changes build no markdown-breaking issues**

There is no markdown linter in this project's `make ci`/`make tck` — visually confirm the three files render sensibly (headings, section numbers in order, no unclosed code fences) by reading them back.

- [ ] **Step 5: Commit**

```bash
git add DECISIONS.md docs/follow-ups.md README.md
git commit -m "docs: record range/resumption decisions and the orphaned-partial follow-up"
```

---

## Task 8: Demo — a dedicated resume scenario

**Files:**
- Modify: `demo/provider.yaml`
- Modify: `demo/compose.yaml`
- Modify: `demo/run.sh`

**Interfaces:** None new — this task exercises Tasks 1–6 end to end.

- [ ] **Step 1: Add the second dataset to `demo/provider.yaml`**

Append to the `datasets:` list:

```yaml
  - id: urn:dataset:sample-resume
    source_file: /etc/dsbox/data/sample-resume.csv
    simulate_interrupt_after_bytes: 2000
    transfer_sequence: [STARTED, SUSPENDED, STARTED, COMPLETED]
```

- [ ] **Step 2: Mount the second generated file in `demo/compose.yaml`**

In the `provider` service's `volumes:` list, add a line alongside the existing `sample.csv` mount:

```yaml
      - ./generated/sample-resume.csv:/etc/dsbox/data/sample-resume.csv:ro
```

- [ ] **Step 3: Generate the second file in `demo/run.sh`**

Immediately after the existing `sample.csv` heredoc (the block ending `EOF` right before `echo "==> connectors"`), add:

```sh
# A larger file than sample.csv, generated the same way and for the same
# reason — proving a transfer, not the presence of a fixture — but large
# enough that truncating it partway through and resuming is a meaningful
# exercise rather than a coin flip on three lines.
: >"$gen/sample-resume.csv"
i=1
while [ "$i" -le 500 ]; do
	echo "$i,row-$i,$((i * 37))" >>"$gen/sample-resume.csv"
	i=$((i + 1))
done
```

- [ ] **Step 4: Add the second negotiate-and-transfer round**

At the end of `demo/run.sh`, after the existing round's final `echo "  received: $downloaded"` line, add:

```sh
echo
echo "==> negotiate (resume scenario)"
curl -sf -X POST http://127.0.0.1:9280/2025-1/negotiations/initiate \
	-H "Authorization: Bearer $operator" \
	-H 'Content-Type: application/json' \
	-d '{"providerId":"urn:participant:provider","offerId":"urn:dataset:sample-resume#offer","datasetId":"urn:dataset:sample-resume","connectorAddress":"http://provider:8080/2025-1"}' \
	>/dev/null

echo "==> waiting for the resume-scenario agreement"
resume_agreement=""
i=0
while [ "$i" -lt 40 ]; do
	resume_agreement=$(curl -sf http://127.0.0.1:9281/agreements \
		-H "Authorization: Bearer demo-management-token" 2>/dev/null |
		sed -n 's/.*"agreementId":"\([^"]*\)","datasetId":"urn:dataset:sample-resume".*/\1/p' | head -1 || true)
	[ -n "$resume_agreement" ] && break
	i=$((i + 1))
	sleep 1
done
if [ -z "$resume_agreement" ]; then
	echo "no resume-scenario agreement was concluded" >&2
	$compose logs >&2
	exit 1
fi
echo "    agreement $resume_agreement"

echo "==> transfer (resume scenario)"
curl -sf -X POST http://127.0.0.1:9280/2025-1/transfers/initiate \
	-H "Authorization: Bearer $operator" \
	-H 'Content-Type: application/json' \
	-d "{\"providerId\":\"urn:participant:provider\",\"agreementId\":\"$resume_agreement\",\"format\":\"HTTP-PULL\",\"connectorAddress\":\"http://provider:8080/2025-1\"}" \
	>/dev/null

echo "==> waiting for the resumed file"
i=0
resume_downloaded=""
while [ "$i" -lt 60 ]; do
	resume_downloaded=$(find "$gen/consumer-data/downloads" -type f ! -name '.partial-*' ! -samefile "$downloaded" 2>/dev/null | head -1 || true)
	[ -n "$resume_downloaded" ] && break
	i=$((i + 1))
	sleep 1
done
if [ -z "$resume_downloaded" ]; then
	echo "the resumed file never arrived" >&2
	$compose logs >&2
	exit 1
fi

if ! diff -q "$gen/sample-resume.csv" "$resume_downloaded" >/dev/null; then
	echo "the resumed file does not match what was sent" >&2
	diff "$gen/sample-resume.csv" "$resume_downloaded" >&2 || true
	exit 1
fi

if ! $compose logs consumer 2>/dev/null | grep -q "resumed transfer data pull"; then
	echo "the resumed file matched, but the consumer log shows no evidence the resume path actually ran" >&2
	$compose logs consumer >&2
	exit 1
fi

echo
echo "resumed a deliberately interrupted $(wc -c <"$resume_downloaded" | tr -d ' ')-byte transfer"
echo "after a real suspend/restart cycle, and the recovered file matches byte for byte."
```

- [ ] **Step 5: Run the demo end to end**

Run: `make demo`
Expected: both rounds complete; the script prints the original "moved N bytes..." message, then "resumed a deliberately interrupted N-byte transfer... and the recovered file matches byte for byte." Exit code 0.

If it fails, `demo/demo.log` (written by the script's own `cleanup` trap) has both containers' full logs — check the consumer's log around the second round for whether `Range` was sent and what status came back, and the provider's log for whether the interrupt-simulation branch fired.

- [ ] **Step 6: Run it again to confirm it is not flaky**

Run: `make demo`
Expected: same result. (This exercises real timing — `transferStepDelay` versus how long the truncated and resumed pulls actually take over the compose network — so a second clean run is real evidence, not a formality.)

- [ ] **Step 7: Commit**

```bash
git add demo/provider.yaml demo/compose.yaml demo/run.sh
git commit -m "feat: demo proves a real interrupted transfer resumes correctly"
```

---

## Task 9: Final verification

**Files:** None.

- [ ] **Step 1: Full test suite, with race detection**

Run: `go test -race ./...`
Expected: PASS, every package.

- [ ] **Step 2: Vet**

Run: `go vet ./...`
Expected: clean.

- [ ] **Step 3: Format check**

Run: `gofmt -l .`
Expected: no output (nothing unformatted).

- [ ] **Step 4: TCK gate, confirming this milestone changed nothing it verifies**

Run: `make tck`
Expected: `65 required tests passed, 0 results outside the gate` — unchanged from before this plan. If this regresses, the most likely cause is the `handleData` rewrite in Task 3/4 changing the unranged `200` path's behavior for a TCK request that happens to carry a `Range`-shaped header by coincidence, or the `resolveTransferSequence` signature change reaching a call site this plan's Task 2 missed — re-check `grep -rn "resolveTransferSequence(" internal/` for any caller not updated.

- [ ] **Step 5: Demo, one more time as the closing check**

Run: `make demo`
Expected: PASS, as in Task 8.

- [ ] **Step 6: Confirm nothing is left uncommitted**

Run: `git status -sb`
Expected: clean, everything from Tasks 1–8 committed.
