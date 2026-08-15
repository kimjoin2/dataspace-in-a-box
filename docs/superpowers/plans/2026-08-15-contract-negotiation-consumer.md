# Contract Negotiation (Consumer Role) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the DSP contract negotiation protocol in the consumer role — one trigger endpoint, two new inbound push endpoints, a role-dispatch layer on three existing endpoints, a SQLite-backed consumer-side state machine, and a per-dataset policy configuration — so the `CN_C` TCK suite (16 tests, all 16 required) joins the compliance gate.

**Architecture:** A second SQLite table (`consumer_negotiations`, in the existing `internal/store` package) persists this connector's own negotiations as consumer, keyed by its own generated consumer pid. `internal/dsp/negotiation.go` gains the consumer-role message builders, a per-dataset policy resolver, and the structural-guard functions (all pure, unit-testable without HTTP). `internal/dsp/negotiation_client.go` is a new file holding every outbound HTTP call this connector makes as consumer. `internal/dsp/negotiation_handler.go` gains three new inbound handlers and a role-dispatch layer on the three endpoints the provider role already registered — the TCK's own consumer-role client uses the identical path shapes, and Go's `http.ServeMux` rejects a second registration of an identical pattern. `internal/dsp/callback.go`'s `pushCallback` gains a `bool` return, the one shared-code change this milestone makes, needed for the one reaction (verify-on-agreement) whose local state must not advance unless the provider actually acknowledged it.

**Tech Stack:** Go 1.26 standard library. No new dependency — everything reuses `modernc.org/sqlite` (already in `go.mod`) and this project's own `net/http`-based HTTP client pattern.

**Spec:** `docs/superpowers/specs/2026-08-15-contract-negotiation-consumer-design.md` (commit `5691178` — the design after two rounds of independent cross-check against the TCK's actual source; read its "Revision note" first if anything here seems to contradict an earlier version of the spec you may have seen). Read the referenced spec section if a task mentions one by name and the summary here is not enough.

## Global Constraints

- English only: code, comments, commit messages, docs (`CLAUDE.md`).
- No plugin system, no SPI, no inheritance-based extension points (`CLAUDE.md`).
- Standard library first. This plan adds no new dependency — ask before adding one.
- No organizational affiliation anywhere in the repo. Copyright is b7g.
- Storage: one SQLite file under `data_dir`, WAL mode. In-memory SQLite (`:memory:`) is for tests only, never a runtime path (`DECISIONS.md` §8, `CLAUDE.md`).
- JSON-LD fixed compact form, validated by direct field checks, not a schema library (`DECISIONS.md` §22.5).
- Every node this project emits carries `@type`.
- `{id}` in a provider-pushed path (`.../offers`, `.../agreement`, and the consumer branch of `.../events`/`.../termination`/`GET`) is this connector's **own generated consumer pid** — the mirror of the provider role, where `{id}` is the provider's own pid. Never confuse the two.
- `POST /negotiations/initiate` is a TCK-only trigger hook, not a DSP protocol message: plain JSON, no `@context`/`@type`, response body ignored by the caller (only the status code is asserted).
- A structural-guard rejection is always `400`, never `404` — the TCK's own assertion helper (`HttpFunctions.postJson`) throws immediately on `404` even where an error is otherwise expected, which would fail every one of the six `03-xx` tests a `404` guard was meant to satisfy.
- Every handler that makes an outbound HTTP call as part of handling an inbound request must dispatch that call with `go`, never inline — `net/http` does not put a response on the wire until the handler function returns (`DECISIONS.md` §23.8). This applies to `handleInitiate` exactly as much as it did to the provider role's handlers.
- `pushCallback`'s existing retry schedule (`callbackRetryBackoffs`) is what survives the TCK's callback/termination-listener registration-order race (`DECISIONS.md` §23.7) — any outbound call this connector makes *after* the initial request must go through it, not a bespoke one-shot send.
- Go test command: `go test ./...`. TCK command: `make tck`.

---

### Task 1: Config — `consumer_policies`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `config.example.yaml`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `config.ConsumerPolicy{DatasetID, OnOffer, OnAgreement, OnIdle string}`; `config.Config.ConsumerPolicies []ConsumerPolicy`. Task 4's `resolvePolicy` reads both directly.

`consumer_policies` is optional — an empty or absent list is valid, matching every dataset's default behavior (accept, verify, wait). Each present entry is validated at load time: `dataset_id` is required, and `on_offer`/`on_agreement`/`on_idle` are each either empty (deferred to the default, decided later by `resolvePolicy` in Task 4 — not here) or one of a fixed enum.

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestConsumerPoliciesEmptyWhenAbsent(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.ConsumerPolicies) != 0 {
		t.Errorf("ConsumerPolicies = %v, want empty when the key is absent", cfg.ConsumerPolicies)
	}
}

