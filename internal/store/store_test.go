package store

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func testNegotiation() Negotiation {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	return Negotiation{
		ProviderPID:     "urn:uuid:provider-1",
		ConsumerPID:     "urn:uuid:consumer-1",
		State:           "REQUESTED",
		DatasetID:       "urn:dataset:a",
		OfferID:         "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func TestCreateAndGet(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testNegotiation()
	if err := s.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, ok, err := s.Get(n.ProviderPID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: not found, want the created negotiation")
	}
	if got.ProviderPID != n.ProviderPID || got.ConsumerPID != n.ConsumerPID ||
		got.State != n.State || got.DatasetID != n.DatasetID ||
		got.OfferID != n.OfferID || got.CallbackAddress != n.CallbackAddress {
		t.Errorf("Get returned %+v, want %+v", got, n)
	}
	if !got.CreatedAt.Equal(n.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, n.CreatedAt)
	}
	if !got.UpdatedAt.Equal(n.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, n.UpdatedAt)
	}
}

func TestGetMissingReturnsFalse(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	_, ok, err := s.Get("does-not-exist")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("Get: found a negotiation that was never created")
	}
}

func TestSetStateUpdatesRow(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testNegotiation()
	if err := s.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
	}

	updatedAt := n.UpdatedAt.Add(time.Hour)
	if err := s.SetState(n.ProviderPID, "AGREED", updatedAt); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	got, ok, err := s.Get(n.ProviderPID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: not found after SetState")
	}
	if got.State != "AGREED" {
		t.Errorf("State = %q, want AGREED", got.State)
	}
	if !got.UpdatedAt.Equal(updatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, updatedAt)
	}
}

func TestSetStateMissingIsError(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SetState("does-not-exist", "AGREED", time.Now()); err == nil {
		t.Error("SetState: expected an error updating a negotiation that does not exist")
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dsbox.db"

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	n := testNegotiation()
	if err := s1.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	got, ok, err := s2.Get(n.ProviderPID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: the row created before reopening the store is gone")
	}
	if got.ProviderPID != n.ProviderPID {
		t.Errorf("ProviderPID = %q, want %q", got.ProviderPID, n.ProviderPID)
	}
}

func TestNewUUIDIsUnique(t *testing.T) {
	a, err := NewUUID()
	if err != nil {
		t.Fatalf("NewUUID: %v", err)
	}
	b, err := NewUUID()
	if err != nil {
		t.Fatalf("NewUUID: %v", err)
	}
	if a == "" || b == "" {
		t.Fatal("NewUUID returned an empty string")
	}
	if a == b {
		t.Errorf("two calls to NewUUID both returned %q", a)
	}
}

func TestConcurrentCreateOnFileBackedStore(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dsbox.db"

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	const n = 30
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			neg := testNegotiation()
			neg.ProviderPID = fmt.Sprintf("urn:uuid:provider-%d", i)
			errs[i] = s.Create(neg)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Create goroutine %d: %v", i, err)
		}
	}

	for i := 0; i < n; i++ {
		pid := fmt.Sprintf("urn:uuid:provider-%d", i)
		if _, ok, err := s.Get(pid); err != nil {
			t.Errorf("Get %s: %v", pid, err)
		} else if !ok {
			t.Errorf("Get %s: not found after concurrent Create", pid)
		}
	}
}
