package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestDrainPullsCancelsWhatItWaitsFor is the test run() cannot have. run
// parses flags on the global CommandLine, binds two real listeners, and
// blocks on os/signal, so the line that connects pull cancellation to the
// running connector had no test at all — dropping it compiled, passed
// everything, and would have shown up only as a lost outcome row on a
// transfer interrupted by a restart.
//
// The pull here ends on cancellation and on nothing else, and the budget is
// far longer than it needs to be. That combination is what makes a true
// result mean something: it cannot be a pull that happened to finish in
// time, because this one never finishes on its own.
func TestDrainPullsCancelsWhatItWaitsFor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Not the assertion — insurance. If drainPulls does not cancel, the
	// goroutine below would outlive the test.
	t.Cleanup(cancel)

	pulls := &sync.WaitGroup{}
	pulls.Add(1)
	go func() {
		defer pulls.Done()
		<-ctx.Done()
	}()

	if !drainPulls(cancel, pulls, 2*time.Second) {
		t.Fatal("drainPulls reported a pull still in flight, but the pull it was given ends as soon as it is cancelled — " +
			"so it was never cancelled, and at shutdown its outcome row would be lost")
	}
}

// TestDrainPullsGivesUpAtTheBudget pins the other half: bounded, not
// indefinite. A pull that cancellation does not reach — blocked in a syscall
// on the file it is writing — must not hold the connector open, and the
// false return is what makes run log that the outcome went unrecorded rather
// than claim a clean shutdown.
func TestDrainPullsGivesUpAtTheBudget(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	pulls := &sync.WaitGroup{}
	pulls.Add(1)
	go func() {
		defer pulls.Done()
		<-release // deaf to cancellation, like a blocking write
	}()

	if drainPulls(func() {}, pulls, 20*time.Millisecond) {
		t.Error("drainPulls reported every pull finished while one was still running; " +
			"shutdown would close the store under it and log nothing")
	}
}
