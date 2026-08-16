// Package store persists connector runtime state — currently the contract
// negotiation state machine — in a single SQLite file.
package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store opens the connector's SQLite database and exposes negotiation
// persistence. Open pins the underlying *sql.DB to a single connection
// (SetMaxOpenConns(1)), so database/sql itself serializes every query
// through it and a single Store is safe for concurrent use. Without that,
// database/sql's connection pool opens multiple physical connections, and
// since SQLite's WAL mode allows only one writer at a time, a second writer
// gets an immediate SQLITE_BUSY instead of queuing behind the first.
type Store struct {
	db *sql.DB
}

// Negotiation is one persisted contract negotiation.
type Negotiation struct {
	ProviderPID     string
	ConsumerPID     string
	State           string
	DatasetID       string
	OfferID         string
	CallbackAddress string
	// Rerequested is whether POST /negotiations/{id}/request has already
	// been accepted once while this negotiation was OFFERED. The real TCK
	// (CN:03-04) confirmed a re-request that repeats the offer already on
	// the table is accepted the first time and rejected the second — see
	// negotiation_handler.go's handleReRequest doc comment for the rule
	// this field enforces.
	Rerequested bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ConsumerNegotiation is one persisted contract negotiation this connector
// is running as consumer — the mirror of Negotiation, which is this
// connector's provider-role state. Keyed by this connector's own generated
// consumer pid, not the provider's. A second table rather than a role
// column on negotiations: see the design spec's Storage section.
type ConsumerNegotiation struct {
	ConsumerPID string
	// ProviderPID is empty until the initial request's synchronous response
	// reveals it.
	ProviderPID string
	// ProviderBaseURL is the connectorAddress POST /negotiations/initiate
	// supplied — every subsequent outbound call this connector makes as
	// consumer for this negotiation is addressed relative to it.
	ProviderBaseURL string
	State           string
	DatasetID       string
	OfferID         string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS negotiations (
    provider_pid     TEXT PRIMARY KEY,
    consumer_pid     TEXT NOT NULL,
    state            TEXT NOT NULL,
    dataset_id       TEXT NOT NULL,
    offer_id         TEXT NOT NULL,
    callback_address TEXT NOT NULL,
    rerequested      INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);`

const consumerSchema = `
CREATE TABLE IF NOT EXISTS consumer_negotiations (
    consumer_pid      TEXT PRIMARY KEY,
    provider_pid      TEXT NOT NULL DEFAULT '',
    provider_base_url TEXT NOT NULL,
    state             TEXT NOT NULL,
    dataset_id        TEXT NOT NULL,
    offer_id          TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);`

const timeFormat = time.RFC3339Nano

// Errors a conditional update can report. Both mean the UPDATE matched no
// row; they differ in why, which is the difference between a bug and a lost
// race, so callers can tell them apart with errors.Is.
var (
	// ErrNotFound means there is no negotiation with that provider pid.
	ErrNotFound = errors.New("negotiation not found")
	// ErrStateChanged means the negotiation exists but no longer holds the
	// value the caller expected to update from — something else changed it
	// first, and the caller's decision was made against a stale read.
	ErrStateChanged = errors.New("negotiation changed concurrently")
)

// Open opens (creating if necessary) the SQLite file at path, enables WAL
// mode, ensures the schema exists, and applies the one schema migration this
// project has (see migrate). path may be ":memory:" for tests —
// DECISIONS.md section 8 reserves that for tests only, never a runtime path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL on %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema in %s: %w", path, err)
	}
	if _, err := db.Exec(consumerSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create consumer schema in %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// migrate brings an older database file up to the schema above. There is no
// migration framework and no version table (DECISIONS.md section 23.1): each
// column added after the first release is one hand-written, self-checking
// step here.
//
// The check is what makes this a migration at all. `CREATE TABLE IF NOT
// EXISTS` is a no-op against a table that already exists, so editing the
// schema literal changes nothing for a database file an earlier build
// created — it opens without complaint and then fails on the first query
// naming the new column. `pragma_table_info` is SQLite's table-valued form of
// `PRAGMA table_info(negotiations)`, one row per column with the column's
// name in `name`. On a fresh database the CREATE above already made the
// column, so the check finds it and nothing runs; on an older one the column
// is added. Idempotent either way.
func migrate(db *sql.DB) error {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('negotiations') WHERE name = 'rerequested'`,
	).Scan(&n); err != nil {
		return fmt.Errorf("inspect negotiations columns: %w", err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE negotiations ADD COLUMN rerequested INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("add rerequested column: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// NewUUID generates a random RFC 4122 v4 UUID string. It is used both for a
// new negotiation's provider pid and for the @id of every outgoing DSP
// message. crypto/rand rather than a UUID package: CLAUDE.md's default
// answer to a dependency question is the standard library, and this project
// fully controls the value's shape.
func NewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Create persists a new negotiation.
func (s *Store) Create(n Negotiation) error {
	_, err := s.db.Exec(
		`INSERT INTO negotiations (provider_pid, consumer_pid, state, dataset_id, offer_id, callback_address, rerequested, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ProviderPID, n.ConsumerPID, n.State, n.DatasetID, n.OfferID, n.CallbackAddress, n.Rerequested,
		n.CreatedAt.UTC().Format(timeFormat), n.UpdatedAt.UTC().Format(timeFormat),
	)
	if err != nil {
		return fmt.Errorf("create negotiation %s: %w", n.ProviderPID, err)
	}
	return nil
}

// Get returns the negotiation with the given provider pid.
func (s *Store) Get(providerPID string) (Negotiation, bool, error) {
	row := s.db.QueryRow(
		`SELECT provider_pid, consumer_pid, state, dataset_id, offer_id, callback_address, rerequested, created_at, updated_at
		 FROM negotiations WHERE provider_pid = ?`, providerPID)

	var n Negotiation
	var created, updated string
	err := row.Scan(&n.ProviderPID, &n.ConsumerPID, &n.State, &n.DatasetID, &n.OfferID,
		&n.CallbackAddress, &n.Rerequested, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Negotiation{}, false, nil
	}
	if err != nil {
		return Negotiation{}, false, fmt.Errorf("get negotiation %s: %w", providerPID, err)
	}
	if n.CreatedAt, err = time.Parse(timeFormat, created); err != nil {
		return Negotiation{}, false, fmt.Errorf("get negotiation %s: parse created_at: %w", providerPID, err)
	}
	if n.UpdatedAt, err = time.Parse(timeFormat, updated); err != nil {
		return Negotiation{}, false, fmt.Errorf("get negotiation %s: parse updated_at: %w", providerPID, err)
	}
	return n, true, nil
}

// SetState moves a negotiation from state `from` to state `to`, recording
// updatedAt. It is a compare-and-swap, not an overwrite: the UPDATE matches
// only while the stored state is still `from`, so a caller that decided what
// to write from an earlier read cannot silently clobber a decision another
// request made in between. If the state moved on, the update does nothing
// and the error wraps ErrStateChanged — the caller lost the race and should
// drop what it was about to do, not retry it.
//
// This matters because pushes run in their own goroutines (DECISIONS.md
// section 23.8) and can live for the length of a whole retry schedule. Without
// the condition, a goroutine started before a termination arrived would
// finish afterwards and write the state the terminated negotiation was never
// in.
func (s *Store) SetState(providerPID, from, to string, updatedAt time.Time) error {
	res, err := s.db.Exec(`UPDATE negotiations SET state = ?, updated_at = ? WHERE provider_pid = ? AND state = ?`,
		to, updatedAt.UTC().Format(timeFormat), providerPID, from)
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	if rows == 0 {
		return s.explainNoUpdate(providerPID, "state "+from)
	}
	return nil
}

// SetRerequested marks a negotiation as having accepted its one allowed
// re-request while OFFERED. See Negotiation.Rerequested. Like SetState it is
// conditional — the UPDATE matches only while the flag is still unset — so
// the "exactly one re-request" rule holds even if two re-requests read the
// negotiation at the same time and both see the flag clear. The second one's
// error wraps ErrStateChanged, which is the same rejection the read told the
// first one to make.
func (s *Store) SetRerequested(providerPID string) error {
	res, err := s.db.Exec(`UPDATE negotiations SET rerequested = 1 WHERE provider_pid = ? AND rerequested = 0`, providerPID)
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	if rows == 0 {
		return s.explainNoUpdate(providerPID, "rerequested = 0")
	}
	return nil
}

// explainNoUpdate says why a conditional UPDATE matched no row: the
// negotiation is gone (ErrNotFound), or it is there but no longer holds the
// value the caller expected (ErrStateChanged). The two need different
// handling — one is a bug or a deleted row, the other is a lost race — and
// RowsAffected alone cannot tell them apart. want describes what the caller
// required, for the log line.
func (s *Store) explainNoUpdate(providerPID, want string) error {
	n, ok, err := s.Get(providerPID)
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	if !ok {
		return fmt.Errorf("update negotiation %s: %w", providerPID, ErrNotFound)
	}
	return fmt.Errorf("update negotiation %s: %w: wanted %s, found state %s, rerequested %t",
		providerPID, ErrStateChanged, want, n.State, n.Rerequested)
}

// CreateConsumer persists a new consumer-role negotiation.
func (s *Store) CreateConsumer(n ConsumerNegotiation) error {
	_, err := s.db.Exec(
		`INSERT INTO consumer_negotiations (consumer_pid, provider_pid, provider_base_url, state, dataset_id, offer_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ConsumerPID, n.ProviderPID, n.ProviderBaseURL, n.State, n.DatasetID, n.OfferID,
		n.CreatedAt.UTC().Format(timeFormat), n.UpdatedAt.UTC().Format(timeFormat),
	)
	if err != nil {
		return fmt.Errorf("create consumer negotiation %s: %w", n.ConsumerPID, err)
	}
	return nil
}

// GetConsumer returns the consumer-role negotiation with the given consumer pid.
func (s *Store) GetConsumer(consumerPID string) (ConsumerNegotiation, bool, error) {
	row := s.db.QueryRow(
		`SELECT consumer_pid, provider_pid, provider_base_url, state, dataset_id, offer_id, created_at, updated_at
		 FROM consumer_negotiations WHERE consumer_pid = ?`, consumerPID)

	var n ConsumerNegotiation
	var created, updated string
	err := row.Scan(&n.ConsumerPID, &n.ProviderPID, &n.ProviderBaseURL, &n.State, &n.DatasetID, &n.OfferID,
		&created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return ConsumerNegotiation{}, false, nil
	}
	if err != nil {
		return ConsumerNegotiation{}, false, fmt.Errorf("get consumer negotiation %s: %w", consumerPID, err)
	}
	if n.CreatedAt, err = time.Parse(timeFormat, created); err != nil {
		return ConsumerNegotiation{}, false, fmt.Errorf("get consumer negotiation %s: parse created_at: %w", consumerPID, err)
	}
	if n.UpdatedAt, err = time.Parse(timeFormat, updated); err != nil {
		return ConsumerNegotiation{}, false, fmt.Errorf("get consumer negotiation %s: parse updated_at: %w", consumerPID, err)
	}
	return n, true, nil
}

// SetConsumerState moves a consumer-role negotiation from state `from` to
// state `to` — the same compare-and-swap SetState uses for the provider
// role, for the same reason: consumer-role reactions also run in goroutines
// (DECISIONS.md section 23.8) and can outlive a termination that arrived in
// the meantime.
func (s *Store) SetConsumerState(consumerPID, from, to string, updatedAt time.Time) error {
	res, err := s.db.Exec(`UPDATE consumer_negotiations SET state = ?, updated_at = ? WHERE consumer_pid = ? AND state = ?`,
		to, updatedAt.UTC().Format(timeFormat), consumerPID, from)
	if err != nil {
		return fmt.Errorf("update consumer negotiation %s: %w", consumerPID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update consumer negotiation %s: %w", consumerPID, err)
	}
	if rows == 0 {
		return s.explainNoConsumerUpdate(consumerPID, "state "+from)
	}
	return nil
}

// SetConsumerProviderPID records the counterparty's pid once the initial
// request's synchronous response reveals it. A plain update, not a CAS:
// nothing else ever writes this column, so there is no concurrent write to
// lose a race against — see the design spec's "The initial request"
// section. It refreshes updated_at like every other write here: this is a
// change to the row, and leaving the column alone would have it report a
// time the row demonstrably did not last change at.
func (s *Store) SetConsumerProviderPID(consumerPID, providerPID string, updatedAt time.Time) error {
	res, err := s.db.Exec(`UPDATE consumer_negotiations SET provider_pid = ?, updated_at = ? WHERE consumer_pid = ?`,
		providerPID, updatedAt.UTC().Format(timeFormat), consumerPID)
	if err != nil {
		return fmt.Errorf("update consumer negotiation %s: %w", consumerPID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update consumer negotiation %s: %w", consumerPID, err)
	}
	if rows == 0 {
		return fmt.Errorf("update consumer negotiation %s: %w", consumerPID, ErrNotFound)
	}
	return nil
}

// explainNoConsumerUpdate is explainNoUpdate's consumer-table counterpart —
// kept separate because explainNoUpdate hard-codes a Get against
// negotiations and would name the wrong table's state otherwise.
func (s *Store) explainNoConsumerUpdate(consumerPID, want string) error {
	n, ok, err := s.GetConsumer(consumerPID)
	if err != nil {
		return fmt.Errorf("update consumer negotiation %s: %w", consumerPID, err)
	}
	if !ok {
		return fmt.Errorf("update consumer negotiation %s: %w", consumerPID, ErrNotFound)
	}
	return fmt.Errorf("update consumer negotiation %s: %w: wanted %s, found state %s",
		consumerPID, ErrStateChanged, want, n.State)
}
