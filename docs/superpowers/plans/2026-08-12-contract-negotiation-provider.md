# Contract Negotiation (Provider Role) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the DSP contract negotiation protocol in the provider role — six endpoints, a SQLite-backed state machine, and a validity-period policy check — so the `CN` TCK suite (15 tests, 14 required to pass) joins the compliance gate.

**Architecture:** A new `internal/store` package opens a SQLite file and persists one row per negotiation. `internal/dsp/negotiation.go` holds pure decision logic (given a request, decide the next state and what to push) and the wire-document builders. `internal/dsp/negotiation_handler.go` wires HTTP onto that logic, and `internal/dsp/callback.go` performs the best-effort asynchronous push to a consumer's callback address. `cmd/tckgate` gains a named-exemption mechanism so the gate can honestly report "14 of 15 pass, one tracked gap" instead of lying in either direction.

**Tech Stack:** Go 1.26 standard library, plus one new dependency: `modernc.org/sqlite` (pure Go, no CGO — already approved by the user for this milestone).

**Spec:** `docs/superpowers/specs/2026-08-11-contract-negotiation-provider-design.md` (commit `3f13840`). Read it if a task references a section by name (e.g. "the offer/agreement divergence rule") and the summary here is not enough.

## Global Constraints

- English only: code, comments, commit messages, docs (`CLAUDE.md`).
- No plugin system, no SPI, no inheritance-based extension points (`CLAUDE.md`).
- Standard library first. The one exception in this plan, `modernc.org/sqlite`, is already approved — do not add anything else without asking.
- No organizational affiliation anywhere in the repo. Copyright is b7g.
- Storage: one SQLite file under `data_dir`, WAL mode. In-memory SQLite (`:memory:`) is for tests only, never a runtime path (`DECISIONS.md` §8, `CLAUDE.md`).
- JSON-LD fixed compact form, validated by direct field checks, not a schema library (`DECISIONS.md` §22.5).
- Every node this project emits carries `@type` (established in `internal/dsp/catalog.go`).
- `{id}` in every `/negotiations/{id}` path is the **provider's own generated pid**, never the consumer's.
- The synchronous response to `POST /negotiations/request` is always a plain `REQUESTED` acknowledgment. What the provider actually decided (offer / agree / terminate) is communicated only via an asynchronous push afterward — never in the synchronous response body.
- `CN:02-07` is out of scope for this milestone (see spec). It is tracked as a named gate exemption, not silently ignored and not force-fit.
- Go test command: `go test ./...`. TCK command: `make tck`.

---

### Task 1: Gate — named exemptions

**Files:**
- Modify: `cmd/tckgate/main.go`
- Modify: `cmd/tckgate/main_test.go`

**Interfaces:**
- Consumes: nothing from other tasks — this is the first task and touches only the existing gate.
- Produces: `evaluate(output string, expected map[string]int, exempt map[string]bool) (Report, error)` — the third parameter is new and every existing caller must be updated. `Report` gains `Exempted []string` and `UnexpectedPasses []string`. Later tasks do not call this package directly; Task 6 changes the package-level `expected`/`exempt` vars to add `CN`.

The gate currently expresses "every gated result must pass." This milestone needs a second shape: "this specific, named test is known to fail — do not let it silently disappear, and do not let it silently start passing either." A `FAILED` result whose ID is in `exempt` is counted separately instead of failing the gate. If that same ID ever comes back `SUCCESSFUL`, the gate must fail — a stale exemption hiding a real pass is worse than one hiding a real failure.

- [ ] **Step 1: Write the failing tests**

Add these test functions to `cmd/tckgate/main_test.go` (append after `TestExpectedCountsMetPasses`):

```go
func TestExemptedFailureDoesNotFailTheGate(t *testing.T) {
	report, err := evaluate(
		synthetic("SUCCESSFUL: MET:01-01", "FAILED: CN:02-07"),
		map[string]int{"MET": 1, "CN": 1},
		map[string]bool{"CN:02-07": true},
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !report.OK() {
		t.Errorf("gate rejected a run whose only failure was a named exemption: %s", report)
	}
	if len(report.Exempted) != 1 || !strings.Contains(report.Exempted[0], "CN:02-07") {
		t.Errorf("Exempted = %v, want one entry naming CN:02-07", report.Exempted)
	}
	if len(report.Failed) != 0 {
		t.Errorf("Failed = %v, want the exempted result kept out of it", report.Failed)
	}
}

func TestExemptedTestUnexpectedlyPassingFailsTheGate(t *testing.T) {
	report, err := evaluate(
		synthetic("SUCCESSFUL: MET:01-01", "SUCCESSFUL: CN:02-07"),
		map[string]int{"MET": 1, "CN": 1},
		map[string]bool{"CN:02-07": true},
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.OK() {
		t.Error("gate accepted a run where an exempted test unexpectedly passed")
	}
	if len(report.UnexpectedPasses) != 1 || !strings.Contains(report.UnexpectedPasses[0], "CN:02-07") {
		t.Errorf("UnexpectedPasses = %v, want one entry naming CN:02-07", report.UnexpectedPasses)
	}
	if !strings.Contains(report.String(), "CN:02-07") {
		t.Errorf("report does not name the stale exemption: %s", report)
	}
}

func TestNonExemptedFailureStillFailsTheGate(t *testing.T) {
	report, err := evaluate(
		synthetic("SUCCESSFUL: MET:01-01", "FAILED: CN:02-01"),
		map[string]int{"MET": 1, "CN": 1},
		map[string]bool{"CN:02-07": true},
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.OK() {
		t.Error("gate accepted a run with a non-exempted failure")
	}
	if len(report.Failed) != 1 {
		t.Errorf("Failed = %v, want CN:02-01 in it (it is not the exempted test)", report.Failed)
	}
}

func TestNilExemptMapBehavesAsNoExemptions(t *testing.T) {
	report, err := evaluate(synthetic("SUCCESSFUL: MET:01-01"), map[string]int{"MET": 1}, nil)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !report.OK() {
		t.Errorf("gate rejected a clean run with a nil exempt map: %s", report)
	}
}
```

Every other existing call to `evaluate` in this file (`TestPassingOutputSatisfiesTheGate`, `TestFailingMETTestFailsTheGate`, `TestFailureOutsideTheWhitelistIsIgnored`, `TestTruncatedOutputIsAnError`, `TestCompletionMarkerMentionedInProseIsNotCompletion`, `TestUnderscoreSuiteIsNotSwallowedByItsPrefix` (both calls), `TestSuiteShortOfItsExpectedCountFailsTheGate`, `TestSuiteOverItsExpectedCountFailsTheGate`, `TestExpectedCountsMetPasses`) passes a two-argument call today. Add a third argument, `nil`, to every one of them — e.g. `evaluate(read(t, "testdata/passing.txt"), map[string]int{"MET": 1})` becomes `evaluate(read(t, "testdata/passing.txt"), map[string]int{"MET": 1}, nil)`. `TestZeroValueReportIsNotOK` calls `(Report{}).OK()` directly and needs no change.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/tckgate/...`
Expected: compile failure — `evaluate` is called with the wrong number of arguments, and `report.Exempted`/`report.UnexpectedPasses` do not exist yet.

- [ ] **Step 3: Extend `Report` and `evaluate`**

In `cmd/tckgate/main.go`, replace the `Report` struct:

```go
// Report is the gate's verdict over one TCK run.
type Report struct {
	// Expected is the count each gated suite had to produce, carried along so
	// that OK and String can explain a mismatch without the caller supplying it
	// a second time.
	Expected map[string]int
	// Seen counts the results observed for each gated suite.
	Seen map[string]int
	// Failed holds full result lines (timestamp included) for gated tests that
	// did not pass and are not named exemptions.
	Failed []string
	// Exempted holds full result lines for gated tests that failed but are
	// named in the exemption list passed to evaluate — expected failures,
	// tracked rather than hidden.
	Exempted []string
	// UnexpectedPasses holds full result lines for exempted tests that
	// SUCCEEDED. An exemption is a claim that this specific test cannot pass
	// yet; a pass means that claim is stale and the exemption must be removed,
	// not silently kept.
	UnexpectedPasses []string
	// Skipped counts results outside the gate: reported, but not gating.
	Skipped int
}
```

Replace `OK`:

```go
// OK reports whether the run satisfies the gate: every gated suite produced
// exactly the expected number of results, every non-exempted result passed,
// and no exempted result unexpectedly passed. An empty Expected map means
// nothing was gated, so it must not report a pass.
func (r Report) OK() bool {
	return len(r.Expected) > 0 && len(r.shortfalls()) == 0 &&
		len(r.Failed) == 0 && len(r.UnexpectedPasses) == 0
}
```

Replace `String`:

```go
func (r Report) String() string {
	if r.OK() {
		s := fmt.Sprintf("%d required tests passed, %d results outside the gate", r.total(), r.Skipped)
		if len(r.Exempted) > 0 {
			s += fmt.Sprintf(", %d known exemption(s)", len(r.Exempted))
		}
		return s
	}
	var parts []string
	if s := r.shortfalls(); len(s) > 0 {
		parts = append(parts, strings.Join(s, "; "))
	}
	if len(r.Failed) > 0 {
		// Each entry starts with its timestamp, so this sorts by run order
		// rather than by test identifier. That is fine here: this report exists
		// to be read, not diffed.
		sort.Strings(r.Failed)
		parts = append(parts, fmt.Sprintf("%d of %d required tests failed: %s",
			len(r.Failed), r.total(), strings.Join(r.Failed, ", ")))
	}
	if len(r.UnexpectedPasses) > 0 {
		sort.Strings(r.UnexpectedPasses)
		parts = append(parts, fmt.Sprintf("%d exempted test(s) unexpectedly passed, remove the exemption: %s",
			len(r.UnexpectedPasses), strings.Join(r.UnexpectedPasses, ", ")))
	}
	return strings.Join(parts, "; ")
}
```

Replace `evaluate`:

```go
// evaluate reads a complete TCK run and reports the gate's verdict. It errors
// when the run did not finish, because an incomplete run proves nothing and
// must never be mistaken for a pass. exempt names individual test IDs that
// are known to fail and must not fail the gate — but an exempted ID that
// unexpectedly passes fails the gate instead, so a stale exemption cannot
// hide a real pass. exempt may be nil, meaning no exemptions.
func evaluate(output string, expected map[string]int, exempt map[string]bool) (Report, error) {
	if !hasCompletionMarker(output) {
		return Report{}, fmt.Errorf("the TCK run did not complete: %q not found in the output", completionMarker)
	}

	report := Report{Expected: expected, Seen: make(map[string]int, len(expected))}
	for _, line := range strings.Split(output, "\n") {
		m := resultLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		outcome, id := m[1], m[2]
		suite, gated := gatedSuite(id, expected)
		if !gated {
			report.Skipped++
			continue
		}
		report.Seen[suite]++
		switch {
		case outcome == "FAILED" && exempt[id]:
			report.Exempted = append(report.Exempted, strings.TrimSpace(line))
		case outcome == "FAILED":
			report.Failed = append(report.Failed, strings.TrimSpace(line))
		case outcome == "SUCCESSFUL" && exempt[id]:
			report.UnexpectedPasses = append(report.UnexpectedPasses, strings.TrimSpace(line))
		}
	}
	return report, nil
}
```

Add the package-level `exempt` var next to `expected`, and update `main`'s call site:

```go
// exempt names individual gated test IDs that are known to fail and are
// tracked rather than required — see docs/follow-ups.md for each entry's
// story. A suite's count in expected still includes these tests: the gate
// proves the suite ran to completion regardless of exemptions.
var exempt = map[string]bool{}
```

In `main`, change `report, err := evaluate(string(data), expected)` to `report, err := evaluate(string(data), expected, exempt)`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/tckgate/...`
Expected: PASS, all tests including the four new ones.

