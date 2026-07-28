package main

import (
	"os"
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
	report, err := evaluate(read(t, "testdata/passing.txt"), []string{"MET"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !report.OK() {
		t.Errorf("gate rejected a passing run: %s", report)
	}
	if report.Required == 0 {
		t.Error("no MET tests were recognized; the result line pattern is wrong")
	}
}

func TestFailingMETTestFailsTheGate(t *testing.T) {
	report, err := evaluate(read(t, "testdata/failing.txt"), []string{"MET"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.OK() {
		t.Error("gate accepted a run with a failing MET test")
	}
}

func TestFailureOutsideTheWhitelistIsIgnored(t *testing.T) {
	report, err := evaluate(read(t, "testdata/passing.txt"), []string{"MET"})
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
	_, err := evaluate("starting tests\n", []string{"MET"})
	if err == nil {
		t.Error("expected an error when the run did not complete")
	}
}

func TestCompletionMarkerMentionedInProseIsNotCompletion(t *testing.T) {
	output := "[2026-07-28T17:44:57.951351385] the run stopped short of test run complete\n"
	_, err := evaluate(output, []string{"MET"})
	if err == nil {
		t.Error("expected an error: the marker only appears mid-line, in prose, not as a line of its own")
	}
}

func TestUnderscoreSuiteIsNotSwallowedByItsPrefix(t *testing.T) {
	// Regression test for the CN / CN_C disambiguation in matchesAny: without
	// the trailing colon, whitelisting "CN" would also match every "CN_C:"
	// result, silently gating a suite nobody declared done. Counts verified
	// against testdata/passing.txt.
	report, err := evaluate(read(t, "testdata/passing.txt"), []string{"CN"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.Required != 15 {
		t.Errorf("Required = %d, want 15 (CN must not also match CN_C results)", report.Required)
	}

	report, err = evaluate(read(t, "testdata/passing.txt"), []string{"CN_C"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.Required != 16 {
		t.Errorf("Required = %d, want 16", report.Required)
	}
}
