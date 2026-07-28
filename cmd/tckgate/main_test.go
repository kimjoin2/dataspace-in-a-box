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
