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

// Replacing the predicate this wiring hands the management listener with nil
// leaves go build, go vet and every other test in the tree green — measured,
// whole suite, not a subset. What it deletes is the whole of the design's
// section 5.2: /health goes back to answering 200 unconditionally, so a
// connector that can serve no counterparty stays in rotation behind its
// probe. internal/mgmt's own tests cannot catch it, because they pass a
// predicate in directly and so exercise the handler rather than the wiring,
// and nothing here builds the management router.
//
// That is the same hole TestMainRecordsTheRosterVersion above closes for the
// other wiring this milestone added, by the same technique and for the same
// reason: a call site Go does not require and no test observes.
//
// The pattern requires routers.RosterUsable in the predicate position rather
// than merely requiring the call, because what the deletion replaces is the
// argument and not the call — a pattern matching mgmt.NewRouter( alone would
// pass under the exact mutation this exists to catch. Naming the arguments
// ahead of the predicate is stricter than the property needs; it makes
// renaming one a visible edit to a test, which is the posture the guard
// above takes as well.
func TestMainGivesTheManagementListenerTheRosterPredicate(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	wiring := regexp.MustCompile(`mgmt\.NewRouter\(\s*cfg\s*,\s*st\s*,\s*routers\.RosterUsable\s*,`)
	if !wiring.Match(src) {
		t.Error("main.go does not hand routers.RosterUsable to mgmt.NewRouter as the roster predicate; " +
			"/health would answer 200 on a connector whose roster has expired, keeping it in rotation " +
			"while it can serve no counterparty")
	}
}

// Which handler reaches which route is decided here, positionally, among
// arguments of one type. Transposing a pair compiles, satisfies every
// assertion in internal/mgmt -- whose tests pass their own stubs and so
// exercise the router rather than this wiring -- and surfaces only as the TCK
// failing its consumer-role suites for what reads like a protocol fault.
// Measured on the pair that already existed.
//
// It joins the guards already in this file against one class of defect: a
// call site Go does not require and no test observes. The pattern names the
// arguments in order rather than merely requiring the call, because what a
// transposition changes is the order and not the call.
func TestMainWiresTheManagementHandlersInOrder(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	wiring := regexp.MustCompile(`mgmt\.NewRouter\(\s*cfg\s*,\s*st\s*,\s*routers\.RosterUsable\s*,` +
		`\s*routers\.Initiate\.Negotiation\s*,\s*routers\.Initiate\.Transfer\s*,\s*routers\.CatalogLookup\s*,?\s*\)`)
	if !wiring.Match(src) {
		t.Error("main.go does not pass the management handlers to mgmt.NewRouter in the order it declares them; " +
			"a transposed pair compiles and surfaces only as consumer-role suites failing")
	}
}
