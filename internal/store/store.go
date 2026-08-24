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
// database/sql's connection pool would open multiple physical connections
// and every one of them would need its own locking story.
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
	// CounterpartyID is the participant this row is with. It comes from the
	// verified issuer of the authenticated request that created the row, which
	// makes it two things rather than one: the address an outbound message is
	// sent to, and the anchor an inbound message about this exchange is
	// authorized against (auth_middleware.go's refuseIfNotParty, and
	// DECISIONS.md section 32.3). Empty on rows written before authentication
	// existed.
	CounterpartyID string
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
	// ProviderBaseURL is the connectorAddress the management listener's
	// POST /negotiations/initiate supplied — every subsequent outbound call
	// this connector makes as consumer for this negotiation is addressed
	// relative to it.
	ProviderBaseURL string
	State           string
	DatasetID       string
	OfferID         string
	// CounterpartyID is the participant this row is with, and it is two
	// things rather than one: the address an outbound message is sent to,
	// and the anchor an inbound message about this negotiation is authorized
	// against (auth_middleware.go's refuseIfNotParty). It comes from the
	// providerId of an operator's own initiate call on the management
	// listener, which with authentication on may only name a participant the
	// roster lists — DECISIONS.md section 35.2, which is what makes the
	// second of those two things possible. Section 32.3 recorded this field
	// as addressing only, and that was true while any roster participant
	// could choose it.
	//
	// Empty on rows written before authentication existed. With
	// authentication on, an empty counterparty refuses every inbound message
	// about this row rather than admitting anyone (section 35.3).
	CounterpartyID string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Agreement records an agreement this connector is party to, however it came
// to be. Three writers — a negotiation reaching AGREED in the provider role,
// a consumer-role negotiation accepting a remote provider's agreement, and
// POST /agreements — and Origin names which.
//
// It is the single source of truth for "does this agreement exist" as both
// roles of the transfer protocol ask it: the provider role refuses a
// transfer citing an agreement with no row, and the management listener's
// POST /transfers/initiate refuses to start one as consumer for the same
// reason. That symmetry is why the consumer role became the third writer
// rather than keeping a record of its own — one table, one rule.
type Agreement struct {
	AgreementID string
	DatasetID   string
	// ConsumerPID is the consumer pid of the negotiation that produced this
	// agreement — an internal correlation id, not a participant. Who the
	// agreement is with is CounterpartyID below; before that column existed
	// this field was the closest thing to it, which is why it used to be
	// described as the counterparty. An imported agreement may have no
	// consumer pid, because the negotiation that produced it did not happen
	// here.
	ConsumerPID string
	Origin      string
	// CounterpartyID is the participant this agreement is with, recorded so a
	// message citing it can be checked against who sent it. Role-relative like
	// the same column on the exchange tables: for a negotiated agreement it is
	// the consumer, for one this connector accepted as consumer it is the
	// provider, and for an imported one it is whoever the operator named.
	// Empty means not known, and is permitted rather than refused (see the
	// design spec's section 4.2). Three kinds of row reach that state, and
	// only the first is about imports. An agreement imported before this
	// column existed, or imported without naming a counterparty. A negotiated
	// agreement recorded before this column existed, whose counterparty
	// survives on the negotiation row but was never copied here — recoverable
	// in principle, and deliberately not recovered, because a backfill would
	// be the update path DECISIONS.md section 25.3 says this connector has
	// nowhere. And, permanently, any agreement concluded while require_auth
	// was false: issuerFrom returned "" and there was no verified identity to
	// record anywhere, so nothing can reconstruct it later. That last subset
	// is why the gap this column closes does not close for every negotiated
	// agreement — see DECISIONS.md section 32's first trade-off.
	CounterpartyID string
	CreatedAt      time.Time
}

// How an agreement came to be. Stored rather than inferred: the difference
// matters when deciding what this connector can attest to.
const (
	OriginNegotiated = "negotiated"
	OriginImported   = "imported"
	// OriginAgreed is an agreement this connector accepted as consumer, from
	// a provider's ContractAgreementMessage. Unlike OriginNegotiated this
	// connector did not author it, and unlike OriginImported no operator
	// asserted it out of band.
	OriginAgreed = "agreed"
)

