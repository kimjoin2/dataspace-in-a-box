package dsp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// blockingReader returns one byte per call until released, then blocks
// forever. It stands in for a counterparty that stops sending.
type blockingReader struct {
	remaining int
	blocked   chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	if b.remaining > 0 {
		b.remaining--
		p[0] = 'x'
		return 1, nil
	}
	<-b.blocked
	return 0, io.EOF
}

func TestIdleTimeoutReaderCancelsWhenProgressStops(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	src := &blockingReader{remaining: 2, blocked: make(chan struct{})}
	r := newIdleTimeoutReader(src, 50*time.Millisecond, cancel)
	defer r.Stop()

	buf := make([]byte, 1)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("first read: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the context was never cancelled after progress stopped")
	}
	if got := context.Cause(ctx); !errors.Is(got, errIdleTimeout) {
		t.Errorf("cause = %v, want errIdleTimeout — a bare cancel is not a reason an operator can read", got)
	}
}

func TestIdleTimeoutReaderDoesNotCancelWhileBytesKeepArriving(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	// Far more bytes than the idle window would allow if the bound were on
	// total elapsed time rather than on time without progress.
	src := strings.NewReader(strings.Repeat("x", 400))
	r := newIdleTimeoutReader(src, 40*time.Millisecond, cancel)
	defer r.Stop()

	buf := make([]byte, 1)
	for i := 0; i < 400; i++ {
		if _, err := r.Read(buf); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		time.Sleep(time.Millisecond)
		if ctx.Err() != nil {
			t.Fatalf("cancelled after %d bytes while still making progress: %v", i, context.Cause(ctx))
		}
	}
}

func TestIdleTimeoutReaderStopReleasesTheTimer(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	src := strings.NewReader("xx")
	r := newIdleTimeoutReader(src, 30*time.Millisecond, cancel)

	buf := make([]byte, 1)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	r.Stop()

	time.Sleep(120 * time.Millisecond)
	if ctx.Err() != nil {
		t.Errorf("the context was cancelled after Stop: %v", context.Cause(ctx))
	}
}

func TestIdleTimeoutReaderReportsAlreadyCancelledRatherThanResetting(t *testing.T) {
	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	src := strings.NewReader(strings.Repeat("x", 10000))
	r := newIdleTimeoutReader(src, time.Millisecond, cancel)
	defer r.Stop()

	// Sleeping well past the idle window is what makes this deterministic:
	// the timer has certainly fired before the first Read, so the single
	// read below is the case under test — a cancel that has already run.
	// Racing a read loop against the timer measures the scheduler instead,
	// and under full-suite load that race has already lost once.
	time.Sleep(50 * time.Millisecond)

	buf := make([]byte, 1)
	if _, err := r.Read(buf); !errors.Is(err, errIdleTimeout) {
		t.Fatalf("err = %v, want errIdleTimeout — Read reset a timer whose function had already fired", err)
	}
}
