# Transfer Process, Provider Role — Phase A (control plane) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the DSP 2025-1 transfer process protocol in the provider role, control plane only, and take the TCK's `TP` suite to 15/15 in the compliance gate.

**Architecture:** A `transfer_processes` table mirroring `negotiations`, a five-state machine whose transition legality is pure functions, and handlers that follow the negotiation milestones' established shape (structural guards return 400, outbound calls dispatch with `go` through `pushCallback`). Agreements become a first-class table with two writers — a negotiation reaching `AGREED`, and an import through the management API, which this phase authenticates for the first time.

**Tech Stack:** Go 1.x standard library, SQLite via the existing `internal/store`, no new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-16-transfer-process-provider-design.md`

**TCK survey the spec argues from:** `docs/superpowers/specs/2026-08-16-transfer-process-tck-requirements.md`

## Global Constraints

- English only: code, comments, commit messages, docs.
- Standard library first. This plan adds no dependency — ask before adding one.
- No organizational affiliation anywhere in the repo. Copyright is b7g. Commits carry no `Co-Authored-By` trailer.
- Storage: one SQLite file under `data_dir`, WAL mode. In-memory SQLite is for tests only, never a runtime path (`DECISIONS.md` §8).
- JSON-LD fixed compact form, validated by direct field checks, not a schema library (`DECISIONS.md` §22.5).
- **Every node this connector emits carries `@type`.**
- A structural-guard rejection is always `400`, never `404` — the TCK's `HttpFunctions.postJson` throws immediately on `404` even where an error is otherwise expected.
- Every handler that makes an outbound HTTP call while handling an inbound request must dispatch it with `go`, never inline (`DECISIONS.md` §23.8).
- Any outbound call after the first must go through `pushCallback`'s retry schedule (`DECISIONS.md` §23.7), not a bespoke one-shot send.
- Compliance is owed to the DSP 2025-1 spec, verified by the official TCK. When behavior is unclear, the spec and the TCK decide.
- Go test command: `go test ./...`. Race gate: `go test -race ./...` (now in CI). TCK: `make tck`.
- `POST /agreements` records an agreement and nothing more. It is not the start of a general management CRUD surface.

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `internal/config/config.go` | `MgmtToken` field, env override, validation | 1 |
| `internal/store/store.go` | `agreements` table + CRUD | 2 |
| `internal/store/store.go` | `transfer_processes` table + CRUD | 3 |
| `internal/mgmt/router.go` | bearer auth, `POST /agreements`, `NewRouter(cfg, st)` | 4 |
| `cmd/dsbox/main.go` | pass config and store to `mgmt.NewRouter` | 4 |
| `internal/dsp/negotiation_handler.go` | write the agreement row on `AGREED` | 5 |
| `internal/dsp/transfer.go` | message documents, state machine, pure decisions | 6 |
| `internal/dsp/transfer_handler.go` | inbound handlers | 7 |
| `internal/dsp/router.go` | mount the transfer routes | 7 |
| `internal/config/config.go` | `transfer_policies` — the provider's own transitions, keyed by agreement | 8 |
| `internal/dsp/transfer_handler.go` | the driver that walks a configured sequence | 8 |
| `test/tck/config.properties`, `test/tck/run.sh`, `test/tck/dsbox.yaml`, `cmd/tckgate/main.go` | fixtures, seeding, gate | 9 |
| `README.md`, `DECISIONS.md` | status table, §25 | 9 |

Transfer code goes in its own files from the start. `negotiation_handler.go` was split at 867 lines in the previous milestone precisely so this one would not grow it further.

---

### Task 1: Config — `mgmt_token`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `config.example.yaml`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `config.Config.MgmtToken string`. Task 4 reads it.

`mgmt_token` is optional. Absent means the management API refuses every authenticated request rather than allowing them — a missing token must never mean "no auth required", which is the failure mode that turns a localhost-bound default into an open write endpoint the day someone changes `mgmt_addr`. `/health` stays unauthenticated.

When present it must be at least 16 characters, so a token that is obviously a placeholder fails at load rather than in production.

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestMgmtTokenEmptyWhenAbsent(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MgmtToken != "" {
		t.Errorf("MgmtToken = %q, want empty when the key is absent", cfg.MgmtToken)
	}
}

func TestMgmtTokenParses(t *testing.T) {
	cfg, err := Load(minimal("mgmt_token: 0123456789abcdef\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MgmtToken != "0123456789abcdef" {
		t.Errorf("MgmtToken = %q, want the configured token", cfg.MgmtToken)
	}
}

func TestMgmtTokenFromEnvironment(t *testing.T) {
	cfg, err := Load(minimal(""), env(map[string]string{"DSBOX_MGMT_TOKEN": "fedcba9876543210"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MgmtToken != "fedcba9876543210" {
		t.Errorf("MgmtToken = %q, want the environment value", cfg.MgmtToken)
	}
}

func TestMgmtTokenTooShortIsAnError(t *testing.T) {
	_, err := Load(minimal("mgmt_token: short\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a token below the minimum length")
	}
}
```

If `minimal` and `env` are named differently in this file, use whatever the existing tests use — do not add new helpers.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -run TestMgmtToken -v`
Expected: FAIL — `cfg.MgmtToken` undefined.

- [ ] **Step 3: Add the field**

In `internal/config/config.go`, inside `type Config struct`, after `MgmtAddr`:

```go
	// MgmtToken is the single static bearer token the management API accepts
	// (DECISIONS.md section 11). Optional, and absent means the management
	// API refuses every authenticated request rather than allowing them: a
	// missing token must never read as "no auth required", or the localhost
	// default becomes an open write endpoint the moment mgmt_addr changes.
	// /health is unauthenticated either way.
	MgmtToken string `yaml:"mgmt_token"`
```

- [ ] **Step 4: Add the environment override**

In `Load`, beside the other overrides:

```go
	if v := getenv("DSBOX_MGMT_TOKEN"); v != "" {
		cfg.MgmtToken = v
	}
```

- [ ] **Step 5: Add the validation**

In `validate()`:

```go
	if c.MgmtToken != "" && len(c.MgmtToken) < minMgmtTokenLen {
		return fmt.Errorf("mgmt_token must be at least %d characters", minMgmtTokenLen)
	}
```

And with the other constants:

```go
// minMgmtTokenLen is the shortest management token accepted. It exists to
// fail an obviously placeholder value at load rather than in production.
const minMgmtTokenLen = 16
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/config/ -run TestMgmtToken -v`
Expected: PASS, 4 tests.

- [ ] **Step 7: Document it in `config.example.yaml`**

After the `mgmt_addr` block:

```yaml
# The single bearer token the management API accepts. Optional. With no token
# configured the management API refuses every authenticated request — absent
# never means "no auth required". Minimum 16 characters.
# DSBOX_MGMT_TOKEN overrides it.
mgmt_token: ""
```

- [ ] **Step 8: Run the full suite and commit**

```bash
go test ./... && gofmt -l . && go vet ./...
git add internal/config/config.go internal/config/config_test.go config.example.yaml
git commit -m "feat: add the management API's bearer token to configuration"
```

---

### Task 2: Storage — `agreements`

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces:
  - `store.Agreement{AgreementID, DatasetID, ConsumerPID, Origin string; CreatedAt time.Time}`
  - `store.OriginNegotiated = "negotiated"`, `store.OriginImported = "imported"`
  - `func (s *Store) CreateAgreement(a Agreement) error`
  - `func (s *Store) GetAgreement(agreementID string) (Agreement, bool, error)`

  Task 4 calls `CreateAgreement` with `OriginImported`, Task 5 with `OriginNegotiated`, Task 7 calls `GetAgreement`.

An agreement row is the record that this connector is party to a contract. `CreateAgreement` on an existing id returns an error rather than overwriting: an agreement is immutable once made, and a silent overwrite would let an import rewrite the dataset a negotiated agreement covers.

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/store_test.go`:

```go
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
}
```

Note the idiom: `internal/store/store_test.go` is `package store` (an internal test package), so types are referenced unqualified — `Agreement`, not `store.Agreement`. There is no shared store-opening helper in that file; every test opens `Open(":memory:")` and defers `Close()`, and these follow suit rather than introducing a new helper mid-file.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run TestAgreement -v; go test ./internal/store/ -run TestCreateAgreement -v`
Expected: FAIL — `Agreement` undefined.

- [ ] **Step 3: Add the type and constants**

In `internal/store/store.go`, near `Negotiation`:

```go
// Agreement records that this connector is party to a contract. It is the
// single source of truth for "does this agreement exist", which the transfer
// protocol asks on every request. Rows arrive two ways: a negotiation
// reaching AGREED, and an operator importing an agreement concluded outside
// this connector.
type Agreement struct {
	AgreementID string
	DatasetID   string
	// ConsumerPID is the counterparty of a negotiated agreement. An imported
	// agreement may have none, because the negotiation that produced it did
	// not happen here.
	ConsumerPID string
	Origin      string
	CreatedAt   time.Time
}

