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

const timeFormat = time.RFC3339Nano

// Open opens (creating if necessary) the SQLite file at path, enables WAL
// mode, and ensures the schema exists. path may be ":memory:" for tests —
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
	return &Store{db: db}, nil
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

// SetState updates a negotiation's state and updated_at.
func (s *Store) SetState(providerPID, state string, updatedAt time.Time) error {
	res, err := s.db.Exec(`UPDATE negotiations SET state = ?, updated_at = ? WHERE provider_pid = ?`,
		state, updatedAt.UTC().Format(timeFormat), providerPID)
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	if rows == 0 {
		return fmt.Errorf("update negotiation %s: not found", providerPID)
	}
	return nil
}

// SetRerequested marks a negotiation as having accepted its one allowed
// re-request while OFFERED. See Negotiation.Rerequested.
func (s *Store) SetRerequested(providerPID string) error {
	res, err := s.db.Exec(`UPDATE negotiations SET rerequested = 1 WHERE provider_pid = ?`, providerPID)
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update negotiation %s: %w", providerPID, err)
	}
	if rows == 0 {
		return fmt.Errorf("update negotiation %s: not found", providerPID)
	}
	return nil
}
