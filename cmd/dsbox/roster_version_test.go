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
// The pattern requires the call to be present, its error to be inspected,
// and the branch that inspects it to return. Each of those is a separate way
// to lose the property, and each was measured to survive a pattern that
// checked only the others: a draft matching the call alone let a swallowed
// error through, and a draft matching the inspection but not the return let
// `if err != nil { slog.Error(...) }` through, which downgrades the whole
// point of the check from fatal to a log line and boots a connector holding
// a roster it has already superseded.
//
// Requiring return to be the first statement in that branch is stricter than
// the property strictly needs — logging first and then returning is still
// fatal. That is deliberate: relaxing it later is a visible edit to a test,
// which is the same posture the route-coverage guards take.
func TestMainRecordsTheRosterVersion(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	call := regexp.MustCompile(`RecordRosterVersion\(\s*roster\.Version\(\)\s*\)\s*;\s*err\s*!=\s*nil\s*\{\s*return\b`)
	if !call.Match(src) {
		t.Error("main.go does not call st.RecordRosterVersion(roster.Version()) and return its error; " +
			"a rollback to a superseded roster would boot silently, or boot with only a log line to say so")
	}
}
