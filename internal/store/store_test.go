package store

import (
	"database/sql"
	"errors"
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
	if err := s.SetState(n.ProviderPID, n.State, "AGREED", updatedAt); err != nil {
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

	err = s.SetState("does-not-exist", "REQUESTED", "AGREED", time.Now())
	if err == nil {
		t.Fatal("SetState: expected an error updating a negotiation that does not exist")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SetState on a missing negotiation = %v, want ErrNotFound", err)
	}
}

// TestSetStateFromTheWrongStateIsRejected is the compare-and-swap: a caller
// that decided what to write from a read taken before someone else moved the
// negotiation must not win. The stale write has to fail, and fail
// distinguishably from a missing row, because the two need different
// handling — one is a lost race to drop, the other is a bug.
func TestSetStateFromTheWrongStateIsRejected(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testNegotiation()
	if err := s.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.SetState(n.ProviderPID, "REQUESTED", "TERMINATED", time.Now()); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	// The stale writer still believes the negotiation is REQUESTED.
	err = s.SetState(n.ProviderPID, "REQUESTED", "FINALIZED", time.Now())
	if err == nil {
		t.Fatal("SetState from a state the negotiation left = nil, want an error")
	}
	if !errors.Is(err, ErrStateChanged) {
		t.Errorf("SetState from a stale state = %v, want ErrStateChanged", err)
	}
	got, _, _ := s.Get(n.ProviderPID)
	if got.State != "TERMINATED" {
		t.Errorf("State = %q, want TERMINATED — the stale write overwrote it", got.State)
	}
}

// TestSetRerequestedIsOnlyAcceptedOnce covers the same rule for the
// re-request flag: DECISIONS.md section 23.9 allows exactly one, and two
// concurrent re-requests would both read the flag clear, so only the update
// itself can decide which one got it.
func TestSetRerequestedIsOnlyAcceptedOnce(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testNegotiation()
	if err := s.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.SetRerequested(n.ProviderPID); err != nil {
		t.Fatalf("first SetRerequested: %v", err)
	}
	err = s.SetRerequested(n.ProviderPID)
	if err == nil {
		t.Fatal("second SetRerequested = nil, want an error")
	}
	if !errors.Is(err, ErrStateChanged) {
		t.Errorf("second SetRerequested = %v, want ErrStateChanged", err)
	}
}

// TestOpenMigratesADatabaseMissingRerequested is the migration this project
// has exactly one of. A database file written by a build from before the
// rerequested column existed must keep working: `CREATE TABLE IF NOT EXISTS`
// is a no-op against the table it already has, so without a real migration
// step Open would succeed and every query naming the column would then fail
// with "no such column: rerequested".
func TestOpenMigratesADatabaseMissingRerequested(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dsbox.db"

	// The negotiations table exactly as a build before this column created it.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the old database: %v", err)
	}
	if _, err := old.Exec(`
CREATE TABLE negotiations (
    provider_pid     TEXT PRIMARY KEY,
    consumer_pid     TEXT NOT NULL,
    state            TEXT NOT NULL,
    dataset_id       TEXT NOT NULL,
    offer_id         TEXT NOT NULL,
    callback_address TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);`); err != nil {
		t.Fatalf("create the old schema: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the old database: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a database missing the column: %v", err)
	}
	defer s.Close()

	n := testNegotiation()
	if err := s.Create(n); err != nil {
		t.Fatalf("Create after migrating: %v", err)
	}
	if err := s.SetRerequested(n.ProviderPID); err != nil {
		t.Fatalf("SetRerequested after migrating: %v", err)
	}
	got, ok, err := s.Get(n.ProviderPID)
	if err != nil {
		t.Fatalf("Get after migrating: %v", err)
	}
	if !ok {
		t.Fatal("Get after migrating: not found")
	}
	if !got.Rerequested {
		t.Error("Rerequested = false, want true — the migrated column is not being written")
	}
}

// TestOpenTwiceDoesNotRepeatTheMigration proves the migration is idempotent
// the way startup needs it to be: every run of every build calls it.
func TestOpenTwiceDoesNotRepeatTheMigration(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dsbox.db"

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	n := testNegotiation()
	if err := s2.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
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

func testConsumerNegotiation() ConsumerNegotiation {
	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	return ConsumerNegotiation{
		ConsumerPID:     "urn:uuid:consumer-1",
		ProviderBaseURL: "https://provider.example.org",
		State:           "REQUESTED",
		DatasetID:       "urn:dataset:a",
		OfferID:         "urn:dataset:a#offer",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func TestCreateConsumerAndGetConsumer(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testConsumerNegotiation()
	if err := s.CreateConsumer(n); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}

	got, ok, err := s.GetConsumer(n.ConsumerPID)
	if err != nil {
		t.Fatalf("GetConsumer: %v", err)
	}
	if !ok {
		t.Fatal("GetConsumer: not found, want the created negotiation")
	}
	if got.ConsumerPID != n.ConsumerPID || got.ProviderPID != n.ProviderPID ||
		got.ProviderBaseURL != n.ProviderBaseURL || got.State != n.State ||
		got.DatasetID != n.DatasetID || got.OfferID != n.OfferID {
		t.Errorf("GetConsumer returned %+v, want %+v", got, n)
	}
	if !got.CreatedAt.Equal(n.CreatedAt) || !got.UpdatedAt.Equal(n.UpdatedAt) {
		t.Errorf("timestamps = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, n.CreatedAt, n.UpdatedAt)
	}
}

func TestGetConsumerMissingReturnsFalse(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	_, ok, err := s.GetConsumer("does-not-exist")
	if err != nil {
		t.Fatalf("GetConsumer: %v", err)
	}
	if ok {
		t.Error("GetConsumer: found a negotiation that was never created")
	}
}

func TestSetConsumerStateUpdatesRow(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testConsumerNegotiation()
	if err := s.CreateConsumer(n); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}

	updatedAt := n.UpdatedAt.Add(time.Hour)
	if err := s.SetConsumerState(n.ConsumerPID, "REQUESTED", "OFFERED", updatedAt); err != nil {
		t.Fatalf("SetConsumerState: %v", err)
	}

	got, ok, err := s.GetConsumer(n.ConsumerPID)
	if err != nil {
		t.Fatalf("GetConsumer: %v", err)
	}
	if !ok {
		t.Fatal("GetConsumer: not found after SetConsumerState")
	}
	if got.State != "OFFERED" {
		t.Errorf("State = %q, want OFFERED", got.State)
	}
	if !got.UpdatedAt.Equal(updatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, updatedAt)
	}
}

func TestSetConsumerStateFromTheWrongStateIsRejected(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testConsumerNegotiation()
	if err := s.CreateConsumer(n); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}

	err = s.SetConsumerState(n.ConsumerPID, "AGREED", "VERIFIED", time.Now())
	if !errors.Is(err, ErrStateChanged) {
		t.Errorf("SetConsumerState from the wrong state = %v, want ErrStateChanged", err)
	}
}

