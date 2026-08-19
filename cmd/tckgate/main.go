// Command tckgate decides whether a TCK run passes the compliance gate.
//
// Only suites whose protocol is implemented are gated. Adding a suite to the
// expected map, with the exact number of results it produces, is how a
// protocol is declared done, which keeps the README's claims and the build in
// agreement. Requiring an exact count means a run that stops partway through a
// suite fails rather than reporting green. Results, not tests: a suite can
// declare more tests than it runs, and expected's own comment records where
// the two currently diverge.
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
// many results that suite produces upstream — which equals its test count only
// when every test in it actually runs. evaluate counts results for every
// suite, without exception. Requiring an exact count means a run that stops
// halfway through a suite fails instead of reporting green.
// TP and TP_C are where the two diverge: each suite declares 16
// @MandatoryTest methods, and each has one — tp_02_04 and tp_c_02_04 — that
// also carries JUnit's @Disabled. A disabled test produces no result, so both
// counts are 15 rather than 16.
var expected = map[string]int{"MET": 1, "CAT": 3, "CN": 15, "CN_C": 16, "TP": 15, "TP_C": 15}

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
// exactly the expected number of results, every non-exempted result passed,
// and no exempted result unexpectedly passed. An empty Expected map means
// nothing was gated, so it must not report a pass.
func (r Report) OK() bool {
	return len(r.Expected) > 0 && len(r.shortfalls()) == 0 &&
		len(r.Failed) == 0 && len(r.UnexpectedPasses) == 0
}

func (r Report) String() string {
	if r.OK() {
		// total() counts every gated result, exempted failures included —
		// they are gated tests that ran, which is what the count exists to
		// prove. They did not pass, though, so subtracting them is what makes
		// this line agree with README.md's stated pass rate instead of
		// claiming one more test than actually passed.
		s := fmt.Sprintf("%d required tests passed, %d results outside the gate",
			r.total()-len(r.Exempted), r.Skipped)
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
	report, err := evaluate(string(data), expected, exempt)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(report)
	if !report.OK() {
		os.Exit(1)
	}
}