func TestConsumerPolicyParses(t *testing.T) {
	cfg, err := Load(minimal("consumer_policies:\n  - dataset_id: urn:dataset:a\n    on_offer: passive\n    on_agreement: reject\n    on_idle: abandon\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.ConsumerPolicies) != 1 {
		t.Fatalf("ConsumerPolicies = %v, want one entry", cfg.ConsumerPolicies)
	}
	p := cfg.ConsumerPolicies[0]
	if p.DatasetID != "urn:dataset:a" || p.OnOffer != "passive" || p.OnAgreement != "reject" || p.OnIdle != "abandon" {
		t.Errorf("ConsumerPolicies[0] = %+v, want the parsed fixture", p)
	}
}

func TestConsumerPolicyOmittedFieldsStayEmpty(t *testing.T) {
	cfg, err := Load(minimal("consumer_policies:\n  - dataset_id: urn:dataset:a\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.ConsumerPolicies[0]
	if p.OnOffer != "" || p.OnAgreement != "" || p.OnIdle != "" {
		t.Errorf("ConsumerPolicies[0] = %+v, want every unset field to stay empty — defaulting happens where the policy is resolved, not at load time", p)
	}
}

func TestConsumerPolicyRequiresDatasetID(t *testing.T) {
	_, err := Load(minimal("consumer_policies:\n  - on_offer: accept\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a consumer_policies entry with no dataset_id")
	}
}

func TestConsumerPolicyRejectsInvalidOnOffer(t *testing.T) {
	_, err := Load(minimal("consumer_policies:\n  - dataset_id: urn:dataset:a\n    on_offer: bogus\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for an invalid on_offer value")
	}
}

func TestConsumerPolicyRejectsInvalidOnAgreement(t *testing.T) {
	_, err := Load(minimal("consumer_policies:\n  - dataset_id: urn:dataset:a\n    on_agreement: bogus\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for an invalid on_agreement value")
	}
}

func TestConsumerPolicyRejectsInvalidOnIdle(t *testing.T) {
	_, err := Load(minimal("consumer_policies:\n  - dataset_id: urn:dataset:a\n    on_idle: bogus\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for an invalid on_idle value")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/...`
Expected: compile failure — `cfg.ConsumerPolicies` does not exist yet.

- [ ] **Step 3: Implement `ConsumerPolicy` and `Config.ConsumerPolicies`**

In `internal/config/config.go`, add the field to `Config`, right after `DataDir`:

```go
	// ConsumerPolicies configures this connector's autonomous behavior when
	// it is negotiating as consumer, keyed by the dataset_id this connector
	// itself requests via POST /negotiations/initiate. A dataset_id with no
	// matching entry gets every field's default (accept, verify, wait) —
	// see the design spec's "Why a policy configuration, not a content
	// rule".
	ConsumerPolicies []ConsumerPolicy `yaml:"consumer_policies"`
```

Add the new type, after `Dataset`:

```go
// ConsumerPolicy selects this connector's autonomous reaction to what a
// provider sends back for a given requested dataset, when this connector is
// negotiating as consumer. Every field left empty here is filled in with
// its default where the policy is looked up (dsp.resolvePolicy), not here —
// this type only validates that a *present* value is one of the values that
// field supports.
type ConsumerPolicy struct {
	DatasetID string `yaml:"dataset_id"`

	// OnOffer: "accept" (default), "passive", "reject", or "counter".
	OnOffer string `yaml:"on_offer"`

	// OnAgreement: "verify" (default) or "reject".
	OnAgreement string `yaml:"on_agreement"`

	// OnIdle: "wait" (default) or "abandon".
	OnIdle string `yaml:"on_idle"`
}
```

Add the validation, in `validate()`, after the existing dataset loop and before the `DataDir` check:

```go
	validOnOffer := map[string]bool{"accept": true, "passive": true, "reject": true, "counter": true}
	validOnAgreement := map[string]bool{"verify": true, "reject": true}
	validOnIdle := map[string]bool{"wait": true, "abandon": true}
	for i, p := range c.ConsumerPolicies {
		if p.DatasetID == "" {
			return fmt.Errorf("consumer_policies[%d]: dataset_id is required", i)
		}
		if p.OnOffer != "" && !validOnOffer[p.OnOffer] {
			return fmt.Errorf("consumer_policies[%d]: on_offer %q is not one of accept, passive, reject, counter", i, p.OnOffer)
		}
		if p.OnAgreement != "" && !validOnAgreement[p.OnAgreement] {
			return fmt.Errorf("consumer_policies[%d]: on_agreement %q is not one of verify, reject", i, p.OnAgreement)
		}
		if p.OnIdle != "" && !validOnIdle[p.OnIdle] {
			return fmt.Errorf("consumer_policies[%d]: on_idle %q is not one of wait, abandon", i, p.OnIdle)
		}
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/...`
Expected: PASS, all tests.

- [ ] **Step 5: Add a commented example to `config.example.yaml`**

Add, after the existing `datasets` block:

```yaml
# Optional. Configures this connector's autonomous behavior when it is
# negotiating as consumer (POST /negotiations/initiate), keyed by the
# dataset_id it itself requests. An unmatched dataset_id gets every field's
# default: accept an offer, verify an agreement, wait indefinitely if
# nothing arrives. Uncomment to try a non-default policy:
# consumer_policies:
#   - dataset_id: urn:dataset:example
#     on_offer: accept        # accept (default) | passive | reject | counter
#     on_agreement: verify    # verify (default) | reject
#     on_idle: wait           # wait (default) | abandon
```

- [ ] **Step 6: Run the tests to verify they still pass**

Run: `go test ./internal/config/...`
Expected: PASS (the comment does not change parsing; this step exists to catch a stray syntax mistake in the YAML comment block).

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go config.example.yaml
git commit -m "feat: add consumer_policies configuration"
```

---

### Task 2: Storage — `consumer_negotiations`

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing from other tasks — this package still imports neither `config` nor `dsp`.
- Produces:
  - `store.ConsumerNegotiation{ConsumerPID, ProviderPID, ProviderBaseURL, State, DatasetID, OfferID string; CreatedAt, UpdatedAt time.Time}`
  - `(*Store).CreateConsumer(n ConsumerNegotiation) error`
  - `(*Store).GetConsumer(consumerPID string) (ConsumerNegotiation, bool, error)`
  - `(*Store).SetConsumerState(consumerPID, from, to string, updatedAt time.Time) error`
  - `(*Store).SetConsumerProviderPID(consumerPID, providerPID string) error`

  Task 4, 5, and 6 all use every symbol above. `SetConsumerState` returns `ErrStateChanged`/`ErrNotFound` exactly like `SetState` (same sentinel errors, already defined — not redeclared).

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/store_test.go`:

```go
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

	if err := s.SetConsumerProviderPID(n.ConsumerPID, "urn:uuid:provider-1"); err != nil {
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
}

func TestSetConsumerProviderPIDMissingIsError(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SetConsumerProviderPID("does-not-exist", "urn:uuid:provider-1"); err == nil {
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/...`
Expected: compile failure — `ConsumerNegotiation` and every `*Consumer*` method do not exist yet.

- [ ] **Step 3: Implement the consumer table and its CRUD functions**

In `internal/store/store.go`, add the type after `Negotiation`:

```go
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
```

Add the schema, after `schema`:

```go
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
```

In `Open`, add a second `db.Exec` call right after the existing `db.Exec(schema)` block and before `migrate(db)`:

```go
	if _, err := db.Exec(consumerSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create consumer schema in %s: %w", path, err)
	}
```

Add the CRUD functions, after `explainNoUpdate`:

```go
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
// section.
func (s *Store) SetConsumerProviderPID(consumerPID, providerPID string) error {
	res, err := s.db.Exec(`UPDATE consumer_negotiations SET provider_pid = ? WHERE consumer_pid = ?`,
		providerPID, consumerPID)
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/store/...`
Expected: PASS, all tests (existing `negotiations`-table tests included, unaffected).

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: add consumer_negotiations storage"
```

---

### Task 3: `pushCallback` reports success

**Files:**
- Modify: `internal/dsp/callback.go`
- Modify: `internal/dsp/callback_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `pushCallback(url string, v any) bool` (was `func(url string, v any)` — every existing call site is a bare statement discarding the result, so this compiles unchanged for them). Task 5's `sendVerification` is the one caller that reads the return value.

- [ ] **Step 1: Write the failing tests**

Append to `internal/dsp/callback_test.go`:

```go
func TestPushCallbackReturnsTrueOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !pushCallback(srv.URL, map[string]string{"hello": "world"}) {
		t.Error("pushCallback = false, want true for a server that accepts the push")
	}
}

func TestPushCallbackReturnsFalseAfterExhaustingRetries(t *testing.T) {
	orig := callbackRetryBackoffs
	callbackRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { callbackRetryBackoffs = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if pushCallback(srv.URL, map[string]string{"hello": "world"}) {
		t.Error("pushCallback = true, want false once every attempt is rejected")
	}
}
```

Check the existing imports at the top of `internal/dsp/callback_test.go` already include `net/http`, `net/http/httptest`, and `time` — the file already exercises `httptest.NewServer` and `callbackRetryBackoffs` in its existing tests, so no new imports should be needed.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run TestPushCallback`
Expected: FAIL — `pushCallback`'s current return type is nothing, so `!pushCallback(...)` and `if pushCallback(...)` are compile errors.

- [ ] **Step 3: Change `pushCallback`'s signature**

In `internal/dsp/callback.go`, replace the function:

```go
// pushCallback sends v as a JSON POST to url, retrying on failure per
// callbackRetryBackoffs, and reports whether it ultimately succeeded. Most
// callers discard the return value: the provider role's own pushes, and
// most of this connector's consumer-role reactions, write their state
// unconditionally once the push is dispatched (DECISIONS.md section 23.12).
// The one exception is the consumer role's verify-on-agreement reaction,
// which must not advance to VERIFIED unless this returns true — see the
// design spec's "03-06 verification-ack rule".
//
// pushCallback itself does not filter url — callers must run it through
// validateCallbackURL first (negotiation_handler.go's pushAndStore does).
func pushCallback(url string, v any) bool {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal callback push", "url", url, "error", err)
		return false
	}
	for attempt := 0; ; attempt++ {
		if attemptPush(url, body, attempt) {
			return true
		}
		if attempt >= len(callbackRetryBackoffs) {
			return false
		}
		time.Sleep(callbackRetryBackoffs[attempt])
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/...`
Expected: PASS, all tests — including every existing test in `negotiation_handler_test.go` that exercises `pushAndStore` (which calls `pushCallback` as a bare statement, unaffected by the new return value).

- [ ] **Step 5: Commit**

```bash
git add internal/dsp/callback.go internal/dsp/callback_test.go
git commit -m "feat: pushCallback reports whether the push ultimately succeeded"
```

---

### Task 4: Consumer-role message documents, policy resolution, structural guards

**Files:**
- Modify: `internal/dsp/negotiation.go`
- Modify: `internal/dsp/negotiation_test.go`

**Interfaces:**
- Consumes: `store.ConsumerNegotiation` (Task 2); `config.Config`, `config.ConsumerPolicy` (Task 1); existing package-level constants (`StateRequested`, `StateOffered`, `StateAccepted`, `StateAgreed`, `StateVerified`, `StateFinalized`, `StateTerminated`, `ContextURL`, `ContractRequestMessageType`, `ContractNegotiationEventMessageType`, `ContractAgreementVerificationMessageType`, `ContractNegotiationTerminationMessageType`, `ContractNegotiationType`, `eventTypeAccepted`, `terminationCode`) and existing types (`RequestMessage`, `OfferRef`, `EventMessage`, `TerminationMessage`, `TerminationReason`, `NegotiationStateDocument`) — all already defined, same package, no import needed.
- Produces (all consumed by Task 5 and Task 6):
  - `CounterRequestMessage{Context []string; Type string; ProviderPID, ConsumerPID string; Offer OfferRef}`
  - `VerificationMessage{Context []string; ID, Type, ProviderPID, ConsumerPID string}`
  - `ContractAgreementVerificationMessageType` already exists (provider role declared it) — reused, not redeclared.
  - `buildConsumerRequestMessage(consumerPID, datasetID, offerID, callbackAddress string) RequestMessage`
  - `buildCounterRequestMessage(n store.ConsumerNegotiation) CounterRequestMessage`
  - `buildAcceptedEventMessage(n store.ConsumerNegotiation) EventMessage`
  - `buildVerificationMessage(n store.ConsumerNegotiation) VerificationMessage`
  - `buildConsumerTerminationMessage(n store.ConsumerNegotiation) TerminationMessage`
  - `buildConsumerNegotiationStateDocument(n store.ConsumerNegotiation) NegotiationStateDocument`
  - `resolvePolicy(cfg config.Config, datasetID string) config.ConsumerPolicy` (every field always filled in — never returns an empty `OnOffer`/`OnAgreement`/`OnIdle`)
  - `offerLegalFrom(state string) bool`
  - `agreementLegalFrom(state string) bool`
  - `finalizedEventLegalFrom(state string) bool`

This task has no HTTP in it — every function is pure or touches only `store.NewUUID`/`time.Now` via the existing `newMessageID` helper, matching the provider milestone's `negotiation.go`.

- [ ] **Step 1: Write the failing tests for the message builders**

Append to `internal/dsp/negotiation_test.go`:

```go
func testConsumerNegotiation() store.ConsumerNegotiation {
	return store.ConsumerNegotiation{
		ConsumerPID:     "urn:uuid:consumer-1",
		ProviderPID:     "urn:uuid:provider-1",
		ProviderBaseURL: "https://provider.example.org",
		State:           StateOffered,
		DatasetID:       "urn:dataset:a",
		OfferID:         "urn:dataset:a#offer",
	}
}

func TestBuildConsumerRequestMessage(t *testing.T) {
	msg := buildConsumerRequestMessage("urn:uuid:consumer-1", "urn:dataset:a", "urn:dataset:a#offer", "https://connector.example.org/2025-1")
	if msg.Type != ContractRequestMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractRequestMessageType)
	}
	if msg.ConsumerPID != "urn:uuid:consumer-1" {
		t.Errorf("ConsumerPID = %q, want urn:uuid:consumer-1", msg.ConsumerPID)
	}
	if msg.Offer.ID != "urn:dataset:a#offer" || msg.Offer.Target != "urn:dataset:a" {
		t.Errorf("Offer = %+v, want the exact ids passed in, not regenerated", msg.Offer)
	}
	if msg.CallbackAddress != "https://connector.example.org/2025-1" {
		t.Errorf("CallbackAddress = %q, want the address passed in", msg.CallbackAddress)
	}
}

func TestBuildCounterRequestMessage(t *testing.T) {
	n := testConsumerNegotiation()
	msg := buildCounterRequestMessage(n)
	if msg.Type != ContractRequestMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractRequestMessageType)
	}
	if msg.ProviderPID != n.ProviderPID {
		t.Errorf("ProviderPID = %q, want %q — a counter-request must carry it or the TCK's mock provider treats it as a duplicate initial request", msg.ProviderPID, n.ProviderPID)
	}
	if msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("ConsumerPID = %q, want %q", msg.ConsumerPID, n.ConsumerPID)
	}
	if msg.Offer.ID != n.OfferID || msg.Offer.Target != n.DatasetID {
		t.Errorf("Offer = %+v, want the negotiation's original ask repeated", msg.Offer)
	}
}

func TestBuildAcceptedEventMessage(t *testing.T) {
	n := testConsumerNegotiation()
	msg := buildAcceptedEventMessage(n)
	if msg.Type != ContractNegotiationEventMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractNegotiationEventMessageType)
	}
	if msg.EventType != eventTypeAccepted {
		t.Errorf("EventType = %q, want %q", msg.EventType, eventTypeAccepted)
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
}

func TestBuildVerificationMessage(t *testing.T) {
	n := testConsumerNegotiation()
	msg := buildVerificationMessage(n)
	if msg.Type != ContractAgreementVerificationMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractAgreementVerificationMessageType)
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
	if msg.ID == "" {
		t.Error("ID is empty, want a generated message id")
	}
}

func TestBuildConsumerTerminationMessage(t *testing.T) {
	n := testConsumerNegotiation()
	msg := buildConsumerTerminationMessage(n)
	if msg.Type != ContractNegotiationTerminationMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractNegotiationTerminationMessageType)
	}
	if msg.Code != terminationCode {
		t.Errorf("Code = %q, want %q", msg.Code, terminationCode)
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
}

func TestBuildConsumerNegotiationStateDocument(t *testing.T) {
	n := testConsumerNegotiation()
	doc := buildConsumerNegotiationStateDocument(n)
	if doc.Type != ContractNegotiationType {
		t.Errorf("Type = %q, want %q", doc.Type, ContractNegotiationType)
	}
	if doc.ProviderPID != n.ProviderPID || doc.ConsumerPID != n.ConsumerPID || doc.State != n.State {
		t.Errorf("doc = %+v, want it to carry n's identifiers and state", doc)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run TestBuild`
Expected: compile failure — none of the new types or `build*` functions exist yet.

- [ ] **Step 3: Implement the message types and builders**

Append to `internal/dsp/negotiation.go`:

```go
// CounterRequestMessage is the body of the consumer role's counter-request
// — POST {provider}/negotiations/{providerPid}/request — sent when this
// connector's on_offer:counter policy decides to repeat its original ask
// rather than accept a provider's counter-offer. Unlike RequestMessage (the
// very first request, which has no providerPid yet), this carries the
// providerPid the synchronous response to that first request returned:
// without it, the TCK's own reference provider treats the message as a
// duplicate initial request rather than a counter, and the test that
// expects it hangs. See the design spec's "The 01-02 counter-request
// shape".
type CounterRequestMessage struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
	Offer       OfferRef `json:"offer"`
}

// VerificationMessage is the ContractAgreementVerificationMessage this
// connector sends once its on_agreement:verify policy decides to verify a
// received agreement.
type VerificationMessage struct {
	Context     []string `json:"@context"`
	ID          string   `json:"@id"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

// buildConsumerRequestMessage is the initial ContractRequestMessage this
// connector sends as consumer. datasetID and offerID are echoed verbatim
// from what POST /negotiations/initiate received — never regenerated. The
// TCK's own mock provider recovers datasetID from offerID via its own
// "offer"+datasetID convention, a different shape from this connector's
// own provider-role offerIDSuffix convention; conflating the two would
// break the request the TCK's mock provider needs to parse.
func buildConsumerRequestMessage(consumerPID, datasetID, offerID, callbackAddress string) RequestMessage {
	return RequestMessage{
		Context:         []string{ContextURL},
		Type:            ContractRequestMessageType,
		ConsumerPID:     consumerPID,
		Offer:           OfferRef{ID: offerID, Target: datasetID},
		CallbackAddress: callbackAddress,
	}
}

func buildCounterRequestMessage(n store.ConsumerNegotiation) CounterRequestMessage {
	return CounterRequestMessage{
		Context:     []string{ContextURL},
		Type:        ContractRequestMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		Offer:       OfferRef{ID: n.OfferID, Target: n.DatasetID},
	}
}

func buildAcceptedEventMessage(n store.ConsumerNegotiation) EventMessage {
	return EventMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationEventMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		EventType:   eventTypeAccepted,
	}
}

func buildVerificationMessage(n store.ConsumerNegotiation) VerificationMessage {
	return VerificationMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractAgreementVerificationMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
	}
}

func buildConsumerTerminationMessage(n store.ConsumerNegotiation) TerminationMessage {
	return TerminationMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationTerminationMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		Code:        terminationCode,
	}
}

func buildConsumerNegotiationStateDocument(n store.ConsumerNegotiation) NegotiationStateDocument {
	return NegotiationStateDocument{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		State:       n.State,
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/... -run TestBuild`
Expected: PASS, all seven.

- [ ] **Step 5: Write the failing tests for policy resolution and structural guards**

Append to `internal/dsp/negotiation_test.go`:

```go
func TestResolvePolicy_UnmatchedDatasetGetsEveryDefault(t *testing.T) {
	cfg := config.Config{}
	p := resolvePolicy(cfg, "urn:dataset:unmatched")
	if p.OnOffer != "accept" || p.OnAgreement != "verify" || p.OnIdle != "wait" {
		t.Errorf("resolvePolicy = %+v, want accept/verify/wait for an unmatched dataset", p)
	}
}

func TestResolvePolicy_UsesTheMatchingEntry(t *testing.T) {
	cfg := config.Config{ConsumerPolicies: []config.ConsumerPolicy{
		{DatasetID: "urn:dataset:a", OnOffer: "passive"},
		{DatasetID: "urn:dataset:b", OnOffer: "reject"},
	}}
	p := resolvePolicy(cfg, "urn:dataset:b")
	if p.OnOffer != "reject" {
		t.Errorf("resolvePolicy(...,\"urn:dataset:b\") = %+v, want on_offer reject", p)
	}
}

func TestResolvePolicy_UnsetFieldsOnAMatchedEntryStillDefault(t *testing.T) {
	cfg := config.Config{ConsumerPolicies: []config.ConsumerPolicy{
		{DatasetID: "urn:dataset:a", OnOffer: "passive"},
	}}
	p := resolvePolicy(cfg, "urn:dataset:a")
	if p.OnOffer != "passive" {
		t.Errorf("OnOffer = %q, want passive (the configured value)", p.OnOffer)
	}
	if p.OnAgreement != "verify" || p.OnIdle != "wait" {
		t.Errorf("OnAgreement/OnIdle = %q/%q, want the defaults for fields the entry left unset", p.OnAgreement, p.OnIdle)
	}
}

func TestOfferLegalFrom(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{StateRequested, true},
		{StateOffered, false},
		{StateAccepted, false},
		{StateAgreed, false},
		{StateVerified, false},
	}
	for _, c := range cases {
		if got := offerLegalFrom(c.state); got != c.want {
			t.Errorf("offerLegalFrom(%q) = %v, want %v", c.state, got, c.want)
		}
	}
}

func TestAgreementLegalFrom(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{StateRequested, true},
		{StateAccepted, true},
		{StateOffered, false},
		{StateAgreed, false},
		{StateVerified, false},
	}
	for _, c := range cases {
		if got := agreementLegalFrom(c.state); got != c.want {
			t.Errorf("agreementLegalFrom(%q) = %v, want %v", c.state, got, c.want)
		}
	}
}

func TestFinalizedEventLegalFrom(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{StateVerified, true},
		{StateRequested, false},
		{StateOffered, false},
		{StateAccepted, false},
		{StateAgreed, false},
	}
	for _, c := range cases {
		if got := finalizedEventLegalFrom(c.state); got != c.want {
			t.Errorf("finalizedEventLegalFrom(%q) = %v, want %v", c.state, got, c.want)
		}
	}
}
```

- [ ] **Step 6: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run 'TestResolvePolicy|TestOfferLegalFrom|TestAgreementLegalFrom|TestFinalizedEventLegalFrom'`
Expected: compile failure — `resolvePolicy`, `offerLegalFrom`, `agreementLegalFrom`, `finalizedEventLegalFrom` do not exist yet.

- [ ] **Step 7: Implement policy resolution and the structural guards**

Append to `internal/dsp/negotiation.go`:

```go
// resolvePolicy returns the consumer policy for datasetID, with every field
// defaulted to this connector's sane, real-world behavior: accept an offer,
// verify an agreement, wait if nothing arrives. See the design spec's "Why
// a policy configuration, not a content rule".
func resolvePolicy(cfg config.Config, datasetID string) config.ConsumerPolicy {
	for _, p := range cfg.ConsumerPolicies {
		if p.DatasetID == datasetID {
			return normalizedPolicy(p)
		}
	}
	return normalizedPolicy(config.ConsumerPolicy{DatasetID: datasetID})
}

func normalizedPolicy(p config.ConsumerPolicy) config.ConsumerPolicy {
	if p.OnOffer == "" {
		p.OnOffer = "accept"
	}
	if p.OnAgreement == "" {
		p.OnAgreement = "verify"
	}
	if p.OnIdle == "" {
		p.OnIdle = "wait"
	}
	return p
}

// offerLegalFrom reports whether an incoming ContractOfferMessage is a
// legal transition from state — the consumer-role mirror of the provider's
// own CN:03 structural checks. Only REQUESTED accepts an offer; CN_C:03-05
// confirms a second offer is illegal once ACCEPTED, and there is no test
// that ever sends a second offer while already OFFERED either.
func offerLegalFrom(state string) bool {
	return state == StateRequested
}

// agreementLegalFrom reports whether an incoming ContractAgreementMessage
// is a legal transition from state. Legal from REQUESTED (the
// direct-agreement path with no offer ever pushed, CN_C:01-04) or ACCEPTED
// (the normal path after this connector accepted an offer); illegal from
// OFFERED (CN_C:03-02).
func agreementLegalFrom(state string) bool {
	return state == StateRequested || state == StateAccepted
}

// finalizedEventLegalFrom reports whether an incoming FINALIZED event is a
// legal transition from state. Legal only from VERIFIED — CN_C:03-01,
// 03-03, 03-04, and 03-06 each require rejection from a different other
// state.
func finalizedEventLegalFrom(state string) bool {
	return state == StateVerified
}
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/dsp/...`
Expected: PASS, all tests in the package (existing provider-role tests included, unaffected).

- [ ] **Step 9: Commit**

```bash
git add internal/dsp/negotiation.go internal/dsp/negotiation_test.go
git commit -m "feat: add consumer-role message documents, policy resolution, and structural guards"
```

---

### Task 5: Outbound consumer client — `negotiation_client.go`

**Files:**
- Create: `internal/dsp/negotiation_client.go`
- Create: `internal/dsp/negotiation_client_test.go`

**Interfaces:**
- Consumes: `store.ConsumerNegotiation` (Task 2); `RequestMessage`, `CounterRequestMessage`, `NegotiationStateDocument`, `buildCounterRequestMessage`, `buildAcceptedEventMessage`, `buildVerificationMessage`, `buildConsumerTerminationMessage` (Task 4); `pushCallback(url string, v any) bool`, `callbackHTTPClient *http.Client` (Task 3, `callback.go` — already package-level, no import needed, same package).
- Produces (all consumed by Task 6):
  - `sendInitialRequest(providerBaseURL string, msg RequestMessage) (providerPID string, err error)`
  - `sendCounterRequest(n store.ConsumerNegotiation) bool`
  - `sendAcceptedEvent(n store.ConsumerNegotiation) bool`
  - `sendVerification(n store.ConsumerNegotiation) bool`
  - `sendConsumerTermination(n store.ConsumerNegotiation) bool`

`sendInitialRequest` is the one function in this file that is not retried — see the design spec's "The initial request: goroutine dispatch, no retry" section. Every other function here goes through `pushCallback`, which is what makes `sendConsumerTermination` (used by `on_idle: abandon`) survive the TCK's registration-order race for `CN_C:02-02` — see `DECISIONS.md` §23.7.

- [ ] **Step 1: Write the failing tests**

Create `internal/dsp/negotiation_client_test.go`:

```go
package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

func TestSendInitialRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg RequestMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Fatalf("provider: decode request: %v", err)
		}
		if msg.Offer.ID != "urn:dataset:a#offer" || msg.Offer.Target != "urn:dataset:a" {
			t.Errorf("provider received Offer = %+v, want the exact ids sent", msg.Offer)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(NegotiationStateDocument{ProviderPID: "urn:uuid:provider-1"})
	}))
	defer srv.Close()

	msg := buildConsumerRequestMessage("urn:uuid:consumer-1", "urn:dataset:a", "urn:dataset:a#offer", "https://connector.example.org/2025-1")
	got, err := sendInitialRequest(srv.URL, msg)
	if err != nil {
		t.Fatalf("sendInitialRequest: %v", err)
	}
	if got != "urn:uuid:provider-1" {
		t.Errorf("providerPID = %q, want urn:uuid:provider-1", got)
	}
}

func TestSendInitialRequest_ProviderRejectsSynchronously(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	msg := buildConsumerRequestMessage("urn:uuid:consumer-1", "urn:dataset:a", "urn:dataset:a#offer", "https://connector.example.org/2025-1")
	if _, err := sendInitialRequest(srv.URL, msg); err == nil {
		t.Error("sendInitialRequest: expected an error when the provider rejects the request")
	}
}

func testConsumerNegotiationAt(url string) store.ConsumerNegotiation {
	n := testConsumerNegotiation()
	n.ProviderBaseURL = url
	return n
}

func TestSendCounterRequest_PostsProviderPIDAndOffer(t *testing.T) {
	var gotPath string
	var gotBody CounterRequestMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testConsumerNegotiationAt(srv.URL)
	if !sendCounterRequest(n) {
		t.Fatal("sendCounterRequest = false, want true")
	}
	wantPath := "/negotiations/" + n.ProviderPID + "/request"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotBody.ProviderPID != n.ProviderPID || gotBody.Offer.ID != n.OfferID {
		t.Errorf("body = %+v, want providerPid and the original offer", gotBody)
	}
}

func TestSendAcceptedEvent_PostsEventType(t *testing.T) {
	var gotBody EventMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testConsumerNegotiationAt(srv.URL)
	if !sendAcceptedEvent(n) {
		t.Fatal("sendAcceptedEvent = false, want true")
	}
	if gotBody.EventType != eventTypeAccepted {
		t.Errorf("EventType = %q, want %q", gotBody.EventType, eventTypeAccepted)
	}
}

func TestSendVerification_ReturnsTrueOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testConsumerNegotiationAt(srv.URL)
	if !sendVerification(n) {
		t.Error("sendVerification = false, want true")
	}
}

func TestSendVerification_ReturnsFalseWhenNeverAcknowledged(t *testing.T) {
	orig := callbackRetryBackoffs
	callbackRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { callbackRetryBackoffs = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	n := testConsumerNegotiationAt(srv.URL)
	if sendVerification(n) {
		t.Error("sendVerification = true, want false — mirrors CN_C:03-06, where no handler is ever registered")
	}
}

func TestSendConsumerTermination_PostsToProviderPIDPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := testConsumerNegotiationAt(srv.URL)
	if !sendConsumerTermination(n) {
		t.Fatal("sendConsumerTermination = false, want true")
	}
	wantPath := "/negotiations/" + n.ProviderPID + "/termination"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run TestSend`
Expected: compile failure — the package `internal/dsp` has no `negotiation_client.go` yet, so none of the `send*` functions exist.

- [ ] **Step 3: Implement `negotiation_client.go`**

Create `internal/dsp/negotiation_client.go`:

```go
// Package dsp: this file holds every outbound HTTP call this connector
// makes as consumer. Everything in negotiation_handler.go answers an
// inbound request; this file initiates one — a different responsibility,
// kept in its own file per this project's design-for-isolation convention.
package dsp

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// Path templates for calls this connector makes as consumer, formatted
// against a provider's base URL (store.ConsumerNegotiation.ProviderBaseURL)
// and, where present, the provider's own pid.
const (
	consumerRequestPath        = "/negotiations/request"
	consumerCounterRequestPath = "/negotiations/%s/request"
	consumerEventsPath         = "/negotiations/%s/events"
	consumerVerificationPath   = "/negotiations/%s/agreement/verification"
	consumerTerminationPath    = "/negotiations/%s/termination"
)

// sendInitialRequest POSTs the initial ContractRequestMessage to
// providerBaseURL and returns the providerPid from the provider's
// synchronous ContractNegotiation response. Not retried, unlike every other
// function in this file: the provider mock is already live by the time
// this is called, so the registration race pushCallback's retry schedule
// exists for does not apply here — see the design spec's "The initial
// request: goroutine dispatch, no retry" section.
func sendInitialRequest(providerBaseURL string, msg RequestMessage) (string, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal initial request: %w", err)
	}
	resp, err := callbackHTTPClient.Post(providerBaseURL+consumerRequestPath, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("post initial request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("post initial request: provider responded %d", resp.StatusCode)
	}
	var doc NegotiationStateDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("decode initial request response: %w", err)
	}
	if doc.ProviderPID == "" {
		return "", fmt.Errorf("initial request response carries no providerPid")
	}
	return doc.ProviderPID, nil
}

// sendCounterRequest POSTs a counter-request that repeats n's original ask.
// See CounterRequestMessage's doc comment for why this cannot be
// buildConsumerRequestMessage's shape resent.
func sendCounterRequest(n store.ConsumerNegotiation) bool {
	url := n.ProviderBaseURL + fmt.Sprintf(consumerCounterRequestPath, n.ProviderPID)
	return pushCallback(url, buildCounterRequestMessage(n))
}

// sendAcceptedEvent POSTs an ACCEPTED event for the offer n received.
func sendAcceptedEvent(n store.ConsumerNegotiation) bool {
	url := n.ProviderBaseURL + fmt.Sprintf(consumerEventsPath, n.ProviderPID)
	return pushCallback(url, buildAcceptedEventMessage(n))
}

// sendVerification POSTs verification for the agreement n received. Its
// return value is load-bearing, unlike every other function in this file:
// the design spec's "03-06 verification-ack rule" requires this
// connector's local state to advance to VERIFIED only when this returns
// true.
func sendVerification(n store.ConsumerNegotiation) bool {
	url := n.ProviderBaseURL + fmt.Sprintf(consumerVerificationPath, n.ProviderPID)
	return pushCallback(url, buildVerificationMessage(n))
}

// sendConsumerTermination POSTs a termination for n.
func sendConsumerTermination(n store.ConsumerNegotiation) bool {
	url := n.ProviderBaseURL + fmt.Sprintf(consumerTerminationPath, n.ProviderPID)
	return pushCallback(url, buildConsumerTerminationMessage(n))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/... -run TestSend`
Expected: PASS, all seven.

- [ ] **Step 5: Run the full package test suite**

Run: `go test ./internal/dsp/...`
Expected: PASS, everything (provider-role tests unaffected).

- [ ] **Step 6: Commit**

```bash
git add internal/dsp/negotiation_client.go internal/dsp/negotiation_client_test.go
git commit -m "feat: add the outbound consumer-role negotiation client"
```

---

### Task 6: Inbound handlers, role-dispatch, and router wiring

**Files:**
- Modify: `internal/dsp/negotiation_handler.go`
- Modify: `internal/dsp/negotiation_handler_test.go`
- Modify: `internal/dsp/router.go`

**Interfaces:**
- Consumes: everything Task 1 through Task 5 produced.
- Produces: `negotiationHandler.handleInitiate`, `.handleOffers`, `.handleAgreement` (new); `.handleEvent`, `.handleTermination`, `.handleGetNegotiation` (role-dispatch added, same method names — `router.go`'s existing registrations for these three do not change). Nothing outside this package consumes any of these directly; `router.go`'s `NewRouter` is the only caller, updated in this same task.

This is the largest task. It has four independent pieces — read them in order, but each has its own test-then-implement cycle so a mistake in one is caught before the next builds on it.

- [ ] **Step 1: Write the failing tests for `handleInitiate`**

Append to `internal/dsp/negotiation_handler_test.go`. First, add this helper near the top of the file if one shaped like it does not already exist (check for `waitForState` — this adds its consumer-table counterpart):

```go
// waitForConsumerState polls st for consumerPID to reach want, the
// consumer-table counterpart of waitForState — needed for the same reason:
// every reaction this milestone adds runs in a goroutine.
func waitForConsumerState(t *testing.T, st *store.Store, consumerPID, want string) store.ConsumerNegotiation {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		n, ok, err := st.GetConsumer(consumerPID)
		if err != nil {
			t.Fatalf("GetConsumer: %v", err)
		}
		if ok && n.State == want {
			return n
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("consumer negotiation %s did not reach state %s in time", consumerPID, want)
	return store.ConsumerNegotiation{}
}

func TestHandleInitiate_Success(t *testing.T) {
	var gotOffer OfferRef
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg RequestMessage
		json.NewDecoder(r.Body).Decode(&msg)
		gotOffer = msg.Offer
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(NegotiationStateDocument{ProviderPID: "urn:uuid:provider-1"})
	}))
	defer provider.Close()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	restore := validateOutgoingCallback
	validateOutgoingCallback = func(string) error { return nil }
	defer func() { validateOutgoingCallback = restore }()

	cfg := config.Config{PublicURL: "https://connector.example.org"}
	h := negotiationHandler{cfg: cfg, store: st}
	srv := httptest.NewServer(http.HandlerFunc(h.handleInitiate))
	defer srv.Close()

	body := `{"providerId":"urn:participant:tck","offerId":"urn:dataset:a#offer","datasetId":"urn:dataset:a","connectorAddress":"` + provider.URL + `"}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The handler generates its own consumer pid; find the one row this test
	// created by listing what state REQUESTED holds. There is exactly one
	// negotiation in this store, so this is a targeted poll, not a scan.
	var found store.ConsumerNegotiation
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		// gotOffer is set only after the provider receives the request, which
		// only happens after the row exists — safe to read here without a race
		// once gotOffer.ID is non-empty.
		if gotOffer.ID != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if gotOffer.ID != "urn:dataset:a#offer" || gotOffer.Target != "urn:dataset:a" {
		t.Fatalf("provider received Offer = %+v, want the exact initiate-call ids", gotOffer)
	}
	_ = found
}

func TestHandleInitiate_MissingFieldIs400(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	h := negotiationHandler{cfg: config.Config{}, store: st}
	srv := httptest.NewServer(http.HandlerFunc(h.handleInitiate))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"providerId":"urn:participant:tck"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleInitiate_RespondsBeforeTheOutboundRequestCompletes(t *testing.T) {
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(NegotiationStateDocument{ProviderPID: "urn:uuid:provider-1"})
	}))
	defer provider.Close()
	defer close(release)

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	restore := validateOutgoingCallback
	validateOutgoingCallback = func(string) error { return nil }
	defer func() { validateOutgoingCallback = restore }()

	h := negotiationHandler{cfg: config.Config{PublicURL: "https://connector.example.org"}, store: st}
	srv := httptest.NewServer(http.HandlerFunc(h.handleInitiate))
	defer srv.Close()

	body := `{"providerId":"urn:participant:tck","offerId":"urn:dataset:a#offer","datasetId":"urn:dataset:a","connectorAddress":"` + provider.URL + `"}`
	start := time.Now()
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("handleInitiate took %v to respond, want it to return before the provider (which is still blocked) responds", elapsed)
	}
}

func TestHandleInitiate_OnIdleAbandon_TerminatesThroughTheRetryingPath(t *testing.T) {
	var attempts int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/negotiations/request" {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(NegotiationStateDocument{ProviderPID: "urn:uuid:provider-1"})
			return
		}
		// Every other path is the termination attempt. Reject the first —
		// the registration-order race this policy exists to survive — and
		// accept the second, proving the send goes through pushCallback's
		// retrying path rather than a bespoke one-shot send.
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	origBackoffs := callbackRetryBackoffs
	callbackRetryBackoffs = []time.Duration{time.Millisecond}
	defer func() { callbackRetryBackoffs = origBackoffs }()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	restore := validateOutgoingCallback
	validateOutgoingCallback = func(string) error { return nil }
	defer func() { validateOutgoingCallback = restore }()

	cfg := config.Config{
		PublicURL:        "https://connector.example.org",
		ConsumerPolicies: []config.ConsumerPolicy{{DatasetID: "urn:dataset:a", OnIdle: "abandon"}},
	}
	h := negotiationHandler{cfg: cfg, store: st}
	srv := httptest.NewServer(http.HandlerFunc(h.handleInitiate))
	defer srv.Close()

	body := `{"providerId":"urn:participant:tck","offerId":"urn:dataset:a#offer","datasetId":"urn:dataset:a","connectorAddress":"` + provider.URL + `"}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	deadline := time.Now().Add(time.Second)
	for attempts < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if attempts < 2 {
		t.Fatalf("provider received %d termination attempt(s), want at least 2 — the first rejection must not be the end of it", attempts)
	}
}
```

Check the top of `internal/dsp/negotiation_handler_test.go` already imports `"strings"`, `"encoding/json"`, `"net/http"`, `"net/http/httptest"`, `"testing"`, `"time"`, and `"github.com/kimjoin2/dataspace-in-a-box/internal/config"` / `.../internal/store` — the file already has provider-role tests using every one of these; add `"strings"` if it is not already there.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run TestHandleInitiate`
Expected: compile failure — `handleInitiate` does not exist yet.

- [ ] **Step 3: Implement `handleInitiate`**

In `internal/dsp/negotiation_handler.go`, add the request body type and handler after `handleContractRequest`:

```go
// initiateRequestBody is the plain-JSON (not JSON-LD) body the TCK's own
// negotiation.initiate.url hook POSTs to trigger this connector to start a
// negotiation as consumer. Not a DSP protocol message — see the design
// spec's "The initiate endpoint is not a management feature".
type initiateRequestBody struct {
	ProviderID       string `json:"providerId"`
	OfferID          string `json:"offerId"`
	DatasetID        string `json:"datasetId"`
	ConnectorAddress string `json:"connectorAddress"`
}

// handleInitiate serves POST /negotiations/initiate. It responds 200 as
// soon as the negotiation is recorded and dispatches the actual outbound
// ContractRequestMessage in a goroutine — the same requirement as every
// other handler in this file, even though this endpoint is not itself a
// DSP message: net/http still will not put the 200 on the wire until this
// handler returns.
func (h negotiationHandler) handleInitiate(w http.ResponseWriter, r *http.Request) {
	var body initiateRequestBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes))
	if err := dec.Decode(&body); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "the request body is not a JSON object")
		return
	}
	if body.ProviderID == "" || body.OfferID == "" || body.DatasetID == "" || body.ConnectorAddress == "" {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"providerId, offerId, datasetId, and connectorAddress are all required")
		return
	}
	if err := validateOutgoingCallback(body.ConnectorAddress); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "connectorAddress: "+err.Error())
		return
	}

	consumerPID, err := store.NewUUID()
	if err != nil {
		slog.Error("generate consumer pid", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	now := time.Now()
	n := store.ConsumerNegotiation{
		ConsumerPID:     consumerPID,
		ProviderBaseURL: body.ConnectorAddress,
		State:           StateRequested,
		DatasetID:       body.DatasetID,
		OfferID:         body.OfferID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := h.store.CreateConsumer(n); err != nil {
		slog.Error("create consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	go h.startNegotiation(n)
}

// startNegotiation sends the initial ContractRequestMessage and, once the
// provider's synchronous response reveals its providerPid, applies this
// connector's on_idle policy: wait (do nothing further) or abandon (an
// immediate termination through sendConsumerTermination's retrying path —
// see the design spec's "on_idle: abandon" policy row for why this must
// not be a bespoke one-shot send).
func (h negotiationHandler) startNegotiation(n store.ConsumerNegotiation) {
	msg := buildConsumerRequestMessage(n.ConsumerPID, n.DatasetID, n.OfferID, h.cfg.PublicURL+VersionPath)
	providerPID, err := sendInitialRequest(n.ProviderBaseURL, msg)
	if err != nil {
		slog.Error("send initial request", "consumer_pid", n.ConsumerPID, "error", err)
		return
	}
	if err := h.store.SetConsumerProviderPID(n.ConsumerPID, providerPID); err != nil {
		slog.Error("record provider pid", "consumer_pid", n.ConsumerPID, "error", err)
		return
	}
	n.ProviderPID = providerPID

	policy := resolvePolicy(h.cfg, n.DatasetID)
	if policy.OnIdle == "abandon" {
		sendConsumerTermination(n)
		if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateTerminated, time.Now()); err != nil {
			slog.Warn("drop stale consumer negotiation state update", "consumer_pid", n.ConsumerPID, "error", err)
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dsp/... -run TestHandleInitiate`
Expected: PASS, all four.

- [ ] **Step 5: Write the failing tests for `handleOffers` and `handleAgreement`**

Append to `internal/dsp/negotiation_handler_test.go`:

```go
func newConsumerHandlerWithNegotiation(t *testing.T, cfg config.Config, n store.ConsumerNegotiation) (negotiationHandler, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateConsumer(n); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}
	return negotiationHandler{cfg: cfg, store: st}, st
}

func TestHandleOffers_Accept_SendsAcceptedEvent(t *testing.T) {
	var gotEventType string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg EventMessage
		json.NewDecoder(r.Body).Decode(&msg)
		gotEventType = msg.EventType
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	n := testConsumerNegotiationAt(provider.URL)
	n.State = StateRequested
	h, st := newConsumerHandlerWithNegotiation(t, config.Config{}, n)
	srv := httptest.NewServer(http.HandlerFunc(h.handleOffers))
	defer srv.Close()

	req := httptest.NewRequest("POST", "/x", strings.NewReader(offerMessageJSON(n)))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleOffers(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	waitForConsumerState(t, st, n.ConsumerPID, StateAccepted)
	if gotEventType != eventTypeAccepted {
		t.Errorf("provider received EventType = %q, want ACCEPTED", gotEventType)
	}
}

func TestHandleOffers_Passive_TakesNoAction(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("provider received a request, want none — this negotiation's policy is passive")
	}))
	defer provider.Close()

	n := testConsumerNegotiationAt(provider.URL)
	n.State = StateRequested
	cfg := config.Config{ConsumerPolicies: []config.ConsumerPolicy{{DatasetID: n.DatasetID, OnOffer: "passive"}}}
	h, st := newConsumerHandlerWithNegotiation(t, cfg, n)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(offerMessageJSON(n)))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleOffers(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	got := waitForConsumerState(t, st, n.ConsumerPID, StateOffered)
	if got.State != StateOffered {
		t.Errorf("State = %q, want it to durably hold OFFERED", got.State)
	}
}

func TestHandleOffers_Reject_SendsTermination(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	n := testConsumerNegotiationAt(provider.URL)
	n.State = StateRequested
	cfg := config.Config{ConsumerPolicies: []config.ConsumerPolicy{{DatasetID: n.DatasetID, OnOffer: "reject"}}}
	h, st := newConsumerHandlerWithNegotiation(t, cfg, n)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(offerMessageJSON(n)))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleOffers(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	waitForConsumerState(t, st, n.ConsumerPID, StateTerminated)
}

func TestHandleOffers_Counter_SendsCounterRequest(t *testing.T) {
	var gotProviderPID string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg CounterRequestMessage
		json.NewDecoder(r.Body).Decode(&msg)
		gotProviderPID = msg.ProviderPID
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	n := testConsumerNegotiationAt(provider.URL)
	n.State = StateRequested
	cfg := config.Config{ConsumerPolicies: []config.ConsumerPolicy{{DatasetID: n.DatasetID, OnOffer: "counter"}}}
	h, st := newConsumerHandlerWithNegotiation(t, cfg, n)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(offerMessageJSON(n)))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleOffers(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	deadline := time.Now().Add(time.Second)
	for gotProviderPID == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if gotProviderPID != n.ProviderPID {
		t.Errorf("provider received ProviderPID = %q, want %q", gotProviderPID, n.ProviderPID)
	}
	got := waitForConsumerState(t, st, n.ConsumerPID, StateOffered)
	if got.State != StateOffered {
		t.Error("counter reaction must not change local state — the negotiation stays OFFERED")
	}
}

func TestHandleOffers_IllegalFromAcceptedIs400(t *testing.T) {
	n := testConsumerNegotiation()
	n.State = StateAccepted
	h, _ := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(offerMessageJSON(n)))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleOffers(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleAgreement_Verify_SendsVerification(t *testing.T) {
	var gotVerification bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVerification = true
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	n := testConsumerNegotiationAt(provider.URL)
	n.State = StateAccepted
	h, st := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(agreementMessageJSON(n)))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleAgreement(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	waitForConsumerState(t, st, n.ConsumerPID, StateVerified)
	if !gotVerification {
		t.Error("provider never received a verification POST")
	}
}

func TestHandleAgreement_Verify_NeverAcknowledged_StaysAgreed(t *testing.T) {
	orig := callbackRetryBackoffs
	callbackRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { callbackRetryBackoffs = orig }()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer provider.Close()

	n := testConsumerNegotiationAt(provider.URL)
	n.State = StateAccepted
	h, st := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(agreementMessageJSON(n)))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleAgreement(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	time.Sleep(50 * time.Millisecond)
	got := waitForConsumerState(t, st, n.ConsumerPID, StateAgreed)
	if got.State != StateAgreed {
		t.Errorf("State = %q, want AGREED — verification was never acknowledged, so this connector must not report VERIFIED", got.State)
	}
}

func TestHandleAgreement_Reject_SendsTermination(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	n := testConsumerNegotiationAt(provider.URL)
	n.State = StateRequested
	cfg := config.Config{ConsumerPolicies: []config.ConsumerPolicy{{DatasetID: n.DatasetID, OnAgreement: "reject"}}}
	h, st := newConsumerHandlerWithNegotiation(t, cfg, n)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(agreementMessageJSON(n)))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleAgreement(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	waitForConsumerState(t, st, n.ConsumerPID, StateTerminated)
}

func TestHandleAgreement_IllegalFromOfferedIs400(t *testing.T) {
	n := testConsumerNegotiation()
	n.State = StateOffered
	h, _ := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(agreementMessageJSON(n)))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleAgreement(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// offerMessageJSON and agreementMessageJSON build minimal, valid message
// bodies for the handler tests above — the fields these handlers actually
// read, nothing more (matching this project's own direct-field-check
// convention).
func offerMessageJSON(n store.ConsumerNegotiation) string {
	return `{"@context":["` + ContextURL + `"],"@type":"` + ContractOfferMessageType + `",` +
		`"providerPid":"` + n.ProviderPID + `","consumerPid":"` + n.ConsumerPID + `",` +
		`"offer":{"@id":"` + n.OfferID + `","target":"` + n.DatasetID + `","permission":[]}}`
}

func agreementMessageJSON(n store.ConsumerNegotiation) string {
	return `{"@context":["` + ContextURL + `"],"@type":"` + ContractAgreementMessageType + `",` +
		`"providerPid":"` + n.ProviderPID + `","consumerPid":"` + n.ConsumerPID + `",` +
		`"agreement":{"@id":"` + n.ProviderPID + `","target":"` + n.DatasetID + `","permission":[],"assigner":"x","assignee":"y","timestamp":"2026-08-15T00:00:00Z"}}`
}
```

- [ ] **Step 6: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run 'TestHandleOffers|TestHandleAgreement'`
Expected: compile failure — `handleOffers`, `handleAgreement`, and `reactToOffer`/`reactToAgreement` do not exist yet.

- [ ] **Step 7: Implement `handleOffers` and `handleAgreement`**

Add to `internal/dsp/negotiation_handler.go`, after `startNegotiation`:

```go
// handleOffers serves POST /negotiations/{id}/offers, a ContractOfferMessage
// pushed by the provider. {id} is this connector's own consumer pid.
func (h negotiationHandler) handleOffers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, ok, err := h.store.GetConsumer(id)
	if err != nil {
		slog.Error("get consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !ok {
		writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+id)
		return
	}

	var msg OfferMessage
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, msg.Context, msg.Type, ContractOfferMessageType) {
		return
	}
	if !offerLegalFrom(n.State) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"an offer is only valid from REQUESTED, negotiation is "+n.State)
		return
	}

	if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateOffered, time.Now()); err != nil {
		writeStateUpdateError(w, n.ConsumerPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateOffered
	go h.reactToOffer(n)
}

// reactToOffer applies this connector's on_offer policy once an offer has
// been durably recorded as OFFERED.
func (h negotiationHandler) reactToOffer(n store.ConsumerNegotiation) {
	policy := resolvePolicy(h.cfg, n.DatasetID)
	switch policy.OnOffer {
	case "accept":
		sendAcceptedEvent(n)
		if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateAccepted, time.Now()); err != nil {
			slog.Warn("drop stale consumer negotiation state update", "consumer_pid", n.ConsumerPID, "error", err)
		}
	case "reject":
		sendConsumerTermination(n)
		if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateTerminated, time.Now()); err != nil {
			slog.Warn("drop stale consumer negotiation state update", "consumer_pid", n.ConsumerPID, "error", err)
		}
	case "counter":
		sendCounterRequest(n)
	case "passive":
		// Take no action; the negotiation durably holds OFFERED.
	}
}

// handleAgreement serves POST /negotiations/{id}/agreement, a
// ContractAgreementMessage pushed by the provider.
func (h negotiationHandler) handleAgreement(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, ok, err := h.store.GetConsumer(id)
	if err != nil {
		slog.Error("get consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !ok {
		writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+id)
		return
	}

	var msg AgreementMessage
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, msg.Context, msg.Type, ContractAgreementMessageType) {
		return
	}
	if !agreementLegalFrom(n.State) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"an agreement is only valid from REQUESTED or ACCEPTED, negotiation is "+n.State)
		return
	}

	if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateAgreed, time.Now()); err != nil {
		writeStateUpdateError(w, n.ConsumerPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateAgreed
	go h.reactToAgreement(n)
}

// reactToAgreement applies this connector's on_agreement policy. The verify
// branch's state write is gated on sendVerification's return value — see
// the design spec's "03-06 verification-ack rule": this connector must not
// report VERIFIED unless the provider actually acknowledged it.
func (h negotiationHandler) reactToAgreement(n store.ConsumerNegotiation) {
	policy := resolvePolicy(h.cfg, n.DatasetID)
	switch policy.OnAgreement {
	case "verify":
		if !sendVerification(n) {
			return
		}
		if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateVerified, time.Now()); err != nil {
			slog.Warn("drop stale consumer negotiation state update", "consumer_pid", n.ConsumerPID, "error", err)
		}
	case "reject":
		sendConsumerTermination(n)
		if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateTerminated, time.Now()); err != nil {
			slog.Warn("drop stale consumer negotiation state update", "consumer_pid", n.ConsumerPID, "error", err)
		}
	}
}
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/dsp/... -run 'TestHandleOffers|TestHandleAgreement'`
Expected: PASS, all nine.

- [ ] **Step 9: Write the failing tests for role-dispatch on `events`, `termination`, and `GET`**

Append to `internal/dsp/negotiation_handler_test.go`:

```go
func TestHandleEvent_DispatchesToConsumerBranchForAFinalizedEvent(t *testing.T) {
	n := testConsumerNegotiation()
	n.State = StateVerified
	h, st := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

	body := `{"@context":["` + ContextURL + `"],"@type":"` + ContractNegotiationEventMessageType + `","eventType":"FINALIZED"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleEvent(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	waitForConsumerState(t, st, n.ConsumerPID, StateFinalized)
}

func TestHandleEvent_ConsumerBranch_IllegalFromOfferedIs400(t *testing.T) {
	n := testConsumerNegotiation()
	n.State = StateOffered
	h, _ := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

	body := `{"@context":["` + ContextURL + `"],"@type":"` + ContractNegotiationEventMessageType + `","eventType":"FINALIZED"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleEvent(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleEvent_UnknownIDIs404(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	h := negotiationHandler{cfg: config.Config{}, store: st}

	body := `{"@context":["` + ContextURL + `"],"@type":"` + ContractNegotiationEventMessageType + `","eventType":"FINALIZED"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req.SetPathValue("id", "does-not-exist")
	w := httptest.NewRecorder()
	h.handleEvent(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleTermination_DispatchesToConsumerBranch(t *testing.T) {
	n := testConsumerNegotiation()
	n.State = StateOffered
	h, st := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

	body := `{"@context":["` + ContextURL + `"],"@type":"` + ContractNegotiationTerminationMessageType + `","code":"1"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleTermination(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	waitForConsumerState(t, st, n.ConsumerPID, StateTerminated)
}

func TestHandleGetNegotiation_DispatchesToConsumerBranch(t *testing.T) {
	n := testConsumerNegotiation()
	n.State = StateAgreed
	h, _ := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", n.ConsumerPID)
	w := httptest.NewRecorder()
	h.handleGetNegotiation(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var doc NegotiationStateDocument
	if err := json.NewDecoder(w.Body).Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.State != StateAgreed || doc.ConsumerPID != n.ConsumerPID {
		t.Errorf("doc = %+v, want the consumer negotiation's own state and pid", doc)
	}
}

func TestHandleGetNegotiation_ProviderBranchStillWorks(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	now := time.Now()
	pn := store.Negotiation{
		ProviderPID: "urn:uuid:provider-only", ConsumerPID: "urn:uuid:consumer-only",
		State: StateRequested, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Create(pn); err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := negotiationHandler{cfg: config.Config{}, store: st}

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("id", pn.ProviderPID)
	w := httptest.NewRecorder()
	h.handleGetNegotiation(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a provider-role negotiation must still resolve after this milestone adds a second table", w.Code)
	}
}
```

- [ ] **Step 10: Run the tests to verify they fail**

Run: `go test ./internal/dsp/... -run 'TestHandleEvent|TestHandleTermination|TestHandleGetNegotiation'`
Expected: `TestHandleEvent_DispatchesToConsumerBranchForAFinalizedEvent`, `TestHandleEvent_ConsumerBranch_IllegalFromOfferedIs400`, `TestHandleTermination_DispatchesToConsumerBranch`, and `TestHandleGetNegotiation_DispatchesToConsumerBranch` FAIL (the handlers do not look at the consumer table yet — a consumer-only id resolves as not-found today). `TestHandleEvent_UnknownIDIs404`, `TestHandleGetNegotiation_ProviderBranchStillWorks`, and every pre-existing provider-role test should already PASS.

- [ ] **Step 11: Add role-dispatch to `handleEvent`, `handleTermination`, and `handleGetNegotiation`**

In `internal/dsp/negotiation_handler.go`, replace `handleEvent` with a dispatcher plus two branches:

```go
// handleEvent serves POST /negotiations/{id}/events. {id} names either a
// provider-role negotiation (the consumer's ACCEPTED event) or a
// consumer-role one (the provider's FINALIZED event) — the two suites
// register the identical path shape, which Go's ServeMux would reject as a
// duplicate pattern if this milestone tried to register a second route for
// it, so this dispatches on which table {id} is actually found in.
func (h negotiationHandler) handleEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if n, ok, err := h.store.Get(id); err != nil {
		slog.Error("get negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		h.handleProviderAcceptedEvent(w, r, n)
		return
	}
	if cn, ok, err := h.store.GetConsumer(id); err != nil {
		slog.Error("get consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		h.handleConsumerFinalizedEvent(w, r, cn)
		return
	}
	writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+id)
}

// handleProviderAcceptedEvent is handleEvent's provider-role branch —
// unchanged behavior from before this milestone.
func (h negotiationHandler) handleProviderAcceptedEvent(w http.ResponseWriter, r *http.Request, n store.Negotiation) {
	var msg struct {
		Context   []string `json:"@context"`
		Type      string   `json:"@type"`
		EventType string   `json:"eventType"`
	}
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, msg.Context, msg.Type, ContractNegotiationEventMessageType) {
		return
	}
	if msg.EventType != eventTypeAccepted {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "eventType must be ACCEPTED")
		return
	}
	if n.State != StateOffered {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"accept is only valid from OFFERED, negotiation is "+n.State)
		return
	}

	now := time.Now()
	if err := h.store.SetState(n.ProviderPID, n.State, StateAccepted, now); err != nil {
		writeStateUpdateError(w, n.ProviderPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	n.State = StateAccepted
	outcome := decideAccept(h.cfg, n.DatasetID, now)
	go h.dispatch(n, outcome)
}

// handleConsumerFinalizedEvent is handleEvent's consumer-role branch: the
// FINALIZED event a provider sends once this connector's verification is
// acknowledged. Legal only from VERIFIED — see finalizedEventLegalFrom.
func (h negotiationHandler) handleConsumerFinalizedEvent(w http.ResponseWriter, r *http.Request, n store.ConsumerNegotiation) {
	var msg struct {
		Context   []string `json:"@context"`
		Type      string   `json:"@type"`
		EventType string   `json:"eventType"`
	}
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, msg.Context, msg.Type, ContractNegotiationEventMessageType) {
		return
	}
	if msg.EventType != eventTypeFinalized {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest, "eventType must be FINALIZED")
		return
	}
	if !finalizedEventLegalFrom(n.State) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"finalized is only valid from VERIFIED, negotiation is "+n.State)
		return
	}

	if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateFinalized, time.Now()); err != nil {
		writeStateUpdateError(w, n.ConsumerPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
```

Replace `handleTermination` the same way:

```go
// handleTermination serves POST /negotiations/{id}/termination, from
// either party and, after this milestone, for either role — see
// handleEvent's doc comment for why this dispatches rather than registering
// a second route.
func (h negotiationHandler) handleTermination(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if n, ok, err := h.store.Get(id); err != nil {
		slog.Error("get negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		h.handleProviderTermination(w, r, n)
		return
	}
	if cn, ok, err := h.store.GetConsumer(id); err != nil {
		slog.Error("get consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		h.handleConsumerTermination(w, r, cn)
		return
	}
	writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+id)
}

// handleProviderTermination is handleTermination's provider-role branch —
// unchanged behavior from before this milestone. It is rejected from
// FINALIZED (CN:03-01) and from an already TERMINATED negotiation.
func (h negotiationHandler) handleProviderTermination(w http.ResponseWriter, r *http.Request, n store.Negotiation) {
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	var msg envelope
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, msg.Context, msg.Type, ContractNegotiationTerminationMessageType) {
		return
	}
	if n.State == StateFinalized || n.State == StateTerminated {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"negotiation cannot be terminated from "+n.State)
		return
	}

	if err := h.store.SetState(n.ProviderPID, n.State, StateTerminated, time.Now()); err != nil {
		writeStateUpdateError(w, n.ProviderPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleConsumerTermination is handleTermination's consumer-role branch.
func (h negotiationHandler) handleConsumerTermination(w http.ResponseWriter, r *http.Request, n store.ConsumerNegotiation) {
	body := http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes)
	var msg envelope
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"the request body is not a JSON object in the DSP compact form")
		return
	}
	if !checkEnvelope(w, msg.Context, msg.Type, ContractNegotiationTerminationMessageType) {
		return
	}
	if n.State == StateFinalized || n.State == StateTerminated {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"negotiation cannot be terminated from "+n.State)
		return
	}

	if err := h.store.SetConsumerState(n.ConsumerPID, n.State, StateTerminated, time.Now()); err != nil {
		writeStateUpdateError(w, n.ConsumerPID, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
```

Replace `handleGetNegotiation`:

```go
// handleGetNegotiation serves GET /negotiations/{id}, for either role — see
// handleEvent's doc comment for why this dispatches rather than registering
// a second route.
func (h negotiationHandler) handleGetNegotiation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if n, ok, err := h.store.Get(id); err != nil {
		slog.Error("get negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		writeJSON(w, http.StatusOK, buildNegotiationStateDocument(n))
		return
	}
	if cn, ok, err := h.store.GetConsumer(id); err != nil {
		slog.Error("get consumer negotiation", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if ok {
		writeJSON(w, http.StatusOK, buildConsumerNegotiationStateDocument(cn))
		return
	}
	writeError(w, ContractNegotiationErrorType, http.StatusNotFound, "no negotiation with id "+id)
}
```

`lookup` (used by `handleReRequest` and `handleVerification`, both still provider-only — the DSP protocol has no consumer-role equivalent for either path) is unchanged.

Also update the package doc comment on `negotiationHandler` — replace:

```go
// negotiationHandler serves the contract negotiation protocol, provider
// role only.
```

with:

```go
// negotiationHandler serves the contract negotiation protocol, both roles.
// Which role a given {id} belongs to is resolved by which store table it is
// found in — see handleEvent's doc comment.
```

- [ ] **Step 12: Run the tests to verify they pass**

Run: `go test ./internal/dsp/...`
Expected: PASS, everything — all new tests, and every pre-existing provider-role test unaffected.

- [ ] **Step 13: Wire the three new routes into the router**

In `internal/dsp/router.go`, add these three lines after the existing `GET .../negotiations/{id}` registration:

```go
	mux.HandleFunc("POST "+VersionPath+"/negotiations/initiate", neg.handleInitiate)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/offers", neg.handleOffers)
	mux.HandleFunc("POST "+VersionPath+"/negotiations/{id}/agreement", neg.handleAgreement)
```

- [ ] **Step 14: Run the full test suite and start the server once, manually**

Run: `go test ./...`
Expected: PASS, everything.

Run: `go build ./... && echo build ok`
Expected: `build ok` — this also confirms `router.go` compiles with three new, non-conflicting route registrations (a real pattern conflict is a panic at `mux.HandleFunc` call time, not a compile error, so the build passing is necessary but the next check is what actually proves no conflict).

Run a smoke test that the router itself constructs without panicking (a pattern conflict panics inside `http.NewServeMux`, at `NewRouter` call time, not at compile time):

```bash
cat > /tmp/router_smoke_test.go << 'EOF'
package main

import (
	"fmt"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/dsp"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

func main() {
	st, err := store.Open(":memory:")
	if err != nil {
		panic(err)
	}
	defer st.Close()
	dsp.NewRouter(config.Config{PublicURL: "https://example.org", ParticipantID: "urn:participant:x"}, st)
	fmt.Println("router constructed without panicking")
}
EOF
go run /tmp/router_smoke_test.go
rm /tmp/router_smoke_test.go
```

Expected: `router constructed without panicking`.

- [ ] **Step 15: Commit**

```bash
git add internal/dsp/negotiation_handler.go internal/dsp/negotiation_handler_test.go internal/dsp/router.go
git commit -m "feat: add consumer-role negotiation handlers and role-dispatch routing"
```

---

### Task 7: TCK harness, gate, and documentation

**Files:**
- Modify: `test/tck/config.properties`
- Modify: `test/tck/dsbox.yaml`
- Modify: `cmd/tckgate/main.go`
- Modify: `README.md`
- Modify: `DECISIONS.md`

**Interfaces:**
- Consumes: everything Task 1 through Task 6 produced. This task's own deliverable is `make tck` passing with `CN_C` gated.

- [ ] **Step 1: Add the six fixture datasets to `test/tck/dsbox.yaml`**

Add a `consumer_policies` section (this file's `data_dir`, `participant_id`, and `datasets` blocks already exist from the provider milestone — do not touch them):

```yaml
consumer_policies:
  - dataset_id: urn:dataset:cn-c-counter-offer
    on_offer: counter
  - dataset_id: urn:dataset:cn-c-reject-offer
    on_offer: reject
  - dataset_id: urn:dataset:cn-c-abandon
    on_idle: abandon
  - dataset_id: urn:dataset:cn-c-reject-agreement
    on_agreement: reject
  - dataset_id: urn:dataset:cn-c-passive-offer
    on_offer: passive
```

- [ ] **Step 2: Add the dataset-id overrides to `test/tck/config.properties`**

Append, with a comment explaining the mapping (no `OFFERID` keys — the consumer test suite's `AbstractContractNegotiationConsumerTest` declares only a `datasetId` `@ConfigParam`, confirmed from the TCK's own source):

```properties
# CN_C: nine of sixteen tests use the default policy (accept/verify/wait)
# and need no override here — a random, unmatched datasetId already gets
# it. The other seven select one of five non-default fixtures configured
# in dsbox.yaml's consumer_policies. There is no CN_C_xx_yy_OFFERID key —
# unlike the provider suite, the consumer suite's @ConfigParam set has only
# datasetId; this connector always echoes back whatever offerId the TCK's
# own initiate call supplies.
CN_C_01_02_DATASETID=urn:dataset:cn-c-counter-offer
CN_C_01_03_DATASETID=urn:dataset:cn-c-reject-offer
CN_C_02_02_DATASETID=urn:dataset:cn-c-abandon
CN_C_02_03_DATASETID=urn:dataset:cn-c-reject-agreement
CN_C_02_04_DATASETID=urn:dataset:cn-c-passive-offer
CN_C_03_02_DATASETID=urn:dataset:cn-c-passive-offer
CN_C_03_03_DATASETID=urn:dataset:cn-c-passive-offer
```

- [ ] **Step 3: Add `CN_C` to the gate**

In `cmd/tckgate/main.go`, change the `expected` var:

```go
var expected = map[string]int{"MET": 1, "CAT": 3, "CN": 15, "CN_C": 16}
```

`exempt` is unchanged — no `CN_C` exemption is assumed in advance, per the design spec's Gate section.

- [ ] **Step 4: Run `go test ./...`**

Run: `go test ./...`
Expected: PASS, everything.

- [ ] **Step 5: Run the real TCK**

Run: `make tck`

This is the first point in the whole plan where the design's two flagged-as-unverified mechanisms — `CN_C:01-02`'s counter-request shape, and `CN_C:02-02`'s abandon-through-`pushCallback` retry path — actually run against the real TCK. Expected: `CN_C` reports 16 of 16, alongside `CN`'s unaffected 14 of 15 (`CN:02-07` still exempted), `CAT`'s 3 of 3, and `MET`'s 1 of 1.

If any `CN_C` test fails, treat it exactly as this project always has: read the TCK's own log output for that test's actual request/response sequence (`docker compose logs` or the harness's captured output, per `test/tck/run.sh`), compare it against the design spec's account of that test, and fix the discrepancy in code — do not adjust the test's expectations, and do not add a gate exemption unless a genuine, structural gap is found (the same bar `CN:02-07` was held to, and the same investigative process that found and fixed three real defects during the provider milestone's Task 6). Update the design spec's "Revision note" and the relevant section with what was actually true, the same way this spec already documents its own two rounds of correction, before moving on.

- [ ] **Step 6: Update `README.md`**

Change the status table's `CN_C` row from:

```
| Contract negotiation | `CN_C` (consumer role) | not started |
```

to:

```
| Contract negotiation | `CN_C` (consumer role) | gated in CI, 16 of 16 |
```

Update the pass-rate paragraph: `CN`'s 14 of 15 plus `CN_C`'s 16 of 16 plus `MET`'s 1 plus `CAT`'s 3 is 34 required tests passing; update "Current TCK pass rate" and the "only ... are required by the CI gate" sentence to match the real total `make tck` reported in Step 5 (count `TP`/`TP_C`'s still-unimplemented tests into the honest denominator exactly as the provider milestone's README update did).

- [ ] **Step 7: Add the new decisions to `DECISIONS.md`**

Append a new numbered section after the existing `## 23. Contract negotiation (provider role)` section (renumber if a later section already claims 24 — check the file's current end before choosing a number), covering, each in the established `**N.M sentence.**` / rationale / *Trade-off accepted.* format matching every existing entry in that file:

1. The second-table-over-shared-table storage choice (`consumer_negotiations` vs. a `role` column).
2. `/negotiations/initiate`'s scope: unauthenticated, TCK-only trigger hook on the public listener; a real management-API trigger deliberately not built here.
3. No retry on the initial outbound request (unlike every other outbound call this connector makes).
4. The policy-configuration mechanism (`consumer_policies`), named honestly as TCK-driven today since no real trigger exists yet to make it a product feature.
5. `pushCallback` gaining a `bool` return, and the verify-only-on-ack rule it exists for (`CN_C:03-06`).
6. The consumer-side gap against `CLAUDE.md`'s "never accept a constraint that is not enforced" rule: `on_offer: accept` never inspects an incoming offer's constraint content, since no TCK fixture ever sends a non-empty one to exercise it.

Use the design spec's "Why a policy configuration, not a content rule", "The `03-06` verification-ack rule", and Documentation sections as source material — each decision there already has its rationale written out; this step is transcribing it into `DECISIONS.md`'s format, not inventing new reasoning.

- [ ] **Step 8: Commit**

```bash
git add test/tck/config.properties test/tck/dsbox.yaml cmd/tckgate/main.go README.md DECISIONS.md
git commit -m "feat: add CN_C to the compliance gate and document the milestone"
```

---

## Done criteria

1. `make tck` passes with `CN_C` in the gate's expected map at 16, `CN` still 14 of 15 with `CN:02-07` exempted, green in CI.
2. `go test ./...` passes.
3. `README.md` reflects the real `CN_C` pass count and the new total.
4. `DECISIONS.md` records the six new decisions with their trade-offs.
5. A fresh clone can negotiate as consumer against a manually-run second `dsbox` instance playing provider (or against the TCK harness's own mock provider) — the same manual end-to-end bar the provider milestone's done criteria set.