// How an agreement came to be. Stored rather than inferred: the difference
// matters when deciding what this connector can attest to.
const (
	OriginNegotiated = "negotiated"
	OriginImported   = "imported"
)
```

- [ ] **Step 4: Add the schema**

Beside the other schema constants:

```go
const agreementSchema = `
CREATE TABLE IF NOT EXISTS agreements (
    agreement_id TEXT PRIMARY KEY,
    dataset_id   TEXT NOT NULL,
    consumer_pid TEXT NOT NULL DEFAULT '',
    origin       TEXT NOT NULL,
    created_at   TEXT NOT NULL
);`
```

Execute it where the existing schemas are executed in `Open`, following that function's existing pattern exactly.

- [ ] **Step 5: Implement the two methods**

```go
// CreateAgreement records an agreement. It fails on a duplicate id rather
// than overwriting: an agreement is immutable once made, and a silent
// overwrite would let an import rewrite the dataset a negotiated agreement
// covers.
func (s *Store) CreateAgreement(a Agreement) error {
	_, err := s.db.Exec(
		`INSERT INTO agreements (agreement_id, dataset_id, consumer_pid, origin, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		a.AgreementID, a.DatasetID, a.ConsumerPID, a.Origin,
		a.CreatedAt.UTC().Format(timeFormat),
	)
	if err != nil {
		return fmt.Errorf("create agreement: %w", err)
	}
	return nil
}

// GetAgreement reports whether an agreement with this id exists, and what it
// covers.
func (s *Store) GetAgreement(agreementID string) (Agreement, bool, error) {
	var a Agreement
	var createdAt string
	err := s.db.QueryRow(
		`SELECT agreement_id, dataset_id, consumer_pid, origin, created_at
		 FROM agreements WHERE agreement_id = ?`, agreementID,
	).Scan(&a.AgreementID, &a.DatasetID, &a.ConsumerPID, &a.Origin, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Agreement{}, false, nil
	}
	if err != nil {
		return Agreement{}, false, fmt.Errorf("get agreement: %w", err)
	}
	a.CreatedAt, err = time.Parse(timeFormat, createdAt)
	if err != nil {
		return Agreement{}, false, fmt.Errorf("parse agreement created_at: %w", err)
	}
	return a, true, nil
}
```

Match the surrounding code: if `Get` scans timestamps through a helper rather than `time.Parse` inline, use that helper.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/store/ -v -run 'Agreement'`
Expected: PASS, 3 tests.

- [ ] **Step 7: Commit**

```bash
go test ./... && gofmt -l . && go vet ./...
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: make an agreement a stored record rather than an inference"
```

---

### Task 3: Storage — `transfer_processes`

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces:
  - `store.TransferProcess{ProviderPID, ConsumerPID, AgreementID, State, CallbackAddress, Format string; CreatedAt, UpdatedAt time.Time}`
  - `func (s *Store) CreateTransfer(t TransferProcess) error`
  - `func (s *Store) GetTransfer(providerPID string) (TransferProcess, bool, error)`
  - `func (s *Store) SetTransferState(providerPID, from, to string, updatedAt time.Time) error`

  Task 7 uses all three. `SetTransferState` reports `ErrNotFound` and `ErrStateChanged` the same way `SetState` and `SetConsumerState` already do, so a lost race stays distinguishable from a missing row.

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/store_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run Transfer -v`
Expected: FAIL — `TransferProcess` undefined.

- [ ] **Step 3: Add the type and schema**

```go
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
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
```

```go
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
```

Execute it in `Open` alongside the others.

- [ ] **Step 4: Implement the three methods**

Model `CreateTransfer` and `GetTransfer` on `Create`/`Get`, and `SetTransferState` on `SetConsumerState` — including its `explain...` companion for distinguishing `ErrNotFound` from `ErrStateChanged`. Read `SetConsumerState` and `explainNoConsumerUpdate` first and mirror their structure exactly; the point of this task is a third table that behaves identically to the first two, not a third way of doing it.

`SetTransferState` must refresh `updated_at`, as its siblings do.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/store/ -run Transfer -v`
Expected: PASS, 5 tests.

- [ ] **Step 6: Commit**

```bash
go test ./... && gofmt -l . && go vet ./...
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: add transfer process storage"
```

---

### Task 4: Management API — bearer auth and `POST /agreements`

**Files:**
- Modify: `internal/mgmt/router.go`
- Modify: `internal/mgmt/router_test.go`
- Modify: `cmd/dsbox/main.go`

**Interfaces:**
- Consumes: `config.Config.MgmtToken` (Task 1); `store.Agreement`, `store.CreateAgreement`, `store.OriginImported` (Task 2).
- Produces: `mgmt.NewRouter(cfg config.Config, st *store.Store) http.Handler` — a changed signature; `cmd/dsbox/main.go` is updated in the same task because it will not compile otherwise.

This stands up the management API's authentication for the first time. `DECISIONS.md` §11 decided the model; nothing implemented it.

**Scope guard:** this endpoint records an agreement and nothing else. No list, no delete, no update.

Request body:

```json
{"agreementId": "urn:uuid:...", "datasetId": "urn:dataset:..."}
```

`201` on success, `400` on a malformed body or a missing field, `401` without a valid token, `409` on a duplicate id.

- [ ] **Step 1: Write the failing tests**

Replace the contents of `internal/mgmt/router_test.go` with tests that keep whatever `/health` coverage exists there and add:

```go
// This file is `package mgmt` (an internal test package), so NewRouter is
// referenced unqualified while store and config keep their package prefixes.
const testToken = "0123456789abcdef"

func newTestRouter(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return NewRouter(config.Config{MgmtToken: testToken}, st), st
}

func TestHealthNeedsNoToken(t *testing.T) {
	h, _ := newTestRouter(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /health = %d, want 200 without a token", rec.Code)
	}
}

func TestPostAgreementsRecordsIt(t *testing.T) {
	h, st := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /agreements = %d, want 201 (body %q)", rec.Code, rec.Body.String())
	}
	got, ok, err := st.GetAgreement("urn:uuid:a-1")
	if err != nil {
		t.Fatalf("GetAgreement: %v", err)
	}
	if !ok {
		t.Fatal("POST /agreements returned 201 but stored no agreement")
	}
	if got.DatasetID != "urn:dataset:a" {
		t.Errorf("DatasetID = %q, want urn:dataset:a", got.DatasetID)
	}
	if got.Origin != store.OriginImported {
		t.Errorf("Origin = %q, want %q", got.Origin, store.OriginImported)
	}
}

func TestPostAgreementsWithoutTokenIs401(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agreements", body))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /agreements with no Authorization header = %d, want 401", rec.Code)
	}
}

func TestPostAgreementsWithWrongTokenIs401(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", "Bearer 000000000000wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /agreements with a wrong token = %d, want 401", rec.Code)
	}
}

func TestPostAgreementsIs401WhenNoTokenIsConfigured(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	h := NewRouter(config.Config{}, st) // no token configured
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /agreements with no configured token = %d, want 401 — an unset token must never mean open access", rec.Code)
	}
}

func TestPostAgreementsMissingFieldIs400(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /agreements without datasetId = %d, want 400", rec.Code)
	}
}

