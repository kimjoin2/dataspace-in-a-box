package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

// TestCreateAgreementIfNegotiationAgreedRecordsWhenStateMatches pins the
// positive case: a negotiation sitting in one of the allowed states gets its
// agreement recorded, and the method reports that it did.
func TestCreateAgreementIfNegotiationAgreedRecordsWhenStateMatches(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testNegotiation()
	n.State = "AGREED"
	if err := s.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
	}

	a := testAgreement()
	// Set here rather than on the shared fixture: this is the one INSERT
	// carrying counterparty_id that nothing else round-trips, and a zero value
	// would leave the assertion below vacuous.
	a.CounterpartyID = "urn:participant:peer"
	recorded, currentState, err := s.CreateAgreementIfNegotiationAgreed(n.ProviderPID, []string{"AGREED", "VERIFIED", "FINALIZED"}, a)
	if err != nil {
		t.Fatalf("CreateAgreementIfNegotiationAgreed: %v", err)
	}
	if !recorded {
		t.Fatalf("recorded = false, want true — negotiation was in an allowed state (%q)", currentState)
	}
	if currentState != "AGREED" {
		t.Errorf("currentState = %q, want AGREED", currentState)
	}

	got, ok, err := s.GetAgreement(a.AgreementID)
	if err != nil {
		t.Fatalf("GetAgreement: %v", err)
	}
	if !ok {
		t.Fatal("GetAgreement: no row written despite recorded = true")
	}
	if got.DatasetID != a.DatasetID {
		t.Errorf("DatasetID = %q, want %q", got.DatasetID, a.DatasetID)
	}
	// The second of the two INSERTs into agreements, and until now the
	// uncovered one: CreateAgreement's counterparty round trip is pinned by
	// TestAgreementRoundTripsItsCounterparty, and dropping the column from
	// this statement alone failed nothing.
	if got.CounterpartyID != a.CounterpartyID {
		t.Errorf("CounterpartyID = %q, want %q", got.CounterpartyID, a.CounterpartyID)
	}
}

// TestCreateAgreementIfNegotiationAgreedSkipsWhenStateDoesNotMatch is the
// negative case this method exists to close atomically: a negotiation that
// moved to a state outside the allowed set (a termination racing the push)
// must leave no agreement row behind.
func TestCreateAgreementIfNegotiationAgreedSkipsWhenStateDoesNotMatch(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	n := testNegotiation()
	n.State = "TERMINATED"
	if err := s.Create(n); err != nil {
		t.Fatalf("Create: %v", err)
	}

	a := testAgreement()
	recorded, currentState, err := s.CreateAgreementIfNegotiationAgreed(n.ProviderPID, []string{"AGREED", "VERIFIED", "FINALIZED"}, a)
	if err != nil {
		t.Fatalf("CreateAgreementIfNegotiationAgreed: %v", err)
	}
	if recorded {
		t.Error("recorded = true, want false — TERMINATED is not in the allowed set")
	}
	if currentState != "TERMINATED" {
		t.Errorf("currentState = %q, want TERMINATED", currentState)
	}

	if _, ok, err := s.GetAgreement(a.AgreementID); err != nil {
		t.Fatalf("GetAgreement: %v", err)
	} else if ok {
		t.Error("GetAgreement: found a row despite recorded = false")
	}
}