- [ ] **Step 5: Commit**

```bash
git add cmd/tckgate/main.go cmd/tckgate/main_test.go
git commit -m "feat: add named exemptions to the TCK compliance gate"
```

---

### Task 2: Config — `data_dir` and `validity_until`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `config.example.yaml`
- Modify: `test/tck/dsbox.yaml`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `config.Config.DataDir string`; `config.Dataset.ValidityUntil *time.Time`. Task 3's store package takes a path built from `DataDir` (not `DataDir` itself — the caller in Task 5/`main.go` joins it with the database filename). Task 4's `isValid`/`findConfiguredDataset` read `config.Dataset.ID` and `.ValidityUntil` directly.

`data_dir` becomes a required config key, exactly like `participant_id` did in the catalog milestone — the same ripple applies: `minimal()` and every raw-literal test that expects `Load` to *succeed* need it added. Tests that expect `Load` to *fail* on an earlier check are unaffected, since they never reach the new check.

- [ ] **Step 1: Write the failing tests**

In `internal/config/config_test.go`, change `minimal` to include `data_dir`:

```go
// minimal returns a configuration document that satisfies every required key,
// with extra appended. Tests that are not about a specific required key use
// this, so that adding the next required key does not mean editing every test.
func minimal(extra string) []byte {
	return []byte("public_url: https://connector.example.org\n" +
		"participant_id: urn:participant:example\n" +
		"data_dir: ./data\n" + extra)
}
```

Add `data_dir: ./data\n` to the two raw literals that expect success. `TestLoadAllowsPlainHTTPInDevMode` becomes:

```go
func TestLoadAllowsPlainHTTPInDevMode(t *testing.T) {
	_, err := Load([]byte("public_url: http://dsbox:8080\ndev_mode: true\nparticipant_id: urn:participant:example\ndata_dir: ./data\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}
```

`TestEnvironmentOverridesFile` becomes:

```go
func TestEnvironmentOverridesFile(t *testing.T) {
	cfg, err := Load(
		[]byte("public_url: https://from-file.example.org\ndsp_addr: 0.0.0.0:9999\nparticipant_id: urn:participant:example\ndata_dir: ./data\n"),
		env(map[string]string{
			"DSBOX_PUBLIC_URL": "https://from-env.example.org",
			"DSBOX_DSP_ADDR":   "0.0.0.0:7777",
		}),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicURL != "https://from-env.example.org" {
		t.Errorf("PublicURL = %q, want the environment value", cfg.PublicURL)
	}
	if cfg.DSPAddr != "0.0.0.0:7777" {
		t.Errorf("DSPAddr = %q, want the environment value", cfg.DSPAddr)
	}
}
```

`TestEmptyDocumentWithEnvironmentStillLoads` gains a third environment key:

```go
func TestEmptyDocumentWithEnvironmentStillLoads(t *testing.T) {
	cfg, err := Load([]byte(""), env(map[string]string{
		"DSBOX_PUBLIC_URL":     "https://from-env.example.org",
		"DSBOX_PARTICIPANT_ID": "urn:participant:from-env",
		"DSBOX_DATA_DIR":       "./data",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicURL != "https://from-env.example.org" {
		t.Errorf("PublicURL = %q, want the environment value", cfg.PublicURL)
	}
	if cfg.ParticipantID != "urn:participant:from-env" {
		t.Errorf("ParticipantID = %q, want the environment value", cfg.ParticipantID)
	}
	if cfg.DSPAddr != "0.0.0.0:8080" {
		t.Errorf("DSPAddr = %q, want the default", cfg.DSPAddr)
	}
}
```

All other existing tests are unaffected — leave them exactly as they are (they either use `minimal()`, which now includes `data_dir` automatically, or they expect `Load` to fail on an earlier check, which still happens regardless of `data_dir`'s absence).

Append these new test functions:

```go
func TestLoadRequiresDataDir(t *testing.T) {
	_, err := Load([]byte("public_url: https://connector.example.org\nparticipant_id: urn:participant:example\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error when data_dir is absent")
	}
}

func TestDataDirFromEnvironmentOverridesFile(t *testing.T) {
	cfg, err := Load(
		[]byte("public_url: https://connector.example.org\nparticipant_id: urn:participant:example\ndata_dir: ./from-file\n"),
		env(map[string]string{"DSBOX_DATA_DIR": "./from-env"}),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != "./from-env" {
		t.Errorf("DataDir = %q, want the environment value", cfg.DataDir)
	}
}

func TestValidityUntilIsOptional(t *testing.T) {
	cfg, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Datasets[0].ValidityUntil != nil {
		t.Errorf("ValidityUntil = %v, want nil when absent", cfg.Datasets[0].ValidityUntil)
	}
}

func TestValidityUntilParses(t *testing.T) {
	cfg, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n    validity_until: 2027-01-01T00:00:00Z\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Datasets[0].ValidityUntil == nil {
		t.Fatal("ValidityUntil = nil, want the parsed timestamp")
	}
	want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if !cfg.Datasets[0].ValidityUntil.Equal(want) {
		t.Errorf("ValidityUntil = %v, want %v", cfg.Datasets[0].ValidityUntil, want)
	}
}

func TestMalformedValidityUntilIsAnError(t *testing.T) {
	_, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n    validity_until: not-a-time\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a malformed validity_until")
	}
}
```

Add `"time"` to the import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/...`
Expected: FAIL — `cfg.DataDir` and `cfg.Datasets[0].ValidityUntil` do not exist yet, and `TestLoadRequiresDataDir` fails because there is no such check.

- [ ] **Step 3: Implement `DataDir` and `ValidityUntil`**

In `internal/config/config.go`, add `"time"` to the imports, add the field to `Config`:

```go
	// MgmtAddr is the listen address for the management API. It binds to
	// localhost by default so a firewall mistake cannot expose it.
	MgmtAddr string `yaml:"mgmt_addr"`

	// DataDir is where the connector's SQLite database file lives, at
	// {DataDir}/dsbox.db. Required: the catalog milestone did not need
	// storage, but negotiation state is the connector's first runtime state
	// that must survive a restart (DECISIONS.md section 8).
	DataDir string `yaml:"data_dir"`
```

Add the field to `Dataset`:

```go
type Dataset struct {
	ID string `yaml:"id"`

	// ValidityUntil is the point after which this dataset's offer is no
	// longer valid. Optional: absent means the offer never expires, which is
	// every dataset's behavior before this milestone. This is the second of
	// the two policy shapes DECISIONS.md section 14 permits in v1.
	ValidityUntil *time.Time `yaml:"validity_until"`
}
```

Add the environment override, next to the `participant_id` one:

```go
	if v := getenv("DSBOX_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
```

Add the validation check at the end of `validate`, after the dataset loop:

```go
	if c.DataDir == "" {
		return fmt.Errorf("data_dir is required: it is where the negotiation state database lives")
	}
	return nil
}
```

(This replaces the existing bare `return nil` at the end of the function.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/...`
Expected: FAIL still, on `TestExampleConfigLoads` — `config.example.yaml` has no `data_dir` yet.

- [ ] **Step 5: Update `config.example.yaml`**

Add, after the `mgmt_addr` block:

```yaml
# Where the connector's SQLite database file lives, at {data_dir}/dsbox.db.
# Required. Created on first start if it does not exist.
data_dir: ./data
```

And add a commented `validity_until` example under the existing `datasets` block:

```yaml
datasets:
  - id: urn:dataset:sample
    # Optional. RFC 3339. Absent means the offer never expires. Uncomment to
    # try the validity-period policy:
    # validity_until: 2027-01-01T00:00:00Z
```

- [ ] **Step 6: Update `test/tck/dsbox.yaml`**

Add, after `mgmt_addr: 0.0.0.0:8081`:

```yaml
# Negotiation state lives here. The harness container gets a fresh one on
# every run (see the compose volume).
data_dir: /data
```

(Task 6 adds the three CN datasets to this same file; this step only unblocks `Load` so the harness config stays parseable through the rest of this plan.)

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/config/...`
Expected: PASS, all tests.

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go config.example.yaml test/tck/dsbox.yaml
git commit -m "feat: add data_dir and validity_until configuration"
```

---

### Task 3: Storage — `internal/store`

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/store_test.go`
- Modify: `go.mod`, `go.sum` (via `go get`)

**Interfaces:**
- Consumes: nothing from other tasks (this package does not import `internal/config` or `internal/dsp`).
- Produces:
  - `store.Open(path string) (*Store, error)`
  - `(*Store).Close() error`
  - `store.NewUUID() (string, error)`
  - `(*Store).Create(n Negotiation) error`
  - `(*Store).Get(providerPID string) (Negotiation, bool, error)`
  - `(*Store).SetState(providerPID, state string, updatedAt time.Time) error`
  - `Negotiation{ProviderPID, ConsumerPID, State, DatasetID, OfferID, CallbackAddress string; CreatedAt, UpdatedAt time.Time}`

  Task 4 and Task 5 both import this package and use every symbol above.

- [ ] **Step 1: Add the dependency**

```bash
go get modernc.org/sqlite
```

This updates `go.mod` and `go.sum`. `modernc.org/sqlite` is a pure-Go SQLite driver (no CGO), already approved for this milestone — see the design spec's Architecture section and `DECISIONS.md` §7/§16 (single static binary, cross-compiled by goreleaser).

- [ ] **Step 2: Write the failing tests**

Create `internal/store/store_test.go`:

```go
package store

import (
	"testing"
	"time"
)

func testNegotiation() Negotiation {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	return Negotiation{
		ProviderPID:     "urn:uuid:provider-1",
		ConsumerPID:     "urn:uuid:consumer-1",
		State:           "REQUESTED",
		DatasetID:       "urn:dataset:a",
		OfferID:         "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func TestCreateAndGet(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testNegotiation()
	if err := s.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, ok, err := s.Get(n.ProviderPID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: not found, want the created negotiation")
	}
	if got.ProviderPID != n.ProviderPID || got.ConsumerPID != n.ConsumerPID ||
		got.State != n.State || got.DatasetID != n.DatasetID ||
		got.OfferID != n.OfferID || got.CallbackAddress != n.CallbackAddress {
		t.Errorf("Get returned %+v, want %+v", got, n)
	}
	if !got.CreatedAt.Equal(n.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, n.CreatedAt)
	}
	if !got.UpdatedAt.Equal(n.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, n.UpdatedAt)
	}
}

func TestGetMissingReturnsFalse(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	_, ok, err := s.Get("does-not-exist")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("Get: found a negotiation that was never created")
	}
}

