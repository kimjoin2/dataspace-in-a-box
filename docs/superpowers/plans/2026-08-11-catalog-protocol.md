# Catalog Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve the DSP catalog protocol from configuration and add the `CAT` suite to the compliance gate, taking the TCK pass rate from 1 of 59 to 4 of 59.

**Architecture:** Advertised datasets are operator declarations, so they live in the YAML configuration rather than in storage — no SQLite in this milestone. The connector synthesizes the offer, distribution, and data service around each configured identifier, using pure functions in `internal/dsp` that are testable without a server. Two handlers mount under `/2025-1/`, and a DSP error document writer serves the 400 and 404 paths. The gate is strengthened first: it counts results per suite so that a truncated run cannot report green.

**Tech Stack:** Go 1.26 (standard library), `gopkg.in/yaml.v3`, Docker Compose, GitHub Actions, `eclipsedataspacetck/dsp-tck-runtime`.

Spec: [`docs/superpowers/specs/2026-07-31-catalog-protocol-design.md`](../specs/2026-07-31-catalog-protocol-design.md)

## Global Constraints

Every task's requirements implicitly include this section. Values are copied from the spec and `CLAUDE.md`.

- Go 1.26. Module path `github.com/kimjoin2/dataspace-in-a-box`.
- The only permitted dependency is `gopkg.in/yaml.v3`. Everything else is the standard library. Adding any other dependency requires asking first. In particular: **no JSON Schema library** — incoming messages are validated by direct field checks (spec decision 5).
- All committed content is English: code, comments, docs, and commit messages. This includes working documents under `docs/`.
- No plugin system, no SPI, no inheritance-based extension points.
- Permitted inputs are published, publicly obtainable sources only — the DSP 2025-1 specification, the official TCK, published IEEE standards, and public European dataspace documents. Never consult non-public or draft material.
- `public_url` is required configuration. Never infer the external address from the `Host` header.
- The management listener binds to `127.0.0.1` by default. The DSP and management listeners stay separate.
- The connector speaks plain HTTP. There is no TLS configuration.
- Never accept a constraint that is not enforced. This milestone advertises exactly one policy shape and rejects catalog filters outright.
- No storage. SQLite arrives when a state machine needs it, which is the negotiation milestone.
- Copyright is b7g. No organizational affiliation anywhere.
- When behavior is unclear, the DSP 2025-1 spec and the TCK decide — not intuition, and not how EDC does it.

## File Structure

| Path | Responsibility | Task |
|---|---|---|
| `cmd/tckgate/main.go` | Parse TCK stdout; enforce per-suite expected counts | 1, 5 |
| `cmd/tckgate/main_test.go` | Gate decisions, including count mismatches | 1 |
| `internal/config/config.go` | Add `participant_id` and `datasets`; validate identifiers | 2 |
| `internal/config/config_test.go` | New validation rules; existing tests adapted to the new required key | 2 |
| `config.example.yaml` | Sample participant and dataset so a fresh clone serves a non-empty catalog | 2 |
| `internal/dsp/catalog.go` | Catalog and dataset document types and construction — pure functions | 3 |
| `internal/dsp/catalog_test.go` | Document shape, derived identifiers, `@type` on every node | 3 |
| `internal/dsp/error.go` | DSP error document, and the shared JSON response writer | 3 |
| `internal/dsp/error_test.go` | Error document shape and status code | 3 |
| `internal/dsp/catalog_handler.go` | The two handlers and incoming message validation | 4 |
| `internal/dsp/catalog_handler_test.go` | Handler behavior over `httptest` | 4 |
| `internal/dsp/router.go` | Mount the catalog routes; `NewRouter` takes configuration | 4 |
| `internal/dsp/version_test.go` | Adapted to the new `NewRouter` signature | 4 |
| `cmd/dsbox/main.go` | Pass configuration into the DSP router | 4 |
| `test/tck/dsbox.yaml` | Harness connector configuration: participant and two datasets | 5 |
| `test/tck/config.properties` | Seed the identifiers the `CAT` suite asks for | 5 |
| `README.md` | Status table and honest pass rate | 5 |
| `DECISIONS.md` | §22, and a clarification to §8 | 5 |

No new package. Document construction is a set of pure functions inside `internal/dsp`; a separate package would buy nothing today. When negotiation needs to read offers, that split becomes a rename against a real requirement.

## Ripple Effects Found While Planning

Three consequences of this milestone that the spec does not mention. They are handled inside the tasks that cause them; they are listed here so no one mistakes them for scope creep.

1. **Every existing `internal/config` test loads a document without `participant_id`.** Making it required breaks all of them. Task 2 introduces a `minimal()` test helper so the next required key does not repeat this.
2. **`NewRouter()` is called in four places in `version_test.go`.** Task 4 updates them.
3. **`TestUnknownPathIsNotFound` uses `/2025-1/catalog/request` as its example of an unrouted path.** That path becomes real in Task 4, so the test must point at a path that is still unimplemented.

---

## Task 1: Gate counts results per suite

The gate currently passes when at least one whitelisted test is seen and none failed. A suite that produces two of its three results — a dropped connection, an upstream rename — would report green. This lands **before** `CAT` enters the gate, because shipping the fix together with the change it protects against would make the fix untestable.

**Files:**
- Modify: `cmd/tckgate/main.go:18` (the `whitelist` variable), `:53-76` (`Report`), `:81-115` (`evaluate`, `matchesAny`), `:127` (the call in `main`)
- Test: `cmd/tckgate/main_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `evaluate(output string, expected map[string]int) (Report, error)`; `Report` with fields `Expected map[string]int`, `Seen map[string]int`, `Failed []string`, `Skipped int`, and methods `OK() bool` and `String() string`. Task 5 changes only the package-level `expected` map.

- [ ] **Step 1: Write the failing tests**

Replace the existing calls to `evaluate` — the signature changes from `[]string` to `map[string]int`. In `cmd/tckgate/main_test.go`, change the second argument of every existing call as follows:

| Test | Old second argument | New second argument |
|---|---|---|
| `TestPassingOutputSatisfiesTheGate` | `[]string{"MET"}` | `map[string]int{"MET": 1}` |
| `TestFailingMETTestFailsTheGate` | `[]string{"MET"}` | `map[string]int{"MET": 1}` |
| `TestFailureOutsideTheWhitelistIsIgnored` | `[]string{"MET"}` | `map[string]int{"MET": 1}` |
| `TestTruncatedOutputIsAnError` | `[]string{"MET"}` | `map[string]int{"MET": 1}` |
| `TestCompletionMarkerMentionedInProseIsNotCompletion` | `[]string{"MET"}` | `map[string]int{"MET": 1}` |

In `TestPassingOutputSatisfiesTheGate`, replace the `report.Required == 0` check with:

```go
	if report.Seen["MET"] == 0 {
		t.Error("no MET tests were recognized; the result line pattern is wrong")
	}