func TestSetConsumerStateMissingIsError(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	err = s.SetConsumerState("does-not-exist", "REQUESTED", "OFFERED", time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SetConsumerState on a missing negotiation = %v, want ErrNotFound", err)
	}
}

func TestSetConsumerProviderPIDUpdatesRow(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testConsumerNegotiation()
	if err := s.CreateConsumer(n); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}

	updated := n.CreatedAt.Add(time.Minute)
	if err := s.SetConsumerProviderPID(n.ConsumerPID, "urn:uuid:provider-1", updated); err != nil {
		t.Fatalf("SetConsumerProviderPID: %v", err)
	}

	got, ok, err := s.GetConsumer(n.ConsumerPID)
	if err != nil {
		t.Fatalf("GetConsumer: %v", err)
	}
	if !ok {
		t.Fatal("GetConsumer: not found")
	}
	if got.ProviderPID != "urn:uuid:provider-1" {
		t.Errorf("ProviderPID = %q, want urn:uuid:provider-1", got.ProviderPID)
	}
	// Recording the pid is a change to the row, so updated_at must move with
	// it — every other write here refreshes it, and a column that lied about
	// when the row last changed would be worse than not having one.
	if !got.UpdatedAt.Equal(updated) {
		t.Errorf("UpdatedAt = %s, want %s — recording the provider pid must refresh it",
			got.UpdatedAt, updated.UTC())
	}
}

func TestSetConsumerProviderPIDMissingIsError(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SetConsumerProviderPID("does-not-exist", "urn:uuid:provider-1", time.Now()); err == nil {
		t.Error("SetConsumerProviderPID: expected an error updating a negotiation that does not exist")
	}
}

func TestOpenPersistsBothTablesAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dsbox.db"

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	n := testConsumerNegotiation()
	if err := s1.CreateConsumer(n); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	got, ok, err := s2.GetConsumer(n.ConsumerPID)
	if err != nil {
		t.Fatalf("GetConsumer: %v", err)
	}
	if !ok {
		t.Fatal("GetConsumer: the row created before reopening the store is gone")
	}
	if got.ConsumerPID != n.ConsumerPID {
		t.Errorf("ConsumerPID = %q, want %q", got.ConsumerPID, n.ConsumerPID)
	}
}

func testAgreement() Agreement {
	return Agreement{
		AgreementID: "urn:uuid:agreement-1",
		DatasetID:   "urn:dataset:a",
		ConsumerPID: "urn:uuid:consumer-1",
		Origin:      OriginNegotiated,
		CreatedAt:   time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
	}
}

func TestCreateAndGetAgreement(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	a := testAgreement()
	if err := s.CreateAgreement(a); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}

	got, ok, err := s.GetAgreement(a.AgreementID)
	if err != nil {
		t.Fatalf("GetAgreement: %v", err)
	}
	if !ok {
		t.Fatal("GetAgreement: agreement not found after CreateAgreement")
	}
	if got.DatasetID != a.DatasetID || got.ConsumerPID != a.ConsumerPID || got.Origin != a.Origin {
		t.Errorf("GetAgreement = %+v, want %+v", got, a)
	}
	if !got.CreatedAt.Equal(a.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, a.CreatedAt)
	}
}

func TestGetAgreementMissingIsNotFound(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	_, ok, err := s.GetAgreement("urn:uuid:nope")
	if err != nil {
		t.Fatalf("GetAgreement: %v", err)
	}
	if ok {
		t.Error("GetAgreement: reported an agreement that was never created")
	}
}

func TestCreateAgreementDuplicateIsAnError(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	a := testAgreement()
	if err := s.CreateAgreement(a); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	a.DatasetID = "urn:dataset:b"
	if err := s.CreateAgreement(a); err == nil {
		t.Error("CreateAgreement: expected an error re-creating an existing agreement, which would silently rewrite its dataset")
	}

	got, _, err := s.GetAgreement(a.AgreementID)
	if err != nil {
		t.Fatalf("GetAgreement: %v", err)
	}
	if got.DatasetID != "urn:dataset:a" {
		t.Errorf("DatasetID = %q after the rejected duplicate, want urn:dataset:a — the row was overwritten", got.DatasetID)
	}
}
