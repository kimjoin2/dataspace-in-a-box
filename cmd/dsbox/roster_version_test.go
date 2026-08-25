package main

import (
	"os"
	"regexp"
	"testing"
)

// Deleting the call that records the roster version leaves build, vet,
// go test, make tck and make demo all green — measured. Go does not error on
// an unreferenced function and go vet does not report one, so extracting the
// logic makes it testable and leaves the call deletable.
//
// This is the technique internal/dsp/auth_middleware_test.go and
// internal/mgmt/route_coverage_test.go already use for the same problem:
// read the source rather than keep a list that goes stale.
//
// The pattern requires the error to be handled, not only the call to be
// present. An earlier draft matched the call alone, and a swallowed error
// then survived — measured.
func TestMainRecordsTheRosterVersion(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	call := regexp.MustCompile(`RecordRosterVersion\(\s*roster\.Version\(\)\s*\)\s*;\s*err\s*!=\s*nil`)
	if !call.Match(src) {
		t.Error("main.go does not call st.RecordRosterVersion(roster.Version()) and act on its error; " +
			"a rollback to a superseded roster would boot silently")
	}
}
