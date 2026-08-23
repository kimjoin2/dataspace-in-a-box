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
	r := newIdleTimeoutReader(src, time.Nanosecond, cancel)
	defer r.Stop()

	// The idle window is already gone by the time anything is read, so the
	// cancel is scheduled or done. Read must surface that rather than reset
	// a timer whose function has already fired.
	deadline := time.After(2 * time.Second)
	buf := make([]byte, 1)
	for {
		select {
		case <-deadline:
			t.Fatal("Read never reported the idle timeout")
		default:
		}
		if _, err := r.Read(buf); err != nil {
			if !errors.Is(err, errIdleTimeout) {
				t.Fatalf("err = %v, want errIdleTimeout", err)
			}
			return
		}
	}
}
