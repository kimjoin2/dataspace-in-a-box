package dsp

import (
	"context"
	"errors"
	"io"
	"time"
)

// errIdleTimeout is the cause a pull's context carries when it was cancelled
// for lack of progress. A bare context.Canceled is indistinguishable from
// every other cancellation, and the reason is what an operator reads.
var errIdleTimeout = errors.New("no progress within the idle timeout")

// idleTimeoutReader bounds the time a transfer may go without progress,
// rather than the time it may take. It wraps a response body: every read
// that returns bytes pushes the deadline out, and a deadline that expires
// cancels the request's context, which closes the connection underneath the
// read.
//
// This is what lets a transfer be arbitrarily large. A bound on total
// elapsed time is a file size limit wearing a clock, and one expressed in
// seconds moves with the network rather than with any decision anyone made.
type idleTimeoutReader struct {
	r     io.Reader
	idle  time.Duration
	timer *time.Timer
	// fired records that the timer's cancel has already been scheduled or
	// run. Cancellation is irreversible, so once this is set the transfer is
	// over and Read says so rather than letting the next read fail for a
	// reason the log would misattribute.
	fired bool
}

func newIdleTimeoutReader(r io.Reader, idle time.Duration, cancel context.CancelCauseFunc) *idleTimeoutReader {
	t := &idleTimeoutReader{r: r, idle: idle}
	t.timer = time.AfterFunc(idle, func() { cancel(errIdleTimeout) })
	return t
}

// Read reports errIdleTimeout rather than resetting a timer whose function
// has already fired. time.Timer documents that Reset on an AfterFunc timer
// returns false when the function is already scheduled or running, and
// gives no guarantee the prior run has finished — with cancel as that
// function, a false means this transfer is already dead.
func (t *idleTimeoutReader) Read(p []byte) (int, error) {
	if t.fired {
		return 0, errIdleTimeout
	}
	n, err := t.r.Read(p)
	if n > 0 {
		if !t.timer.Reset(t.idle) {
			t.fired = true
			return n, errIdleTimeout
		}
	}
	return n, err
}

// Stop releases the timer once the body is done with, so a completed
// transfer does not cancel its own context on the way out.
func (t *idleTimeoutReader) Stop() {
	t.timer.Stop()
}
