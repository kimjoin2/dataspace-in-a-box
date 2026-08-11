package main

import (
	"os"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestPassingOutputSatisfiesTheGate(t *testing.T) {
	report, err := evaluate(read(t, "testdata/passing.txt"), map[string]int{"MET": 1})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !report.OK() {
		t.Errorf("gate rejected a passing run: %s", report)
	}
	if report.Seen["MET"] == 0 {
		t.Error("no MET tests were recognized; the result line pattern is wrong")
	}
}

func TestFailingMETTestFailsTheGate(t *testing.T) {
	report, err := evaluate(read(t, "testdata/failing.txt"), map[string]int{"MET": 1})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.OK() {
		t.Error("gate accepted a run with a failing MET test")
	}
}

func TestFailureOutsideTheWhitelistIsIgnored(t *testing.T) {
	report, err := evaluate(read(t, "testdata/passing.txt"), map[string]int{"MET": 1})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !report.OK() {
		t.Errorf("unimplemented suites must not fail the gate: %s", report)
	}
	if report.Skipped == 0 {
		t.Error("no non-MET results were seen, so this fixture cannot prove they are ignored")
	}
}

func TestTruncatedOutputIsAnError(t *testing.T) {
	_, err := evaluate("starting tests\n", map[string]int{"MET": 1})
	if err == nil {
		t.Error("expected an error when the run did not complete")
	}
}

func TestCompletionMarkerMentionedInProseIsNotCompletion(t *testing.T) {
	output := "[2026-07-28T17:44:57.951351385] the run stopped short of test run complete\n"
	_, err := evaluate(output, map[string]int{"MET": 1})
	if err == nil {
		t.Error("expected an error: the marker only appears mid-line, in prose, not as a line of its own")
	}
}

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

func TestZeroValueReportIsNotOK(t *testing.T) {
	// An empty Expected map means nothing was gated. Without this, Report{}.OK()
	// would be vacuously true, which is indistinguishable from a real pass.
	if (Report{}).OK() {
		t.Error("a zero-value Report must not report OK: nothing was gated")
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