```

Replace `TestUnderscoreSuiteIsNotSwallowedByItsPrefix` entirely:

```go
func TestUnderscoreSuiteIsNotSwallowedByItsPrefix(t *testing.T) {
	// Regression test for the CN / CN_C disambiguation in gatedSuite: without
	// the trailing colon, gating "CN" would also match every "CN_C:" result,
	// silently gating a suite nobody declared done. Counts verified against
	// testdata/passing.txt.
	report, err := evaluate(read(t, "testdata/passing.txt"), map[string]int{"CN": 15})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.Seen["CN"] != 15 {
		t.Errorf("Seen[CN] = %d, want 15 (CN must not also match CN_C results)", report.Seen["CN"])
	}

	report, err = evaluate(read(t, "testdata/passing.txt"), map[string]int{"CN_C": 16})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.Seen["CN_C"] != 16 {
		t.Errorf("Seen[CN_C] = %d, want 16", report.Seen["CN_C"])
	}
}
```

Add three new tests. The first two use synthetic output rather than a fixture, so the count is the only thing under test and no failing result muddies the verdict:

```go
// synthetic builds a TCK-shaped output from result lines, terminated by the
// completion marker. Fixtures prove the parser reads real output; these tests
// are about counting, so they state their input directly.
func synthetic(lines ...string) string {
	out := ""
	for _, l := range lines {
		out += "[2026-07-28T17:33:51.948227174] " + l + "\n"
	}
	return out + "[2026-07-28T17:33:52.000000000] Test run complete\n"
}

func TestSuiteShortOfItsExpectedCountFailsTheGate(t *testing.T) {
	report, err := evaluate(synthetic("SUCCESSFUL: MET:01-01"), map[string]int{"MET": 1, "CAT": 3})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.OK() {
		t.Error("gate accepted a run in which the CAT suite produced no results at all")
	}
	if !strings.Contains(report.String(), "CAT produced 0 of 3") {
		t.Errorf("report does not name the shortfall: %s", report)
	}
}