// TestCreateAgreementIfNegotiationAgreedMissingNegotiationIsNotRecorded
// covers the case with no row to check at all — must behave like "not
// allowed", not error.
func TestCreateAgreementIfNegotiationAgreedMissingNegotiationIsNotRecorded(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	a := testAgreement()
	recorded, currentState, err := s.CreateAgreementIfNegotiationAgreed("does-not-exist", []string{"AGREED"}, a)
	if err != nil {
		t.Fatalf("CreateAgreementIfNegotiationAgreed: %v", err)
	}
	if recorded {
		t.Error("recorded = true, want false — there is no negotiation to have agreed")
	}
	if currentState != "" {
		t.Errorf("currentState = %q, want empty for a missing negotiation", currentState)
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

func testTransfer() TransferProcess {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	return TransferProcess{
		ProviderPID:     "urn:uuid:tp-provider-1",
		ConsumerPID:     "urn:uuid:tp-consumer-1",
		AgreementID:     "urn:uuid:agreement-1",
		State:           "REQUESTED",
		CallbackAddress: "http://consumer.example/2025-1",
		Format:          "HTTP-PULL",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func TestCreateAndGetTransfer(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	tp := testTransfer()
	if err := s.CreateTransfer(tp); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	got, ok, err := s.GetTransfer(tp.ProviderPID)
	if err != nil {
		t.Fatalf("GetTransfer: %v", err)
	}
	if !ok {
		t.Fatal("GetTransfer: transfer not found after CreateTransfer")
	}
	if got.ConsumerPID != tp.ConsumerPID || got.AgreementID != tp.AgreementID ||
		got.State != tp.State || got.CallbackAddress != tp.CallbackAddress || got.Format != tp.Format {
		t.Errorf("GetTransfer = %+v, want %+v", got, tp)
	}
}

func TestGetTransferMissingIsNotFound(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	_, ok, err := s.GetTransfer("urn:uuid:nope")
	if err != nil {
		t.Fatalf("GetTransfer: %v", err)
	}
	if ok {
		t.Error("GetTransfer: reported a transfer that was never created")
	}
}

func TestSetTransferStateAdvances(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	tp := testTransfer()
	if err := s.CreateTransfer(tp); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	updated := tp.UpdatedAt.Add(time.Minute)
	if err := s.SetTransferState(tp.ProviderPID, "REQUESTED", "STARTED", updated); err != nil {
		t.Fatalf("SetTransferState: %v", err)
	}

	got, _, err := s.GetTransfer(tp.ProviderPID)
	if err != nil {
		t.Fatalf("GetTransfer: %v", err)
	}
	if got.State != "STARTED" {
		t.Errorf("State = %q, want STARTED", got.State)
	}
	if !got.UpdatedAt.Equal(updated) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, updated)
	}
}

func TestSetTransferStateWrongFromIsStateChanged(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	tp := testTransfer()
	if err := s.CreateTransfer(tp); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	if err := s.SetTransferState(tp.ProviderPID, "STARTED", "COMPLETED", time.Now().UTC()); !errors.Is(err, ErrStateChanged) {
		t.Errorf("SetTransferState from a state the row does not hold = %v, want ErrStateChanged", err)
	}
}

func TestSetTransferStateMissingIsNotFound(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SetTransferState("urn:uuid:nope", "REQUESTED", "STARTED", time.Now().UTC()); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetTransferState on a missing transfer = %v, want ErrNotFound", err)
	}
}

// TestSentinelErrorsNameNoProtocol pins the two sentinel texts. Both are
// returned by three tables now — negotiations, consumer_negotiations, and
// transfer_processes — and both reach the connector log, which
// test/tck/run.sh captures as this project's evidence surface for what
// happened on the wire. A sentinel that says "negotiation" makes every
// transfer warning that carries it read as a negotiation fault. Nothing else
// asserts these strings, and errors.Is would keep passing through a wrong one.
func TestSentinelErrorsNameNoProtocol(t *testing.T) {
	if got, want := ErrNotFound.Error(), "record not found"; got != want {
		t.Errorf("ErrNotFound = %q, want %q", got, want)
	}
	if got, want := ErrStateChanged.Error(), "record changed concurrently"; got != want {
		t.Errorf("ErrStateChanged = %q, want %q", got, want)
	}
	for _, err := range []error{ErrNotFound, ErrStateChanged} {
		for _, protocol := range []string{"negotiation", "transfer", "agreement"} {
			if strings.Contains(err.Error(), protocol) {
				t.Errorf("%q names %q: it is returned for every table, so its text must name none", err, protocol)
			}
		}
	}
}

// TestTransferErrorNamesTheTransfer is the other half: the sentinel names no
// table, so the wrapping message must. Without it a "record changed
// concurrently" line in the log says nothing about what changed.
func TestTransferErrorNamesTheTransfer(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	tp := testTransfer()
	if err := s.CreateTransfer(tp); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	got := s.SetTransferState(tp.ProviderPID, "STARTED", "COMPLETED", time.Now().UTC()).Error()
	if !strings.Contains(got, "update transfer") || !strings.Contains(got, tp.ProviderPID) {
		t.Errorf("SetTransferState error = %q, want it to name the transfer and its pid", got)
	}
}

func TestConsumerTransferRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)
	want := ConsumerTransfer{
		ConsumerPID:     "urn:uuid:c-1",
		ProviderBaseURL: "http://provider.example/2025-1",
		AgreementID:     "urn:uuid:a-1",
		Format:          "HTTP-PULL",
		State:           "REQUESTED",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.CreateConsumerTransfer(want); err != nil {
		t.Fatalf("CreateConsumerTransfer: %v", err)
	}
	got, ok, err := s.GetConsumerTransfer("urn:uuid:c-1")
	if err != nil || !ok {
		t.Fatalf("GetConsumerTransfer: ok=%v err=%v", ok, err)
	}
	// ProviderPID is empty until the ACK to the initial request reveals it.
	if got.ProviderPID != "" {
		t.Errorf("ProviderPID = %q, want empty", got.ProviderPID)
	}
	if got.AgreementID != want.AgreementID || got.Format != want.Format ||
		got.ProviderBaseURL != want.ProviderBaseURL || got.State != want.State {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestGetConsumerTransferUnknownIsNotAnError(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	_, ok, err := s.GetConsumerTransfer("urn:uuid:nope")
	if err != nil {
		t.Fatalf("GetConsumerTransfer: %v", err)
	}
	if ok {
		t.Error("ok = true for an id that was never written")
	}
}

// The consumer table needs its own compare-and-swap for the same reason the
// provider table has one: the driver runs in a goroutine and can outlive a
// termination that arrived while it was sleeping between steps.
func TestSetConsumerTransferStateIsCompareAndSwap(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	if err := s.CreateConsumerTransfer(ConsumerTransfer{
		ConsumerPID: "urn:uuid:c-2", ProviderBaseURL: "http://p.example/2025-1",
		AgreementID: "urn:uuid:a-2", Format: "HTTP-PULL", State: "REQUESTED",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateConsumerTransfer: %v", err)
	}
	if err := s.SetConsumerTransferState("urn:uuid:c-2", "REQUESTED", "STARTED", now); err != nil {
		t.Fatalf("first update: %v", err)
	}
	err = s.SetConsumerTransferState("urn:uuid:c-2", "REQUESTED", "COMPLETED", now)
	if !errors.Is(err, ErrStateChanged) {
		t.Errorf("stale update error = %v, want ErrStateChanged", err)
	}
	err = s.SetConsumerTransferState("urn:uuid:missing", "REQUESTED", "STARTED", now)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("missing row error = %v, want ErrNotFound", err)
	}
}

func TestSetConsumerTransferProviderPID(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	if err := s.CreateConsumerTransfer(ConsumerTransfer{
		ConsumerPID: "urn:uuid:c-3", ProviderBaseURL: "http://p.example/2025-1",
		AgreementID: "urn:uuid:a-3", Format: "HTTP-PULL", State: "REQUESTED",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateConsumerTransfer: %v", err)
	}
	if err := s.SetConsumerTransferProviderPID("urn:uuid:c-3", "urn:uuid:p-3", now); err != nil {
		t.Fatalf("SetConsumerTransferProviderPID: %v", err)
	}
	got, _, err := s.GetConsumerTransfer("urn:uuid:c-3")
	if err != nil {
		t.Fatalf("GetConsumerTransfer: %v", err)
	}
	if got.ProviderPID != "urn:uuid:p-3" {
		t.Errorf("ProviderPID = %q, want urn:uuid:p-3", got.ProviderPID)
	}
}

// The counterparty is who the row is with, and every exchange table carries
// it so an outbound message can be addressed. A column added to the SELECT
// without a matching Scan target compiles cleanly and fails only at runtime,
// so each table gets a round trip.
//
// Four tables, not five: agreements carries the same column and is covered by
// TestAgreementRoundTripsItsCounterparty (CreateAgreement, GetAgreement,
// ListAgreements) and by
// TestCreateAgreementIfNegotiationAgreedRecordsWhenStateMatches (the second
// INSERT). This test's old name said "EveryTable" while excluding it, which
// is how that second INSERT stayed uncovered.
func TestCounterpartyIDRoundTripsOnEveryExchangeTable(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	now := time.Now().UTC()
	const peer = "urn:participant:peer"

	if err := s.Create(Negotiation{ProviderPID: "p1", ConsumerPID: "c1", State: "REQUESTED",
		DatasetID: "d", OfferID: "o", CallbackAddress: "http://x", CounterpartyID: peer,
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, _, err := s.Get("p1"); err != nil || got.CounterpartyID != peer {
		t.Errorf("negotiations: %q, %v", got.CounterpartyID, err)
	}

	if err := s.CreateConsumer(ConsumerNegotiation{ConsumerPID: "c2", ProviderBaseURL: "http://p",
		State: "REQUESTED", DatasetID: "d", OfferID: "o", CounterpartyID: peer,
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}
	if got, _, err := s.GetConsumer("c2"); err != nil || got.CounterpartyID != peer {
		t.Errorf("consumer_negotiations: %q, %v", got.CounterpartyID, err)
	}

	if err := s.CreateTransfer(TransferProcess{ProviderPID: "p3", ConsumerPID: "c3",
		AgreementID: "a", State: "REQUESTED", CallbackAddress: "http://x", Format: "HTTP-PULL",
		CounterpartyID: peer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	if got, _, err := s.GetTransfer("p3"); err != nil || got.CounterpartyID != peer {
		t.Errorf("transfer_processes: %q, %v", got.CounterpartyID, err)
	}

	if err := s.CreateConsumerTransfer(ConsumerTransfer{ConsumerPID: "c4", ProviderBaseURL: "http://p",
		AgreementID: "a", Format: "HTTP-PULL", State: "REQUESTED", CounterpartyID: peer,
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateConsumerTransfer: %v", err)
	}
	if got, _, err := s.GetConsumerTransfer("c4"); err != nil || got.CounterpartyID != peer {
		t.Errorf("consumer_transfer_processes: %q, %v", got.CounterpartyID, err)
	}
}

func TestAgreementRoundTripsItsCounterparty(t *testing.T) {
	t.Parallel()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	want := Agreement{
		AgreementID: "urn:uuid:a", DatasetID: "urn:dataset:a",
		Origin: OriginNegotiated, CounterpartyID: "urn:participant:peer",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := s.CreateAgreement(want); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	got, ok, err := s.GetAgreement("urn:uuid:a")
	if err != nil || !ok {
		t.Fatalf("GetAgreement: %v ok=%t", err, ok)
	}
	if got.CounterpartyID != want.CounterpartyID {
		t.Fatalf("counterparty = %q, want %q", got.CounterpartyID, want.CounterpartyID)
	}
	list, err := s.ListAgreements()
	if err != nil {
		t.Fatalf("ListAgreements: %v", err)
	}
	if len(list) != 1 || list[0].CounterpartyID != want.CounterpartyID {
		t.Fatalf("ListAgreements gave %+v", list)
	}
}

func TestConsumerTransferExpectedBytesRoundTrips(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	if err := st.CreateConsumerTransfer(ConsumerTransfer{
		ConsumerPID: "urn:uuid:c1", ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a1",
		Format: "HttpData-PULL", State: "REQUESTED", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, found, err := st.GetConsumerTransfer("urn:uuid:c1")
	if err != nil || !found {
		t.Fatalf("get: %v found=%v", err, found)
	}
	if got.ExpectedBytes != 0 {
		t.Errorf("a fresh row has ExpectedBytes = %d, want 0 — zero means not known", got.ExpectedBytes)
	}

	if err := st.SetConsumerTransferExpectedBytes("urn:uuid:c1", 4096); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _, err = st.GetConsumerTransfer("urn:uuid:c1")
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if got.ExpectedBytes != 4096 {
		t.Errorf("ExpectedBytes = %d, want 4096", got.ExpectedBytes)
	}
}

func TestConsumerTransferOutcomeRoundTrips(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC().Truncate(time.Second)
	if err := st.CreateConsumerTransfer(ConsumerTransfer{
		ConsumerPID: "urn:uuid:o1", ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a1",
		Format: "HttpData-PULL", State: "STARTED", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, found, err := st.GetConsumerTransfer("urn:uuid:o1")
	if err != nil || !found {
		t.Fatalf("get: %v found=%v", err, found)
	}
	if got.ReceivedBytes != 0 || got.DataPath != "" || got.DataError != "" || !got.DataCompletedAt.IsZero() {
		t.Errorf("a fresh row is not blank: %+v — a transfer that has not fetched anything must read as one", got)
	}

	// A success: bytes, a path, a completion, and no failure.
	if err := st.RecordConsumerTransferOutcome("urn:uuid:o1", 4096, "/data/downloads/urn:uuid:o1", now, ""); err != nil {
		t.Fatalf("record success: %v", err)
	}
	got, _, err = st.GetConsumerTransfer("urn:uuid:o1")
	if err != nil {
		t.Fatalf("get after success: %v", err)
	}
	if got.ReceivedBytes != 4096 {
		t.Errorf("ReceivedBytes = %d, want 4096", got.ReceivedBytes)
	}
	if got.DataPath != "/data/downloads/urn:uuid:o1" {
		t.Errorf("DataPath = %q, want the published path", got.DataPath)
	}
	if !got.DataCompletedAt.Equal(now) {
		t.Errorf("DataCompletedAt = %v, want %v", got.DataCompletedAt, now)
	}
	if got.DataError != "" {
		t.Errorf("DataError = %q, want empty on a success", got.DataError)
	}

	// A failure on a later attempt: the reason is recorded and the
	// completion is cleared, so the row cannot read as both at once.
	if err := st.RecordConsumerTransferOutcome("urn:uuid:o1", 100, "", time.Time{}, "no progress within the idle timeout"); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	got, _, err = st.GetConsumerTransfer("urn:uuid:o1")
	if err != nil {
		t.Fatalf("get after failure: %v", err)
	}
	if got.DataError != "no progress within the idle timeout" {
		t.Errorf("DataError = %q, want the reason", got.DataError)
	}
	if !got.DataCompletedAt.IsZero() {
		t.Errorf("DataCompletedAt = %v, want zero — a failed attempt must not leave a completion behind", got.DataCompletedAt)
	}
	if got.DataPath != "" {
		t.Errorf("DataPath = %q, want empty — nothing was published", got.DataPath)
	}
}

func TestRecordConsumerTransferOutcomeOnAMissingRowIsNotAnError(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Many tests call pullTransferData directly with no seeded row, and
	// the deferred recorder Task 2 adds runs on every one of them. This must
	// stay a silent no-op or all of them start failing.
	if err := st.RecordConsumerTransferOutcome("urn:uuid:absent", 1, "/x", time.Now().UTC(), ""); err != nil {
		t.Errorf("recording against a missing row returned %v, want nil", err)
	}
}

func TestListTransfersReturnsBothRolesInOrder(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	base := time.Now().UTC().Truncate(time.Second)
	if err := st.CreateConsumerTransfer(ConsumerTransfer{
		ConsumerPID: "urn:uuid:c-2", ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a",
		Format: "HttpData-PULL", State: "STARTED", CreatedAt: base.Add(time.Second), UpdatedAt: base,
	}); err != nil {
		t.Fatalf("create consumer 2: %v", err)
	}
	if err := st.CreateConsumerTransfer(ConsumerTransfer{
		ConsumerPID: "urn:uuid:c-1", ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a",
		Format: "HttpData-PULL", State: "COMPLETED", CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatalf("create consumer 1: %v", err)
	}

	consumers, err := st.ListConsumerTransfers()
	if err != nil {
		t.Fatalf("list consumer transfers: %v", err)
	}
	if len(consumers) != 2 {
		t.Fatalf("got %d consumer transfers, want 2", len(consumers))
	}
	if consumers[0].ConsumerPID != "urn:uuid:c-1" {
		t.Errorf("first is %q, want the oldest — the list is ordered by creation like ListAgreements", consumers[0].ConsumerPID)
	}

	providers, err := st.ListTransfers()
	if err != nil {
		t.Fatalf("list transfers: %v", err)
	}
	if len(providers) != 0 {
		t.Errorf("got %d provider transfers, want 0 — none were created", len(providers))
	}
}
