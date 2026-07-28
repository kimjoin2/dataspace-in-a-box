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

// whitelist holds the test identifier prefixes that must pass. Add a prefix
// only when its protocol is implemented.
var whitelist = []string{"MET"}

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

// Report is the gate's verdict over one TCK run.
type Report struct {
	Required int      // whitelisted tests seen
	Failed   []string // whitelisted tests that did not pass
	Skipped  int      // results outside the whitelist, reported but not gating
}

// OK reports whether the run satisfies the gate.
func (r Report) OK() bool { return r.Required > 0 && len(r.Failed) == 0 }

func (r Report) String() string {
	if r.OK() {
		return fmt.Sprintf("%d required tests passed, %d results outside the gate", r.Required, r.Skipped)
	}
	if r.Required == 0 {
		return "no required tests were recognized in the output"
	}
	sort.Strings(r.Failed)
	return fmt.Sprintf("%d of %d required tests failed: %s",
		len(r.Failed), r.Required, strings.Join(r.Failed, ", "))
}

// evaluate reads a complete TCK run and reports the gate's verdict. It errors
// when the run did not finish, because an incomplete run proves nothing and
// must never be mistaken for a pass.
func evaluate(output string, prefixes []string) (Report, error) {
	if !strings.Contains(strings.ToLower(output), completionMarker) {
		return Report{}, fmt.Errorf("the TCK run did not complete: %q not found in the output", completionMarker)
	}

	var report Report
	for _, line := range strings.Split(output, "\n") {
		m := resultLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		outcome, id := m[1], m[2]
		if !matchesAny(id, prefixes) {
			report.Skipped++
			continue
		}
		report.Required++
		if outcome == "FAILED" {
			report.Failed = append(report.Failed, strings.TrimSpace(line))
		}
	}
	return report, nil
}

// matchesAny reports whether id belongs to one of the given suites. The colon
// matters: without it the prefix "CN" would also swallow every "CN_C:" test,
// silently gating a suite nobody declared done.
func matchesAny(id string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(id, prefix+":") {
			return true
		}
	}
	return false
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
	report, err := evaluate(string(data), whitelist)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(report)
	if !report.OK() {
		os.Exit(1)
	}
}