func TestSuiteOverItsExpectedCountFailsTheGate(t *testing.T) {
	// An extra result means the suite is not the one the gate was calibrated
	// against, so its pass rate no longer means what the README claims.
	report, err := evaluate(
		synthetic("SUCCESSFUL: MET:01-01", "SUCCESSFUL: MET:01-02"),
		map[string]int{"MET": 1},
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.OK() {
		t.Error("gate accepted a run with more MET results than expected")
	}
}

func TestExpectedCountsMetPasses(t *testing.T) {
	report, err := evaluate(
		synthetic("SUCCESSFUL: MET:01-01", "SUCCESSFUL: CAT:01-01", "SUCCESSFUL: CAT:01-02", "SUCCESSFUL: CAT:01-03"),
		map[string]int{"MET": 1, "CAT": 3},
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !report.OK() {
		t.Errorf("gate rejected a run that met every expected count: %s", report)
	}
}
```

Add `"strings"` to the test file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/tckgate/`
Expected: compile failure — `evaluate` takes `[]string`, and `Report` has no field `Seen`.

- [ ] **Step 3: Write the implementation**

In `cmd/tckgate/main.go`, replace the `whitelist` variable:

```go
// expected holds the number of results each gated suite must produce. A suite
// enters this map only when its protocol is implemented, and the count is how
// many tests that suite contains upstream. Requiring an exact count means a run
// that stops halfway through a suite fails instead of reporting green.
var expected = map[string]int{"MET": 1}
```

Replace `Report`, `OK`, and `String`:

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
	// did not pass.
	Failed []string
	// Skipped counts results outside the gate: reported, but not gating.
	Skipped int
}

// shortfalls returns one message per suite whose result count differs from its
// expectation, sorted so the output is stable. A count mismatch is a different
// failure from a test failing, and the fix differs too, so it is reported
// separately rather than folded into the failure list.
func (r Report) shortfalls() []string {
	var out []string
	for suite, want := range r.Expected {
		if got := r.Seen[suite]; got != want {
			out = append(out, fmt.Sprintf("%s produced %d of %d expected results", suite, got, want))
		}
	}
	sort.Strings(out)
	return out
}

// total counts every gated result seen, across all suites.
func (r Report) total() int {
	n := 0
	for _, c := range r.Seen {
		n += c
	}
	return n
}

// OK reports whether the run satisfies the gate: every gated suite produced
// exactly the expected number of results, and none of them failed.
func (r Report) OK() bool { return len(r.shortfalls()) == 0 && len(r.Failed) == 0 }

func (r Report) String() string {
	if r.OK() {
		return fmt.Sprintf("%d required tests passed, %d results outside the gate", r.total(), r.Skipped)
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
	return strings.Join(parts, "; ")
}
```

Replace `evaluate` and `matchesAny`:

```go
// evaluate reads a complete TCK run and reports the gate's verdict. It errors
// when the run did not finish, because an incomplete run proves nothing and
// must never be mistaken for a pass.
func evaluate(output string, expected map[string]int) (Report, error) {
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
		if outcome == "FAILED" {
			report.Failed = append(report.Failed, strings.TrimSpace(line))
		}
	}
	return report, nil
}

// gatedSuite returns the gated suite a test identifier belongs to. The colon
// matters: without it the suite "CN" would also match every "CN_C:" test,
// silently gating a suite nobody declared done.
func gatedSuite(id string, expected map[string]int) (string, bool) {
	for suite := range expected {
		if strings.HasPrefix(id, suite+":") {
			return suite, true
		}
	}
	return "", false
}
```

In `main`, change the call:

```go
	report, err := evaluate(string(data), expected)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/tckgate/ -v`
Expected: PASS, including `TestSuiteShortOfItsExpectedCountFailsTheGate`, `TestSuiteOverItsExpectedCountFailsTheGate`, and `TestExpectedCountsMetPasses`.

- [ ] **Step 5: Verify the gate still accepts the last real run**

Run: `go run ./cmd/tckgate tck-output.txt`
Expected: exit 0, printing `1 required tests passed, 58 results outside the gate`. This proves the rewritten gate reaches the same verdict on the first milestone's captured output.

- [ ] **Step 6: Commit**

```bash
git add cmd/tckgate/main.go cmd/tckgate/main_test.go
git commit -m "fix: gate on an exact result count per suite"
```

---

## Task 2: Configuration for the advertised catalog

`participant_id` and a list of dataset identifiers. Nothing else: the connector synthesizes the offer, the distribution, and the data service, because exposing a policy syntax now would ship a vocabulary nothing checks.

**Files:**
- Modify: `internal/config/config.go:22-39` (the `Config` struct), `:63-71` (environment overrides), `:86-110` (`validate`)
- Modify: `config.example.yaml`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `config.Config` gains `ParticipantID string` and `Datasets []Dataset`, where `type Dataset struct { ID string }`. Tasks 3 and 4 read both.

- [ ] **Step 1: Write the failing tests**

First add the helper and adapt the existing tests, which all load documents that lack the new required key. In `internal/config/config_test.go`, add:

```go
// minimal returns a configuration document that satisfies every required key,
// with extra appended. Tests that are not about a specific required key use
// this, so that adding the next required key does not mean editing every test.
func minimal(extra string) []byte {
	return []byte("public_url: https://connector.example.org\n" +
		"participant_id: urn:participant:example\n" + extra)
}
```

Then change the first argument of each existing test's `Load` call:

| Test | New first argument |
|---|---|
| `TestLoadAppliesDefaults` | `minimal("")` |
| `TestLoadRequiresPublicURL` | `[]byte("dev_mode: true\nparticipant_id: urn:participant:example\n")` |
| `TestLoadRejectsPlainHTTPOutsideDevMode` | `[]byte("public_url: http://connector.example.org\nparticipant_id: urn:participant:example\n")` |
| `TestLoadAllowsPlainHTTPInDevMode` | `[]byte("public_url: http://dsbox:8080\ndev_mode: true\nparticipant_id: urn:participant:example\n")` |
| `TestLoadRejectsRelativePublicURL` | `[]byte("public_url: /dsp\nparticipant_id: urn:participant:example\n")` |
| `TestLoadRejectsTrailingSlash` | `[]byte("public_url: https://connector.example.org/\nparticipant_id: urn:participant:example\n")` |
| `TestEnvironmentOverridesFile` | `[]byte("public_url: https://from-file.example.org\ndsp_addr: 0.0.0.0:9999\nparticipant_id: urn:participant:example\n")` |
| `TestInvalidDevModeEnvIsAnError` | `minimal("")` |
| `TestMalformedYAMLIsAnError` | unchanged — parsing fails before validation |
| `TestUnknownKeyIsAnError` | `minimal("dsp_add: 0.0.0.0:9999\n")` |

`TestEmptyDocumentWithEnvironmentStillLoads` needs the new key from the environment as well:

```go
func TestEmptyDocumentWithEnvironmentStillLoads(t *testing.T) {
	cfg, err := Load([]byte(""), env(map[string]string{
		"DSBOX_PUBLIC_URL":     "https://from-env.example.org",
		"DSBOX_PARTICIPANT_ID": "urn:participant:from-env",
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

Now add the new tests:

```go
func TestLoadRequiresParticipantID(t *testing.T) {
	_, err := Load([]byte("public_url: https://connector.example.org\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error when participant_id is absent")
	}
}

func TestParticipantIDFromEnvironmentOverridesFile(t *testing.T) {
	cfg, err := Load(minimal(""), env(map[string]string{
		"DSBOX_PARTICIPANT_ID": "urn:participant:from-env",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ParticipantID != "urn:participant:from-env" {
		t.Errorf("ParticipantID = %q, want the environment value", cfg.ParticipantID)
	}
}

func TestDatasetsAreOptional(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Datasets) != 0 {
		t.Errorf("Datasets = %v, want none", cfg.Datasets)
	}
}

func TestDatasetsAreLoadedInOrder(t *testing.T) {
	cfg, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n  - id: urn:dataset:b\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Datasets) != 2 {
		t.Fatalf("Datasets has %d entries, want 2", len(cfg.Datasets))
	}
	if cfg.Datasets[0].ID != "urn:dataset:a" || cfg.Datasets[1].ID != "urn:dataset:b" {
		t.Errorf("Datasets = %v, want a then b", cfg.Datasets)
	}
}

func TestDuplicateDatasetIDIsAnError(t *testing.T) {
	_, err := Load(minimal("datasets:\n  - id: urn:dataset:a\n  - id: urn:dataset:a\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a duplicate dataset id")
	}
}

func TestRelativeDatasetIDIsAnError(t *testing.T) {
	// An @id is an IRI. A relative one's fate under JSON-LD expansion depends
	// on a document base the TCK never sets.
	_, err := Load(minimal("datasets:\n  - id: dataset-a\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a dataset id with no scheme")
	}
}

func TestDatasetIDWithPathSeparatorIsAnError(t *testing.T) {
	// The dataset endpoint routes on the identifier as a single path segment.
	_, err := Load(minimal("datasets:\n  - id: https://example.org/datasets/a\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a dataset id containing a slash")
	}
}

func TestEmptyDatasetIDIsAnError(t *testing.T) {
	_, err := Load(minimal("datasets:\n  - id: \"\"\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for an empty dataset id")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/`
Expected: compile failure — `cfg.ParticipantID` and `cfg.Datasets` do not exist.

- [ ] **Step 3: Write the implementation**

In `internal/config/config.go`, add to the `Config` struct after `PublicURL`:

```go
	// ParticipantID identifies this participant in every catalog it serves. It
	// is required and never inferred. Section 9 will eventually make this a
	// did:web identifier; only the value changes when that day comes, because
	// deriving one now would mint DIDs nothing can resolve.
	ParticipantID string `yaml:"participant_id"`

	// Datasets are the identifiers this connector advertises. Advertised
	// datasets are an operator declaration rather than connector runtime state,
	// so they belong in configuration rather than in storage — see
	// DECISIONS.md section 8. Changing them means editing this file and
	// restarting.
	Datasets []Dataset `yaml:"datasets"`
```

Add the type below the `Config` struct:

```go
// Dataset is one advertised dataset. Only the identifier is configurable: the
// connector synthesizes the offer, the distribution, and the data service.
// Advertising a policy the negotiation code cannot yet enforce would claim
// something untrue, so the configuration grows a policy key when evaluation is
// written, and not before.
type Dataset struct {
	ID string `yaml:"id"`
}
```

Add the environment override alongside the others, after the `DSBOX_PUBLIC_URL` block:

```go
	if v := getenv("DSBOX_PARTICIPANT_ID"); v != "" {
		cfg.ParticipantID = v
	}
	// datasets has no environment override: a list has no sensible environment
	// representation, and inventing one would be a second configuration syntax.
```

Add to `validate`, before the final `return nil`:

```go
	if c.ParticipantID == "" {
		return fmt.Errorf("participant_id is required: it identifies this participant in every catalog served")
	}
	seen := make(map[string]bool, len(c.Datasets))
	for i, d := range c.Datasets {
		if err := validateDatasetID(d.ID); err != nil {
			return fmt.Errorf("datasets[%d]: %w", i, err)
		}
		if seen[d.ID] {
			return fmt.Errorf("datasets[%d]: duplicate id %q", i, d.ID)
		}
		seen[d.ID] = true
	}
```

Add the helper at the end of the file:

```go
// validateDatasetID enforces the two properties a dataset identifier must have.
//
// It is an @id, so it must be an absolute IRI: a relative identifier's fate
// under JSON-LD expansion depends on a document base the TCK never sets. It is
// also routed on directly as a single path segment, so any character that would
// split or truncate a path is rejected. A urn: name satisfies both; an http URL
// identifier does not, and is rejected deliberately.
func validateDatasetID(id string) error {
	if id == "" {
		return errors.New("id is required")
	}
	u, err := url.Parse(id)
	if err != nil {
		return fmt.Errorf("id %q: %w", id, err)
	}
	if !u.IsAbs() {
		return fmt.Errorf("id must be an absolute IRI, got %q", id)
	}
	if strings.ContainsAny(id, "/?# \t\n") {
		return fmt.Errorf("id must be a single URL path segment with no /, ?, # or whitespace, got %q", id)
	}
	return nil
}
```

`errors`, `fmt`, `net/url`, and `strings` are already imported.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS, all tests including the eight new ones.

- [ ] **Step 5: Write the example configuration**

Append to `config.example.yaml`:

```yaml

# This participant's identifier, as it appears in every catalog this connector
# serves. Required and never inferred. DSBOX_PARTICIPANT_ID overrides it.
participant_id: urn:participant:example

# The datasets this connector advertises. Optional: with none configured, the
# catalog is served without a dataset key at all.
#
# Each id must be an absolute IRI and must survive as a single URL path segment,
# because the dataset endpoint routes on it directly — no /, ?, # or whitespace.
# A urn: name satisfies both. The connector synthesizes the offer, the
# distribution, and the data service around each id; there is no policy syntax
# here yet, because nothing evaluates one until the negotiation milestone.
#
# There is no environment override for this list.
datasets:
  - id: urn:dataset:sample
```

- [ ] **Step 6: Verify the example configuration loads**

Run: `go build -o dsbox ./cmd/dsbox && ./dsbox -config config.example.yaml`
Expected: it starts and logs `connector started`. Stop it with Ctrl-C. (It will not serve a catalog until Task 4.)

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go config.example.yaml
git commit -m "feat: configure the participant and the datasets it advertises"
```

---

## Task 3: Catalog, dataset, and error documents

Pure construction, no HTTP. Every node carries `@type` because the DSP context defines most terms inside type-scoped contexts — a node without `@type` silently loses those keys during expansion, and the document still parses.

**Files:**
- Create: `internal/dsp/catalog.go`, `internal/dsp/catalog_test.go`, `internal/dsp/error.go`, `internal/dsp/error_test.go`

**Interfaces:**
- Consumes: `config.Config` with `ParticipantID` and `Datasets` from Task 2.
- Produces:
  - `buildCatalog(cfg config.Config) Catalog`
  - `buildDataset(publicURL, id string) Dataset`
  - `findDataset(cfg config.Config, id string) (Dataset, bool)`
  - `writeError(w http.ResponseWriter, dspType string, status int, reason string)`
  - `writeJSON(w http.ResponseWriter, status int, v any)`
  - constant `CatalogErrorType = "CatalogError"`

  Task 4 calls all of these.

- [ ] **Step 1: Write the failing tests**

Create `internal/dsp/catalog_test.go`:

```go
package dsp

import (
	"encoding/json"
	"testing"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

func testConfig(ids ...string) config.Config {
	cfg := config.Config{
		PublicURL:     "https://connector.example.org",
		ParticipantID: "urn:participant:example",
	}
	for _, id := range ids {
		cfg.Datasets = append(cfg.Datasets, config.Dataset{ID: id})
	}
	return cfg
}

// decode marshals a document and decodes it back into a generic map, so the
// assertions run against the JSON that actually goes on the wire rather than
// against the Go struct. Keys dropped by an omitempty tag are then genuinely
// absent.
func decode(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestCatalogWithNoDatasetsOmitsTheDatasetKey(t *testing.T) {
	// The schema allows the key to be absent but requires at least one entry
	// when it is present, so an empty array would be invalid.
	m := decode(t, buildCatalog(testConfig()))
	if _, present := m["dataset"]; present {
		t.Error("dataset key is present with no datasets configured; it must be omitted entirely")
	}
}

func TestCatalogRootCarriesTheRequiredFields(t *testing.T) {
	m := decode(t, buildCatalog(testConfig("urn:dataset:a")))

	ctx, ok := m["@context"].([]any)
	if !ok || len(ctx) != 1 || ctx[0] != ContextURL {
		t.Errorf("@context = %v, want the array [%s]", m["@context"], ContextURL)
	}
	if got, want := m["@id"], "https://connector.example.org/2025-1/catalog"; got != want {
		t.Errorf("@id = %v, want %q", got, want)
	}
	if got, want := m["@type"], "Catalog"; got != want {
		t.Errorf("@type = %v, want %q", got, want)
	}
	if got, want := m["participantId"], "urn:participant:example"; got != want {
		t.Errorf("participantId = %v, want %q", got, want)
	}
}

func TestCatalogListsEveryConfiguredDataset(t *testing.T) {
	m := decode(t, buildCatalog(testConfig("urn:dataset:a", "urn:dataset:b")))
	list, ok := m["dataset"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("dataset = %v, want two entries", m["dataset"])
	}
	for i, want := range []string{"urn:dataset:a", "urn:dataset:b"} {
		node := list[i].(map[string]any)
		if node["@id"] != want {
			t.Errorf("dataset[%d].@id = %v, want %q", i, node["@id"], want)
		}
	}
}

func TestDatasetNodeCarriesTypeOnEveryNode(t *testing.T) {
	// Type-scoped contexts are why this matters: hasPolicy and distribution
	// exist only inside Dataset, format and accessService only inside
	// Distribution, endpointURL only inside DataService. A missing @type drops
	// those keys silently during expansion.
	m := decode(t, buildDataset("https://connector.example.org", "urn:dataset:a"))

	if m["@type"] != "Dataset" {
		t.Errorf("dataset @type = %v, want Dataset", m["@type"])
	}
	offer := m["hasPolicy"].([]any)[0].(map[string]any)
	if offer["@type"] != "Offer" {
		t.Errorf("offer @type = %v, want Offer", offer["@type"])
	}
	dist := m["distribution"].([]any)[0].(map[string]any)
	if dist["@type"] != "Distribution" {
		t.Errorf("distribution @type = %v, want Distribution", dist["@type"])
	}
	svc := dist["accessService"].(map[string]any)
	if svc["@type"] != "DataService" {
		t.Errorf("data service @type = %v, want DataService", svc["@type"])
	}
}

func TestDatasetDerivedIdentifiers(t *testing.T) {
	m := decode(t, buildDataset("https://connector.example.org", "urn:dataset:a"))

	offer := m["hasPolicy"].([]any)[0].(map[string]any)
	if got, want := offer["@id"], "urn:dataset:a#offer"; got != want {
		t.Errorf("offer @id = %v, want %q", got, want)
	}
	if got, want := offer["permission"].([]any)[0].(map[string]any)["action"], "use"; got != want {
		t.Errorf("permission action = %v, want %q", got, want)
	}
	if _, present := offer["target"]; present {
		t.Error("offer carries target; the schema forbids it")
	}

	dist := m["distribution"].([]any)[0].(map[string]any)
	svc := dist["accessService"].(map[string]any)
	const endpoint = "https://connector.example.org/2025-1"
	if svc["@id"] != endpoint || svc["endpointURL"] != endpoint {
		t.Errorf("data service = %v, want @id and endpointURL both %q", svc, endpoint)
	}
}

func TestDatasetInsideACatalogHasNoContext(t *testing.T) {
	// A context belongs to a document, not to a node nested in one.
	m := decode(t, buildCatalog(testConfig("urn:dataset:a")))
	node := m["dataset"].([]any)[0].(map[string]any)
	if _, present := node["@context"]; present {
		t.Error("a dataset nested in a catalog carries its own @context")
	}
}

func TestFindDatasetReturnsASelfContainedDocument(t *testing.T) {
	ds, ok := findDataset(testConfig("urn:dataset:a", "urn:dataset:b"), "urn:dataset:b")
	if !ok {
		t.Fatal("findDataset: configured dataset not found")
	}
	m := decode(t, ds)
	if m["@id"] != "urn:dataset:b" {
		t.Errorf("@id = %v, want urn:dataset:b", m["@id"])
	}
	ctx, ok := m["@context"].([]any)
	if !ok || len(ctx) != 1 || ctx[0] != ContextURL {
		t.Errorf("@context = %v, want the array [%s]", m["@context"], ContextURL)
	}
	if _, present := m["distribution"]; !present {
		t.Error("the dataset document must carry its distribution; it is served standalone")
	}
}

func TestFindDatasetReportsAnUnknownIdentifier(t *testing.T) {
	if _, ok := findDataset(testConfig("urn:dataset:a"), "urn:dataset:missing"); ok {
		t.Error("findDataset accepted an identifier that is not configured")
	}
}
```

Create `internal/dsp/error_test.go`:

```go
package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestErrorDocumentShape(t *testing.T) {
	doc := errorDocument(CatalogErrorType, http.StatusNotFound, "Dataset not found")

	if len(doc.Context) != 1 || doc.Context[0] != ContextURL {
		t.Errorf("@context = %v, want [%s]", doc.Context, ContextURL)
	}
	if doc.Type != "CatalogError" {
		t.Errorf("@type = %q, want CatalogError", doc.Type)
	}
	if doc.Code != "404" {
		t.Errorf("code = %q, want \"404\"", doc.Code)
	}
	if len(doc.Reason) != 1 || doc.Reason[0] != "Dataset not found" {
		t.Errorf("reason = %v, want one entry", doc.Reason)
	}
}

func TestErrorReasonSerializesAsAnArray(t *testing.T) {
	// The context declares reason as @container: @set, so a bare string would
	// expand differently.
	b, err := json.Marshal(errorDocument(CatalogErrorType, http.StatusBadRequest, "nope"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["reason"].([]any); !ok {
		t.Errorf("reason = %v, want a JSON array", m["reason"])
	}
}

func TestErrorTypeNameIsParameterized(t *testing.T) {
	// ContractNegotiationError and TransferError are the same document with a
	// different @type, so the writer must not hard-code one.
	doc := errorDocument("ContractNegotiationError", http.StatusBadRequest, "nope")
	if doc.Type != "ContractNegotiationError" {
		t.Errorf("@type = %q, want ContractNegotiationError", doc.Type)
	}
}

func TestWriteErrorSetsStatusAndContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, CatalogErrorType, http.StatusNotFound, "Dataset not found")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var doc ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("the error body is not JSON: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/`
Expected: compile failure — `buildCatalog`, `buildDataset`, `findDataset`, `errorDocument`, and `writeError` are undefined.

- [ ] **Step 3: Write the catalog documents**

Create `internal/dsp/catalog.go`:

```go
package dsp

import "github.com/kimjoin2/dataspace-in-a-box/internal/config"

// DSP node type names and the derived-value rules for catalog documents.
//
// Every node this project emits carries @type, including where the JSON Schema
// does not require it. The DSP context defines most terms inside type-scoped
// contexts — participantId and dataset only inside Catalog, hasPolicy and
// distribution only inside Dataset, format and accessService only inside
// Distribution, endpointURL only inside DataService. A node without @type
// therefore loses those keys silently during expansion: the document still
// parses, and the information is simply gone.
const (
	CatalogType      = "Catalog"
	DatasetType      = "Dataset"
	OfferType        = "Offer"
	DistributionType = "Distribution"
	DataServiceType  = "DataService"

	// catalogPath is the catalog's own identifier, relative to the public URL.
	catalogPath = VersionPath + "/catalog"

	// offerIDSuffix derives an offer identifier from its dataset's. Deriving it
	// keeps the identifier stable across restarts without storage; when
	// negotiation has to pin an offer to an agreement, that is the point at
	// which storage decides the identifier instead.
	offerIDSuffix = "#offer"

	// unspecifiedFormat is a placeholder. DSP does not define the distribution
	// format vocabulary, and advertising a real transfer format such as
	// HttpData-PULL would claim a transfer capability this connector does not
	// have. The value changes when the transfer milestone makes a real one true.
	unspecifiedFormat = "dsbox:unspecified"

	// useAction expands to http://www.w3.org/ns/odrl/2/use, the exact value the
	// TCK's own reference dataset uses.
	useAction = "use"
)

// Catalog is a catalog document.
type Catalog struct {
	Context       []string `json:"@context"`
	ID            string   `json:"@id"`
	Type          string   `json:"@type"`
	ParticipantID string   `json:"participantId"`
	// Dataset is omitted entirely when empty: the schema allows the key to be
	// absent but requires at least one entry when it is present.
	Dataset []Dataset `json:"dataset,omitempty"`
}

// Dataset is one advertised dataset. Context is set only when the dataset is
// served as its own document; nested inside a catalog it stays empty, because a
// context belongs to a document rather than to a node.
type Dataset struct {
	Context      []string       `json:"@context,omitempty"`
	ID           string         `json:"@id"`
	Type         string         `json:"@type"`
	HasPolicy    []Offer        `json:"hasPolicy"`
	Distribution []Distribution `json:"distribution"`
}

// Offer is the policy advertised with a dataset. target is deliberately absent:
// the schema forbids it on an offer inside a dataset.
type Offer struct {
	ID         string       `json:"@id"`
	Type       string       `json:"@type"`
	Permission []Permission `json:"permission"`
}

// Permission is one ODRL rule. This milestone advertises unrestricted use and
// nothing else, because the code that would enforce anything narrower belongs
// to the negotiation milestone.
type Permission struct {
	Action string `json:"action"`
}

// Distribution describes how a dataset can be obtained.
type Distribution struct {
	Type   string `json:"@type"`
	Format string `json:"format"`
	// AccessService holds the full DataService object rather than a string
	// reference. The schema permits either, but the context does not declare
	// accessService as @type: @id, so a bare string expands to a literal rather
	// than to a link.
	AccessService DataService `json:"accessService"`
}

// DataService is the endpoint a distribution is served from.
type DataService struct {
	ID          string `json:"@id"`
	Type        string `json:"@type"`
	EndpointURL string `json:"endpointURL"`
}

// buildCatalog returns the catalog document this participant serves. The
// catalog is built from configuration on every request: it is an operator
// declaration, not runtime state, and it is small enough that caching it would
// be an optimization with nothing to show for it.
func buildCatalog(cfg config.Config) Catalog {
	cat := Catalog{
		Context:       []string{ContextURL},
		ID:            cfg.PublicURL + catalogPath,
		Type:          CatalogType,
		ParticipantID: cfg.ParticipantID,
	}
	for _, d := range cfg.Datasets {
		cat.Dataset = append(cat.Dataset, buildDataset(cfg.PublicURL, d.ID))
	}
	return cat
}

// buildDataset returns one dataset node, without a context. The offer, the
// distribution, and the data service are all derived from the identifier and
// the public URL, so the document is identical across restarts.
func buildDataset(publicURL, id string) Dataset {
	endpoint := publicURL + VersionPath
	return Dataset{
		ID:   id,
		Type: DatasetType,
		HasPolicy: []Offer{{
			ID:         id + offerIDSuffix,
			Type:       OfferType,
			Permission: []Permission{{Action: useAction}},
		}},
		Distribution: []Distribution{{
			Type:   DistributionType,
			Format: unspecifiedFormat,
			AccessService: DataService{
				ID:          endpoint,
				Type:        DataServiceType,
				EndpointURL: endpoint,
			},
		}},
	}
}

// findDataset returns the advertised dataset with the given identifier as a
// standalone document, context included. A linear scan is right here: the list
// is an operator's hand-written configuration, not a data store.
func findDataset(cfg config.Config, id string) (Dataset, bool) {
	for _, d := range cfg.Datasets {
		if d.ID == id {
			ds := buildDataset(cfg.PublicURL, d.ID)
			ds.Context = []string{ContextURL}
			return ds, true
		}
	}
	return Dataset{}, false
}
```

- [ ] **Step 4: Write the error documents**

Create `internal/dsp/error.go`:

```go
package dsp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
)

// DSP error type names. Each protocol names its own; the document is otherwise
// identical, which is why the writer takes the name as a parameter.
const CatalogErrorType = "CatalogError"

// ErrorResponse is a DSP error document.
type ErrorResponse struct {
	Context []string `json:"@context"`
	Type    string   `json:"@type"`
	Code    string   `json:"code"`
	// Reason is an array because the context declares it @container: @set.
	Reason []string `json:"reason"`
}

// errorDocument builds the DSP error document for an HTTP status.
func errorDocument(dspType string, status int, reason string) ErrorResponse {
	return ErrorResponse{
		Context: []string{ContextURL},
		Type:    dspType,
		Code:    strconv.Itoa(status),
		Reason:  []string{reason},
	}
}

// writeError sends a DSP error document. The body is JSON even for a 404,
// because a consumer parses every response as JSON-LD: a plain-text body fails
// at its parser rather than at the protocol, which tells it nothing about what
// went wrong.
func writeError(w http.ResponseWriter, dspType string, status int, reason string) {
	writeJSON(w, status, errorDocument(dspType, status, reason))
}

// writeJSON marshals v and sends it as a DSP response. Marshalling happens
// before the status is written so that a failure can still be reported as a
// 500 — once WriteHeader has been called, the status is no longer negotiable.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal response", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dsp/ -v`
Expected: PASS for every test in `catalog_test.go` and `error_test.go`. The tests from `version_test.go` still pass — nothing in this task touches the router.

- [ ] **Step 6: Commit**

```bash
git add internal/dsp/catalog.go internal/dsp/catalog_test.go internal/dsp/error.go internal/dsp/error_test.go
git commit -m "feat: catalog, dataset, and DSP error documents"
```

---

## Task 4: The two catalog endpoints

**Files:**
- Create: `internal/dsp/catalog_handler.go`, `internal/dsp/catalog_handler_test.go`
- Modify: `internal/dsp/router.go` (whole file), `internal/dsp/version_test.go:13,41,75,88` (the `NewRouter` calls) and `:72-80` (`TestUnknownPathIsNotFound`), `cmd/dsbox/main.go:53`

**Interfaces:**
- Consumes: `buildCatalog`, `findDataset`, `writeError`, `writeJSON`, `CatalogErrorType` from Task 3; `config.Config` from Task 2.
- Produces: `NewRouter(cfg config.Config) http.Handler`. Nothing later in this plan consumes it beyond `main`.

- [ ] **Step 1: Write the failing tests**

Create `internal/dsp/catalog_handler_test.go`:

```go
package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

// catalogRequest is the well-formed request body the TCK sends. Tests that are
// about something other than the body use it unchanged.
const catalogRequest = `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"CatalogRequestMessage"}`

func post(t *testing.T, cfg config.Config, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/2025-1/catalog/request", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewRouter(cfg).ServeHTTP(rec, req)
	return rec
}

func TestCatalogRequestReturnsTheCatalog(t *testing.T) {
	rec := post(t, testConfig("urn:dataset:a"), catalogRequest)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var cat Catalog
	if err := json.Unmarshal(rec.Body.Bytes(), &cat); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(cat.Dataset) != 1 || cat.Dataset[0].ID != "urn:dataset:a" {
		t.Errorf("dataset = %v, want the configured identifier", cat.Dataset)
	}
}

func TestCatalogRequestRejectsAFilter(t *testing.T) {
	// DSP leaves the filter expression implementation-defined, so returning the
	// full catalog would let a consumer believe it received a filtered view.
	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],` +
		`"@type":"CatalogRequestMessage","filter":[{"foo":"bar"}]}`
	rec := post(t, testConfig("urn:dataset:a"), body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCatalogError(t, rec)
}

func TestCatalogRequestAcceptsANullFilter(t *testing.T) {
	// An explicit null is the absence of a filter, not a filter nobody can read.
	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],` +
		`"@type":"CatalogRequestMessage","filter":null}`
	rec := post(t, testConfig("urn:dataset:a"), body)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestCatalogRequestRejectsAMalformedBody(t *testing.T) {
	rec := post(t, testConfig(), "not json at all")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertCatalogError(t, rec)
}

func TestCatalogRequestRejectsTheWrongMessageType(t *testing.T) {
	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"SomethingElse"}`
	rec := post(t, testConfig(), body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCatalogRequestRejectsAMissingContext(t *testing.T) {
	rec := post(t, testConfig(), `{"@context":["https://example.org/other"],"@type":"CatalogRequestMessage"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDatasetRequestReturnsTheDataset(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/2025-1/catalog/datasets/urn:dataset:a", nil)
	rec := httptest.NewRecorder()
	NewRouter(testConfig("urn:dataset:a")).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var ds Dataset
	if err := json.Unmarshal(rec.Body.Bytes(), &ds); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if ds.ID != "urn:dataset:a" {
		t.Errorf("@id = %q, want the requested identifier", ds.ID)
	}
	if len(ds.Context) != 1 {
		t.Errorf("@context = %v, want the document context", ds.Context)
	}
}

func TestUnknownDatasetIsACatalogError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/2025-1/catalog/datasets/urn:dataset:missing", nil)
	rec := httptest.NewRecorder()
	NewRouter(testConfig("urn:dataset:a")).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	assertCatalogError(t, rec)
}

// assertCatalogError checks that the body is a DSP CatalogError rather than a
// plain-text message. This is the assertion CAT:01-03 turns on.
func assertCatalogError(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var doc ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body is not JSON (%v): %s", err, rec.Body)
	}
	if doc.Type != CatalogErrorType {
		t.Errorf("@type = %q, want %s", doc.Type, CatalogErrorType)
	}
}
```

`testConfig` comes from `catalog_test.go` in the same package, written in Task 3.

Now adapt `internal/dsp/version_test.go`. Every `NewRouter()` call becomes `NewRouter(testConfig())` — four occurrences, at lines 13, 41, 75, and 88. And replace `TestUnknownPathIsNotFound` entirely, because its example path becomes a real route in this task:

```go
func TestUnknownPathIsNotFound(t *testing.T) {
	// Contract negotiation is the next protocol in TCK order and is not
	// implemented, so its path is still the honest example of an unrouted one.
	req := httptest.NewRequest(http.MethodPost, "/2025-1/negotiations/request", nil)
	rec := httptest.NewRecorder()
	NewRouter(testConfig()).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 while contract negotiation is unimplemented", rec.Code)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/`
Expected: compile failure — `NewRouter` takes no arguments.

- [ ] **Step 3: Write the handlers**

Create `internal/dsp/catalog_handler.go`:

```go
package dsp

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

// CatalogRequestMessageType is the @type a catalog request must carry.
const CatalogRequestMessageType = "CatalogRequestMessage"

// CatalogRequestMessage is the body of a catalog request. Only the fields this
// connector inspects are declared; unknown fields are ignored, which is what a
// JSON-LD consumer does anyway.
//
// Validation is a set of direct field checks rather than JSON Schema
// validation: the standard library has no schema validator, and three messages
// with two or three required fields each do not justify adding an engine. This
// is revisited when negotiation and transfer push the message count past a
// dozen.
type CatalogRequestMessage struct {
	Context []string        `json:"@context"`
	Type    string          `json:"@type"`
	Filter  json.RawMessage `json:"filter"`
}

// hasFilter reports whether the message carries a filter expression. An
// explicit JSON null is the absence of one.
func (m CatalogRequestMessage) hasFilter() bool {
	return len(m.Filter) > 0 && string(m.Filter) != "null"
}

// catalogHandler serves the catalog protocol from the connector's
// configuration. It holds no mutable state: an advertised catalog is an
// operator declaration, so there is nothing here for storage to hold.
type catalogHandler struct {
	cfg config.Config
}

// handleCatalogRequest serves the catalog. Every rejection answers with a DSP
// CatalogError so that a consumer learns what was wrong from the protocol
// rather than from a status code alone.
func (h catalogHandler) handleCatalogRequest(w http.ResponseWriter, r *http.Request) {
	var msg CatalogRequestMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !slices.Contains(msg.Context, ContextURL) {
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"@context must contain "+ContextURL)
		return
	}
	if msg.Type != CatalogRequestMessageType {
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"@type must be "+CatalogRequestMessageType)
		return
	}
	if msg.hasFilter() {
		// DSP leaves the filter expression implementation-defined, so a provider
		// cannot know what an arbitrary filter means. Returning the full catalog
		// would let a consumer that asked for a subset believe it received a
		// filtered view, which is a worse failure than a rejection.
		writeError(w, CatalogErrorType, http.StatusBadRequest,
			"catalog filtering is not implemented")
		return
	}
	writeJSON(w, http.StatusOK, buildCatalog(h.cfg))
}

// handleDatasetRequest serves one advertised dataset as a standalone document.
func (h catalogHandler) handleDatasetRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ds, ok := findDataset(h.cfg, id)
	if !ok {
		writeError(w, CatalogErrorType, http.StatusNotFound, "no dataset with id "+id)
		return
	}
	writeJSON(w, http.StatusOK, ds)
}
```

- [ ] **Step 4: Mount the routes**

Replace `internal/dsp/router.go` entirely:

```go
package dsp

import (
	"net/http"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

// NewRouter returns the handler for the public DSP listener. It takes the
// configuration because the catalog is served from it: what this participant
// advertises is a declaration, not runtime state.
func NewRouter(cfg config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/dspace-version", handleVersion)

	cat := catalogHandler{cfg: cfg}
	mux.HandleFunc("POST "+VersionPath+"/catalog/request", cat.handleCatalogRequest)
	// The identifier is matched as a single path segment, which is why
	// configuration rejects one containing a slash.
	mux.HandleFunc("GET "+VersionPath+"/catalog/datasets/{id}", cat.handleDatasetRequest)

	// Contract negotiation and transfer process mount here next, in TCK order.
	// Until then, requests below those paths are correctly 404.

	return mux
}
```

- [ ] **Step 5: Pass the configuration in from main**

In `cmd/dsbox/main.go`, change the DSP server's handler:

```go
		Handler:           dsp.NewRouter(cfg),
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./... -v`
Expected: PASS across every package.

- [ ] **Step 7: Verify the endpoints by hand**

```bash
go build -o dsbox ./cmd/dsbox
./dsbox -config config.example.yaml &
curl -s -X POST http://127.0.0.1:8080/2025-1/catalog/request \
  -H 'content-type: application/json' \
  -d '{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"CatalogRequestMessage"}'
curl -s http://127.0.0.1:8080/2025-1/catalog/datasets/urn:dataset:sample
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/2025-1/catalog/datasets/urn:dataset:nope
kill %1
```

Expected: a catalog containing `urn:dataset:sample`; the dataset document with an `@context`; and `404` for the unknown identifier.

- [ ] **Step 8: Commit**

```bash
git add internal/dsp/catalog_handler.go internal/dsp/catalog_handler_test.go internal/dsp/router.go internal/dsp/version_test.go cmd/dsbox/main.go
git commit -m "feat: serve the catalog and dataset endpoints"
```

---

## Task 5: Gate the CAT suite and tell the truth in the docs

**Files:**
- Modify: `test/tck/dsbox.yaml`, `test/tck/config.properties`, `cmd/tckgate/main.go` (the `expected` map), `README.md`, `DECISIONS.md`

**Interfaces:**
- Consumes: everything from Tasks 1 through 4.
- Produces: nothing further in this plan.

- [ ] **Step 1: Seed the harness**

Append to `test/tck/dsbox.yaml`:

```yaml

# The participant and the datasets the CAT suite asks for. Two identifiers
# rather than one shared identifier, so that CAT:01-01 proves the catalog lists
# datasets and CAT:01-02 proves lookup selects among them.
participant_id: urn:participant:dsbox-test
datasets:
  - id: urn:dataset:tck-catalog
  - id: urn:dataset:tck-request
```

Append to `test/tck/config.properties`:

```properties

# Dataset identifiers for the CAT suite.
#
# Key format: <TEST METHOD NAME UPPERCASED>_<FIELD NAME UPPERCASED>, resolved by
# the TCK's @ConfigParam injection from these properties first, then from a
# system property or environment variable. Without this note the names cannot be
# derived and the values look arbitrary.
#
# CAT_01_03_DATASETID is deliberately absent: an unset key makes the TCK
# generate a fresh random UUID per run, which is exactly what that test needs —
# an identifier the connector does not have.
CAT_01_01_DATASETID=urn:dataset:tck-catalog
CAT_01_02_DATASETID=urn:dataset:tck-request
```

- [ ] **Step 2: Run the TCK before changing the gate**

Run: `make tck`
Expected: the gate still passes on `MET` alone, and the three `CAT` results in `tck-output.txt` are now `SUCCESSFUL`. Confirm with:

```bash
grep -E '(SUCCESSFUL|FAILED): CAT:' tck-output.txt
```

Expected: three lines, all `SUCCESSFUL`.

If any `CAT` line still fails, stop and read the failure before touching the gate. The likely causes, in order: the `@ConfigParam` key format is wrong (the requested path will not carry the configured identifier — visible in the output); a node is missing `@type` and its keys vanished during expansion; or `@context` was serialized as a string rather than an array.

- [ ] **Step 3: Add CAT to the gate**

In `cmd/tckgate/main.go`:

```go
var expected = map[string]int{"MET": 1, "CAT": 3}
```

- [ ] **Step 4: Run the gated TCK**

Run: `make tck`
Expected: exit 0, printing `4 required tests passed, 55 results outside the gate`.

- [ ] **Step 5: Run the full test suite**

Run: `go test ./...`
Expected: PASS. The gate's own tests supply their own expectation maps, so adding `CAT` to the package-level map does not invalidate the captured fixture from the first milestone.

- [ ] **Step 6: Update the README**

In `README.md`, change the catalog row of the status table:

```markdown
| Catalog | `CAT` | gated in CI |
```

Replace the paragraph beginning "`MET` is the only suite" and the pass-rate paragraph with:

```markdown
`MET` and `CAT` are in the gate's whitelist; contract negotiation and transfer
process are unimplemented.

Current TCK pass rate: **4 of 59 tests total** (`MET` 1, `CAT` 3, `CN`+`CN_C`
31, `TP`+`TP_C` 24). Only those 4 are required by the CI gate; the other 55
currently fail, because their protocols are unimplemented.

The current milestone serves the catalog protocol from configuration. A protocol
counts as done only when its TCK suite is added to the gate's whitelist, so this
table cannot drift ahead of reality.
```

- [ ] **Step 7: Record the decisions**

In `DECISIONS.md`, add to the end of §8's rationale paragraph, after "itself.":

```markdown
Advertised datasets are not runtime state: they are an operator declaration and
live in the configuration file (see §22.1).
```

Append §22 at the end of the file:

```markdown
---

## 22. Catalog: advertised from configuration

**Decision.** Five decisions taken while implementing the catalog protocol.

**22.1 Advertised datasets come from the configuration file, not from storage.**
§8 justifies SQLite with connector runtime state — negotiation and transfer
state machines, agreements. A dataset list is none of those; it is something the
operator declares. Introducing storage here would drag the still-open migration
question in with it and double the milestone. SQLite arrives when a state
machine needs it.

*Trade-off accepted.* Changing what is advertised means editing the
configuration and restarting — the same cost §11 already accepted for token
rotation.

**22.2 The configuration carries dataset identifiers and nothing else.** The
connector synthesizes the offer, the distribution, and the data service.
Advertising a policy and enforcing one are different acts, and the code that
enforces belongs to the negotiation milestone. Exposing a policy syntax now
would ship a vocabulary nothing checks. When evaluation is written, the
configuration grows a `policy:` key alongside it.

*Trade-off accepted.* Every advertised dataset carries the same unrestricted-use
offer until negotiation lands.

**22.3 `participant_id` is a required configuration value.** No inference. §9
will eventually make this a `did:web` identifier; deriving one now would mint
DIDs that nothing can resolve, because the roster does not exist yet. The key
stays the same when that day comes — only its value changes.

*Trade-off accepted.* One more required value in the smallest possible
configuration file.

**22.4 A catalog request carrying `filter` is rejected with `CatalogError`.**
DSP leaves the filter expression implementation-defined, which means a provider
cannot know what an arbitrary filter means. Returning the full catalog to a
consumer that asked for a subset lets it believe it received a filtered view.
Explicit rejection is the stance §14 already takes on policy constraints.

*Trade-off accepted.* Consumers that attach a filter unconditionally will not
interoperate until filtering is implemented.

**22.5 Incoming messages are validated by direct field checks, not by a JSON
Schema library.** §20 specifies JSON Schema validation; the standard library has
none, and the default answer to a dependency question is the standard library.
Three messages with two or three required fields each do not justify a
validation engine. §20 stands as written and is revisited when negotiation and
transfer push the message count past a dozen.

*Trade-off accepted.* Validation coverage is whatever the handwritten checks
cover, and a missed field is a silent gap rather than a schema failure.
```

- [ ] **Step 8: Commit**

```bash
git add test/tck/dsbox.yaml test/tck/config.properties cmd/tckgate/main.go README.md DECISIONS.md tck-output.txt
git commit -m "feat: gate the CAT suite"
```

- [ ] **Step 9: Verify the same run is green in CI**

```bash
git push
gh run watch
```

Expected: the workflow passes. Done criterion 1 requires the gated run to be green in GitHub Actions, not only locally.

---

## Done Criteria

From the spec, restated as checks:

- [ ] `make tck` passes with `CAT` in the gate's expected map, and the same run is green in GitHub Actions
- [ ] `go test ./...` passes and covers every case in the spec's testing table
- [ ] A short TCK run — fewer results than expected for a gated suite — fails the gate, proven by `TestSuiteShortOfItsExpectedCountFailsTheGate`
- [ ] `README.md` states 4 of 59 and marks contract negotiation and transfer as not implemented
- [ ] A fresh clone with `config.example.yaml` serves a catalog containing the sample dataset
- [ ] `DECISIONS.md` §22 records the five decisions with their trade-offs