func TestPostAgreementsDuplicateIs409(t *testing.T) {
	h, _ := newTestRouter(t)
	post := func() int {
		body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
		req := httptest.NewRequest(http.MethodPost, "/agreements", body)
		req.Header.Set("Authorization", "Bearer 0123456789abcdef")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(); code != http.StatusCreated {
		t.Fatalf("first POST = %d, want 201", code)
	}
	if code := post(); code != http.StatusConflict {
		t.Errorf("second POST with the same id = %d, want 409", code)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/mgmt/ -v`
Expected: FAIL — `NewRouter` takes no arguments.

- [ ] **Step 3: Implement the router**

Rewrite `internal/mgmt/router.go`:

```go
// Package mgmt serves the management API. It listens on a separate port from
// the DSP endpoints and binds to localhost by default, so exposing it is a
// deliberate configuration choice rather than a firewall accident.
package mgmt

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// maxAgreementBodyBytes bounds an import request body. An agreement is two
// short strings; anything larger is a mistake or an attack.
const maxAgreementBodyBytes = 4 << 10

// NewRouter returns the handler for the management listener. It takes the
// configuration for the bearer token and the store because importing an
// agreement writes to it.
func NewRouter(cfg config.Config, st *store.Store) http.Handler {
	mux := http.NewServeMux()

	// /health is deliberately unauthenticated: it carries no information and
	// a readiness probe should not need a credential.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	h := agreementHandler{store: st}
	mux.Handle("POST /agreements", authenticated(cfg.MgmtToken, http.HandlerFunc(h.importAgreement)))

	return mux
}

// authenticated rejects any request without the configured bearer token. An
// empty configured token rejects everything: a missing token must never read
// as "no auth required", or the localhost default becomes an open write
// endpoint the moment mgmt_addr changes.
func authenticated(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || !ok || subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type agreementHandler struct {
	store *store.Store
}

// importAgreement records an agreement concluded outside this connector.
// It records an agreement and nothing else — this is not the beginning of a
// general management CRUD surface.
func (h agreementHandler) importAgreement(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AgreementID string `json:"agreementId"`
		DatasetID   string `json:"datasetId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAgreementBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "malformed request body", http.StatusBadRequest)
		return
	}
	if body.AgreementID == "" || body.DatasetID == "" {
		http.Error(w, "agreementId and datasetId are both required", http.StatusBadRequest)
		return
	}

	err := h.store.CreateAgreement(store.Agreement{
		AgreementID: body.AgreementID,
		DatasetID:   body.DatasetID,
		Origin:      store.OriginImported,
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		// A duplicate is the one failure a caller can act on. Everything else
		// is this connector's problem, and its detail stays in the log.
		if _, found, getErr := h.store.GetAgreement(body.AgreementID); getErr == nil && found {
			http.Error(w, "an agreement with that id already exists", http.StatusConflict)
			return
		}
		slog.Error("import agreement", "agreement_id", body.AgreementID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	slog.Info("imported agreement", "agreement_id", body.AgreementID, "dataset_id", body.DatasetID)
	w.WriteHeader(http.StatusCreated)
}
```

- [ ] **Step 4: Update the wiring**

In `cmd/dsbox/main.go`, change `Handler: mgmt.NewRouter(),` to `Handler: mgmt.NewRouter(cfg, st),`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/mgmt/ -v && go build ./...`
Expected: PASS, and the binary builds.

- [ ] **Step 6: Commit**

```bash
go test ./... && gofmt -l . && go vet ./...
git add internal/mgmt/router.go internal/mgmt/router_test.go cmd/dsbox/main.go
git commit -m "feat: authenticate the management API and let it import an agreement"
```

---

### Task 5: A negotiation reaching `AGREED` records its agreement

**Files:**
- Modify: `internal/dsp/negotiation_handler.go`
- Modify: `internal/dsp/negotiation_handler_test.go`

**Interfaces:**
- Consumes: `store.Agreement`, `store.CreateAgreement`, `store.OriginNegotiated` (Task 2).
- Produces: nothing new. Task 7 relies on the row existing.

The provider role already emits `Agreement.ID = n.ProviderPID` in `buildAgreementMessage`. This task makes that fact durable so the transfer protocol can check it.

There is exactly one place the provider role moves a negotiation into `StateAgreed`: the `pushAndStore(n, StateAgreed, agreementCallbackPath, buildAgreementMessage(...))` call inside `dispatch` (`internal/dsp/negotiation_handler.go`, around line 449). Confirm with `grep -n "StateAgreed" internal/dsp/negotiation_handler.go` before editing — if a second site has appeared, record the agreement in both rather than in a place that only happens to cover one.

Put the write at that call site, not inside `pushAndStore`: that helper is generic over the target state, and burying an agreement-specific side effect in it would fire on transitions that have nothing to do with agreements.

**Failure handling:** if `CreateAgreement` fails, log at error and continue. The negotiation has already been announced to the counterparty, so refusing to advance would leave the two sides disagreeing about a contract that was in fact made. A duplicate-id error is expected on a legitimate retry path and must not be treated as a failure.

- [ ] **Step 1: Write the failing test**

Append to `internal/dsp/negotiation_handler_test.go`:

```go
// This mirrors TestHandleContractRequest_MatchedValid_PushesAgreement exactly
// — same helpers, same immediate-AGREED path — and adds the durability
// assertions. Do not hand-roll a second setup for the same journey.
func TestNegotiationReachingAgreedRecordsTheAgreement(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:a"+offerIDSuffix, "urn:dataset:a", fc.srv.URL))
	var doc NegotiationStateDocument
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	fc.wait(t, "/agreement")
	n := waitForState(t, st, doc.ProviderPID, StateAgreed)

	got, ok, err := st.GetAgreement(n.ProviderPID)
	if err != nil {
		t.Fatalf("GetAgreement: %v", err)
	}
	if !ok {
		t.Fatal("a negotiation reached AGREED but recorded no agreement")
	}
	if got.AgreementID != n.ProviderPID {
		t.Errorf("AgreementID = %q, want the negotiation's provider pid %q — buildAgreementMessage puts that value on the wire", got.AgreementID, n.ProviderPID)
	}
	if got.DatasetID != n.DatasetID {
		t.Errorf("DatasetID = %q, want %q", got.DatasetID, n.DatasetID)
	}
	if got.ConsumerPID != n.ConsumerPID {
		t.Errorf("ConsumerPID = %q, want %q", got.ConsumerPID, n.ConsumerPID)
	}
	if got.Origin != store.OriginNegotiated {
		t.Errorf("Origin = %q, want %q", got.Origin, store.OriginNegotiated)
	}
}
```

`waitForState` is needed because the agreement is pushed from a goroutine — it polls the store for up to a second and returns the negotiation. It already exists in this file.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dsp/ -run TestNegotiationReachingAgreedRecordsTheAgreement -v`
Expected: FAIL — no agreement recorded.

- [ ] **Step 3: Record the agreement**

At the point where the negotiation becomes `AGREED`:

```go
	// The agreement this connector just issued becomes a durable record, so
	// the transfer protocol can answer "does this agreement exist" without
	// scanning negotiations. The id is the one buildAgreementMessage puts on
	// the wire: this negotiation's provider pid.
	err := h.store.CreateAgreement(store.Agreement{
		AgreementID: n.ProviderPID,
		DatasetID:   n.DatasetID,
		ConsumerPID: n.ConsumerPID,
		Origin:      store.OriginNegotiated,
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		// Log and continue. The agreement has already been announced to the
		// counterparty, so refusing to advance here would leave the two sides
		// disagreeing about a contract that was in fact made.
		slog.Error("record agreement", "provider_pid", n.ProviderPID, "error", err)
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/ -run 'Agreed|Agreement' -v`
Expected: PASS, including the pre-existing negotiation tests.

- [ ] **Step 5: Commit**

```bash
go test ./... && go test -race ./internal/dsp/ && gofmt -l . && go vet ./...
git add internal/dsp/negotiation_handler.go internal/dsp/negotiation_handler_test.go
git commit -m "feat: record the agreement a negotiation issues"
```

---

### Task 6: Transfer messages and state machine

**Files:**
- Create: `internal/dsp/transfer.go`
- Create: `internal/dsp/transfer_test.go`

**Interfaces:**
- Consumes: `ContextURL`, `VersionPath`, `newMessageID()` from the existing `dsp` package.
- Produces, all used by Task 7:
  - States: `TransferRequested = "REQUESTED"`, `TransferStarted = "STARTED"`, `TransferCompleted = "COMPLETED"`, `TransferSuspended = "SUSPENDED"`, `TransferTerminated = "TERMINATED"`
  - Types: `TransferRequestMessage`, `TransferProcessDoc`, `TransferStartMessage`, `TransferSuspensionMessage`, `TransferTerminationMessage`, `TransferCompletionMessage`
  - `func startLegalFrom(state string) bool`
  - `func completionLegalFrom(state string) bool`
  - `func suspensionLegalFrom(state string) bool`
  - `func terminationLegalFrom(state string) bool`
  - `func buildTransferProcessDoc(t store.TransferProcess) TransferProcessDoc`
  - `func buildTransferStartMessage(t store.TransferProcess) TransferStartMessage`

Distinct state constants rather than reusing negotiation's `StateRequested`/`StateTerminated`: the two protocols' state sets overlap by name and differ in meaning, and one shared constant would make a wrong-protocol comparison compile silently.

The legality rules, from the spec's state machine:

| From | start | completion | suspension | termination |
|---|---|---|---|---|
| `REQUESTED` | yes | no | no | yes |
| `STARTED` | no | yes | yes | yes |
| `SUSPENDED` | yes | no | no | yes |
| `COMPLETED` | no | no | no | no |
| `TERMINATED` | no | no | no | no |

`SUSPENDED -> STARTED` is why `startLegalFrom` accepts two states. Both terminal states accept nothing.

- [ ] **Step 1: Write the failing tests**

Create `internal/dsp/transfer_test.go`:

```go
package dsp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

func TestTransferTransitionLegality(t *testing.T) {
	states := []string{TransferRequested, TransferStarted, TransferSuspended, TransferCompleted, TransferTerminated}
	cases := []struct {
		name string
		fn   func(string) bool
		want map[string]bool
	}{
		{"start", startLegalFrom, map[string]bool{
			TransferRequested: true, TransferStarted: false, TransferSuspended: true,
			TransferCompleted: false, TransferTerminated: false,
		}},
		{"completion", completionLegalFrom, map[string]bool{
			TransferRequested: false, TransferStarted: true, TransferSuspended: false,
			TransferCompleted: false, TransferTerminated: false,
		}},
		{"suspension", suspensionLegalFrom, map[string]bool{
			TransferRequested: false, TransferStarted: true, TransferSuspended: false,
			TransferCompleted: false, TransferTerminated: false,
		}},
		{"termination", terminationLegalFrom, map[string]bool{
			TransferRequested: true, TransferStarted: true, TransferSuspended: true,
			TransferCompleted: false, TransferTerminated: false,
		}},
	}
	for _, c := range cases {
		for _, s := range states {
			if got := c.fn(s); got != c.want[s] {
				t.Errorf("%sLegalFrom(%s) = %v, want %v", c.name, s, got, c.want[s])
			}
		}
	}
}

func TestBuildTransferProcessDocCarriesTheRequiredFields(t *testing.T) {
	tp := store.TransferProcess{
		ProviderPID: "urn:uuid:p-1", ConsumerPID: "urn:uuid:c-1", State: TransferStarted,
	}
	raw, err := json.Marshal(buildTransferProcessDoc(tp))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// transfer-process-schema.json requires all five.
	for _, k := range []string{"@context", "@type", "providerPid", "consumerPid", "state"} {
		if _, ok := got[k]; !ok {
			t.Errorf("TransferProcess is missing required property %q", k)
		}
	}
	if got["@type"] != "TransferProcess" {
		t.Errorf("@type = %v, want TransferProcess", got["@type"])
	}
	if got["state"] != TransferStarted {
		t.Errorf("state = %v, want %s", got["state"], TransferStarted)
	}
}

func TestBuildTransferStartMessageCarriesTheRequiredFields(t *testing.T) {
	tp := store.TransferProcess{
		ProviderPID: "urn:uuid:p-1", ConsumerPID: "urn:uuid:c-1",
		State: TransferRequested, CreatedAt: time.Now().UTC(),
	}
	raw, err := json.Marshal(buildTransferStartMessage(tp))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// transfer-start-message-schema.json requires these four. dataAddress is
	// optional and Phase A sends none.
	for _, k := range []string{"@context", "@type", "providerPid", "consumerPid"} {
		if _, ok := got[k]; !ok {
			t.Errorf("TransferStartMessage is missing required property %q", k)
		}
	}
	if got["@type"] != "TransferStartMessage" {
		t.Errorf("@type = %v, want TransferStartMessage", got["@type"])
	}
	if _, present := got["dataAddress"]; present {
		t.Error("Phase A must not emit a dataAddress: nothing serves it yet, and announcing an endpoint that serves nothing is a claim this connector cannot keep")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/ -run Transfer -v`
Expected: FAIL — undefined identifiers.

- [ ] **Step 3: Write `internal/dsp/transfer.go`**

```go
package dsp

import (
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// The transfer process states, from transfer-process-schema.json's own enum.
// These are deliberately separate constants from the negotiation states even
// where the strings match: the two protocols' state sets overlap by name and
// differ in meaning, and sharing one constant would let a wrong-protocol
// comparison compile silently.
const (
	TransferRequested  = "REQUESTED"
	TransferStarted    = "STARTED"
	TransferCompleted  = "COMPLETED"
	TransferSuspended  = "SUSPENDED"
	TransferTerminated = "TERMINATED"
)

// Message @type values.
const (
	TransferRequestMessageType     = "TransferRequestMessage"
	TransferProcessType            = "TransferProcess"
	TransferStartMessageType       = "TransferStartMessage"
	TransferSuspensionMessageType  = "TransferSuspensionMessage"
	TransferTerminationMessageType = "TransferTerminationMessage"
	TransferCompletionMessageType  = "TransferCompletionMessage"
	TransferErrorType              = "TransferError"
)

// TransferRequestMessage is the body of POST /transfers/request. Only the
// fields this connector inspects are declared, matching the direct-field-check
// approach of DECISIONS.md section 22.5. dataAddress is deliberately absent:
// it is optional in the schema, Phase A has no data plane, and declaring a
// field nothing reads invites someone to believe it is used.
type TransferRequestMessage struct {
	Context         []string `json:"@context"`
	Type            string   `json:"@type"`
	ConsumerPID     string   `json:"consumerPid"`
	AgreementID     string   `json:"agreementId"`
	Format          string   `json:"format"`
	CallbackAddress string   `json:"callbackAddress"`
}

// TransferProcessDoc is the response body for every transfer endpoint that
// returns the process itself.
type TransferProcessDoc struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
	State       string   `json:"state"`
}

// TransferStartMessage is pushed to the consumer's callback address when this
// connector starts a transfer. Phase A carries no dataAddress.
type TransferStartMessage struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

// TransferSuspensionMessage, TransferTerminationMessage, and
// TransferCompletionMessage are the inbound messages this connector accepts on
// a running transfer. The TCK registers no schema validator for these three,
// so their shape is checked only by what its pipeline reads out of them; this
// connector emits and accepts them to the same standard as the rest.
type TransferSuspensionMessage struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

type TransferTerminationMessage struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

type TransferCompletionMessage struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

// startLegalFrom reports whether a transfer in this state may start. SUSPENDED
// is included because resuming a suspended transfer is a start.
func startLegalFrom(state string) bool {
	return state == TransferRequested || state == TransferSuspended
}

// completionLegalFrom reports whether a transfer in this state may complete.
// Only a running transfer can finish.
func completionLegalFrom(state string) bool {
	return state == TransferStarted
}

// suspensionLegalFrom reports whether a transfer in this state may be
// suspended. Only a running transfer can be paused.
func suspensionLegalFrom(state string) bool {
	return state == TransferStarted
}

// terminationLegalFrom reports whether a transfer in this state may be
// terminated. Anything not already in a terminal state can be.
func terminationLegalFrom(state string) bool {
	return state == TransferRequested || state == TransferStarted || state == TransferSuspended
}

func buildTransferProcessDoc(t store.TransferProcess) TransferProcessDoc {
	return TransferProcessDoc{
		Context:     []string{ContextURL},
		Type:        TransferProcessType,
		ProviderPID: t.ProviderPID,
		ConsumerPID: t.ConsumerPID,
		State:       t.State,
	}
}

func buildTransferStartMessage(t store.TransferProcess) TransferStartMessage {
	return TransferStartMessage{
		Context:     []string{ContextURL},
		Type:        TransferStartMessageType,
		ProviderPID: t.ProviderPID,
		ConsumerPID: t.ConsumerPID,
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/ -run Transfer -v`
Expected: PASS, 3 tests.

- [ ] **Step 5: Commit**

```bash
go test ./... && gofmt -l . && go vet ./...
git add internal/dsp/transfer.go internal/dsp/transfer_test.go
git commit -m "feat: add transfer process messages and state machine"
```

---

### Task 7: Transfer handlers and routing

**Files:**
- Create: `internal/dsp/transfer_handler.go`
- Create: `internal/dsp/transfer_handler_test.go`
- Modify: `internal/dsp/router.go`

**Interfaces:**
- Consumes: everything Task 6 produces; `store.CreateTransfer`, `GetTransfer`, `SetTransferState`, `GetAgreement` (Tasks 2-3); the existing `checkEnvelope`, `writeError`, `pushCallback`, `newMessageID`, `maxNegotiationRequestBodyBytes`.
- Produces: `transferHandler` and its methods; the routes below.

Routes, mounted where `router.go`'s "Transfer process mounts here next" comment is:

```
POST   {VersionPath}/transfers/request
GET    {VersionPath}/transfers/{id}
POST   {VersionPath}/transfers/{id}/start
POST   {VersionPath}/transfers/{id}/completion
POST   {VersionPath}/transfers/{id}/suspension
POST   {VersionPath}/transfers/{id}/termination
```

`{id}` is this connector's own generated provider pid, the same convention the provider-role negotiation endpoints use.

**Rules that are not negotiable:**

- Unknown `{id}` is `404`. A structural failure on a known transfer — bad envelope, illegal transition — is `400`, never `404`.
- An unknown `agreementId` on `POST /transfers/request` is `400`. This is the spec's central decision: this connector does not start a transfer under a contract it has no record of.
- `handleTransferRequest` pushes the `TransferStartMessage` with `go`, through `pushCallback`. Never inline: `net/http` puts nothing on the wire until the handler returns (`DECISIONS.md` §23.8).

- [ ] **Step 1: Write the failing tests**

Create `internal/dsp/transfer_handler_test.go`. Read `negotiation_handler_test.go` first and reuse its helper shapes; do not invent a second test idiom for the same package.

```go
package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// newTestTransferHandler mirrors newTestHandler: an in-memory store, and the
// outgoing-callback SSRF filter disabled because httptest servers are always
// on loopback, which is exactly what that filter rejects in production. The
// filter has its own direct table test in callback_test.go.
func newTestTransferHandler(t *testing.T, cfg config.Config) (transferHandler, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	origValidate := validateOutgoingCallback
	validateOutgoingCallback = func(string) error { return nil }
	t.Cleanup(func() { validateOutgoingCallback = origValidate })

	return transferHandler{cfg: cfg, store: st}, st
}

func seedAgreement(t *testing.T, st *store.Store, id string) {
	t.Helper()
	err := st.CreateAgreement(store.Agreement{
		AgreementID: id,
		DatasetID:   "urn:dataset:a",
		Origin:      store.OriginImported,
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
}

func seedTransfer(t *testing.T, st *store.Store, state string) store.TransferProcess {
	t.Helper()
	now := time.Now().UTC()
	tp := store.TransferProcess{
		ProviderPID:     "urn:uuid:tp-1",
		ConsumerPID:     "urn:uuid:tc-1",
		AgreementID:     "urn:uuid:agreement-1",
		State:           state,
		CallbackAddress: "http://consumer.example/2025-1",
		Format:          "HTTP-PULL",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := st.CreateTransfer(tp); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	return tp
}

// transferRequestBody is a well-formed TransferRequestMessage. Individual
// tests override one field to exercise a specific guard.
func transferRequestBody(agreementID string) string {
	return `{"@context":["` + ContextURL + `"],"@type":"` + TransferRequestMessageType + `",` +
		`"consumerPid":"urn:uuid:tc-1","agreementId":"` + agreementID + `",` +
		`"format":"HTTP-PULL","callbackAddress":"http://consumer.example/2025-1"}`
}

func TestTransferRequestWithKnownAgreementIsAccepted(t *testing.T) {
	h, st := newTestTransferHandler(t, config.Config{})
	seedAgreement(t, st, "urn:uuid:agreement-1")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/request",
		strings.NewReader(transferRequestBody("urn:uuid:agreement-1")))
	h.handleTransferRequest(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("POST /transfers/request = %d, want 200 or 201 (body %q)", rec.Code, rec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response body is not JSON: %v", err)
	}
	for _, k := range []string{"@context", "@type", "providerPid", "consumerPid", "state"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("response TransferProcess is missing required property %q", k)
		}
	}
	if doc["state"] != TransferRequested {
		t.Errorf("state = %v, want %s", doc["state"], TransferRequested)
	}
	providerPID, _ := doc["providerPid"].(string)
	got, ok, err := st.GetTransfer(providerPID)
	if err != nil {
		t.Fatalf("GetTransfer: %v", err)
	}
	if !ok {
		t.Fatal("the response named a providerPid that was never stored")
	}
	if got.AgreementID != "urn:uuid:agreement-1" {
		t.Errorf("stored AgreementID = %q, want the requested one", got.AgreementID)
	}
}

func TestTransferRequestWithUnknownAgreementIs400(t *testing.T) {
	h, st := newTestTransferHandler(t, config.Config{})
	// Deliberately seed nothing: this is the spec's central guard.

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/request",
		strings.NewReader(transferRequestBody("urn:uuid:never-negotiated")))
	h.handleTransferRequest(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /transfers/request citing an unknown agreement = %d, want 400", rec.Code)
	}
	// Assert on the response rather than on the store. The handler generates
	// its own provider pid, so a "was anything stored?" check would have to
	// guess that pid — and a check for an id the handler was never going to
	// use passes whether or not a transfer was created, which is worse than
	// no check at all. A 400 with no providerPid is the property the
	// counterparty and the TCK actually observe.
	if strings.Contains(rec.Body.String(), "providerPid") {
		t.Errorf("a rejected request returned a providerPid: %q", rec.Body.String())
	}
	_ = st
}

func TestTransferRequestBadEnvelopeIs400(t *testing.T) {
	h, st := newTestTransferHandler(t, config.Config{})
	seedAgreement(t, st, "urn:uuid:agreement-1")

	cases := map[string]string{
		"wrong @type": `{"@context":["` + ContextURL + `"],"@type":"NotATransferRequest",` +
			`"consumerPid":"urn:uuid:tc-1","agreementId":"urn:uuid:agreement-1",` +
			`"format":"HTTP-PULL","callbackAddress":"http://consumer.example/2025-1"}`,
		"missing @context": `{"@context":[],"@type":"` + TransferRequestMessageType + `",` +
			`"consumerPid":"urn:uuid:tc-1","agreementId":"urn:uuid:agreement-1",` +
			`"format":"HTTP-PULL","callbackAddress":"http://consumer.example/2025-1"}`,
		"missing consumerPid": `{"@context":["` + ContextURL + `"],"@type":"` + TransferRequestMessageType + `",` +
			`"agreementId":"urn:uuid:agreement-1","format":"HTTP-PULL",` +
			`"callbackAddress":"http://consumer.example/2025-1"}`,
	}
	for name, body := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/request", strings.NewReader(body))
		h.handleTransferRequest(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, rec.Code)
		}
	}
}

func TestGetTransferReturnsTheDocument(t *testing.T) {
	h, st := newTestTransferHandler(t, config.Config{})
	tp := seedTransfer(t, st, TransferStarted)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/transfers/"+tp.ProviderPID, nil)
	req.SetPathValue("id", tp.ProviderPID)
	h.handleGetTransfer(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /transfers/{id} = %d, want 200", rec.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response body is not JSON: %v", err)
	}
	if doc["state"] != TransferStarted || doc["providerPid"] != tp.ProviderPID {
		t.Errorf("document = %v, want state %s and providerPid %s", doc, TransferStarted, tp.ProviderPID)
	}
}

// TestTransferTransitionsOverHTTP walks the whole legality matrix through the
// handlers, so the pure functions in transfer.go and the wiring that calls
// them are pinned together. An illegal transition is 400 and must leave the
// stored state untouched — a handler that returns 400 after already writing
// would pass a status-only assertion.
func TestTransferTransitionsOverHTTP(t *testing.T) {
	type step struct {
		endpoint  string
		msgType   string
		from      string
		wantCode  int
		wantState string
	}
	steps := []step{
		{"start", TransferStartMessageType, TransferRequested, http.StatusOK, TransferStarted},
		{"start", TransferStartMessageType, TransferSuspended, http.StatusOK, TransferStarted},
		{"start", TransferStartMessageType, TransferStarted, http.StatusBadRequest, TransferStarted},
		{"start", TransferStartMessageType, TransferCompleted, http.StatusBadRequest, TransferCompleted},
		{"start", TransferStartMessageType, TransferTerminated, http.StatusBadRequest, TransferTerminated},

		{"completion", TransferCompletionMessageType, TransferStarted, http.StatusOK, TransferCompleted},
		{"completion", TransferCompletionMessageType, TransferRequested, http.StatusBadRequest, TransferRequested},
		{"completion", TransferCompletionMessageType, TransferSuspended, http.StatusBadRequest, TransferSuspended},

		{"suspension", TransferSuspensionMessageType, TransferStarted, http.StatusOK, TransferSuspended},
		{"suspension", TransferSuspensionMessageType, TransferRequested, http.StatusBadRequest, TransferRequested},
		{"suspension", TransferSuspensionMessageType, TransferCompleted, http.StatusBadRequest, TransferCompleted},

		{"termination", TransferTerminationMessageType, TransferRequested, http.StatusOK, TransferTerminated},
		{"termination", TransferTerminationMessageType, TransferStarted, http.StatusOK, TransferTerminated},
		{"termination", TransferTerminationMessageType, TransferSuspended, http.StatusOK, TransferTerminated},
		{"termination", TransferTerminationMessageType, TransferCompleted, http.StatusBadRequest, TransferCompleted},
		{"termination", TransferTerminationMessageType, TransferTerminated, http.StatusBadRequest, TransferTerminated},
	}

	for _, s := range steps {
		h, st := newTestTransferHandler(t, config.Config{})
		tp := seedTransfer(t, st, s.from)

		body := `{"@context":["` + ContextURL + `"],"@type":"` + s.msgType + `",` +
			`"providerPid":"` + tp.ProviderPID + `","consumerPid":"` + tp.ConsumerPID + `"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost,
			VersionPath+"/transfers/"+tp.ProviderPID+"/"+s.endpoint, strings.NewReader(body))
		req.SetPathValue("id", tp.ProviderPID)

		switch s.endpoint {
		case "start":
			h.handleTransferStart(rec, req)
		case "completion":
			h.handleTransferCompletion(rec, req)
		case "suspension":
			h.handleTransferSuspension(rec, req)
		case "termination":
			h.handleTransferTermination(rec, req)
		}

		if rec.Code != s.wantCode {
			t.Errorf("%s from %s: got %d, want %d", s.endpoint, s.from, rec.Code, s.wantCode)
		}
		got, _, err := st.GetTransfer(tp.ProviderPID)
		if err != nil {
			t.Fatalf("GetTransfer: %v", err)
		}
		if got.State != s.wantState {
			t.Errorf("%s from %s: stored state = %s, want %s", s.endpoint, s.from, got.State, s.wantState)
		}
	}
}

// TestTransferEndpointsUnknownIDIs404 pins the one place 404 is correct. Every
// other rejection in this protocol is 400, because the TCK's own assertion
// helper throws immediately on a 404 even where an error is expected.
func TestTransferEndpointsUnknownIDIs404(t *testing.T) {
	endpoints := map[string]func(transferHandler, http.ResponseWriter, *http.Request){
		"start":       func(h transferHandler, w http.ResponseWriter, r *http.Request) { h.handleTransferStart(w, r) },
		"completion":  func(h transferHandler, w http.ResponseWriter, r *http.Request) { h.handleTransferCompletion(w, r) },
		"suspension":  func(h transferHandler, w http.ResponseWriter, r *http.Request) { h.handleTransferSuspension(w, r) },
		"termination": func(h transferHandler, w http.ResponseWriter, r *http.Request) { h.handleTransferTermination(w, r) },
		"get":         func(h transferHandler, w http.ResponseWriter, r *http.Request) { h.handleGetTransfer(w, r) },
	}
	for name, call := range endpoints {
		h, _ := newTestTransferHandler(t, config.Config{})
		body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferStartMessageType + `",` +
			`"providerPid":"urn:uuid:nope","consumerPid":"urn:uuid:tc-1"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/urn:uuid:nope/"+name, strings.NewReader(body))
		req.SetPathValue("id", "urn:uuid:nope")
		call(h, rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s with an unknown id = %d, want 404", name, rec.Code)
		}
	}
}

// TestTransferRequestPushesStartMessage asserts the request PATH as well as the
// body. A wrong callback path is what makes every TP test time out with no
// useful message, so it is worth catching here quietly instead.
func TestTransferRequestPushesStartMessage(t *testing.T) {
	gotPath := make(chan string, 1)
	gotBody := make(chan map[string]any, 1)
	consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			// t.Errorf, never t.Fatalf: this runs on the server goroutine,
			// where Fatalf calls runtime.Goexit on the wrong stack and hangs
			// the request. See assertEmittedOffer in negotiation_test.go.
			t.Errorf("decode pushed message: %v", err)
		}
		select {
		case gotPath <- r.URL.Path:
		default:
		}
		select {
		case gotBody <- body:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer consumer.Close()

	h, st := newTestTransferHandler(t, config.Config{})
	seedAgreement(t, st, "urn:uuid:agreement-1")

	body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferRequestMessageType + `",` +
		`"consumerPid":"urn:uuid:tc-1","agreementId":"urn:uuid:agreement-1",` +
		`"format":"HTTP-PULL","callbackAddress":"` + consumer.URL + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/request", strings.NewReader(body))
	h.handleTransferRequest(rec, req)

	select {
	case path := <-gotPath:
		if !strings.Contains(path, "/transfers/") || !strings.HasSuffix(path, "/start") {
			t.Errorf("pushed to %q, want a .../transfers/{consumerPid}/start path", path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no TransferStartMessage was pushed to the consumer's callback address")
	}

	msg := <-gotBody
	if msg["@type"] != TransferStartMessageType {
		t.Errorf("pushed @type = %v, want %s", msg["@type"], TransferStartMessageType)
	}
	if _, present := msg["dataAddress"]; present {
		t.Error("Phase A must not emit a dataAddress")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/ -run TransferHandler -v`
Expected: FAIL — undefined identifiers.

- [ ] **Step 3: Implement `internal/dsp/transfer_handler.go`**

Structure it exactly like `negotiation_handler.go`'s provider half:

```go
package dsp

// transferHandler serves the transfer process protocol in the provider role.
// It lives in its own file because negotiation_handler.go was split at 867
// lines in the previous milestone specifically so this protocol would not
// grow it further.
type transferHandler struct {
	cfg   config.Config
	store *store.Store
}
```

- `handleTransferRequest`: decode with `http.MaxBytesReader` and `maxNegotiationRequestBodyBytes`; `checkEnvelope(w, msg.Context, msg.Type, TransferRequestMessageType)`; reject an empty `consumerPid`, `agreementId`, `format`, or `callbackAddress` with `400`; `GetAgreement` and reject an unknown id with `400`; generate the provider pid with `newMessageID()`; `CreateTransfer` in `TransferRequested`; write the `TransferProcessDoc`; then `go h.startTransfer(t)`.
- `startTransfer`: pushes `buildTransferStartMessage(t)` to `t.CallbackAddress + "/transfers/" + t.ConsumerPID + "/start"` through `pushCallback`, then `SetTransferState(..., TransferRequested, TransferStarted, ...)`. Confirm the exact callback path against the TCK log in Task 9 — if it is wrong, every `TP` test times out, and the log says so plainly.
- `handleTransferStart`, `handleTransferCompletion`, `handleTransferSuspension`, `handleTransferTermination`: look the transfer up (`404` if absent), `checkEnvelope`, check the matching `...LegalFrom` (`400` if illegal), then `SetTransferState`.
- `handleGetTransfer`: look up (`404` if absent), write `buildTransferProcessDoc`.

Use `writeError` with `TransferErrorType` for the error bodies, mirroring how the negotiation handlers use `ContractNegotiationErrorType`.

- [ ] **Step 4: Mount the routes**

Replace the placeholder comment in `internal/dsp/router.go`:

```go
	tr := transferHandler{cfg: cfg, store: st}
	mux.HandleFunc("POST "+VersionPath+"/transfers/request", tr.handleTransferRequest)
	mux.HandleFunc("GET "+VersionPath+"/transfers/{id}", tr.handleGetTransfer)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/start", tr.handleTransferStart)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/completion", tr.handleTransferCompletion)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/suspension", tr.handleTransferSuspension)
	mux.HandleFunc("POST "+VersionPath+"/transfers/{id}/termination", tr.handleTransferTermination)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dsp/ -v && go test -race -count=2 ./internal/dsp/`
Expected: PASS both, output pristine.

- [ ] **Step 6: Commit**

```bash
go test ./... && gofmt -l . && go vet ./...
git add internal/dsp/transfer_handler.go internal/dsp/transfer_handler_test.go internal/dsp/router.go
git commit -m "feat: add transfer process handlers and routing"
```

---

### Task 8: Autonomous provider transitions, keyed by agreement

> **Added after Task 7, because the TCK falsified the plan's scope.** See the spec's
> "Autonomous provider behavior, keyed by agreement" — an amendment written after
> implementation began. Without this task, roughly 8 of the suite's 15 counted
> tests fail, and the gate count of 15 in Task 9 does not hold.

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `config.example.yaml`
- Modify: `internal/dsp/transfer_handler.go`, `internal/dsp/transfer_handler_test.go`

**Interfaces:**
- Consumes: everything Tasks 3, 6 and 7 produce.
- Produces: `config.TransferPolicy{AgreementID string; Sequence []string}`, `config.Config.TransferPolicies []TransferPolicy`, and a `resolveTransferSequence(cfg, agreementID) []string` in the `dsp` package.

**Why it exists.** In `TP:01-xx` the TCK sends exactly one message — the `TransferRequestMessage` — and thereafter only polls `GET /transfers/{id}`. No trigger, no control call. The connector decides its own subsequent transitions, and the only test-varying field on the wire is `agreementId`. This is the same shape `consumer_policies` already has, where `datasetId` selects a configured reaction.

**The sequences the TCK asks for**, each the states this connector walks on its own after accepting a request:

| Selector | Sequence |
|---|---|
| default (no entry) | `[STARTED]` |
| `TP:01-01`'s agreement | `[STARTED, TERMINATED]` |
| `TP:01-02`'s | `[STARTED, COMPLETED]` |
| `TP:01-03`'s | `[STARTED, SUSPENDED, TERMINATED]` |
| `TP:01-04`'s | `[STARTED, SUSPENDED, STARTED, COMPLETED]` |
| `TP:01-05`'s | `[TERMINATED]` |
| `TP:02-05`, `TP:03-01`, `TP:03-02`'s | `[]` — stay in `REQUESTED` |

**The trap that is easy to miss:** starting must itself become conditional. Four provider tests carry no "provider started" step and poll for `REQUESTED`; today's unconditional start breaks them.

- [ ] **Step 1: Write the failing config tests**

Append to `internal/config/config_test.go`, matching the file's existing `minimal`/`env` helpers:

```go
func TestTransferPoliciesEmptyWhenAbsent(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TransferPolicies) != 0 {
		t.Errorf("TransferPolicies = %v, want empty when the key is absent", cfg.TransferPolicies)
	}
}

func TestTransferPolicyParses(t *testing.T) {
	cfg, err := Load(minimal("transfer_policies:\n  - agreement_id: urn:uuid:a\n    sequence: [STARTED, SUSPENDED, TERMINATED]\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TransferPolicies) != 1 {
		t.Fatalf("TransferPolicies = %v, want one entry", cfg.TransferPolicies)
	}
	p := cfg.TransferPolicies[0]
	if p.AgreementID != "urn:uuid:a" || len(p.Sequence) != 3 || p.Sequence[1] != "SUSPENDED" {
		t.Errorf("TransferPolicies[0] = %+v, want the parsed fixture", p)
	}
}

func TestTransferPolicyEmptySequenceIsValid(t *testing.T) {
	// An explicit empty sequence is the only way to say "accept the request and
	// stay in REQUESTED", which four TCK tests assert. It must survive loading
	// as a present-but-empty entry, distinct from having no entry at all.
	cfg, err := Load(minimal("transfer_policies:\n  - agreement_id: urn:uuid:a\n    sequence: []\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TransferPolicies) != 1 || cfg.TransferPolicies[0].Sequence == nil && len(cfg.TransferPolicies[0].Sequence) != 0 {
		t.Errorf("TransferPolicies = %+v, want one entry with an empty sequence", cfg.TransferPolicies)
	}
}

func TestTransferPolicyRejectsAnUnknownState(t *testing.T) {
	_, err := Load(minimal("transfer_policies:\n  - agreement_id: urn:uuid:a\n    sequence: [STARTED, NONSENSE]\n"), env(nil))
	if err == nil {
		t.Error("Load: expected an error for a sequence naming a state that is not a transfer state")
	}
}

func TestTransferPolicyRequiresAnAgreementID(t *testing.T) {
	_, err := Load(minimal("transfer_policies:\n  - sequence: [STARTED]\n"), env(nil))
	if err == nil {
		t.Error("Load: expected an error for a policy with no agreement_id")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/config/ -run TransferPolic -v`
Expected: FAIL — `TransferPolicies` undefined.

- [ ] **Step 3: Add the config type and validation**

In `internal/config/config.go`, beside `ConsumerPolicy`:

```go
// TransferPolicy configures this connector's autonomous behavior as transfer
// provider, keyed by the agreement a transfer is requested under. Sequence is
// the states it walks on its own after accepting the request, pushing the
// matching message to the consumer's callback address at each step.
//
// An agreement with no entry gets [STARTED]: accept, then start. An explicit
// empty sequence means accept and stay in REQUESTED, which is a different
// thing from having no entry and is why the field cannot simply be omitted.
//
// This is the transfer analogue of ConsumerPolicy, and it exists for the same
// reason: v1 has none of the operational inputs a real provider would use to
// decide to suspend or complete a transfer, so the decision comes from
// configuration instead. See the design spec's "Autonomous provider behavior,
// keyed by agreement".
type TransferPolicy struct {
	AgreementID string   `yaml:"agreement_id"`
	Sequence    []string `yaml:"sequence"`
}
```

Add `TransferPolicies []TransferPolicy \`yaml:"transfer_policies"\`` to `Config`, and validate in `validate()`: `agreement_id` is required, and every element of `Sequence` must be one of `STARTED`, `SUSPENDED`, `COMPLETED`, `TERMINATED`. Reject `REQUESTED` in a sequence — it is the state the transfer starts in, not one it can be driven to. Follow the file's existing one-`if`-per-rule style and its error-message voice.

- [ ] **Step 4: Run the config tests to verify they pass**

Run: `go test ./internal/config/ -run TransferPolic -v`
Expected: PASS, 5 tests.

- [ ] **Step 5: Write the failing driver tests**

Append to `internal/dsp/transfer_handler_test.go`. Read the file's existing helpers first (`newTestTransferHandler`, `seedAgreement`, `waitForTransferState`) and reuse them rather than adding new ones.

Cover, each as its own test function:

1. `resolveTransferSequence` returns `[]string{TransferStarted}` for an agreement with no configured entry.
2. It returns the configured sequence for a matching `agreement_id`.
3. It returns an empty sequence — not the default — for an entry configured with `sequence: []`.
4. An agreement configured `[]` leaves the transfer in `REQUESTED` after `POST /transfers/request`, and **pushes nothing**. Assert both: poll briefly and confirm the stored state is still `REQUESTED`, and use a fake consumer that records every request it receives, asserting it received none.
5. An agreement configured `[STARTED, TERMINATED]` walks both steps: the fake consumer receives a `TransferStartMessage` then a `TransferTerminationMessage`, in that order, and the stored state ends `TERMINATED`.
6. An agreement configured `[STARTED, SUSPENDED, STARTED, COMPLETED]` walks all four in order and ends `COMPLETED` — this is `TP:01-04`, the longest sequence and the one that revisits `STARTED`.

For 5 and 6, assert the **order** of received message `@type`s, not merely that each arrived. A driver that pushed them concurrently would satisfy a set assertion and fail the TCK, whose handler registration is single-shot and ordered.

- [ ] **Step 6: Run them to verify they fail**

Run: `go test ./internal/dsp/ -run 'TransferSequence|TransferPolicy' -v`
Expected: FAIL — `resolveTransferSequence` undefined.

- [ ] **Step 7: Implement the resolver and the driver**

In `internal/dsp/transfer_handler.go`:

```go
// resolveTransferSequence returns the states this connector walks on its own
// after accepting a transfer request under this agreement. An agreement with
// no configured entry starts and stops there; an entry with an empty sequence
// deliberately does nothing, which is how a transfer stays in REQUESTED.
func resolveTransferSequence(cfg config.Config, agreementID string) []string {
	for _, p := range cfg.TransferPolicies {
		if p.AgreementID == agreementID {
			return p.Sequence
		}
	}
	return []string{TransferStarted}
}
```

Replace the unconditional `go h.startTransfer(t)` with `go h.driveTransfer(t)`, which walks the resolved sequence. Each step:

- picks the message for the target state — `buildTransferStartMessage` for `STARTED`, and the suspension / completion / termination builders for the others;
- pushes it to `t.CallbackAddress + "/transfers/" + t.ConsumerPID + "/" + <segment>` through `pushCallback`, where the segment is `start`, `suspension`, `completion`, or `termination`;
- then advances the stored state with `SetTransferState` from the state it currently holds to the target, exactly as `startTransfer` does today;
- and **stops the whole sequence** if a state write is lost to `ErrStateChanged` — a counterparty that terminated the transfer mid-sequence has taken it over, and continuing would push messages for a transfer that is no longer in the state they claim.

Keep `startTransfer`'s existing shape for the single-step case rather than writing a second idiom: the driver is that function generalised over a list, including its `validateOutgoingCallback` gate and its `ErrStateChanged` handling.

- [ ] **Step 8: Run the driver tests to verify they pass**

Run: `go test ./internal/dsp/ -run 'TransferSequence|TransferPolicy' -v && go test ./internal/dsp/ -run Transfer -v`
Expected: PASS, and the Task 7 transfer tests still pass — several of them assume the default `[STARTED]` behavior, which the resolver must preserve.

- [ ] **Step 9: Full gates and commit**

```bash
go test ./... && go test -race -count=2 ./internal/dsp/ && gofmt -l . && go vet ./...
git add internal/config/config.go internal/config/config_test.go config.example.yaml internal/dsp/transfer_handler.go internal/dsp/transfer_handler_test.go
git commit -m "feat: drive provider-initiated transfer transitions from configuration"
```

---

### Task 9: TCK fixtures, seeding, gate, and the real run

**Files:**
- Modify: `test/tck/config.properties`
- Modify: `test/tck/run.sh`
- Modify: `test/tck/dsbox.yaml`
- Modify: `cmd/tckgate/main.go`
- Modify: `README.md`
- Modify: `DECISIONS.md`

**Interfaces:**
- Consumes: everything above.
- Produces: `TP` in the gate at 15.

This is the task that decides whether the milestone is real. **Do not edit a test expectation, a gate count, or a fixture to make a number go green.** When something fails, read `tck-output.txt`; when the log is not enough, read the TCK's own classes out of the pinned image (the exact commands are in `docs/superpowers/specs/2026-08-16-transfer-process-tck-requirements.md`). An exemption is legitimate only where something is shown to be structurally impossible, the way `CN:02-07` was, and it needs that evidence recorded.

- [ ] **Step 1: Add the management token to the harness config**

In `test/tck/dsbox.yaml`, add a token the seeding step can use:

```yaml
# The TCK harness imports its fixture agreements through the management API
# before the suite runs (see run.sh). Any value at least 16 characters works;
# this one is not a secret and never leaves the compose network.
mgmt_token: tck-harness-token-0
```

The management listener binds to localhost by default. `run.sh` seeds from inside the compose network, so `test/tck/compose.yaml` must either publish `8081` (it already does, for the readiness probe) or the seeding must run via `docker compose exec`. Use the published port that is already there.

- [ ] **Step 2: Seed the fixture agreements in `run.sh`**

After the readiness loop that waits on `/health`, before the TCK runs:

```sh
# The TP suite asks this connector to transfer under an agreement id the TCK
# supplies from config.properties. Those agreements were concluded outside
# this connector as far as it is concerned, so they are imported through the
# management API — the same path a real operator would use. This connector
# rejects a transfer citing an agreement it has no record of, which is the
# point of the check and the reason this step exists.
seed_agreement() {
	code=$(curl -s -o /dev/null -w '%{http_code}' \
		-X POST http://127.0.0.1:8081/agreements \
		-H 'Authorization: Bearer tck-harness-token-0' \
		-H 'Content-Type: application/json' \
		-d "{\"agreementId\":\"$1\",\"datasetId\":\"urn:dataset:tck-transfer\"}")
	if [ "$code" != "201" ]; then
		echo "seeding agreement $1 failed with HTTP $code" >&2
		exit 1
	fi
}

seed_agreement urn:uuid:tck-tp-01-01
seed_agreement urn:uuid:tck-tp-01-02
seed_agreement urn:uuid:tck-tp-01-03
seed_agreement urn:uuid:tck-tp-01-04
seed_agreement urn:uuid:tck-tp-01-05
seed_agreement urn:uuid:tck-tp-default
seed_agreement urn:uuid:tck-tp-nostart
```

Seven ids, matching Step 3's `config.properties` and Step 3b's `transfer_policies`. Seeding must fail the run loudly: a silent seeding failure would look exactly like a protocol bug, since a connector that rejects an unknown agreement and a connector that was never given one produce the same `400`.

- [ ] **Step 3: Pin the agreement id for every test in `config.properties`**

**The key derivation is settled**, not a guess: `InstanceInjector.getKey` builds it as the test method's name uppercased, an underscore, then the field name uppercased — no camelCase splitting, so `agreementId` becomes `AGREEMENTID` as one token. It is scoped to the individual `@Test` method, with no class-level or global fallback. See `docs/superpowers/specs/2026-08-16-transfer-process-tck-wire-contract.md` §1.1.

That means **sixteen keys, one per test method**, not one. `tp_01_01..05`, `tp_02_01..05`, `tp_03_01..06`.

**A missing or blank key fails silently.** `@ConfigParam.required()` defaults to `false` and the test class constructor pre-seeds `agreementId` with a random UUID, so injection just returns and the random value wins. A green test is not evidence the key was read.

Each id selects the agreement *and* the autonomous behavior, because the agreement id is the only test-varying field on the wire. Use distinct ids so `dsbox.yaml`'s `transfer_policies` can key on them:

```properties
# TP: the agreement each test cites. The id is also the selector for this
# connector's autonomous behavior (see dsbox.yaml's transfer_policies) —
# there is no other test-varying field on the wire.
#
# The key is <TESTMETHOD>_<FIELDNAME> uppercased, per test method, with no
# class-level fallback: a missing key silently falls back to a random UUID.
TP_01_01_AGREEMENTID=urn:uuid:tck-tp-01-01
TP_01_02_AGREEMENTID=urn:uuid:tck-tp-01-02
TP_01_03_AGREEMENTID=urn:uuid:tck-tp-01-03
TP_01_04_AGREEMENTID=urn:uuid:tck-tp-01-04
TP_01_05_AGREEMENTID=urn:uuid:tck-tp-01-05
TP_02_01_AGREEMENTID=urn:uuid:tck-tp-default
TP_02_02_AGREEMENTID=urn:uuid:tck-tp-default
TP_02_03_AGREEMENTID=urn:uuid:tck-tp-default
TP_02_04_AGREEMENTID=urn:uuid:tck-tp-default
TP_02_05_AGREEMENTID=urn:uuid:tck-tp-nostart
TP_03_01_AGREEMENTID=urn:uuid:tck-tp-nostart
TP_03_02_AGREEMENTID=urn:uuid:tck-tp-nostart
TP_03_03_AGREEMENTID=urn:uuid:tck-tp-default
TP_03_04_AGREEMENTID=urn:uuid:tck-tp-default
TP_03_05_AGREEMENTID=urn:uuid:tck-tp-default
TP_03_06_AGREEMENTID=urn:uuid:tck-tp-default
```

`TP_02_04` is included even though that test is `@Disabled` upstream — the key costs nothing and stops the list from looking like it has a hole.

**On the first run, prove the keys were read.** Set one to a sentinel and confirm that exact string arrives as `agreementId` on the wire, in the connector's log. This is the single highest-value check of the first run: everything else can look right while every test silently uses a random UUID.

- [ ] **Step 3b: Configure the autonomous behavior in `dsbox.yaml`**

```yaml
# TP: what this connector does on its own after accepting a transfer request,
# keyed by the agreement it was requested under. The TCK sends one message and
# then only polls, so the agreement id is the only thing that can select a
# behavior — see the design spec's "Autonomous provider behavior".
#
# An agreement with no entry here gets [STARTED]. An explicit empty sequence
# means accept and stay in REQUESTED, which four tests assert and which an
# unconditional start would break.
transfer_policies:
  - agreement_id: urn:uuid:tck-tp-01-01
    sequence: [STARTED, TERMINATED]
  - agreement_id: urn:uuid:tck-tp-01-02
    sequence: [STARTED, COMPLETED]
  - agreement_id: urn:uuid:tck-tp-01-03
    sequence: [STARTED, SUSPENDED, TERMINATED]
  - agreement_id: urn:uuid:tck-tp-01-04
    sequence: [STARTED, SUSPENDED, STARTED, COMPLETED]
  - agreement_id: urn:uuid:tck-tp-01-05
    sequence: [TERMINATED]
  - agreement_id: urn:uuid:tck-tp-nostart
    sequence: []
```

`urn:uuid:tck-tp-default` needs no entry — the default is `[STARTED]`, which is what `TP:02-01..04` and `TP:03-03..06` expect.

Every id above must also be seeded in Step 2, including `tck-tp-default` and `tck-tp-nostart`.

- [ ] **Step 4: Add the dataset the agreements cover**

In `test/tck/dsbox.yaml`'s `datasets:` list:

```yaml
  # TP: the dataset every imported transfer agreement covers.
  - id: urn:dataset:tck-transfer
```

- [ ] **Step 5: Add `TP` to the gate**

In `cmd/tckgate/main.go`:

```go
var expected = map[string]int{"MET": 1, "CAT": 3, "CN": 15, "CN_C": 16, "TP": 15}
```

15, not 16: `TP:02-04` carries JUnit's `@Disabled` upstream, so the suite produces 15 results. That is measured, not assumed — see the requirements survey.

- [ ] **Step 6: Run the full Go suite**

Run: `go test ./... && go test -race -count=2 ./internal/dsp/ && gofmt -l . && go vet ./...`
Expected: all pass, `gofmt` silent.

- [ ] **Step 7: Run the real TCK**

Run: `make tck`
Expected: `34 required tests passed` becomes `49 required tests passed`, with `TP` at 15/15 and `CN`, `CN_C`, `CAT`, `MET` unchanged.

Run it in the foreground. It takes several minutes. Expect more than one iteration: the callback path in `startTransfer` and the `config.properties` key spelling are both unconfirmed until this run says so.

- [ ] **Step 8: Update `README.md`**

Add the `TP` row to the status table, move the pass rate to 49 of 65, and state that the transfer process is implemented in the provider role, control plane only, with no data transfer yet.

- [ ] **Step 9: Add `DECISIONS.md` §25**

Record, each with its rationale and accepted trade-off: the agreement-validation decision; why agreements are a table with two writers rather than a config list, and the rule that boundary sets (runtime state in the database, static declarations in configuration); why the management API's auth was stood up here and the guard rail that `POST /agreements` records an agreement and nothing more; why an absent `mgmt_token` refuses rather than allows; the separate transfer state constants; and that Phase A emits no `dataAddress`.

- [ ] **Step 10: Commit**

```bash
git add test/tck/config.properties test/tck/run.sh test/tck/dsbox.yaml cmd/tckgate/main.go README.md DECISIONS.md
git commit -m "feat: add the transfer process provider role to the compliance gate"
```

---

## Notes for the executor

- Task 9 is the only task whose success is decided by something outside this repository. Everything before it can be green and still wrong; treat the first `make tck` as the real review. Task 8 exists because that turned out to be true before the run rather than during it — the TCK was read instead, and it falsified the plan's scope.
- Two values in this plan are explicitly unconfirmed and will be settled by that run: the `config.properties` key derivation (Step 3) and the callback path `startTransfer` pushes to (Task 7). Both fail loudly rather than silently.
- Phase B — the HTTP-PULL data plane — is a separate plan, written after this one is merged. Do not start it here, and do not emit a `dataAddress` in anticipation of it.
