// Command tckgate decides whether a TCK run passes the compliance gate.
//
// Only suites whose protocol is implemented are required to pass. Adding a
// prefix to the whitelist is how a protocol is declared done, which keeps the
// README's claims and the build in agreement.
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// expected holds the number of results each gated suite must produce. A suite
// enters this map only when its protocol is implemented, and the count is how
// many tests that suite contains upstream. Requiring an exact count means a run
// that stops halfway through a suite fails instead of reporting green.
var expected = map[string]int{"MET": 1, "CAT": 3}

// resultLine matches one per-test result in the TCK's stdout. Group 1 is the
// outcome, group 2 the test identifier. Verified against real output:
//
//	[2026-07-28T17:33:51.948227174] SUCCESSFUL: MET:01-01
//	[2026-07-28T17:33:52.263217216] FAILED: CAT:01-01
var resultLine = regexp.MustCompile(`\b(SUCCESSFUL|FAILED):\s*([A-Z_]+:[\d-]+)`)

// completionMarker is the only reliable end-of-run signal this TCK version
// emits. It does not print the "there were failing tests" phrase upstream's
// own example keys on, so failure is derived from the per-test lines.
const completionMarker = "test run complete"

// completionTimestampPrefix matches the timestamp the TCK prefixes onto every
// line it prints, e.g. "[2026-07-28T17:44:57.951351385] ". Stripping it lets
// hasCompletionMarker compare a line's remaining content against
// completionMarker exactly, rather than searching for the phrase anywhere in
// the output — which would also match a failure message that merely mentions
// it in prose.
var completionTimestampPrefix = regexp.MustCompile(`^\[[^\]]*\]\s*`)

// hasCompletionMarker reports whether output contains a line that, once its
// timestamp prefix is stripped and it is trimmed and lowercased, is exactly
// completionMarker.
func hasCompletionMarker(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		content := completionTimestampPrefix.ReplaceAllString(line, "")
		if strings.ToLower(strings.TrimSpace(content)) == completionMarker {
			return true
		}
	}
	return false
}

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

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: tckgate <tck-output-file>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", os.Args[1], err)
		os.Exit(2)
	}
	report, err := evaluate(string(data), expected)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(report)
	if !report.OK() {
		os.Exit(1)
	}
}