// TransferProcess is one transfer this connector is running as provider. It
// mirrors Negotiation: the pid this connector generated is the primary key,
// and state moves by compare-and-swap so a lost race is distinguishable from
// a missing row.
type TransferProcess struct {
	ProviderPID     string
	ConsumerPID     string
	AgreementID     string
	State           string
	CallbackAddress string
	Format          string
	// CounterpartyID is the participant this row is with. It comes from the
	// verified issuer of the authenticated request that created the row, which
	// makes it two things rather than one: the address an outbound message is
	// sent to, and the anchor an inbound message about this exchange is
	// authorized against (auth_middleware.go's refuseIfNotParty, and
	// DECISIONS.md section 32.3). Empty on rows written before authentication
	// existed.
	CounterpartyID string
	CreatedAt      time.Time
	UpdatedAt      time.Time
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

const agreementSchema = `
CREATE TABLE IF NOT EXISTS agreements (
    agreement_id    TEXT PRIMARY KEY,
    dataset_id      TEXT NOT NULL,
    consumer_pid    TEXT NOT NULL DEFAULT '',
    origin          TEXT NOT NULL,
    counterparty_id TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL
);`

const transferSchema = `
CREATE TABLE IF NOT EXISTS transfer_processes (
    provider_pid     TEXT PRIMARY KEY,
    consumer_pid     TEXT NOT NULL,
    agreement_id     TEXT NOT NULL,
    state            TEXT NOT NULL,
    callback_address TEXT NOT NULL,
    format           TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);`

// consumerTransferSchema holds transfers this connector runs as consumer —
// the mirror of transfer_processes, which is its provider-role state. Keyed
// by this connector's own generated consumer pid, because that is the
// identifier the provider puts in the callback path it POSTs to. A second
// table rather than a role column on transfer_processes, for the reasons
// consumer_negotiations already records.
const consumerTransferSchema = `
CREATE TABLE IF NOT EXISTS consumer_transfer_processes (
    consumer_pid      TEXT PRIMARY KEY,
    provider_pid      TEXT NOT NULL DEFAULT '',
    provider_base_url TEXT NOT NULL,
    agreement_id      TEXT NOT NULL,
    format            TEXT NOT NULL,
    state             TEXT NOT NULL,
    expected_bytes    INTEGER NOT NULL DEFAULT 0,
    received_bytes    INTEGER NOT NULL DEFAULT 0,
    data_path         TEXT NOT NULL DEFAULT '',
    data_completed_at TEXT NOT NULL DEFAULT '',
    data_error        TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);`

const timeFormat = time.RFC3339Nano

// Errors a conditional update can report. Both mean the UPDATE matched no
// row; they differ in why, which is the difference between a bug and a lost
// race, so callers can tell them apart with errors.Is.
//
// Both texts name no protocol on purpose. They started out negotiation-only
// and are now returned by the consumer-negotiation and transfer tables too,
// where "negotiation not found" in a transfer warning is simply false — and
// these strings reach the connector log, which test/tck/run.sh captures
// because it is this project's evidence surface for what actually happened on
// the wire. The wrapping message supplies the table and the id
// ("update transfer <pid>: record changed concurrently: ..."), so the
// sentinel does not have to. TestSentinelErrorsNameNoProtocol pins this.
var (
	// ErrNotFound means there is no record with that primary key.
	ErrNotFound = errors.New("record not found")
	// ErrStateChanged means the record exists but no longer holds the
	// value the caller expected to update from — something else changed it
	// first, and the caller's decision was made against a stale read.
	ErrStateChanged = errors.New("record changed concurrently")
)

// Open opens (creating if necessary) the SQLite file at path, sets its
// journal mode, ensures the schema exists, and applies this project's schema
// migrations (see migrate). Not "the one migration this project has", which
// this line claimed until the counterparty column landed: migrate now adds
// columns across several tables, and the count this line used to carry was
// wrong by the time anyone read it. migrate itself is the inventory.
//
// path may be ":memory:" for tests — DECISIONS.md section 8 reserves that
// for tests only, never a runtime path.
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
	if _, err := db.Exec(agreementSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create agreement schema in %s: %w", path, err)
	}
	if _, err := db.Exec(transferSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create transfer schema in %s: %w", path, err)
	}
	if _, err := db.Exec(consumerTransferSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create consumer transfer schema in %s: %w", path, err)
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
// name in `name`. On an older database the column is added, always. On a fresh
// one the answer now depends on which column, and that is worth stating rather
// than left to be inferred from the CREATE literals above. `rerequested` and
// `agreements.counterparty_id` are both declared there, so the CREATE already
// makes them and the check finds nothing to do — the case this comment used to
// describe as the only one. The other four counterparty_id columns appear in no
// CREATE literal at all and exist solely because the loop below adds them, so a
// fresh database creates those four tables without the column and immediately
// alters them to add it. Both routes converge on the same schema, because the
// check-and-add is idempotent either way, and DECISIONS.md section 32.5 records
// why that inconsistency still does not earn a migration tool.
func migrate(db *sql.DB) error {
	if err := addColumnIfMissing(db, "negotiations", "rerequested",
		`ALTER TABLE negotiations ADD COLUMN rerequested INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	// counterparty_id is who the row is with, recorded so an outbound message
	// can be addressed to them. Empty on rows created before authentication
	// existed, which is why the default is '' rather than a failure: those
	// exchanges predate anyone to address.
	for _, table := range []string{
		"negotiations", "consumer_negotiations",
		"transfer_processes", "consumer_transfer_processes", "agreements",
	} {
		if err := addColumnIfMissing(db, table, "counterparty_id",
			`ALTER TABLE `+table+` ADD COLUMN counterparty_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// expected_bytes is in both the CREATE literal above and this loop.
	// counterparty_id is in the literal only on agreements (section 32.5);
	// doing both here is what that section's own reasoning argues for.
	if err := addColumnIfMissing(db, "consumer_transfer_processes", "expected_bytes",
		`ALTER TABLE consumer_transfer_processes ADD COLUMN expected_bytes INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	// In the CREATE literal above and in this loop both, the same way
	// expected_bytes is, for the reason section 32.5 gives.
	for _, col := range []struct{ name, stmt string }{
		{"received_bytes", `ALTER TABLE consumer_transfer_processes ADD COLUMN received_bytes INTEGER NOT NULL DEFAULT 0`},
		{"data_path", `ALTER TABLE consumer_transfer_processes ADD COLUMN data_path TEXT NOT NULL DEFAULT ''`},
		{"data_completed_at", `ALTER TABLE consumer_transfer_processes ADD COLUMN data_completed_at TEXT NOT NULL DEFAULT ''`},
		{"data_error", `ALTER TABLE consumer_transfer_processes ADD COLUMN data_error TEXT NOT NULL DEFAULT ''`},
	} {
		if err := addColumnIfMissing(db, "consumer_transfer_processes", col.name, col.stmt); err != nil {
			return err
		}
	}
	return nil
}

// addColumnIfMissing is idempotent: SQLite has no ADD COLUMN IF NOT EXISTS,
// so the column list is checked first. Every migration in this file is a
// column addition, which is the only schema change SQLite performs cheaply
// and the only one this connector has needed.
func addColumnIfMissing(db *sql.DB, table, column, stmt string) error {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
	).Scan(&n); err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("add %s.%s column: %w", table, column, err)
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
		`INSERT INTO negotiations (provider_pid, consumer_pid, state, dataset_id, offer_id, callback_address, rerequested, counterparty_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ProviderPID, n.ConsumerPID, n.State, n.DatasetID, n.OfferID, n.CallbackAddress, n.Rerequested, n.CounterpartyID,
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
		`SELECT provider_pid, consumer_pid, state, dataset_id, offer_id, callback_address, rerequested, created_at, updated_at, counterparty_id
		 FROM negotiations WHERE provider_pid = ?`, providerPID)

	var n Negotiation
	var created, updated string
	err := row.Scan(&n.ProviderPID, &n.ConsumerPID, &n.State, &n.DatasetID, &n.OfferID,
		&n.CallbackAddress, &n.Rerequested, &created, &updated, &n.CounterpartyID)
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
		`INSERT INTO consumer_negotiations (consumer_pid, provider_pid, provider_base_url, state, dataset_id, offer_id, counterparty_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ConsumerPID, n.ProviderPID, n.ProviderBaseURL, n.State, n.DatasetID, n.OfferID, n.CounterpartyID,
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
		`SELECT consumer_pid, provider_pid, provider_base_url, state, dataset_id, offer_id, created_at, updated_at, counterparty_id
		 FROM consumer_negotiations WHERE consumer_pid = ?`, consumerPID)

	var n ConsumerNegotiation
	var created, updated string
	err := row.Scan(&n.ConsumerPID, &n.ProviderPID, &n.ProviderBaseURL, &n.State, &n.DatasetID, &n.OfferID,
		&created, &updated, &n.CounterpartyID)
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

// CreateAgreement records an agreement. It fails on a duplicate id rather
// than overwriting: an agreement is immutable once made, and a silent
// overwrite would let an import rewrite the dataset a negotiated agreement
// covers.
func (s *Store) CreateAgreement(a Agreement) error {
	_, err := s.db.Exec(
		`INSERT INTO agreements (agreement_id, dataset_id, consumer_pid, origin, counterparty_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		a.AgreementID, a.DatasetID, a.ConsumerPID, a.Origin, a.CounterpartyID,
		a.CreatedAt.UTC().Format(timeFormat),
	)
	if err != nil {
		return fmt.Errorf("create agreement %s: %w", a.AgreementID, err)
	}
	return nil
}

// GetAgreement reports whether an agreement with this id exists, and what it
// covers.
func (s *Store) GetAgreement(agreementID string) (Agreement, bool, error) {
	var a Agreement
	var createdAt string
	err := s.db.QueryRow(
		`SELECT agreement_id, dataset_id, consumer_pid, origin, counterparty_id, created_at
		 FROM agreements WHERE agreement_id = ?`, agreementID,
	).Scan(&a.AgreementID, &a.DatasetID, &a.ConsumerPID, &a.Origin, &a.CounterpartyID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Agreement{}, false, nil
	}
	if err != nil {
		return Agreement{}, false, fmt.Errorf("get agreement %s: %w", agreementID, err)
	}
	a.CreatedAt, err = time.Parse(timeFormat, createdAt)
	if err != nil {
		return Agreement{}, false, fmt.Errorf("get agreement %s: parse created_at: %w", agreementID, err)
	}
	return a, true, nil
}

// CreateAgreementIfNegotiationAgreed re-checks providerPID's negotiation
// state and, only if it is one of allowedStates, inserts a in the same
// transaction. It reports whether the agreement was recorded, and the state
// the negotiation was actually found in (empty if there is no such
// negotiation at all).
//
// The check and the insert run inside one *sql.Tx rather than as two
// separate calls, and that is the whole point: Open pins this Store to a
// single connection (SetMaxOpenConns(1)), so a transaction holds that
// connection from the SELECT to the COMMIT, and no concurrent SetState can
// land in between. That closes the race negotiation_handler.go's dispatch
// used to accept in a comment — a termination arriving between the re-read
// and the INSERT, leaving a stale agreement row for a dead negotiation.
func (s *Store) CreateAgreementIfNegotiationAgreed(providerPID string, allowedStates []string, a Agreement) (recorded bool, currentState string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, "", fmt.Errorf("record agreement %s: %w", a.AgreementID, err)
	}
	defer tx.Rollback()

	err = tx.QueryRow(`SELECT state FROM negotiations WHERE provider_pid = ?`, providerPID).Scan(&currentState)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("record agreement %s: %w", a.AgreementID, err)
	}

	allowed := false
	for _, want := range allowedStates {
		if currentState == want {
			allowed = true
			break
		}
	}
	if !allowed {
		return false, currentState, nil
	}

	if _, err := tx.Exec(
		`INSERT INTO agreements (agreement_id, dataset_id, consumer_pid, origin, counterparty_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		a.AgreementID, a.DatasetID, a.ConsumerPID, a.Origin, a.CounterpartyID, a.CreatedAt.UTC().Format(timeFormat),
	); err != nil {
		return false, currentState, fmt.Errorf("record agreement %s: %w", a.AgreementID, err)
	}
	if err := tx.Commit(); err != nil {
		return false, currentState, fmt.Errorf("record agreement %s: %w", a.AgreementID, err)
	}
	return true, currentState, nil
}

// CreateTransfer persists a new transfer process.
func (s *Store) CreateTransfer(t TransferProcess) error {
	_, err := s.db.Exec(
		`INSERT INTO transfer_processes (provider_pid, consumer_pid, agreement_id, state, callback_address, format, counterparty_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ProviderPID, t.ConsumerPID, t.AgreementID, t.State, t.CallbackAddress, t.Format, t.CounterpartyID,
		t.CreatedAt.UTC().Format(timeFormat), t.UpdatedAt.UTC().Format(timeFormat),
	)
	if err != nil {
		return fmt.Errorf("create transfer %s: %w", t.ProviderPID, err)
	}
	return nil
}

// GetTransfer returns the transfer process with the given provider pid.
func (s *Store) GetTransfer(providerPID string) (TransferProcess, bool, error) {
	row := s.db.QueryRow(
		`SELECT provider_pid, consumer_pid, agreement_id, state, callback_address, format, created_at, updated_at, counterparty_id
		 FROM transfer_processes WHERE provider_pid = ?`, providerPID)

	var t TransferProcess
	var created, updated string
	err := row.Scan(&t.ProviderPID, &t.ConsumerPID, &t.AgreementID, &t.State, &t.CallbackAddress, &t.Format,
		&created, &updated, &t.CounterpartyID)
	if errors.Is(err, sql.ErrNoRows) {
		return TransferProcess{}, false, nil
	}
	if err != nil {
		return TransferProcess{}, false, fmt.Errorf("get transfer %s: %w", providerPID, err)
	}
	if t.CreatedAt, err = time.Parse(timeFormat, created); err != nil {
		return TransferProcess{}, false, fmt.Errorf("get transfer %s: parse created_at: %w", providerPID, err)
	}
	if t.UpdatedAt, err = time.Parse(timeFormat, updated); err != nil {
		return TransferProcess{}, false, fmt.Errorf("get transfer %s: parse updated_at: %w", providerPID, err)
	}
	return t, true, nil
}

// SetTransferState moves a transfer process from state `from` to state `to`
// — the same compare-and-swap SetState and SetConsumerState use, for the
// same reason: a lost race must be distinguishable from a missing row.
func (s *Store) SetTransferState(providerPID, from, to string, updatedAt time.Time) error {
	res, err := s.db.Exec(`UPDATE transfer_processes SET state = ?, updated_at = ? WHERE provider_pid = ? AND state = ?`,
		to, updatedAt.UTC().Format(timeFormat), providerPID, from)
	if err != nil {
		return fmt.Errorf("update transfer %s: %w", providerPID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update transfer %s: %w", providerPID, err)
	}
	if rows == 0 {
		return s.explainNoTransferUpdate(providerPID, "state "+from)
	}
	return nil
}

// explainNoTransferUpdate is explainNoUpdate's transfer-table counterpart —
// kept separate because explainNoUpdate hard-codes a Get against
// negotiations and would name the wrong table's state otherwise.
func (s *Store) explainNoTransferUpdate(providerPID, want string) error {
	t, ok, err := s.GetTransfer(providerPID)
	if err != nil {
		return fmt.Errorf("update transfer %s: %w", providerPID, err)
	}
	if !ok {
		return fmt.Errorf("update transfer %s: %w", providerPID, ErrNotFound)
	}
	return fmt.Errorf("update transfer %s: %w: wanted %s, found state %s",
		providerPID, ErrStateChanged, want, t.State)
}

// ConsumerTransfer is one transfer process this connector is running as
// consumer — the mirror of TransferProcess, which is its provider-role
// state. Keyed by this connector's own generated consumer pid, because that
// is the identifier the provider puts in the callback path.
//
// ProviderPID is empty until the ACK to the initial TransferRequestMessage
// reveals it. That is also why nothing this connector sends as consumer can
// be sent before that ACK: every outbound URL contains it.
//
// No CallbackAddress field: unlike the provider role, this connector's own
// callback address is not per-transfer data — it is always
// config.Config.PublicURL + the version path, computed at startup.
type ConsumerTransfer struct {
	ConsumerPID string
	ProviderPID string
	// ProviderBaseURL is connectorAddress from the initiate call — the base
	// every later outbound message is addressed against.
	ProviderBaseURL string
	AgreementID     string
	Format          string
	State           string
	// CounterpartyID is the participant this row is with, and it is two
	// things rather than one: the address an outbound message is sent to,
	// and the anchor an inbound message about this transfer is authorized
	// against (auth_middleware.go's refuseIfNotParty). It comes from the
	// providerId of an operator's own initiate call on the management
	// listener, which with authentication on may only name a participant the
	// roster lists — DECISIONS.md section 35.2, which is what makes the
	// second of those two things possible. Section 32.3 recorded this field
	// as addressing only, and that was true while any roster participant
	// could choose it.
	//
	// Empty on rows written before authentication existed. With
	// authentication on, an empty counterparty refuses every inbound message
	// about this row rather than admitting anyone (section 35.3).
	CounterpartyID string
	// ExpectedBytes is the complete length the counterparty stated for this
	// transfer's data, recorded on the first attempt so a later one can tell
	// a resumption from a different representation. Zero means not known —
	// never known to be zero — because a counterparty that streams chunked
	// states no length at all and that is not an error.
	ExpectedBytes int64
	// ReceivedBytes, DataPath, DataCompletedAt, and DataError record what a
	// pull did. They are the answer to the only question an operator can ask
	// about a transfer that the protocol state does not answer: did the data
	// arrive? A completed download has DataCompletedAt set and DataError
	// empty; a failed one is the reverse, and the two can never both hold,
	// because RecordConsumerTransferOutcome writes all four together.
	//
	// DataError holds the reason a pull stopped rather than a code. The
	// reasons are already distinct sentences in the log, and the sentence is
	// what an operator reading this field needs.
	ReceivedBytes   int64
	DataPath        string
	DataCompletedAt time.Time
	DataError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CreateConsumerTransfer persists a new consumer-role transfer.
func (s *Store) CreateConsumerTransfer(t ConsumerTransfer) error {
	completedAt := ""
	if !t.DataCompletedAt.IsZero() {
		completedAt = t.DataCompletedAt.UTC().Format(timeFormat)
	}
	_, err := s.db.Exec(
		`INSERT INTO consumer_transfer_processes (consumer_pid, provider_pid, provider_base_url, agreement_id, format, state, counterparty_id, expected_bytes, received_bytes, data_path, data_completed_at, data_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ConsumerPID, t.ProviderPID, t.ProviderBaseURL, t.AgreementID, t.Format, t.State, t.CounterpartyID, t.ExpectedBytes,
		t.ReceivedBytes, t.DataPath, completedAt, t.DataError,
		t.CreatedAt.UTC().Format(timeFormat), t.UpdatedAt.UTC().Format(timeFormat),
	)
	if err != nil {
		return fmt.Errorf("create consumer transfer %s: %w", t.ConsumerPID, err)
	}
	return nil
}

// GetConsumerTransfer returns the consumer-role transfer with the given
// consumer pid.
func (s *Store) GetConsumerTransfer(consumerPID string) (ConsumerTransfer, bool, error) {
	row := s.db.QueryRow(
		`SELECT consumer_pid, provider_pid, provider_base_url, agreement_id, format, state, created_at, updated_at, counterparty_id, expected_bytes, received_bytes, data_path, data_completed_at, data_error
		 FROM consumer_transfer_processes WHERE consumer_pid = ?`, consumerPID)

	var t ConsumerTransfer
	var created, updated, completedAt string
	err := row.Scan(&t.ConsumerPID, &t.ProviderPID, &t.ProviderBaseURL, &t.AgreementID, &t.Format, &t.State,
		&created, &updated, &t.CounterpartyID, &t.ExpectedBytes, &t.ReceivedBytes, &t.DataPath, &completedAt, &t.DataError)
	if errors.Is(err, sql.ErrNoRows) {
		return ConsumerTransfer{}, false, nil
	}
	if err != nil {
		return ConsumerTransfer{}, false, fmt.Errorf("get consumer transfer %s: %w", consumerPID, err)
	}
	if t.CreatedAt, err = time.Parse(timeFormat, created); err != nil {
		return ConsumerTransfer{}, false, fmt.Errorf("get consumer transfer %s: parse created_at: %w", consumerPID, err)
	}
	if t.UpdatedAt, err = time.Parse(timeFormat, updated); err != nil {
		return ConsumerTransfer{}, false, fmt.Errorf("get consumer transfer %s: parse updated_at: %w", consumerPID, err)
	}
	if completedAt != "" {
		if t.DataCompletedAt, err = time.Parse(timeFormat, completedAt); err != nil {
			return ConsumerTransfer{}, false, fmt.Errorf("get consumer transfer %s: parse data_completed_at: %w", consumerPID, err)
		}
	}
	return t, true, nil
}

// SetConsumerTransferState moves a consumer-role transfer from state `from`
// to state `to` — the same compare-and-swap SetTransferState uses for the
// provider role, for the same reason: the consumer driver also runs in a
// goroutine and can outlive a termination that arrived while it slept
// between steps.
func (s *Store) SetConsumerTransferState(consumerPID, from, to string, updatedAt time.Time) error {
	res, err := s.db.Exec(
		`UPDATE consumer_transfer_processes SET state = ?, updated_at = ? WHERE consumer_pid = ? AND state = ?`,
		to, updatedAt.UTC().Format(timeFormat), consumerPID, from)
	if err != nil {
		return fmt.Errorf("update consumer transfer %s: %w", consumerPID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update consumer transfer %s: %w", consumerPID, err)
	}
	if rows == 0 {
		return s.explainNoConsumerTransferUpdate(consumerPID, from)
	}
	return nil
}

// explainNoConsumerTransferUpdate separates a lost race from a missing row,
// naming consumer_transfer_processes rather than any other table — the same
// reason explainNoConsumerUpdate is kept separate from explainNoUpdate.
func (s *Store) explainNoConsumerTransferUpdate(consumerPID, want string) error {
	t, ok, err := s.GetConsumerTransfer(consumerPID)
	if err != nil {
		return fmt.Errorf("update consumer transfer %s: %w", consumerPID, err)
	}
	if !ok {
		return fmt.Errorf("update consumer transfer %s: %w", consumerPID, ErrNotFound)
	}
	return fmt.Errorf("update consumer transfer %s: %w: wanted state %s, found state %s",
		consumerPID, ErrStateChanged, want, t.State)
}

// SetConsumerTransferProviderPID records the provider pid the ACK revealed.
// Unconditional rather than compare-and-swap: it is written exactly once, by
// the goroutine that made the request, before any other writer can exist.
func (s *Store) SetConsumerTransferProviderPID(consumerPID, providerPID string, updatedAt time.Time) error {
	res, err := s.db.Exec(
		`UPDATE consumer_transfer_processes SET provider_pid = ?, updated_at = ? WHERE consumer_pid = ?`,
		providerPID, updatedAt.UTC().Format(timeFormat), consumerPID)
	if err != nil {
		return fmt.Errorf("update consumer transfer %s: %w", consumerPID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update consumer transfer %s: %w", consumerPID, err)
	}
	if rows == 0 {
		return fmt.Errorf("update consumer transfer %s: %w", consumerPID, ErrNotFound)
	}
	return nil
}

// SetConsumerTransferExpectedBytes records the complete length a
// counterparty stated for a transfer's data. Unguarded, unlike the state
// setters: this is not a protocol transition, and folding it into one would
// make SetConsumerTransferState's from/to guard mean two things.
func (s *Store) SetConsumerTransferExpectedBytes(consumerPID string, expected int64) error {
	if _, err := s.db.Exec(
		`UPDATE consumer_transfer_processes SET expected_bytes = ? WHERE consumer_pid = ?`,
		expected, consumerPID,
	); err != nil {
		return fmt.Errorf("set expected bytes for consumer transfer %s: %w", consumerPID, err)
	}
	return nil
}

// RecordConsumerTransferOutcome writes what a pull did, all four columns at
// once. Together rather than individually so a row can never read as both
// completed and failed: a success passes a completion and an empty failure,
// a failure passes the reason and a zero time, and each overwrites whatever
// the last attempt left.
//
// A missing row is not an error. Many tests drive pullTransferData
// directly with no row behind it, and in production there is always one —
// lookup found it, and there is no delete path. Surfacing "not found" here
// would add noise to the tests and tell production nothing it could act on.
func (s *Store) RecordConsumerTransferOutcome(consumerPID string, received int64, path string, completedAt time.Time, failure string) error {
	stamp := ""
	if !completedAt.IsZero() {
		stamp = completedAt.UTC().Format(timeFormat)
	}
	if _, err := s.db.Exec(
		`UPDATE consumer_transfer_processes
		 SET received_bytes = ?, data_path = ?, data_completed_at = ?, data_error = ?
		 WHERE consumer_pid = ?`,
		received, path, stamp, failure, consumerPID,
	); err != nil {
		return fmt.Errorf("record outcome for consumer transfer %s: %w", consumerPID, err)
	}
	return nil
}

// ListAgreements returns every agreement, oldest first. Unpaginated: an
// agreement list large enough to need paging is a problem worth having first.
func (s *Store) ListAgreements() ([]Agreement, error) {
	rows, err := s.db.Query(
		`SELECT agreement_id, dataset_id, consumer_pid, origin, counterparty_id, created_at
		 FROM agreements ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list agreements: %w", err)
	}
	defer rows.Close()

	var out []Agreement
	for rows.Next() {
		var a Agreement
		var created string
		if err := rows.Scan(&a.AgreementID, &a.DatasetID, &a.ConsumerPID, &a.Origin, &a.CounterpartyID, &created); err != nil {
			return nil, fmt.Errorf("list agreements: %w", err)
		}
		if a.CreatedAt, err = time.Parse(timeFormat, created); err != nil {
			return nil, fmt.Errorf("list agreements: parse created_at: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agreements: %w", err)
	}
	return out, nil
}

// ListTransfers returns every provider-role transfer, oldest first.
// Unpaginated, for the reason ListAgreements is: a transfer list large
// enough to need paging is a problem worth having first.
func (s *Store) ListTransfers() ([]TransferProcess, error) {
	rows, err := s.db.Query(
		`SELECT provider_pid, consumer_pid, agreement_id, state, callback_address, format, created_at, updated_at, counterparty_id
		 FROM transfer_processes ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list transfers: %w", err)
	}
	defer rows.Close()

	var out []TransferProcess
	for rows.Next() {
		var t TransferProcess
		var created, updated string
		if err := rows.Scan(&t.ProviderPID, &t.ConsumerPID, &t.AgreementID, &t.State, &t.CallbackAddress, &t.Format,
			&created, &updated, &t.CounterpartyID); err != nil {
			return nil, fmt.Errorf("list transfers: %w", err)
		}
		if t.CreatedAt, err = time.Parse(timeFormat, created); err != nil {
			return nil, fmt.Errorf("list transfers: parse created_at: %w", err)
		}
		if t.UpdatedAt, err = time.Parse(timeFormat, updated); err != nil {
			return nil, fmt.Errorf("list transfers: parse updated_at: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list transfers: %w", err)
	}
	return out, nil
}

// ListConsumerTransfers returns every consumer-role transfer, oldest first.
// Unpaginated, for the reason ListAgreements is.
func (s *Store) ListConsumerTransfers() ([]ConsumerTransfer, error) {
	rows, err := s.db.Query(
		`SELECT consumer_pid, provider_pid, provider_base_url, agreement_id, format, state, created_at, updated_at, counterparty_id, expected_bytes, received_bytes, data_path, data_completed_at, data_error
		 FROM consumer_transfer_processes ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list consumer transfers: %w", err)
	}
	defer rows.Close()

	var out []ConsumerTransfer
	for rows.Next() {
		var t ConsumerTransfer
		var created, updated, completedAt string
		if err := rows.Scan(&t.ConsumerPID, &t.ProviderPID, &t.ProviderBaseURL, &t.AgreementID, &t.Format, &t.State,
			&created, &updated, &t.CounterpartyID, &t.ExpectedBytes, &t.ReceivedBytes, &t.DataPath, &completedAt, &t.DataError); err != nil {
			return nil, fmt.Errorf("list consumer transfers: %w", err)
		}
		if t.CreatedAt, err = time.Parse(timeFormat, created); err != nil {
			return nil, fmt.Errorf("list consumer transfers: parse created_at: %w", err)
		}
		if t.UpdatedAt, err = time.Parse(timeFormat, updated); err != nil {
			return nil, fmt.Errorf("list consumer transfers: parse updated_at: %w", err)
		}
		if completedAt != "" {
			if t.DataCompletedAt, err = time.Parse(timeFormat, completedAt); err != nil {
				return nil, fmt.Errorf("list consumer transfers: parse data_completed_at: %w", err)
			}
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list consumer transfers: %w", err)
	}
	return out, nil
}