func TestSetStateUpdatesRow(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testNegotiation()
	if err := s.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
	}

	updatedAt := n.UpdatedAt.Add(time.Hour)
	if err := s.SetState(n.ProviderPID, "AGREED", updatedAt); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	got, ok, err := s.Get(n.ProviderPID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: not found after SetState")
	}
	if got.State != "AGREED" {
		t.Errorf("State = %q, want AGREED", got.State)
	}
	if !got.UpdatedAt.Equal(updatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, updatedAt)
	}
}

func TestSetStateMissingIsError(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SetState("does-not-exist", "AGREED", time.Now()); err == nil {
		t.Error("SetState: expected an error updating a negotiation that does not exist")
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dsbox.db"

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	n := testNegotiation()
	if err := s1.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	got, ok, err := s2.Get(n.ProviderPID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: the row created before reopening the store is gone")
	}
	if got.ProviderPID != n.ProviderPID {
		t.Errorf("ProviderPID = %q, want %q", got.ProviderPID, n.ProviderPID)
	}
}

func TestNewUUIDIsUnique(t *testing.T) {
	a, err := NewUUID()
	if err != nil {
		t.Fatalf("NewUUID: %v", err)
	}
	b, err := NewUUID()
	if err != nil {
		t.Fatalf("NewUUID: %v", err)
	}
	if a == "" || b == "" {
		t.Fatal("NewUUID returned an empty string")
	}
	if a == b {
		t.Errorf("two calls to NewUUID both returned %q", a)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/store/...`
Expected: FAIL to compile — the package does not exist yet.

- [ ] **Step 4: Implement the store**

Create `internal/store/store.go`:

```go
// Package store persists connector runtime state — currently the contract
// negotiation state machine — in a single SQLite file.
package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store opens the connector's SQLite database and exposes negotiation
// persistence. database/sql pools and serializes access itself, so a single
// Store is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Negotiation is one persisted contract negotiation.
type Negotiation struct {
	ProviderPID     string
	ConsumerPID     string
	State           string
	DatasetID       string
	OfferID         string
	CallbackAddress string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS negotiations (
    provider_pid     TEXT PRIMARY KEY,
    consumer_pid     TEXT NOT NULL,
    state            TEXT NOT NULL,
    dataset_id       TEXT NOT NULL,
    offer_id         TEXT NOT NULL,
    callback_address TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);`

const timeFormat = time.RFC3339Nano

// Open opens (creating if necessary) the SQLite file at path, enables WAL
// mode, and ensures the schema exists. path may be ":memory:" for tests —
// DECISIONS.md section 8 reserves that for tests only, never a runtime path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL on %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema in %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// NewUUID generates a random RFC 4122 v4 UUID string. It is used both for a
// new negotiation's provider pid and for the @id of every outgoing DSP
// message. crypto/rand rather than a UUID package: CLAUDE.md's default
// answer to a dependency question is the standard library, and this project
// fully controls the value's shape.
func NewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Create persists a new negotiation.
func (s *Store) Create(n Negotiation) error {
	_, err := s.db.Exec(
		`INSERT INTO negotiations (provider_pid, consumer_pid, state, dataset_id, offer_id, callback_address, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ProviderPID, n.ConsumerPID, n.State, n.DatasetID, n.OfferID, n.CallbackAddress,
		n.CreatedAt.UTC().Format(timeFormat), n.UpdatedAt.UTC().Format(timeFormat),
	)
	if err != nil {
		return fmt.Errorf("create negotiation %s: %w", n.ProviderPID, err)
	}
	return nil
}

// Get returns the negotiation with the given provider pid.
func (s *Store) Get(providerPID string) (Negotiation, bool, error) {
	row := s.db.QueryRow(
		`SELECT provider_pid, consumer_pid, state, dataset_id, offer_id, callback_address, created_at, updated_at
		 FROM negotiations WHERE provider_pid = ?`, providerPID)

	var n Negotiation
	var created, updated string
	err := row.Scan(&n.ProviderPID, &n.ConsumerPID, &n.State, &n.DatasetID, &n.OfferID,
		&n.CallbackAddress, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Negotiation{}, false, nil
	}
	if err != nil {
		return Negotiation{}, false, fmt.Errorf("get negotiation %s: %w", providerPID, err)
	}
	if n.CreatedAt, err = time.Parse(timeFormat, created); err != nil {
		return Negotiation{}, false, fmt.Errorf("get negotiation %s: parse created_at: %w", providerPID, err)
	}
	if n.UpdatedAt, err = time.Parse(timeFormat, updated); err != nil {
		return Negotiation{}, false, fmt.Errorf("get negotiation %s: parse updated_at: %w", providerPID, err)
	}
	return n, true, nil
}

// SetState updates a negotiation's state and updated_at.
func (s *Store) SetState(providerPID, state string, updatedAt time.Time) error {
	res, err := s.db.Exec(`UPDATE negotiations SET state = ?, updated_at = ? WHERE provider_pid = ?`,
		state, updatedAt.UTC().Format(timeFormat), providerPID)
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	if rows == 0 {
		return fmt.Errorf("update negotiation %s: not found", providerPID)
	}
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/store/...`
Expected: PASS, all tests.

- [ ] **Step 6: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go go.mod go.sum
git commit -m "feat: add SQLite-backed negotiation storage"
```

---

### Task 4: Negotiation state machine and message documents

**Files:**
- Create: `internal/dsp/negotiation.go`
- Create: `internal/dsp/negotiation_test.go`

**Interfaces:**
- Consumes: `store.Negotiation`, `store.NewUUID() (string, error)` (Task 3); `config.Config`, `config.Dataset` (Task 2, already existed); `offerIDSuffix`, `useAction`, `OfferType`, `Permission{Action string}`, `ContextURL` (already exist in `internal/dsp/catalog.go` and `internal/dsp/version.go` — same package, no import needed).
- Produces (all consumed by Task 5):
  - State constants: `StateRequested`, `StateOffered`, `StateAccepted`, `StateAgreed`, `StateVerified`, `StateFinalized`, `StateTerminated` (strings, exact DSP state names)
  - Message-type constants: `ContractRequestMessageType`, `ContractNegotiationEventMessageType`, `ContractAgreementVerificationMessageType`, `ContractNegotiationTerminationMessageType`, `ContractNegotiationType`, `ContractNegotiationErrorType`
  - `RequestMessage{Context []string; Type string; ConsumerPID string; Offer OfferRef; CallbackAddress string}`, `OfferRef{ID, Target string}`
  - `negotiationOutcome{state string; pushOffer, pushAgreement, pushTermination bool}` and vars `outcomeNone`, `outcomeOffer`, `outcomeAgree`, `outcomeTerminate`, `outcomeOfferThenTerminate`
  - `decideInitialRequest(cfg config.Config, datasetID, offerID string, now time.Time) negotiationOutcome`
  - `decideAccept(cfg config.Config, datasetID string, now time.Time) negotiationOutcome`
  - `decideReRequest(currentOfferID, requestedOfferID string) bool` (true means: identical offer, reject synchronously)
  - `buildNegotiationStateDocument(n store.Negotiation) NegotiationStateDocument`
  - `buildOfferMessage(n store.Negotiation) OfferMessage`
  - `buildAgreementMessage(n store.Negotiation, publicURL string) AgreementMessage`
  - `buildFinalizedEventMessage(n store.Negotiation) EventMessage`
  - `buildTerminationMessage(n store.Negotiation) TerminationMessage`

This task has no HTTP in it at all — every function is pure (or, for the `build*` functions, only touches `store.NewUUID` and `time.Now`), which is what makes it fully unit-testable without `httptest`.

- [ ] **Step 1: Write the failing tests for the decision functions**

Create `internal/dsp/negotiation_test.go`:

```go
package dsp

import (
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

func cfgWithDataset(id string, validityUntil *time.Time) config.Config {
	return config.Config{Datasets: []config.Dataset{{ID: id, ValidityUntil: validityUntil}}}
}

func TestDecideInitialRequest_UnknownDataset_TakesNoAction(t *testing.T) {
	cfg := cfgWithDataset("urn:dataset:known", nil)
	got := decideInitialRequest(cfg, "urn:dataset:unknown", "urn:dataset:unknown#offer", time.Now())
	if got != outcomeNone {
		t.Errorf("decideInitialRequest = %+v, want outcomeNone", got)
	}
}

func TestDecideInitialRequest_MatchedValid_Agrees(t *testing.T) {
	cfg := cfgWithDataset("urn:dataset:a", nil)
	got := decideInitialRequest(cfg, "urn:dataset:a", "urn:dataset:a"+offerIDSuffix, time.Now())
	if got != outcomeAgree {
		t.Errorf("decideInitialRequest = %+v, want outcomeAgree", got)
	}
}

func TestDecideInitialRequest_MatchedExpired_Terminates(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cfg := cfgWithDataset("urn:dataset:a", &past)
	got := decideInitialRequest(cfg, "urn:dataset:a", "urn:dataset:a"+offerIDSuffix, time.Now())
	if got != outcomeTerminate {
		t.Errorf("decideInitialRequest = %+v, want outcomeTerminate", got)
	}
}

func TestDecideInitialRequest_MismatchedValid_Offers(t *testing.T) {
	cfg := cfgWithDataset("urn:dataset:a", nil)
	got := decideInitialRequest(cfg, "urn:dataset:a", "urn:dataset:a#some-other-offer", time.Now())
	if got != outcomeOffer {
		t.Errorf("decideInitialRequest = %+v, want outcomeOffer", got)
	}
}

func TestDecideInitialRequest_MismatchedExpired_OffersThenTerminates(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cfg := cfgWithDataset("urn:dataset:a", &past)
	got := decideInitialRequest(cfg, "urn:dataset:a", "urn:dataset:a#some-other-offer", time.Now())
	if got != outcomeOfferThenTerminate {
		t.Errorf("decideInitialRequest = %+v, want outcomeOfferThenTerminate", got)
	}
}

func TestDecideAccept_Valid_Agrees(t *testing.T) {
	cfg := cfgWithDataset("urn:dataset:a", nil)
	got := decideAccept(cfg, "urn:dataset:a", time.Now())
	if got != outcomeAgree {
		t.Errorf("decideAccept = %+v, want outcomeAgree", got)
	}
}

func TestDecideAccept_Expired_Terminates(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cfg := cfgWithDataset("urn:dataset:a", &past)
	got := decideAccept(cfg, "urn:dataset:a", time.Now())
	if got != outcomeTerminate {
		t.Errorf("decideAccept = %+v, want outcomeTerminate", got)
	}
}

func TestDecideAccept_NoLongerAdvertised_Terminates(t *testing.T) {
	cfg := cfgWithDataset("urn:dataset:other", nil)
	got := decideAccept(cfg, "urn:dataset:a", time.Now())
	if got != outcomeTerminate {
		t.Errorf("decideAccept = %+v, want outcomeTerminate", got)
	}
}

func TestDecideReRequest_SameOffer_IsSynchronousReject(t *testing.T) {
	if !decideReRequest("urn:dataset:a#offer", "urn:dataset:a#offer") {
		t.Error("decideReRequest = false, want true for an identical offer")
	}
}

func TestDecideReRequest_DifferentOffer_IsNotSynchronousReject(t *testing.T) {
	if decideReRequest("urn:dataset:a#offer", "urn:dataset:a#different-offer") {
		t.Error("decideReRequest = true, want false for a different offer")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run TestDecide`
Expected: compile failure — none of `decideInitialRequest`, `decideAccept`, `decideReRequest`, `outcomeNone`, etc. exist yet.

- [ ] **Step 3: Implement the state constants, message-type constants, and decision logic**

Create `internal/dsp/negotiation.go` (part 1 of 2 — this step covers everything through the decision functions; Step 5 below adds the message document types and builders to the same file):

```go
// Package dsp implements the DSP protocol handlers. This file holds the
// contract negotiation state machine's decision logic and the shapes of the
// messages it exchanges — no HTTP. negotiation_handler.go wires HTTP onto
// this.
package dsp

import (
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// DSP contract negotiation states, exactly as named in the spec. They are
// also what this project stores and what GET /negotiations/{id} reports:
// there is no separate internal representation.
const (
	StateRequested  = "REQUESTED"
	StateOffered    = "OFFERED"
	StateAccepted   = "ACCEPTED"
	StateAgreed     = "AGREED"
	StateVerified   = "VERIFIED"
	StateFinalized  = "FINALIZED"
	StateTerminated = "TERMINATED"
)

// DSP negotiation message @type names.
const (
	ContractRequestMessageType                = "ContractRequestMessage"
	ContractOfferMessageType                  = "ContractOfferMessage"
	ContractNegotiationEventMessageType       = "ContractNegotiationEventMessage"
	ContractAgreementMessageType              = "ContractAgreementMessage"
	ContractAgreementVerificationMessageType  = "ContractAgreementVerificationMessage"
	ContractNegotiationTerminationMessageType = "ContractNegotiationTerminationMessage"
	ContractNegotiationType                   = "ContractNegotiation"
	ContractNegotiationErrorType              = "ContractNegotiationError"

	// AgreementType is the ODRL @type of the agreement node nested inside a
	// ContractAgreementMessage.
	AgreementType = "Agreement"

	eventTypeAccepted  = "ACCEPTED"
	eventTypeFinalized = "FINALIZED"

	// terminationCode is the value this connector sends as a termination
	// message's "code" field. DSP leaves the value's vocabulary
	// implementation-defined; the TCK's own test code uses the literal "1"
	// for the same field when it plays the consumer role, so this matches
	// that precedent rather than inventing a new one.
	terminationCode = "1"
)

// RequestMessage is the body of POST /negotiations/request and
// POST /negotiations/{id}/request — a ContractRequestMessage, whether it is
// the initial request or a counter-offer/resend. Only the fields this
// connector inspects are declared, matching the direct-field-check approach
// DECISIONS.md section 22.5 established for the catalog protocol.
type RequestMessage struct {
	Context         []string `json:"@context"`
	Type            string   `json:"@type"`
	ConsumerPID     string   `json:"consumerPid"`
	Offer           OfferRef `json:"offer"`
	CallbackAddress string   `json:"callbackAddress"`
}

// OfferRef is the nested offer object inside a RequestMessage. Its own
// @type is deliberately not read: the TCK's own source marks that field
// "@DspTestingWorkaround(Remove @type)", so parsing must not depend on it.
type OfferRef struct {
	ID     string `json:"@id"`
	Target string `json:"target"`
}

// negotiationOutcome is what the provider decides to do in response to a
// contract request or an accept event: the negotiation's next state, and
// what to push to the consumer's callback address. pushOffer and
// pushTermination can both be set — an expired, mismatched dataset gets an
// informational counter-offer followed immediately by an unprompted
// termination, since there is nothing left to agree to.
type negotiationOutcome struct {
	state           string
	pushOffer       bool
	pushAgreement   bool
	pushTermination bool
}

var (
	// outcomeNone: the dataset is not advertised at all. The provider has
	// nothing coherent to say about it, so the negotiation stays REQUESTED
	// with no autonomous action.
	outcomeNone = negotiationOutcome{state: StateRequested}
	// outcomeOffer: the requested offer does not match what this connector
	// advertises, but the dataset's policy is currently valid.
	outcomeOffer = negotiationOutcome{state: StateOffered, pushOffer: true}
	// outcomeAgree: the requested offer matches and is currently valid.
	outcomeAgree = negotiationOutcome{state: StateAgreed, pushAgreement: true}
	// outcomeTerminate: either the offer matches but has expired, or an
	// ACCEPTED event arrived for a dataset that is no longer valid or no
	// longer advertised.
	outcomeTerminate = negotiationOutcome{state: StateTerminated, pushTermination: true}
	// outcomeOfferThenTerminate: the offer does not match AND the dataset has
	// expired. The true terms are still worth telling the consumer, so the
	// offer is pushed; then, since there is nothing left to agree to, an
	// unprompted termination follows.
	outcomeOfferThenTerminate = negotiationOutcome{state: StateOffered, pushOffer: true, pushTermination: true}
)

// findConfiguredDataset returns the advertised dataset configuration with
// the given identifier. Unlike findDataset in catalog.go, this returns the
// raw config.Dataset (for its ValidityUntil), not the built catalog document.
func findConfiguredDataset(cfg config.Config, id string) (config.Dataset, bool) {
	for _, d := range cfg.Datasets {
		if d.ID == id {
			return d, true
		}
	}
	return config.Dataset{}, false
}

// isValid reports whether d's offer is currently valid: it has no
// validity_until, or now is before it.
func isValid(d config.Dataset, now time.Time) bool {
	return d.ValidityUntil == nil || now.Before(*d.ValidityUntil)
}

// decideInitialRequest implements the offer/agreement divergence rule from
// the design spec's "offer/agreement divergence" section: a plain comparison
// against what this connector advertises for datasetID, plus a validity
// check.
func decideInitialRequest(cfg config.Config, datasetID, offerID string, now time.Time) negotiationOutcome {
	ds, ok := findConfiguredDataset(cfg, datasetID)
	if !ok {
		return outcomeNone
	}
	matches := offerID == ds.ID+offerIDSuffix
	valid := isValid(ds, now)
	switch {
	case matches && valid:
		return outcomeAgree
	case matches && !valid:
		return outcomeTerminate
	case !matches && valid:
		return outcomeOffer
	default:
		return outcomeOfferThenTerminate
	}
}

// decideAccept implements the ACCEPTED -> AGREED re-check: an accept only
// advances the negotiation if the dataset is still advertised and still
// valid at the moment of acceptance.
func decideAccept(cfg config.Config, datasetID string, now time.Time) negotiationOutcome {
	ds, ok := findConfiguredDataset(cfg, datasetID)
	if !ok || !isValid(ds, now) {
		return outcomeTerminate
	}
	return outcomeAgree
}

// decideReRequest implements the CN:03-04 vs CN:01-02 distinction: another
// request while OFFERED is a synchronous rejection when it repeats the exact
// offer already on the table, and an asynchronous termination otherwise. See
// the design spec's Risks section — this rule is inferred from the TCK
// source, not confirmed by a targeted assertion, and the first real TCK run
// against both tests is what confirms it.
func decideReRequest(currentOfferID, requestedOfferID string) bool {
	return requestedOfferID == currentOfferID
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/... -run TestDecide`
Expected: PASS, all ten.

- [ ] **Step 5: Write the failing tests for the message-document builders**

Append to `internal/dsp/negotiation_test.go`:

```go
func testStoredNegotiation() store.Negotiation {
	return store.Negotiation{
		ProviderPID:     "urn:uuid:provider-1",
		ConsumerPID:     "urn:uuid:consumer-1",
		State:           StateOffered,
		DatasetID:       "urn:dataset:a",
		OfferID:         "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
	}
}

func TestBuildNegotiationStateDocument(t *testing.T) {
	n := testStoredNegotiation()
	doc := buildNegotiationStateDocument(n)
	if doc.Type != ContractNegotiationType {
		t.Errorf("Type = %q, want %q", doc.Type, ContractNegotiationType)
	}
	if doc.ProviderPID != n.ProviderPID || doc.ConsumerPID != n.ConsumerPID || doc.State != n.State {
		t.Errorf("doc = %+v, want it to carry n's identifiers and state", doc)
	}
	if doc.ID == "" {
		t.Error("ID is empty, want a generated message id")
	}
	if len(doc.Context) == 0 || doc.Context[0] != ContextURL {
		t.Errorf("Context = %v, want it to contain %q", doc.Context, ContextURL)
	}
}

func TestBuildOfferMessage(t *testing.T) {
	n := testStoredNegotiation()
	msg := buildOfferMessage(n)
	if msg.Type != ContractOfferMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractOfferMessageType)
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
	wantOfferID := n.DatasetID + offerIDSuffix
	if msg.Offer.ID != wantOfferID {
		t.Errorf("Offer.ID = %q, want %q (the connector's canonical offer, not the requested one)", msg.Offer.ID, wantOfferID)
	}
	if msg.Offer.Target != n.DatasetID {
		t.Errorf("Offer.Target = %q, want %q", msg.Offer.Target, n.DatasetID)
	}
	if len(msg.Offer.Permission) != 1 || msg.Offer.Permission[0].Action != useAction {
		t.Errorf("Offer.Permission = %v, want one permission with action %q", msg.Offer.Permission, useAction)
	}
}

func TestBuildAgreementMessage(t *testing.T) {
	n := testStoredNegotiation()
	msg := buildAgreementMessage(n, "https://provider.example.org")
	if msg.Type != ContractAgreementMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractAgreementMessageType)
	}
	if msg.Agreement.Target != n.DatasetID {
		t.Errorf("Agreement.Target = %q, want %q", msg.Agreement.Target, n.DatasetID)
	}
	if msg.Agreement.Type != AgreementType {
		t.Errorf("Agreement.Type = %q, want %q", msg.Agreement.Type, AgreementType)
	}
	if msg.Agreement.Timestamp == "" {
		t.Error("Agreement.Timestamp is empty")
	}
	if msg.CallbackAddress != "https://provider.example.org"+VersionPath {
		t.Errorf("CallbackAddress = %q, want the provider's own address", msg.CallbackAddress)
	}
}

func TestBuildFinalizedEventMessage(t *testing.T) {
	n := testStoredNegotiation()
	msg := buildFinalizedEventMessage(n)
	if msg.Type != ContractNegotiationEventMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractNegotiationEventMessageType)
	}
	if msg.EventType != eventTypeFinalized {
		t.Errorf("EventType = %q, want %q", msg.EventType, eventTypeFinalized)
	}
}

func TestBuildTerminationMessage(t *testing.T) {
	n := testStoredNegotiation()
	msg := buildTerminationMessage(n)
	if msg.Type != ContractNegotiationTerminationMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractNegotiationTerminationMessageType)
	}
	if msg.Code == "" {
		t.Error("Code is empty")
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
}
```

Add `"github.com/kimjoin2/dataspace-in-a-box/internal/store"` to the test file's imports.

- [ ] **Step 6: Run the tests to verify they fail**

Run: `go test ./internal/dsp/...`
Expected: compile failure — the message types and `build*` functions do not exist yet.

- [ ] **Step 7: Implement the message documents and builders**

Append to `internal/dsp/negotiation.go`:

```go
// NegotiationOffer is the ODRL offer object carried in negotiation protocol
// messages. Unlike catalog.go's Offer (which never carries a target — the
// schema forbids it there), a negotiation offer always names its target
// dataset explicitly.
type NegotiationOffer struct {
	ID         string       `json:"@id"`
	Type       string       `json:"@type"`
	Target     string       `json:"target"`
	Permission []Permission `json:"permission"`
}

// OfferMessage is the ContractOfferMessage pushed to a consumer's callback
// address when the requested offer does not match what this connector
// advertises.
type OfferMessage struct {
	Context     []string         `json:"@context"`
	ID          string           `json:"@id"`
	Type        string           `json:"@type"`
	ProviderPID string           `json:"providerPid"`
	ConsumerPID string           `json:"consumerPid"`
	Offer       NegotiationOffer `json:"offer"`
}

// Agreement is the ODRL agreement node nested inside an AgreementMessage.
type Agreement struct {
	ID         string       `json:"@id"`
	Type       string       `json:"@type"`
	Target     string       `json:"target"`
	Permission []Permission `json:"permission"`
	Timestamp  string       `json:"timestamp"`
}

// AgreementMessage is the ContractAgreementMessage pushed to a consumer when
// the requested offer matches and is currently valid.
type AgreementMessage struct {
	Context         []string  `json:"@context"`
	ID              string    `json:"@id"`
	Type            string    `json:"@type"`
	ProviderPID     string    `json:"providerPid"`
	ConsumerPID     string    `json:"consumerPid"`
	Agreement       Agreement `json:"agreement"`
	CallbackAddress string    `json:"callbackAddress"`
}

// EventMessage is the ContractNegotiationEventMessage this connector pushes
// for the FINALIZED transition. The ACCEPTED direction is sent by the
// consumer and parsed, not built, by this connector.
type EventMessage struct {
	Context     []string `json:"@context"`
	ID          string   `json:"@id"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
	EventType   string   `json:"eventType"`
}

// TerminationReason is one entry in a TerminationMessage's reason array —
// a different shape from CatalogError.reason, which is an array of plain
// strings. The two are different DSP fields.
type TerminationReason struct {
	Message string `json:"message"`
}

// TerminationMessage is the ContractNegotiationTerminationMessage, pushed by
// the provider or received from the consumer.
type TerminationMessage struct {
	Context     []string             `json:"@context"`
	ID          string               `json:"@id"`
	Type        string               `json:"@type"`
	ProviderPID string               `json:"providerPid"`
	ConsumerPID string               `json:"consumerPid"`
	Code        string               `json:"code"`
	Reason      []TerminationReason  `json:"reason,omitempty"`
}

// NegotiationStateDocument is the ContractNegotiation state document served
// by GET /negotiations/{id} and returned synchronously from
// POST /negotiations/request.
type NegotiationStateDocument struct {
	Context     []string `json:"@context"`
	ID          string   `json:"@id"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
	State       string   `json:"state"`
}

// newMessageID generates this message's own @id. A generation failure here
// means the OS's CSPRNG failed, which this project's own principles say not
// to build a fallback path for — the zero value degrades to an empty string
// rather than crashing the handler.
func newMessageID() string {
	id, _ := store.NewUUID()
	return id
}

func buildNegotiationStateDocument(n store.Negotiation) NegotiationStateDocument {
	return NegotiationStateDocument{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		State:       n.State,
	}
}

func buildOfferMessage(n store.Negotiation) OfferMessage {
	return OfferMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractOfferMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		Offer: NegotiationOffer{
			ID:         n.DatasetID + offerIDSuffix,
			Type:       OfferType,
			Target:     n.DatasetID,
			Permission: []Permission{{Action: useAction}},
		},
	}
}

// buildAgreementMessage builds the agreement pushed on the AGREED
// transition. publicURL is this connector's own address (config.Config's
// PublicURL) — the design spec's Risks section notes that whether the wire
// actually requires this field is unconfirmed; it is included on the
// evidence available and the first real TCK run will say if it was
// unnecessary.
func buildAgreementMessage(n store.Negotiation, publicURL string) AgreementMessage {
	return AgreementMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractAgreementMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		Agreement: Agreement{
			ID:         n.ProviderPID,
			Type:       AgreementType,
			Target:     n.DatasetID,
			Permission: []Permission{{Action: useAction}},
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
		},
		CallbackAddress: publicURL + VersionPath,
	}
}

func buildFinalizedEventMessage(n store.Negotiation) EventMessage {
	return EventMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationEventMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		EventType:   eventTypeFinalized,
	}
}

func buildTerminationMessage(n store.Negotiation) TerminationMessage {
	return TerminationMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationTerminationMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		Code:        terminationCode,
	}
}
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/dsp/...`
Expected: PASS, all tests (the existing catalog tests too — this task adds a new file, it does not touch `catalog.go`).

- [ ] **Step 9: Commit**

```bash
git add internal/dsp/negotiation.go internal/dsp/negotiation_test.go
git commit -m "feat: add the contract negotiation state machine and message documents"
```

---

### Task 5: Negotiation handlers, callback push, router, and `main.go` wiring

**Files:**
- Create: `internal/dsp/callback.go`
- Create: `internal/dsp/callback_test.go`
- Create: `internal/dsp/negotiation_handler.go`
- Create: `internal/dsp/negotiation_handler_test.go`
- Modify: `internal/dsp/router.go`
- Modify: `cmd/dsbox/main.go`

**Interfaces:**
- Consumes: everything from Task 3 (`store.*`) and Task 4 (`decide*`, `Build*`, `Negotiation*` types, `Contract*Type` constants, `State*` constants) plus the existing `writeError`/`writeJSON`/`ContextURL`/`VersionPath` from `internal/dsp/error.go`/`version.go`.
- Produces: `NewRouter(cfg config.Config, st *store.Store) http.Handler` — this **changes NewRouter's existing signature**, which is why `main.go` must be updated in this same task. No later task in this plan depends on anything from this task.

- [ ] **Step 1: Write the failing tests for the callback pusher**

Create `internal/dsp/callback_test.go`:

```go
package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPushCallbackSendsJSON(t *testing.T) {
	received := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode pushed body: %v", err)
		}
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pushCallback(srv.URL, map[string]string{"hello": "world"})

	select {
	case body := <-received:
		if body["hello"] != "world" {
			t.Errorf("received %v, want {hello: world}", body)
		}
	default:
		t.Fatal("pushCallback did not send a request before returning")
	}
}

func TestPushCallbackToUnreachableURLDoesNotPanic(t *testing.T) {
	pushCallback("http://127.0.0.1:1/unreachable", map[string]string{"hello": "world"})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run TestPushCallback`
Expected: compile failure — `pushCallback` does not exist yet.

- [ ] **Step 3: Implement `pushCallback`**

Create `internal/dsp/callback.go`:

```go
package dsp

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
)

// pushCallback sends v as a JSON POST to url, best-effort: a failure is
// logged and never returned to the caller. The provider is authoritative
// over negotiation state in this protocol, so a dropped push does not
// corrupt anything a consumer cannot recover from GET /negotiations/{id};
// no retry queue is built for v1.
func pushCallback(url string, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal callback push", "url", url, "error", err)
		return
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("push callback", "url", url, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Error("callback endpoint rejected push", "url", url, "status", resp.StatusCode)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/... -run TestPushCallback`
Expected: PASS.

- [ ] **Step 5: Write the failing tests for the six handlers**

Create `internal/dsp/negotiation_handler_test.go`:

```go
package dsp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// fakeCallback records every push it receives, keyed by the request path it
// arrived on (e.g. "/negotiations/<pid>/offers").
type fakeCallback struct {
	mu    sync.Mutex
	pushes map[string][]map[string]any
	srv   *httptest.Server
}

func newFakeCallback() *fakeCallback {
	fc := &fakeCallback{pushes: make(map[string][]map[string]any)}
	fc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		fc.mu.Lock()
		fc.pushes[r.URL.Path] = append(fc.pushes[r.URL.Path], body)
		fc.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	return fc
}

// wait polls until pathSuffix has received at least one push, or fails the
// test after one second. terminateAfterOfferDelay is overridden to a few
// milliseconds in the tests that need this, so one second is generous.
func (fc *fakeCallback) wait(t *testing.T, pathSuffix string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		for path, pushes := range fc.pushes {
			if strings.HasSuffix(path, pathSuffix) && len(pushes) > 0 {
				body := pushes[0]
				fc.mu.Unlock()
				return body
			}
		}
		fc.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no push received on a path ending %q within the deadline", pathSuffix)
	return nil
}

func (fc *fakeCallback) neverReceives(t *testing.T, pathSuffix string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		for path := range fc.pushes {
			if strings.HasSuffix(path, pathSuffix) {
				fc.mu.Unlock()
				t.Fatalf("unexpected push received on a path ending %q", pathSuffix)
			}
		}
		fc.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
}

func negotiationTestConfig(publicURL string, datasets ...config.Dataset) config.Config {
	return config.Config{PublicURL: publicURL, Datasets: datasets}
}

func newTestHandler(t *testing.T, cfg config.Config) (negotiationHandler, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return negotiationHandler{cfg: cfg, store: st}, st
}

func postJSON(t *testing.T, handler http.HandlerFunc, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(b))
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func requestMessageBody(consumerPID, offerID, datasetID, callbackAddress string) map[string]any {
	return map[string]any{
		"@context":        ContextURL,
		"@type":           ContractRequestMessageType,
		"consumerPid":     consumerPID,
		"offer":           map[string]any{"@id": offerID, "target": datasetID},
		"callbackAddress": callbackAddress,
	}
}

func TestHandleContractRequest_UnknownDataset_NoPush(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:known"}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:unknown#offer", "urn:dataset:unknown", fc.srv.URL))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body)
	}
	var doc NegotiationStateDocument
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if doc.State != StateRequested {
		t.Errorf("State = %q, want %q", doc.State, StateRequested)
	}

	fc.neverReceives(t, "/offers", 100*time.Millisecond)
	fc.neverReceives(t, "/agreement", 100*time.Millisecond)
	fc.neverReceives(t, "/termination", 100*time.Millisecond)

	n, ok, err := st.Get(doc.ProviderPID)
	if err != nil || !ok {
		t.Fatalf("store.Get(%q): ok=%v err=%v", doc.ProviderPID, ok, err)
	}
	if n.State != StateRequested {
		t.Errorf("stored state = %q, want %q", n.State, StateRequested)
	}
}

func TestHandleContractRequest_MatchedValid_PushesAgreement(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:a"+offerIDSuffix, "urn:dataset:a", fc.srv.URL))
	var doc NegotiationStateDocument
	json.Unmarshal(rr.Body.Bytes(), &doc)

	push := fc.wait(t, "/agreement")
	if push["providerPid"] != doc.ProviderPID {
		t.Errorf("pushed providerPid = %v, want %v", push["providerPid"], doc.ProviderPID)
	}

	n, _, _ := st.Get(doc.ProviderPID)
	if n.State != StateAgreed {
		t.Errorf("stored state = %q, want %q", n.State, StateAgreed)
	}
}

func TestHandleContractRequest_MatchedExpired_PushesTermination(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	past := time.Now().Add(-time.Hour)
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a", ValidityUntil: &past}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:a"+offerIDSuffix, "urn:dataset:a", fc.srv.URL))
	var doc NegotiationStateDocument
	json.Unmarshal(rr.Body.Bytes(), &doc)

	fc.wait(t, "/termination")
	fc.neverReceives(t, "/offers", 50*time.Millisecond)
	fc.neverReceives(t, "/agreement", 50*time.Millisecond)

	n, _, _ := st.Get(doc.ProviderPID)
	if n.State != StateTerminated {
		t.Errorf("stored state = %q, want %q", n.State, StateTerminated)
	}
}

func TestHandleContractRequest_MismatchedValid_PushesOffer(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:a#some-other-offer", "urn:dataset:a", fc.srv.URL))
	var doc NegotiationStateDocument
	json.Unmarshal(rr.Body.Bytes(), &doc)

	push := fc.wait(t, "/offers")
	offer := push["offer"].(map[string]any)
	if offer["@id"] != "urn:dataset:a"+offerIDSuffix {
		t.Errorf("pushed offer @id = %v, want the connector's canonical offer", offer["@id"])
	}

	n, _, _ := st.Get(doc.ProviderPID)
	if n.State != StateOffered {
		t.Errorf("stored state = %q, want %q", n.State, StateOffered)
	}
}

func TestHandleContractRequest_MismatchedExpired_OffersThenTerminates(t *testing.T) {
	orig := terminateAfterOfferDelay
	terminateAfterOfferDelay = 10 * time.Millisecond
	defer func() { terminateAfterOfferDelay = orig }()

	fc := newFakeCallback()
	defer fc.srv.Close()
	past := time.Now().Add(-time.Hour)
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a", ValidityUntil: &past}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:a#some-other-offer", "urn:dataset:a", fc.srv.URL))
	var doc NegotiationStateDocument
	json.Unmarshal(rr.Body.Bytes(), &doc)

	fc.wait(t, "/offers")
	fc.wait(t, "/termination")

	n, _, _ := st.Get(doc.ProviderPID)
	if n.State != StateTerminated {
		t.Errorf("stored state = %q, want %q", n.State, StateTerminated)
	}
}

func TestHandleReRequest_SameOffer_Returns400(t *testing.T) {
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#requested-offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleReRequest, n.ProviderPID,
		map[string]any{
			"@context": ContextURL, "@type": ContractRequestMessageType,
			"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
			"offer": map[string]any{"@id": n.OfferID, "target": n.DatasetID},
		})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body)
	}
}

func TestHandleReRequest_DifferentOffer_PushesTermination(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#requested-offer",
		CallbackAddress: fc.srv.URL,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleReRequest, n.ProviderPID,
		map[string]any{
			"@context": ContextURL, "@type": ContractRequestMessageType,
			"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
			"offer": map[string]any{"@id": "urn:dataset:a#yet-another-offer", "target": n.DatasetID},
		})
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}
	fc.wait(t, "/termination")
}

func TestHandleEvent_Accept_FromOffered_PushesAgreement(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: fc.srv.URL,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleEvent, n.ProviderPID, map[string]any{
		"@context": ContextURL, "@type": ContractNegotiationEventMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID, "eventType": "ACCEPTED",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}
	fc.wait(t, "/agreement")
}

func TestHandleVerification_FromOffered_Returns400(t *testing.T) {
	// CN:03-02: verification is only legal from AGREED.
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleVerification, n.ProviderPID, map[string]any{
		"@context": ContextURL, "@type": ContractAgreementVerificationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body)
	}
}

func TestHandleVerification_FromAccepted_Returns400(t *testing.T) {
	// CN:03-03: verifying immediately after ACCEPTED, before AGREED.
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateAccepted, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleVerification, n.ProviderPID, map[string]any{
		"@context": ContextURL, "@type": ContractAgreementVerificationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body)
	}
}

func TestHandleVerification_FromAgreed_FinalizesAndPushesEvent(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateAgreed, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: fc.srv.URL,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleVerification, n.ProviderPID, map[string]any{
		"@context": ContextURL, "@type": ContractAgreementVerificationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}
	push := fc.wait(t, "/events")
	if push["eventType"] != "FINALIZED" {
		t.Errorf("pushed eventType = %v, want FINALIZED", push["eventType"])
	}

	got, _, _ := st.Get(n.ProviderPID)
	if got.State != StateFinalized {
		t.Errorf("stored state = %q, want %q", got.State, StateFinalized)
	}
}

func TestHandleTermination_FromFinalized_Returns400(t *testing.T) {
	// CN:03-01: terminating a FINALIZED negotiation.
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateFinalized, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleTermination, n.ProviderPID, map[string]any{
		"@context": ContextURL, "@type": ContractNegotiationTerminationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID, "code": "1",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body)
	}
}

func TestHandleTermination_FromOffered_Terminates(t *testing.T) {
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleTermination, n.ProviderPID, map[string]any{
		"@context": ContextURL, "@type": ContractNegotiationTerminationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID, "code": "1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}
	got, _, _ := st.Get(n.ProviderPID)
	if got.State != StateTerminated {
		t.Errorf("stored state = %q, want %q", got.State, StateTerminated)
	}
}

func TestHandleGetNegotiation(t *testing.T) {
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateAgreed, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/negotiations/"+n.ProviderPID, nil)
	req.SetPathValue("id", n.ProviderPID)
	rr := httptest.NewRecorder()
	h.handleGetNegotiation(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}
	var doc NegotiationStateDocument
	json.Unmarshal(rr.Body.Bytes(), &doc)
	if doc.State != StateAgreed {
		t.Errorf("State = %q, want %q", doc.State, StateAgreed)
	}
}

func TestHandleGetNegotiation_Missing_Returns404(t *testing.T) {
	h, _ := newTestHandler(t, negotiationTestConfig("https://provider.example.org"))
	req := httptest.NewRequest(http.MethodGet, "/negotiations/does-not-exist", nil)
	req.SetPathValue("id", "does-not-exist")
	rr := httptest.NewRecorder()
	h.handleGetNegotiation(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// postJSONWithID posts body to a handler that reads r.PathValue("id"), with
// id set the way http.ServeMux would set it after routing.
func postJSONWithID(t *testing.T, handler http.HandlerFunc, id string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/negotiations/"+id, bytes.NewReader(b))
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}
```

- [ ] **Step 6: Run the tests to verify they fail**

Run: `go test ./internal/dsp/...`
Expected: compile failure — `negotiationHandler` and its six `handle*` methods, and `terminateAfterOfferDelay`, do not exist yet.

- [ ] **Step 7: Implement the handlers**

Create `internal/dsp/negotiation_handler.go`:

```go
package dsp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// maxNegotiationRequestBodyBytes bounds every negotiation request body, for
// the same reason catalog_handler.go bounds catalog requests: an unbounded
// read on an unauthenticated public endpoint could exhaust memory before any
// validation runs.
const maxNegotiationRequestBodyBytes = 1 << 20 // 1 MiB

// terminateAfterOfferDelay is how long the provider waits, after pushing an
// informational counter-offer for an expired dataset, before independently
// withdrawing it. The delay exists so a consumer's ACCEPTED event — which
// re-checks validity and would reach the same TERMINATED outcome on its own
// path — has a real chance to arrive first. A var, not a const, so tests can
// shorten it. See the design spec's Risks section: this is a timing
// assumption, not a guarantee.
var terminateAfterOfferDelay = 200 * time.Millisecond

// Callback path suffixes, appended (with the provider pid) to a
// negotiation's stored callback address.
const (
	offerCallbackPath       = "/negotiations/%s/offers"
	agreementCallbackPath   = "/negotiations/%s/agreement"
	eventCallbackPath       = "/negotiations/%s/events"
	terminationCallbackPath = "/negotiations/%s/termination"
)

// negotiationHandler serves the contract negotiation protocol, provider
// role only.
type negotiationHandler struct {
	cfg   config.Config
	store *store.Store
}

// handleContractRequest serves POST /negotiations/request, the only entry
// point into a new negotiation. The synchronous response is always a plain
// REQUESTED acknowledgment — see the design spec's "the protocol is
// asynchronous" section — what the provider decided is pushed afterward.
func (h negotiationHandler) handleContractRequest(w http.ResponseWriter, r *http.Request) {
	var msg RequestMessage
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !slices.Contains(msg.Context, ContextURL) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "@context must contain "+ContextURL)
		return
	}
	if msg.Type != ContractRequestMessageType {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "@type must be "+ContractRequestMessageType)
		return
	}
	if msg.ConsumerPID == "" {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "consumerPid is required")
		return
	}
	if msg.Offer.ID == "" || msg.Offer.Target == "" {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "offer.@id and offer.target are required")
		return
	}
	if msg.CallbackAddress == "" {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "callbackAddress is required")
		return
	}

	providerPID, err := store.NewUUID()
	if err != nil {
		slog.Error("generate provider pid", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	now := time.Now()
	n := store.Negotiation{
		ProviderPID:     providerPID,
		ConsumerPID:     msg.ConsumerPID,
		State:           StateRequested,
		DatasetID:       msg.Offer.Target,
		OfferID:         msg.Offer.ID,
		CallbackAddress: msg.CallbackAddress,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := h.store.Create(n); err != nil {
		slog.Error("create negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, buildNegotiationStateDocument(n))

	outcome := decideInitialRequest(h.cfg, n.DatasetID, n.OfferID, now)
	h.dispatch(n, outcome)
}

// handleReRequest serves POST /negotiations/{id}/request: a consumer
// counter-offer or resend while the negotiation is OFFERED. Resending the
// identical offer is a synchronous rejection (CN:03-04); anything else is
// treated as a decision to walk away, terminated asynchronously (CN:01-02).
func (h negotiationHandler) handleReRequest(w http.ResponseWriter, r *http.Request) {
	n, ok, err := h.lookup(w, r)
	if err != nil || !ok {
		return
	}

	var msg RequestMessage
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !slices.Contains(msg.Context, ContextURL) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "@context must contain "+ContextURL)
		return
	}
	if msg.Type != ContractRequestMessageType {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "@type must be "+ContractRequestMessageType)
		return
	}
	if n.State != StateOffered {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"a counter-offer is only valid from OFFERED, negotiation is "+n.State)
		return
	}
	if decideReRequest(n.OfferID, msg.Offer.ID) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"offer.@id matches the offer already on the table")
		return
	}

	w.WriteHeader(http.StatusOK)
	h.pushAndStore(n, StateTerminated, terminationCallbackPath, buildTerminationMessage(n))
}

// handleEvent serves POST /negotiations/{id}/events, currently only the
// consumer's ACCEPTED event (this connector never receives FINALIZED — it
// sends that one).
func (h negotiationHandler) handleEvent(w http.ResponseWriter, r *http.Request) {
	n, ok, err := h.lookup(w, r)
	if err != nil || !ok {
		return
	}

	var msg struct {
		EventType string `json:"eventType"`
	}
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if msg.EventType != eventTypeAccepted {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "eventType must be ACCEPTED")
		return
	}
	if n.State != StateOffered {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"accept is only valid from OFFERED, negotiation is "+n.State)
		return
	}

	now := time.Now()
	if err := h.store.SetState(n.ProviderPID, StateAccepted, now); err != nil {
		slog.Error("update negotiation state", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateAccepted
	outcome := decideAccept(h.cfg, n.DatasetID, now)
	h.dispatch(n, outcome)
}

// handleVerification serves POST /negotiations/{id}/agreement/verification.
// Verification is only legal from AGREED (CN:03-02, CN:03-03 both violate
// this). VERIFIED -> FINALIZED has no validity check: a negotiation that
// reached AGREED always finalizes on verification — see the design spec's
// note on CN:02-07.
func (h negotiationHandler) handleVerification(w http.ResponseWriter, r *http.Request) {
	n, ok, err := h.lookup(w, r)
	if err != nil || !ok {
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	var msg json.RawMessage
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if n.State != StateAgreed {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"verification is only valid from AGREED, negotiation is "+n.State)
		return
	}

	if err := h.store.SetState(n.ProviderPID, StateVerified, time.Now()); err != nil {
		slog.Error("update negotiation state", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateVerified
	h.pushAndStore(n, StateFinalized, eventCallbackPath, buildFinalizedEventMessage(n))
}

// handleTermination serves POST /negotiations/{id}/termination, from either
// party. It is rejected from FINALIZED (CN:03-01) and from an already
// TERMINATED negotiation — both are terminal states with nothing left to
// terminate.
func (h negotiationHandler) handleTermination(w http.ResponseWriter, r *http.Request) {
	n, ok, err := h.lookup(w, r)
	if err != nil || !ok {
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	var msg json.RawMessage
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if n.State == StateFinalized || n.State == StateTerminated {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"negotiation cannot be terminated from "+n.State)
		return
	}

	if err := h.store.SetState(n.ProviderPID, StateTerminated, time.Now()); err != nil {
		slog.Error("update negotiation state", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetNegotiation serves GET /negotiations/{id}.
func (h negotiationHandler) handleGetNegotiation(w http.ResponseWriter, r *http.Request) {
	n, ok, err := h.lookup(w, r)
	if err != nil || !ok {
		return
	}
	writeJSON(w, http.StatusOK, buildNegotiationStateDocument(n))
}

// lookup resolves {id} to a stored negotiation, writing the appropriate
// error response and returning ok=false if it cannot. Every handler above
// except handleContractRequest starts with this.
func (h negotiationHandler) lookup(w http.ResponseWriter, r *http.Request) (store.Negotiation, bool, error) {
	providerPID := r.PathValue("id")
	n, ok, err := h.store.Get(providerPID)
	if err != nil {
		slog.Error("get negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return store.Negotiation{}, false, err
	}
	if !ok {
		writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+providerPID)
		return store.Negotiation{}, false, nil
	}
	return n, true, nil
}

// dispatch carries out a routing decision: it pushes whatever outcome
// requires to n's callback address and persists the resulting state. It runs
// after the synchronous response has already been written, matching DSP's
// async model.
func (h negotiationHandler) dispatch(n store.Negotiation, outcome negotiationOutcome) {
	switch {
	case outcome.pushOffer && outcome.pushTermination:
		h.pushAndStore(n, StateOffered, offerCallbackPath, buildOfferMessage(n))
		go h.delayedTerminate(n)
	case outcome.pushOffer:
		h.pushAndStore(n, StateOffered, offerCallbackPath, buildOfferMessage(n))
	case outcome.pushAgreement:
		h.pushAndStore(n, StateAgreed, agreementCallbackPath, buildAgreementMessage(n, h.cfg.PublicURL))
	case outcome.pushTermination:
		h.pushAndStore(n, StateTerminated, terminationCallbackPath, buildTerminationMessage(n))
	}
	// outcomeNone falls through: no push, negotiation stays REQUESTED.
}

// delayedTerminate withdraws an informational counter-offer for an expired
// dataset, after terminateAfterOfferDelay. It re-fetches state first: if the
// negotiation moved on while it slept (the consumer's accept arrived and
// reached TERMINATED on its own path, or the consumer terminated first),
// there is nothing left to withdraw.
func (h negotiationHandler) delayedTerminate(n store.Negotiation) {
	time.Sleep(terminateAfterOfferDelay)
	current, ok, err := h.store.Get(n.ProviderPID)
	if err != nil || !ok || current.State != StateOffered {
		return
	}
	h.pushAndStore(current, StateTerminated, terminationCallbackPath, buildTerminationMessage(current))
}

// pushAndStore pushes msg to n's callback address at the given path
// (formatted with the provider pid) and updates the stored state. The push
// happens first, but its failure does not block the state update: the
// provider is authoritative, and a consumer can always recover via GET.
func (h negotiationHandler) pushAndStore(n store.Negotiation, state, path string, msg any) {
	pushCallback(n.CallbackAddress+fmt.Sprintf(path, n.ProviderPID), msg)
	if err := h.store.SetState(n.ProviderPID, state, time.Now()); err != nil {
		slog.Error("update negotiation state", "provider_pid", n.ProviderPID, "error", err)
	}
}
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/dsp/...`
Expected: PASS, all tests.

- [ ] **Step 9: Wire the router and `main.go`**

In `internal/dsp/router.go`, change `NewRouter`:

```go
package dsp

import (
	"net/http"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// NewRouter returns the handler for the public DSP listener. It takes the
// configuration because the catalog is served from it, and the store because
// negotiation state is persisted there.
func NewRouter(cfg config.Config, st *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/dspace-version", handleVersion)

	cat := catalogHandler{cfg: cfg}
	mux.HandleFunc("POST "+VersionPath+"/catalog/request", cat.handleCatalogRequest)
	// The identifier is matched as a single path segment, which is why
	// configuration rejects one containing a slash.
	mux.HandleFunc("GET "+VersionPath+"/catalog/datasets/{id}", cat.handleDatasetRequest)

	neg := negotiationHandler{cfg: cfg, store: st}
	mux.HandleFunc("POST "+VersionPath+"/negotiations/request", neg.handleContractRequest)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/request", neg.handleReRequest)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/events", neg.handleEvent)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/agreement/verification", neg.handleVerification)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/termination", neg.handleTermination)
	mux.HandleFunc("GET "+VersionPath+"/negotiations/{id}", neg.handleGetNegotiation)

	// Transfer process mounts here next, in TCK order. Until then, requests
	// below that path are correctly 404.

	return mux
}
```

In `cmd/dsbox/main.go`, open the store before building the router and close it on shutdown. Add `"path/filepath"` and the store import, then change the section between loading config and building `dspSrv`:

```go
	cfg, err := config.Load(data, os.Getenv)
	if err != nil {
		return fmt.Errorf("load configuration %q: %w", *configPath, err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data_dir %q: %w", cfg.DataDir, err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "dsbox.db"))
	if err != nil {
		return fmt.Errorf("open database in %q: %w", cfg.DataDir, err)
	}
	defer st.Close()

	// These timeouts bound how long a connection can sit idle at each phase,
```

(The `// These timeouts...` comment line marks where the existing code continues unchanged — everything from `dspSrv := &http.Server{` onward stays as it is, except the `Handler` line.)

Change `dspSrv`'s `Handler` field from `dsp.NewRouter(cfg)` to `dsp.NewRouter(cfg, st)`.

Add the two new imports to the import block:

```go
	"path/filepath"
```

and

```go
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
```

- [ ] **Step 10: Build and test**

Run: `go build ./...`
Expected: builds cleanly.

Run: `go test ./...`
Expected: PASS, every package.

- [ ] **Step 11: Manual smoke test**

```bash
mkdir -p /tmp/dsbox-smoke
cat > /tmp/dsbox-smoke/config.yaml <<'EOF'
public_url: http://localhost:8080
dev_mode: true
participant_id: urn:participant:smoke
data_dir: /tmp/dsbox-smoke/data
datasets:
  - id: urn:dataset:smoke
EOF
go run ./cmd/dsbox -config /tmp/dsbox-smoke/config.yaml &
sleep 1
curl -s -X POST http://localhost:8080/2025-1/negotiations/request \
  -H 'content-type: application/json' \
  -d '{"@context":"https://w3id.org/dspace/2025/1/context.jsonld","@type":"ContractRequestMessage","consumerPid":"urn:uuid:smoke-consumer","offer":{"@id":"urn:dataset:smoke#offer","target":"urn:dataset:smoke"},"callbackAddress":"http://localhost:9999/nonexistent"}'
kill %1
```

Expected: a `201` response body with `"state":"https://w3id.org/dspace/2025/1/REQUESTED"`-shaped JSON (a plain `REQUESTED` document — the actual field value is `"REQUESTED"`, unprefixed, matching this project's fixed-compact-form JSON, not JSON-LD-expanded output) and no crash. This is a manual sanity check, not part of the automated suite — Task 6 verifies real TCK compliance.

- [ ] **Step 12: Commit**

```bash
git add internal/dsp/callback.go internal/dsp/callback_test.go \
  internal/dsp/negotiation_handler.go internal/dsp/negotiation_handler_test.go \
  internal/dsp/router.go cmd/dsbox/main.go
git commit -m "feat: add contract negotiation handlers and wire them into the router"
```

---

### Task 6: TCK harness, gate integration, and documentation

**Files:**
- Modify: `test/tck/dsbox.yaml`
- Modify: `test/tck/config.properties`
- Modify: `cmd/tckgate/main.go`
- Modify: `README.md`
- Modify: `docs/follow-ups.md`
- Modify: `DECISIONS.md`

**Interfaces:**
- Consumes: everything built in Tasks 1-5. This task wires it all together against the real TCK and updates every document that names the project's current TCK pass rate.
- Produces: nothing further — this is the milestone's last task.

- [ ] **Step 1: Add the three CN datasets to the TCK harness config**

In `test/tck/dsbox.yaml`, replace the existing `datasets:` block:

```yaml
# The participant and the datasets the CAT and CN suites ask for.
participant_id: urn:participant:dsbox-test
datasets:
  - id: urn:dataset:tck-catalog
  - id: urn:dataset:tck-request
  # CN: a plain, unrestricted dataset. Used wherever a test needs a
  # *recognized* dataset with a mismatched offer id (CN_01_01/02/03,
  # CN_02_02/04).
  - id: urn:dataset:cn-mismatch
  # CN: the dataset CN_01_04 requests, with its offer id matching exactly —
  # the immediate-AGREED path (CN_01_04, CN_02_03).
  - id: urn:dataset:cn-match
  # CN: otherwise identical to the two above, but already past its
  # validity_until — the expired-offer paths (CN_02_01, CN_02_05, CN_02_06).
  - id: urn:dataset:cn-expired
    validity_until: 2020-01-01T00:00:00Z
```

- [ ] **Step 2: Add the CN keys to `test/tck/config.properties`**

Append to `test/tck/config.properties`:

```properties
# Dataset/offer identifiers for the CN suite (provider role).
#
# CN_02_02 is deliberately left with no override: it needs an entirely
# unadvertised identifier, which the TCK's own random default already is.
#
# CN_02_07 is deliberately left with no override too. It is a known,
# tracked gap — see docs/follow-ups.md — and its default random values are
# as good as any other for a test that is expected to fail.

# CN_01: happy path and the offer/agreement divergence rule.
CN_01_01_DATASETID=urn:dataset:cn-mismatch
CN_01_02_DATASETID=urn:dataset:cn-mismatch
CN_01_03_DATASETID=urn:dataset:cn-mismatch
CN_01_04_DATASETID=urn:dataset:cn-match
CN_01_04_OFFERID=urn:dataset:cn-match#offer

# CN_02: termination scenarios.
CN_02_01_DATASETID=urn:dataset:cn-expired
CN_02_01_OFFERID=urn:dataset:cn-expired#offer
CN_02_03_DATASETID=urn:dataset:cn-match
CN_02_03_OFFERID=urn:dataset:cn-match#offer
CN_02_04_DATASETID=urn:dataset:cn-mismatch
CN_02_05_DATASETID=urn:dataset:cn-expired
CN_02_06_DATASETID=urn:dataset:cn-expired

# CN_03: synchronous rejection scenarios. 03-01 needs a negotiation that can
# reach FINALIZED first, so it uses the matching pair; 03-02/03/04 only need
# to reach OFFERED, so they use the mismatched pair.
CN_03_01_DATASETID=urn:dataset:cn-match
CN_03_01_OFFERID=urn:dataset:cn-match#offer
CN_03_02_DATASETID=urn:dataset:cn-mismatch
CN_03_03_DATASETID=urn:dataset:cn-mismatch
CN_03_04_DATASETID=urn:dataset:cn-mismatch
```

- [ ] **Step 3: Add `CN` to the gate's expected map and exemption map**

In `cmd/tckgate/main.go`, change:

```go
var expected = map[string]int{"MET": 1, "CAT": 3}
```

to:

```go
var expected = map[string]int{"MET": 1, "CAT": 3, "CN": 15}
```

and change:

```go
var exempt = map[string]bool{}
```

to:

```go
// exempt names individual gated test IDs that are known to fail and are
// tracked rather than required — see docs/follow-ups.md for each entry's
// story. A suite's count in expected still includes these tests: the gate
// proves the suite ran to completion regardless of exemptions.
//
// CN:02-07 requires an unprompted termination after a negotiation has
// already reached VERIFIED. Every trigger this milestone implements is
// checked once, at accept-time; VERIFIED -> FINALIZED deliberately has no
// further check (see docs/superpowers/specs/2026-08-11-contract-negotiation-provider-design.md,
// "CN:02-07 does not fit this account"). No connector-side mechanism in this
// milestone produces that behavior.
var exempt = map[string]bool{"CN:02-07": true}
```

Run: `go test ./cmd/tckgate/...`
Expected: PASS (existing tests are unaffected — they pass their own `expected`/`exempt` maps explicitly).

- [ ] **Step 4: Run the real TCK and confirm the gate's verdict**

```bash
make tck
```

This is the first point in the milestone where the actual TCK, not this project's own unit tests, is the judge. Three risk items flagged in the design spec become concrete here:

1. The `CN:01-02` (async terminate) vs. `CN:03-04` (sync 400) distinction, implemented in Task 4/5 as "same offer id → 400, different offer id → async terminate."
2. Whether `ContractAgreementMessage.callbackAddress` is actually required on the wire.
3. Whether `terminateAfterOfferDelay` (200ms in production, per Task 5 Step 7) is wide enough for `CN_02_06`'s `acceptLastOffer()` to win its race against `CN_02_05`'s unprompted termination.

If the run reports anything other than "14 of 15 `CN` tests pass, `CN:02-07` a named exemption, `MET`+`CAT` unaffected": read the failing test's output, cross-reference it against the design spec's Risks table, and fix the specific rule that is wrong — do not weaken the gate to make a red run look green. If `CN_01_02`/`CN_03_04` disagree with the implemented rule, or the `callbackAddress` guess was wrong, or the delay is too short, each is a small, localized fix in `internal/dsp/negotiation.go` or `negotiation_handler.go`, not a design change. Increasing `terminateAfterOfferDelay` (still a `var`, still overridable in tests) is the fix if `CN_02_06` is flaky or fails outright.

Do not proceed to Step 5 until `make tck` is green with `CN` in the gate.

- [ ] **Step 5: Update `README.md`**

Replace the status table and the paragraph below it:

```markdown
| DSP protocol | TCK suite | Status |
|---|---|---|
| Version metadata | `MET` | gated in CI |
| Catalog | `CAT` | gated in CI |
| Contract negotiation | `CN` (provider role) | gated in CI, 14 of 15 (`CN:02-07` is a tracked, named gap — see `docs/follow-ups.md`) |
| Contract negotiation | `CN_C` (consumer role) | not started |
| Transfer process | `TP`, `TP_C` | not started |

`MET`, `CAT`, and `CN` are in the gate's whitelist; the consumer negotiation
role and transfer process are unimplemented.

Current TCK pass rate: **18 of 59 tests total** (`MET` 1, `CAT` 3, `CN` 14 of
15 — `CN:02-07` fails by design, tracked rather than hidden — `CN_C` 16,
`TP`+`TP_C` 24). Only `MET`+`CAT`+14 of `CN`'s 15 are required by the CI gate;
the rest currently fail because their protocols are unimplemented, or, for
`CN:02-07` alone, because no connector-side mechanism in this milestone
produces the behavior it requires.

The current milestone serves the contract negotiation protocol, provider
role, from a SQLite-backed state machine. A protocol counts as done only when
its TCK suite is added to the gate's whitelist, so this table cannot drift
ahead of reality.
```

- [ ] **Step 6: Add a `docs/follow-ups.md` entry**

Append a new section:

```markdown
## From the contract negotiation (provider) milestone (2026-08)

**`CN:02-07` has no implemented trigger.** Every autonomous termination this
milestone implements is checked once, at accept-time: either the initial
request's offer matches an expired dataset, or an `ACCEPTED` event arrives
for one. `CN:02-07`'s sequence reaches a clean `AGREED` — meaning the offer
matched and passed the validity check — and only terminates, unprompted,
after `VERIFIED`. A check performed once at accept-time cannot explain a
rejection surfacing later on a negotiation that already passed it. Tracked as
a named gate exemption (`cmd/tckgate/main.go`'s `exempt` map) rather than
silently dropped. Full reasoning:
`docs/superpowers/specs/2026-08-11-contract-negotiation-provider-design.md`,
"`CN:02-07` does not fit this account". Closing this means finding DSP's
actual intended trigger for an unprompted post-verification termination —
not yet determined from the public TCK sources this project is permitted to
use.
```

- [ ] **Step 7: Update `DECISIONS.md`**

Remove the line `- Migration approach for the SQLite schema` from the "Deferred to implementation" list (it now has three struck-through items and this one fully removed, matching how the other three were closed).

Append a note to the end of §8 (after its existing "Trade-off accepted" paragraph):

```markdown

**Note (2026-08, contract negotiation milestone).** This section's
justification stops being aspirational here: the `negotiations` table is the
first runtime state this project persists. See §23.1 for the migration
mechanism this introduced.
```

Add a new `## 23. Contract negotiation (provider role)` section at the end of the file, after §22.5's final paragraph:

```markdown

---

## 23. Contract negotiation (provider role)

**Decision.** Five decisions taken while implementing the contract
negotiation protocol's provider role.

**23.1 No migration framework — a single idempotent `CREATE TABLE IF NOT
EXISTS`, run once at startup.** §8 named "migration approach for the SQLite
schema" as deferred until first needed; the `negotiations` table is that
moment. A second schema change is what decides whether a real migration tool
earns its place — one table does not.

*Trade-off accepted.* No down-migrations, no versioned schema history. Adding
a column later means writing an `ALTER TABLE ... ADD COLUMN` by hand, once.

**23.2 `modernc.org/sqlite` is the SQLite driver.** Pure Go, no CGO. §7
commits to a single static binary and §16 to `goreleaser` cross-compiling
linux/macOS × amd64/arm64; a CGO-based driver needs a C toolchain per target
and would break that promise the first time someone builds from source
without one.

*Trade-off accepted.* Less mature than `mattn/go-sqlite3` for exotic SQLite
features. This project only uses `CREATE TABLE`, `INSERT`, `SELECT`,
`UPDATE` — no feature it needs is exotic.

**23.3 Provider pids are generated with `crypto/rand`, not a UUID
package.** 16 random bytes formatted per RFC 4122 UUID v4. §21's default
answer to a dependency question is the standard library, and this project
fully controls the shape of a value it both generates and consumes.

*Trade-off accepted.* No UUID variant beyond v4, and no parsing of
externally-supplied UUIDs — neither is needed here.

**23.4 A validity-period policy constraint, checked at accept-time, is the
trigger for the autonomous-termination scenarios `CN:02-05` and
`CN:02-06`.** §14 already permits a validity-period constraint as one of
exactly two enforceable v1 policy shapes; this milestone is its first real
use. The check runs at two points: when an unmatched request would otherwise
earn only an informational counter-offer, and when an `ACCEPTED` event would
otherwise advance the negotiation to `AGREED`.

*Trade-off accepted.* `CN:02-07` needs a termination trigger that fires
*after* a negotiation has already passed this check once (at `AGREED`) — a
check performed once at accept-time cannot produce that. `CN:02-07` is
tracked as a named, deliberate gap (see §23.5 and `docs/follow-ups.md`), not
forced to pass with an unjustified mechanism.

**23.5 The TCK compliance gate (`cmd/tckgate`) gained a named-exemption
mechanism.** The existing per-suite exact-count model could not express
"this suite's 15 results all arrive, but one specific, named test is known to
fail." A second map, `exempt`, holds individual test IDs excused from the
failure gate; a suite's expected count still includes them, so the gate still
proves the suite ran to completion. The exemption is self-cleaning in one
direction: an exempted test that unexpectedly *passes* fails the gate, on the
theory that a stale exemption hiding a real pass is worse than one hiding a
real failure.

*Trade-off accepted.* An exemption can go stale in the other direction — the
TCK could change what `CN:02-07` asserts, and this project would not notice
until it happened to pass. Acceptable: the gate already re-runs the full
suite on every CI run, and a stale exemption is not a silent regression, only
a silently-outdated comment.
```

- [ ] **Step 8: Run the full check one more time**

Run: `go test ./...`
Expected: PASS.

Run: `make tck`
Expected: green, same verdict as Step 4.

- [ ] **Step 9: Commit**

```bash
git add test/tck/dsbox.yaml test/tck/config.properties cmd/tckgate/main.go \
  README.md docs/follow-ups.md DECISIONS.md
git commit -m "feat: add CN to the compliance gate and document the milestone"
```

---

## After the plan

Once all six tasks are complete and `make tck` is green in CI (not just locally), use `superpowers:finishing-a-development-branch` to merge, exactly as the catalog milestone did. Do not add `CN_C` or the transfer process to this branch — both are explicitly out of scope (see the design spec's Scope section and "What this unlocks").
