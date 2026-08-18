# Transfer process (consumer role) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the transfer process protocol's consumer role so the `TP_C` suite passes 15 of 15 and enters the compliance gate, taking the required total from 49 to 64 of 65.

**Architecture:** A second table (`consumer_transfer_processes`) keyed by this connector's own consumer pid, mirroring `consumer_negotiations`. The five transfer endpoints already mounted serve both roles: each resolves `{id}` against the consumer table first and falls back to the provider table, and the row's role selects which start-legality predicate applies. A new unauthenticated `POST /transfers/initiate` is the TCK's hook for asking this connector to start a transfer as consumer. Autonomous consumer behavior comes from a new `consumer_transfer_policies` config block whose default is passive.

**Tech Stack:** Go standard library, `modernc.org/sqlite`, `net/http.ServeMux` pattern routing.

**Spec:** `docs/superpowers/specs/2026-08-18-transfer-process-consumer-design.md`

## Global Constraints

- Go standard library only. Ask before adding a dependency; the default answer is no.
- English for all docs, comments, and identifiers. Everything committed here is public.
- No organizational affiliation anywhere. Copyright is b7g.
- Compliance is owed to DSP 2025-1, verified by the official TCK. Where behavior is unclear the spec and the TCK decide — not intuition, not how EDC does it.
- Structural rejections are `400`, never `404`. `404` is only for an unknown id: the TCK's assertion helper throws immediately on `404` even where an error is expected.
- Never accept a constraint that is not enforced.
- In-memory SQLite is for tests only, never a runtime path.
- `X-Forwarded-For` is logged, never used for auth. Never infer the external address from `Host`; use `config.PublicURL`.
- Every state-changing store update is compare-and-swap, because reactions run in goroutines and can outlive a termination that arrived meanwhile.
- Each task ends green: `gofmt -l .` empty, `go vet ./...` clean, `go test ./...` passing.

---

### Task 1: Consumer transfer storage

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `store.ConsumerTransfer` struct; `(*Store).CreateConsumerTransfer(ConsumerTransfer) error`; `(*Store).GetConsumerTransfer(consumerPID string) (ConsumerTransfer, bool, error)`; `(*Store).SetConsumerTransferState(consumerPID, from, to string, updatedAt time.Time) error`; `(*Store).SetConsumerTransferProviderPID(consumerPID, providerPID string, updatedAt time.Time) error`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/store_test.go`:

```go
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
	// ProviderPID is empty until the ACK reveals it.
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
```

`internal/store/store_test.go` is `package store`, so these types are unqualified and each test opens its own in-memory database with `Open(":memory:")` and `defer s.Close()` — the pattern `TestCreateAndGet` already uses. There is no shared constructor helper; do not add one.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run ConsumerTransfer -v`
Expected: FAIL, compilation errors naming `ConsumerTransfer`, `CreateConsumerTransfer`, `GetConsumerTransfer`, `SetConsumerTransferState`, `SetConsumerTransferProviderPID` as undefined.

- [ ] **Step 3: Add the schema**

In `internal/store/store.go`, beside `transferSchema`:

```go
// consumerTransferSchema holds transfers this connector runs as consumer —
// the mirror of transfer_processes, which is its provider-role state. Keyed
// by this connector's own generated consumer pid, because that is the
// identifier the provider puts in the callback path it POSTs to. A second
// table rather than a role column, for the reasons consumer_negotiations
// records: see the design spec's Storage section.
const consumerTransferSchema = `
CREATE TABLE IF NOT EXISTS consumer_transfer_processes (
    consumer_pid      TEXT PRIMARY KEY,
    provider_pid      TEXT NOT NULL DEFAULT '',
    provider_base_url TEXT NOT NULL,
    agreement_id      TEXT NOT NULL,
    format            TEXT NOT NULL,
    state             TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);`
```

Wire it into `migrate` exactly the way `transferSchema` is wired — find that call site and add the new constant alongside it, in the same style. Do not restructure the migration.

- [ ] **Step 4: Add the struct**

```go
// ConsumerTransfer is one transfer process this connector is running as
// consumer. ProviderPID is empty until the ACK to the initial
// TransferRequestMessage reveals it, which is also why nothing this
// connector sends as consumer can be sent before that ACK: every outbound
// URL contains it.
//
// No callback_address column: unlike the provider role, this connector's own
// callback address is not per-transfer data — it is always
// config.Config.PublicURL + VersionPath, computed at startup.
type ConsumerTransfer struct {
	ConsumerPID string
	ProviderPID string
	// ProviderBaseURL is connectorAddress from the initiate call — the base
	// every later outbound message is addressed against.
	ProviderBaseURL string
	AgreementID     string
	Format          string
	State           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
```

- [ ] **Step 5: Add the four functions**

Copy the shapes of `CreateConsumer`, `GetConsumer`, and `SetConsumerState` verbatim in structure — same error wrapping, same `timeFormat` handling, same `sql.ErrNoRows` treatment — substituting the new table and columns. `SetConsumerTransferState` ends with a call to a new `explainNoConsumerTransferUpdate`, modelled on `explainNoConsumerUpdate`: it must name `consumer_transfer_processes`, not another table, or the error will point at the wrong row.

```go
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
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/store/ -run ConsumerTransfer -v`
Expected: PASS, four tests.

- [ ] **Step 7: Verify the whole suite and the race detector**

Run: `gofmt -l . && go vet ./... && go test -race ./...`
Expected: no output from `gofmt`, clean vet, all packages ok.

- [ ] **Step 8: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: storage for transfers this connector runs as consumer"
```

---

### Task 2: The `consumer_transfer_policies` configuration

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`
- Modify: `internal/dsp/transfer.go` (resolver)
- Test: `internal/dsp/transfer_consumer_test.go` (new)

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `config.ConsumerTransferPolicy{AgreementID, After string; Sequence []string}`; `config.Config.ConsumerTransferPolicies []ConsumerTransferPolicy` (`yaml:"consumer_transfer_policies"`); `dsp.resolveConsumerTransferPolicy(cfg config.Config, agreementID string) (after string, sequence []string)`.

- [ ] **Step 1: Write the failing tests**

In `internal/config/config_test.go`:

```go
func TestConsumerTransferPolicyRejectsAnUnknownState(t *testing.T) {
	for _, doc := range []string{
		"consumer_transfer_policies:\n  - agreement_id: urn:uuid:a\n    sequence: [BANANA]\n",
		"consumer_transfer_policies:\n  - agreement_id: urn:uuid:a\n    after: BANANA\n    sequence: [COMPLETED]\n",
		// An entry with no agreement_id can never be selected, so it is a
		// configuration mistake rather than a harmless no-op.
		"consumer_transfer_policies:\n  - sequence: [COMPLETED]\n",
	} {
		if _, err := Load(minimal(doc), env(nil)); err == nil {
			t.Errorf("accepted an invalid policy:\n%s", doc)
		}
	}
}

func TestConsumerTransferPolicyLoads(t *testing.T) {
	cfg, err := Load(minimal(
		"consumer_transfer_policies:\n"+
			"  - agreement_id: urn:uuid:a\n"+
			"    after: REQUESTED\n"+
			"    sequence: [TERMINATED]\n"), env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.ConsumerTransferPolicies) != 1 {
		t.Fatalf("got %d policies, want 1", len(cfg.ConsumerTransferPolicies))
	}
	p := cfg.ConsumerTransferPolicies[0]
	if p.AgreementID != "urn:uuid:a" || p.After != "REQUESTED" ||
		len(p.Sequence) != 1 || p.Sequence[0] != "TERMINATED" {
		t.Errorf("policy = %+v", p)
	}
}
```

`internal/config/config_test.go` is `package config`; `minimal(extra string) []byte` appends `extra` to a minimal valid document and `env(nil)` supplies no environment overrides. Both already exist — reuse them rather than adding new ones.

In a new `internal/dsp/transfer_consumer_test.go`:

```go
// The consumer default is passive, which is the opposite of the provider
// default of [STARTED]. Eleven of the fifteen TP_C tests fail if this
// connector volunteers a message, so "no entry" must mean "send nothing".
func TestResolveConsumerTransferPolicyDefaultsToPassive(t *testing.T) {
	after, sequence := resolveConsumerTransferPolicy(config.Config{}, "urn:uuid:unknown")
	if len(sequence) != 0 {
		t.Errorf("sequence = %v, want empty", sequence)
	}
	if after != TransferStarted {
		t.Errorf("after = %q, want %q", after, TransferStarted)
	}
}

func TestResolveConsumerTransferPolicyUsesTheMatchingEntry(t *testing.T) {
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", Sequence: []string{TransferCompleted}},
		{AgreementID: "urn:uuid:b", After: TransferRequested, Sequence: []string{TransferTerminated}},
	}}
	if after, seq := resolveConsumerTransferPolicy(cfg, "urn:uuid:b"); after != TransferRequested ||
		len(seq) != 1 || seq[0] != TransferTerminated {
		t.Errorf("b: after=%q seq=%v", after, seq)
	}
	// after is omitted on entry a, so it takes the default rather than empty.
	if after, seq := resolveConsumerTransferPolicy(cfg, "urn:uuid:a"); after != TransferStarted ||
		len(seq) != 1 || seq[0] != TransferCompleted {
		t.Errorf("a: after=%q seq=%v", after, seq)
	}
}

// An entry with an explicitly empty sequence is a deliberate "stay passive",
// and must not fall through to the default the way a missing entry does.
// This is the distinction the provider-side resolver also had to make.
func TestResolveConsumerTransferPolicyEmptySequenceIsNotTheDefault(t *testing.T) {
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", Sequence: []string{}},
	}}
	if _, seq := resolveConsumerTransferPolicy(cfg, "urn:uuid:a"); len(seq) != 0 {
		t.Errorf("sequence = %v, want empty", seq)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ ./internal/dsp/ -run ConsumerTransferPolicy -v`
Expected: FAIL with undefined `ConsumerTransferPolicy`, `ConsumerTransferPolicies`, `resolveConsumerTransferPolicy`.

- [ ] **Step 3: Add the config type and field**

```go
// ConsumerTransferPolicy configures this connector's autonomous behavior as
// transfer consumer, keyed by the agreement the transfer runs under.
// Sequence is the states it drives to on its own once the transfer reaches
// After, pushing the matching message to the provider at each step.
//
// An agreement with no entry sends nothing. That default is the opposite of
// TransferPolicy's [STARTED], and it is forced by the TCK: eleven of the
// fifteen TP_C tests require this connector to stay passive after its
// initial request, and any volunteered message fails them.
//
// After exists because legality cannot supply the timing. TP_C:02-01 and
// TP_C:02-05 both send exactly one TransferTerminationMessage and differ
// only in when — 02-01 after the provider starts the transfer, 02-05
// straight from REQUESTED — and terminationLegalFrom admits both states, so
// a driver that fired as soon as its step became legal would send 02-01's
// termination before the provider's start and fail a test meant to pass.
type ConsumerTransferPolicy struct {
	AgreementID string `yaml:"agreement_id"`

	// After is the state whose arrival releases Sequence. Empty means
	// STARTED, filled in at lookup rather than here.
	After string `yaml:"after"`

	Sequence []string `yaml:"sequence"`
}
```

Add to `Config`:

```go
	// ConsumerTransferPolicies configures this connector's autonomous
	// behavior as transfer consumer, keyed by the agreement a transfer runs
	// under. An agreement with no matching entry sends nothing — see
	// ConsumerTransferPolicy.
	ConsumerTransferPolicies []ConsumerTransferPolicy `yaml:"consumer_transfer_policies"`
```

- [ ] **Step 4: Validate**

In `validate`, beside the existing `TransferPolicies` loop, add one for the new block. It must reject an empty `agreement_id`, an `after` that is not a known transfer state, and any `sequence` element that is not a known transfer state. Reuse the existing known-state helper the `TransferPolicies` validation already calls rather than writing a second list of state names — two lists would drift.

`after` may legitimately be empty (meaning the default), so validate it only when present.

- [ ] **Step 5: Add the resolver**

In `internal/dsp/transfer.go`:

```go
// resolveConsumerTransferPolicy returns the trigger state and the sequence
// configured for an agreement, and the defaults when nothing matches:
// STARTED, and no steps at all.
//
// A present entry with an empty sequence is not the same as a missing entry
// here only in intent — both send nothing — but keeping the lookup explicit
// means a later change to the default cannot silently reinterpret a
// deliberate `sequence: []`.
func resolveConsumerTransferPolicy(cfg config.Config, agreementID string) (string, []string) {
	for _, p := range cfg.ConsumerTransferPolicies {
		if p.AgreementID != agreementID {
			continue
		}
		after := p.After
		if after == "" {
			after = TransferStarted
		}
		return after, p.Sequence
	}
	return TransferStarted, nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/config/ ./internal/dsp/ -run ConsumerTransferPolicy -v`
Expected: PASS.

- [ ] **Step 7: Verify**

Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/dsp/transfer.go internal/dsp/transfer_consumer_test.go
git commit -m "feat: configure this connector's autonomous behavior as transfer consumer"
```

---

### Task 3: One path, two roles

**Files:**
- Modify: `internal/dsp/transfer.go` (rename one predicate)
- Modify: `internal/dsp/transfer_handler.go` (role-aware resolution)
- Test: `internal/dsp/transfer_handler_test.go`

**Interfaces:**
- Consumes: `store.ConsumerTransfer`, `(*Store).GetConsumerTransfer`, `(*Store).SetConsumerTransferState` from Task 1.
- Produces: `dsp.resolvedTransfer`; `(transferHandler).lookup` returning it; `providerInboundStartLegalFrom` replacing `inboundStartLegalFrom`.

This is the task the sender split from `2026-08-17` was for. The same `POST /2025-1/transfers/{id}/start` serves both roles, and the same message is legal from `REQUESTED` as consumer and illegal from `REQUESTED` as provider.

- [ ] **Step 1: Write the failing test**

Append to `internal/dsp/transfer_handler_test.go`:

```go
// The pair that pins the whole role split. Identical request, identical
// starting state, opposite outcomes — because the sender differs. A single
// legality table cannot produce both rows.
func TestInboundStartDependsOnTheRowsRole(t *testing.T) {
	for _, c := range []struct {
		name     string
		consumer bool
		wantCode int
		wantState string
	}{
		{"as consumer the provider may start it", true, http.StatusOK, TransferStarted},
		{"as provider the consumer may not", false, http.StatusBadRequest, TransferRequested},
	} {
		h, st := newTestTransferHandler(t, config.Config{})
		var id, consumerPID string
		if c.consumer {
			// A consumer row is addressed by this connector's consumer pid,
			// which is therefore both the path id and the message's
			// consumerPid.
			id = seedConsumerTransfer(t, st, TransferRequested)
			consumerPID = id
		} else {
			tp := seedTransfer(t, st, TransferRequested)
			id, consumerPID = tp.ProviderPID, tp.ConsumerPID
		}

		body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferStartMessageType + `",` +
			`"providerPid":"urn:uuid:p","consumerPid":"` + consumerPID + `"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost,
			VersionPath+"/transfers/"+id+"/start", strings.NewReader(body))
		req.SetPathValue("id", id)
		h.handleTransferStart(rec, req)

		if rec.Code != c.wantCode {
			t.Errorf("%s: got %d, want %d", c.name, rec.Code, c.wantCode)
		}
		if got := currentTransferState(t, st, id, c.consumer); got != c.wantState {
			t.Errorf("%s: stored state = %s, want %s", c.name, got, c.wantState)
		}
	}
}

// GET is how the consumer suite makes 37 of its assertions. A GET that
// resolved only provider rows would fail most of TP_C while every inbound
// handler behaved perfectly, so it is pinned here rather than left to the
// TCK to discover.
func TestGetTransferResolvesAConsumerRow(t *testing.T) {
	h, st := newTestTransferHandler(t, config.Config{})
	id := seedConsumerTransfer(t, st, TransferStarted)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/transfers/"+id, nil)
	req.SetPathValue("id", id)
	h.handleGetTransfer(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc["state"] != TransferStarted {
		t.Errorf("state = %v, want %s", doc["state"], TransferStarted)
	}
	if doc["consumerPid"] != id {
		t.Errorf("consumerPid = %v, want %s", doc["consumerPid"], id)
	}
}
```

Add the two helpers beside the existing `seedTransfer`:

```go
// seedConsumerTransfer writes a consumer-role transfer in the given state and
// returns its consumer pid, which is the id its endpoints are addressed by.
func seedConsumerTransfer(t *testing.T, st *store.Store, state string) string {
	t.Helper()
	now := time.Now()
	id := "urn:uuid:consumer-transfer-" + state
	if err := st.CreateConsumerTransfer(store.ConsumerTransfer{
		ConsumerPID:     id,
		ProviderPID:     "urn:uuid:p",
		ProviderBaseURL: "http://provider.example/2025-1",
		AgreementID:     "urn:uuid:a",
		Format:          "HTTP-PULL",
		State:           state,
		CreatedAt:       now,
		UpdatedAt:       now,
	}); err != nil {
		t.Fatalf("CreateConsumerTransfer: %v", err)
	}
	return id
}

func currentTransferState(t *testing.T, st *store.Store, id string, consumer bool) string {
	t.Helper()
	if consumer {
		c, _, err := st.GetConsumerTransfer(id)
		if err != nil {
			t.Fatalf("GetConsumerTransfer: %v", err)
		}
		return c.State
	}
	p, _, err := st.GetTransfer(id)
	if err != nil {
		t.Fatalf("GetTransfer: %v", err)
	}
	return p.State
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/ -run 'InboundStartDependsOnTheRowsRole|GetTransferResolvesAConsumerRow' -v`
Expected: FAIL — undefined `seedConsumerTransfer`, then once it compiles, a `404` from both new cases because `lookup` only reads the provider table.

- [ ] **Step 3: Rename the predicate to name its role**

In `internal/dsp/transfer.go`, rename `inboundStartLegalFrom` to `providerInboundStartLegalFrom` and update its doc comment's first line. Update the single call site in `transfer_handler.go`. `startLegalFrom` keeps its name: `{REQUESTED, SUSPENDED}` describes the transfer rather than a party, which is why it can answer both "may a provider send a start now" and "may a consumer accept one now". Only the narrower set exists because of who is sending, so only it names a role.

- [ ] **Step 4: Add the resolved type and make lookup role-aware**

```go
// resolvedTransfer is a transfer found by path id, in whichever role owns
// it. The embedded TransferProcess is the shape every existing helper
// already takes — buildTransferProcessDoc, the message builders — so a
// consumer-role row is projected into it rather than given a parallel set of
// functions.
type resolvedTransfer struct {
	store.TransferProcess
	// Consumer reports which table the row came from. It decides which
	// start-legality rule applies and which setter moves the row.
	Consumer bool
	// ProviderBaseURL is set for consumer-role rows only: the base every
	// message this connector sends as consumer is addressed against. The
	// provider role's equivalent is TransferProcess.CallbackAddress.
	ProviderBaseURL string
}
```

`lookup` tries `GetConsumerTransfer(id)` first and falls back to `GetTransfer(id)` — the order `handleEvent`, `handleTermination`, and `handleGetNegotiation` already established for negotiations. Both id spaces are independently generated UUIDs, so a collision is not a practical concern. An id in neither table is the one place `404` is correct; keep that behavior exactly as it is.

- [ ] **Step 5: Route legality and writes by role**

```go
// inboundStartLegalFor returns the rule governing a TransferStartMessage
// this connector receives, which depends on who sent it. DSP 2025-1 gives
// the message a single permitted sender ("Sent by: Provider") and admits the
// consumer's copy only as a resume, so a start arriving from REQUESTED is
// legal when this connector is the consumer and illegal when it is the
// provider.
func inboundStartLegalFor(r resolvedTransfer) func(string) bool {
	if r.Consumer {
		return startLegalFrom
	}
	return providerInboundStartLegalFrom
}
```

`applyTransition` takes `resolvedTransfer` instead of `store.TransferProcess`, and writes through a new `setTransferState` that dispatches on `r.Consumer` to `SetConsumerTransferState` or `SetTransferState`. `handleTransferStart` passes `inboundStartLegalFor(r)`; the other three keep their single predicates, because DSP 2025-1 names both parties in the Sent by rows for suspension, completion, and termination.

`writeTransferStateUpdateError` currently names a provider pid. For a consumer row it must name the consumer pid, or the error document points at an identifier the counterparty has never seen.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/dsp/ -run 'InboundStartDependsOnTheRowsRole|GetTransferResolvesAConsumerRow' -v`
Expected: PASS.

- [ ] **Step 7: Verify nothing regressed, and prove the split still discriminates**

Run: `go test -race -count=2 ./...`
Expected: all green, including `TestTransferTransitionsOverHTTP`, whose provider-role start-from-`REQUESTED` case must still be `400`.

Then mutate to confirm the new test earns its place: make `inboundStartLegalFor` return `startLegalFrom` unconditionally and re-run. Expected: `TestInboundStartDependsOnTheRowsRole` fails on the provider case and `TestTransferTransitionsOverHTTP` fails on `start from REQUESTED`. Restore, and confirm `git diff` is empty before continuing.

- [ ] **Step 8: Commit**

```bash
git add internal/dsp/transfer.go internal/dsp/transfer_handler.go internal/dsp/transfer_handler_test.go
git commit -m "feat: serve the transfer endpoints for both roles from one path"
```

---

### Task 4: The initiate hook and the outbound request

**Files:**
- Create: `internal/dsp/transfer_consumer_handler.go`
- Modify: `internal/dsp/router.go`
- Test: `internal/dsp/transfer_consumer_handler_test.go` (new)

**Interfaces:**
- Consumes: Task 1's storage, Task 3's `resolvedTransfer`.
- Produces: `(transferHandler).handleTransferInitiate`; `buildTransferRequestMessage(t store.ConsumerTransfer, callbackAddress string) any`; the mounted route `POST /2025-1/transfers/initiate`.

The TCK POSTs `{providerId, agreementId, format, connectorAddress}` as **plain JSON, not JSON-LD** — no `@context`, no `@type`. Confirmed from `HttpConsumerTransferProcessClient.initiateTransferRequest`: the body is a four-entry `Map.of` and the call is `postJson(url, body, false, true)`, whose `false` is the JSON-LD flag. `HttpFunctions.postJson` throws immediately on `404` or `5xx` and retries any other `4xx` twice more, so `200` on the first attempt is required.

- [ ] **Step 1: Write the failing tests**

```go
func TestTransferInitiateStartsAConsumerTransfer(t *testing.T) {
	h, st := newTestTransferHandler(t, config.Config{})
	seedAgreement(t, st, "urn:uuid:a-1")

	rec := httptest.NewRecorder()
	body := `{"providerId":"urn:connector:tck","agreementId":"urn:uuid:a-1",` +
		`"format":"HTTP-PULL","connectorAddress":"http://provider.example/2025-1"}`
	h.handleTransferInitiate(rec, httptest.NewRequest(http.MethodPost,
		VersionPath+"/transfers/initiate", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestTransferInitiateRejectsMissingFields(t *testing.T) {
	full := map[string]string{
		"providerId":       "urn:connector:tck",
		"agreementId":      "urn:uuid:a-1",
		"format":           "HTTP-PULL",
		"connectorAddress": "http://provider.example/2025-1",
	}
	for missing := range full {
		h, st := newTestTransferHandler(t, config.Config{})
		seedAgreement(t, st, "urn:uuid:a-1")
		partial := map[string]string{}
		for k, v := range full {
			if k != missing {
				partial[k] = v
			}
		}
		raw, err := json.Marshal(partial)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rec := httptest.NewRecorder()
		h.handleTransferInitiate(rec, httptest.NewRequest(http.MethodPost,
			VersionPath+"/transfers/initiate", bytes.NewReader(raw)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("without %s: got %d, want 400", missing, rec.Code)
		}
	}
}

// The decision this milestone takes deliberately: one rule for both roles.
// The provider role already refuses a transfer citing an agreement it has no
// record of; starting one as consumer under a contract this connector never
// held would be the same defect from the other side.
func TestTransferInitiateRejectsAnUnknownAgreement(t *testing.T) {
	h, _ := newTestTransferHandler(t, config.Config{})
	rec := httptest.NewRecorder()
	body := `{"providerId":"urn:connector:tck","agreementId":"urn:uuid:never-negotiated",` +
		`"format":"HTTP-PULL","connectorAddress":"http://provider.example/2025-1"}`
	h.handleTransferInitiate(rec, httptest.NewRequest(http.MethodPost,
		VersionPath+"/transfers/initiate", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

// The address is where this connector will send; it goes through the same
// SSRF guard both existing roles use, and the reason is logged rather than
// echoed so the endpoint cannot be used as a name-resolution oracle.
func TestTransferInitiateRejectsAnUnsendableAddress(t *testing.T) {
	h, st := newTestTransferHandler(t, config.Config{})
	seedAgreement(t, st, "urn:uuid:a-1")
	rec := httptest.NewRecorder()
	body := `{"providerId":"urn:connector:tck","agreementId":"urn:uuid:a-1",` +
		`"format":"HTTP-PULL","connectorAddress":"http://127.0.0.1:9999/2025-1"}`
	h.handleTransferInitiate(rec, httptest.NewRequest(http.MethodPost,
		VersionPath+"/transfers/initiate", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "127.0.0.1") {
		t.Error("the rejection echoed the address back")
	}
}

func TestTransferRequestMessageShape(t *testing.T) {
	msg := buildTransferRequestMessage(store.ConsumerTransfer{
		ConsumerPID: "urn:uuid:c-1",
		AgreementID: "urn:uuid:a-1",
		Format:      "HTTP-PULL",
	}, "http://consumer.example/2025-1")
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Every field the TCK's transfer-request-message-schema.json requires.
	for _, k := range []string{"@context", "@type", "agreementId", "format", "callbackAddress", "consumerPid"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing required field %q", k)
		}
	}
	if got["@type"] != TransferRequestMessageType {
		t.Errorf("@type = %v", got["@type"])
	}
	// dataAddress is only for push transfers, and this connector pulls.
	if _, ok := got["dataAddress"]; ok {
		t.Error("dataAddress must be absent for a pull transfer")
	}
}
```

`seedAgreement` stands for whatever helper already writes a `store.Agreement` in these tests; reuse it, or add one shaped like `seedTransfer` if none exists.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/ -run 'TransferInitiate|TransferRequestMessageShape' -v`
Expected: FAIL, undefined `handleTransferInitiate` and `buildTransferRequestMessage`.

- [ ] **Step 3: Implement the handler**

`handleTransferInitiate` follows `negotiation_consumer_handler.go`'s `handleInitiate` step for step:

1. Decode the body through `http.MaxBytesReader`, rejecting a non-object with `400`.
2. Reject unless `providerId`, `agreementId`, `format`, and `connectorAddress` are all non-empty.
3. Run `connectorAddress` through `validateOutgoingCallback`, logging the reason and returning a generic `400`.
4. Reject unless `GetAgreement(agreementId)` finds a row.
5. Generate a consumer pid with `store.NewUUID()`.
6. Write the row in `REQUESTED`.
7. Answer `200`.
8. Dispatch the outbound `TransferRequestMessage` in a goroutine.

Steps 7 and 8 are in that order deliberately: the TCK waits for the request to arrive on its own callback endpoint, not for this response, and blocking the response on a network call would make the hook's latency depend on the counterparty.

The error document type is `TransferErrorType`, not the negotiation's.

- [ ] **Step 4: Build the message**

```go
// buildTransferRequestMessage is this connector's opening message as
// consumer. callbackAddress is where the provider sends everything after
// this: config.PublicURL + VersionPath, the same value the provider role
// already uses when it builds an agreement message.
//
// No dataAddress: it is required only when the format calls for a push
// transfer, and it is this connector that would be pushed to. Sending an
// empty one would also trip the schema's minItems on endpointProperties.
func buildTransferRequestMessage(t store.ConsumerTransfer, callbackAddress string) any {
	return TransferRequestMessage{
		Context:         []string{ContextURL},
		Type:            TransferRequestMessageType,
		ConsumerPID:     t.ConsumerPID,
		AgreementID:     t.AgreementID,
		Format:          t.Format,
		CallbackAddress: callbackAddress,
	}
}
```

`TransferRequestMessage` already exists in `internal/dsp/transfer.go` — the provider role decodes inbound requests into it — and its fields are exactly the six the schema requires. Reuse it; do not add a second type for the outbound direction, and do not build a `map[string]any`.

- [ ] **Step 5: Send it, and record what the ACK reveals**

The goroutine POSTs to `<connectorAddress>/transfers/request`, reads `providerPid` out of the ACK, and calls `SetConsumerTransferProviderPID`. No retry — the negotiation consumer role's decision, for the same reason: a retry against a counterparty that already accepted the first attempt creates a second transfer.

If the ACK cannot be read, log and stop. There is nothing to record and nothing this connector can legally send without a provider pid.

- [ ] **Step 6: Mount the route**

In `internal/dsp/router.go`, beside the other transfer routes:

```go
	mux.HandleFunc("POST "+VersionPath+"/transfers/initiate", tr.handleTransferInitiate)
```

`POST /transfers/request` stays provider-only: it is where a counterparty asks *this* connector to provide.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/dsp/ -run 'TransferInitiate|TransferRequestMessageShape' -v`
Expected: PASS.

- [ ] **Step 8: Verify and commit**

Run: `gofmt -l . && go vet ./... && go test -race ./...`

```bash
git add internal/dsp/transfer_consumer_handler.go internal/dsp/transfer_consumer_handler_test.go internal/dsp/router.go
git commit -m "feat: start a transfer as consumer from the initiate hook"
```

---

### Task 5: The consumer driver

**Files:**
- Modify: `internal/dsp/transfer_consumer_handler.go`
- Test: `internal/dsp/transfer_consumer_handler_test.go`

**Interfaces:**
- Consumes: `resolveConsumerTransferPolicy` (Task 2), `resolvedTransfer` (Task 3), the initiate goroutine (Task 4).
- Produces: `(transferHandler).driveConsumerTransfer(t store.ConsumerTransfer)`; `(transferHandler).pushConsumerStep(t store.ConsumerTransfer, to string) bool`; `(transferHandler).onTransferRequestAcknowledged(t store.ConsumerTransfer, providerPID string)` — called by Task 4's goroutine, which must be changed to route through it rather than calling `SetConsumerTransferProviderPID` directly.

Two trigger points, not one. A policy whose `after` is a state the provider drives to (`STARTED`) fires from `applyTransition` once that state is stored. A policy whose `after` is `REQUESTED` fires from the initiate goroutine, **after the ACK** — not when the row is written — because every URL this connector addresses as consumer contains `providerPid`, and that does not exist until the ACK reveals it. Firing earlier would build `.../transfers//termination` and fail `TP_C:02-05` in a way that looks like a policy bug rather than an ordering one.

- [ ] **Step 1: Write the failing tests**

```go
// TP_C:02-01's shape, and the test that justifies the `after` field. A
// driver that fired as soon as its step became legal would send this
// termination from REQUESTED — terminationLegalFrom admits it — and the
// provider's start would then land on a terminated transfer.
func TestConsumerDriverWaitsForTheTriggerState(t *testing.T) {
	pushes := recordPushes(t)
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", After: TransferStarted, Sequence: []string{TransferTerminated}},
	}}
	h, st := newTestTransferHandler(t, cfg)
	id := seedConsumerTransferFor(t, st, TransferRequested, "urn:uuid:a", pushes.srv.URL)

	// Nothing may be sent while the transfer is still REQUESTED.
	if got := pushes.seen(); len(got) != 0 {
		t.Fatalf("pushed %v before the trigger state", got)
	}

	deliverInboundStart(t, h, id)

	if got := pushes.wait(1); len(got) != 1 || got[0].msgType != TransferTerminationMessageType {
		t.Fatalf("after the start, pushed %v, want one termination", got)
	}
}

// TP_C:02-05: the sequence is released by the ACK, not by a provider
// message, because no provider message ever arrives in that test.
func TestConsumerDriverFiresFromRequestedAfterTheAck(t *testing.T) {
	pushes := recordPushes(t)
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", After: TransferRequested, Sequence: []string{TransferTerminated}},
	}}
	h, st := newTestTransferHandler(t, cfg)
	tr := seedConsumerTransferAwaitingAck(t, st, "urn:uuid:a", pushes.srv.URL)

	h.onTransferRequestAcknowledged(tr, "urn:uuid:p-1")

	got := pushes.wait(1)
	if len(got) != 1 || got[0].msgType != TransferTerminationMessageType {
		t.Fatalf("pushed %v, want one termination", got)
	}
	// The URL must carry the provider pid the ACK supplied.
	if !strings.Contains(got[0].path, "urn:uuid:p-1") {
		t.Errorf("pushed to %q, which omits the provider pid", got[0].path)
	}
}

// TP_C:02-03: two steps, in order, spaced.
func TestConsumerDriverWalksTheWholeSequence(t *testing.T) {
	pushes := recordPushes(t)
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", After: TransferStarted,
			Sequence: []string{TransferSuspended, TransferTerminated}},
	}}
	h, st := newTestTransferHandler(t, cfg)
	id := seedConsumerTransferFor(t, st, TransferRequested, "urn:uuid:a", pushes.srv.URL)

	deliverInboundStart(t, h, id)

	got := pushes.wait(2)
	if len(got) != 2 ||
		got[0].msgType != TransferSuspensionMessageType ||
		got[1].msgType != TransferTerminationMessageType {
		t.Fatalf("pushed %v, want suspension then termination", got)
	}
}

// The same stop-not-skip rule the provider driver already enforces, checked
// from the consumer side: a step that is illegal from where the previous one
// left the transfer ends the sequence.
func TestConsumerDriverStopsAtAnIllegalStep(t *testing.T) {
	pushes := recordPushes(t)
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		// COMPLETED is illegal from TERMINATED, and a third step that would
		// be legal again proves the driver stopped rather than skipped.
		{AgreementID: "urn:uuid:a", After: TransferStarted,
			Sequence: []string{TransferTerminated, TransferCompleted, TransferSuspended}},
	}}
	h, st := newTestTransferHandler(t, cfg)
	id := seedConsumerTransferFor(t, st, TransferRequested, "urn:uuid:a", pushes.srv.URL)

	deliverInboundStart(t, h, id)

	got := pushes.wait(1)
	if len(got) != 1 || got[0].msgType != TransferTerminationMessageType {
		t.Fatalf("pushed %v, want exactly one termination", got)
	}
}
```

`recordPushes` above stands for `newFakeTransferConsumer(t)`, which already exists in `transfer_handler_test.go`: it returns a `*fakeTransferConsumer` whose `got []transferPush` records `{path, msgType, at}` in arrival order across paths. Order is the property that matters — the TCK registers the handler for step N+1 only once step N has arrived — so use it rather than a per-path map. Add a `wait(n int)` helper beside it if one is not already there. `deliverInboundStart` posts a `TransferStartMessage` to `handleTransferStart` for the given id, the way the Task 3 tests do.

`seedConsumerTransferFor(t, st, state, agreementID, providerBaseURL)` and `seedConsumerTransferAwaitingAck(t, st, agreementID, providerBaseURL)` are new — add them beside `seedConsumerTransfer` from Task 3. The second writes a row with an empty `provider_pid`, which is the state the ACK has not yet resolved.

Set `stepDelay` to the test value the provider driver tests already use, so these do not sleep for the production 200 ms.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/ -run ConsumerDriver -v`
Expected: FAIL, undefined `onTransferRequestAcknowledged` and no pushes recorded.

- [ ] **Step 3: Implement the push**

`pushConsumerStep` mirrors `pushTransferStep` and differs in exactly three ways: the URL base is `t.ProviderBaseURL` rather than `t.CallbackAddress`, the path id is `t.ProviderPID` rather than `t.ConsumerPID`, and the write goes through `SetConsumerTransferState`. Everything else — the legality check against this connector's own configured step, `validateOutgoingCallback` on the constructed URL, stop-on-refusal, stop-on-dropped-write — is the same, and for the same reasons `pushTransferStep`'s comments already give.

Do not send a `TransferStartMessage` from here. The consumer may send one only to resume a suspended transfer, and no `TP_C` test that produces a result asks for it — `02-04`, the one that does, is `@Disabled`. If a policy names `STARTED`, `startLegalFrom` will refuse it from anything but `SUSPENDED`, which is the correct behavior and needs no special case.

- [ ] **Step 4: Implement the walk and its two triggers**

```go
// driveConsumerTransfer walks the configured sequence, pushing one message
// per step and stopping at the first refusal. Each step's write is the
// precondition the next step is checked against, exactly as in the provider
// role's driver.
func (h transferHandler) driveConsumerTransfer(t store.ConsumerTransfer) {
	_, sequence := resolveConsumerTransferPolicy(h.cfg, t.AgreementID)
	for i, state := range sequence {
		if i > 0 {
			time.Sleep(h.stepDelay)
		}
		if !h.pushConsumerStep(t, state) {
			return
		}
		t.State = state
	}
}
```

Trigger one, in `applyTransition`: after a successful write to a consumer-role row, if the new state equals the policy's `after`, launch `driveConsumerTransfer` in a goroutine. Launch it after the response is written, not before — the counterparty's `200` must not wait on this connector's own outbound calls.

Trigger two, in the initiate goroutine: `onTransferRequestAcknowledged(t, providerPID)` records the provider pid, and if the policy's `after` is `REQUESTED`, calls `driveConsumerTransfer` with the pid now filled in.

A policy whose `after` is a state the transfer never reaches simply never fires. That is not an error: it is what `TP_C:01-*` and `TP_C:03-*` rely on, since they have no policy at all.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dsp/ -run ConsumerDriver -v`
Expected: PASS, four tests.

- [ ] **Step 6: Prove the `after` field earns its place**

Change the driver to ignore `after` and fire on every state change. Re-run.
Expected: `TestConsumerDriverWaitsForTheTriggerState` fails, because the termination is pushed while the transfer is still `REQUESTED`. Restore and confirm `git diff` is empty.

If that mutation does **not** fail the test, the test is not pinning what it claims and must be strengthened before moving on.

- [ ] **Step 7: Verify and commit**

Run: `gofmt -l . && go vet ./... && go test -race -count=2 ./internal/dsp/`

```bash
git add internal/dsp/transfer_consumer_handler.go internal/dsp/transfer_consumer_handler_test.go
git commit -m "feat: drive this connector's own transfer messages as consumer"
```

---

### Task 6: A consumer-role negotiation writes its agreement

**Files:**
- Modify: `internal/store/store.go` (a third `Origin` value)
- Modify: `internal/dsp/negotiation_consumer_handler.go`
- Test: `internal/dsp/negotiation_consumer_handler_test.go`

**Interfaces:**
- Consumes: nothing from Tasks 1-5.
- Produces: `store.OriginAgreed`.

`negotiation_consumer_handler.go` moves a consumer-role negotiation to `AGREED` — this connector accepting a remote provider's agreement — and writes no `store.Agreement` row. Both existing writers are provider-side. The consequence is that a real negotiation cannot be followed by a transfer under the agreement it produced, because Task 4's initiate handler refuses an agreement it has no record of.

The TCK cannot catch this: its transfers cite seeded agreements. So the test here is a unit test that asserts the row, and a green suite is not evidence either way.

- [ ] **Step 1: Write the failing test**

```go
// A consumer-role negotiation that reaches AGREED must leave behind the
// agreement it agreed to, or this connector can never transfer under it —
// POST /transfers/initiate refuses an agreement with no row. No TCK test
// covers this, which is why it is pinned here.
func TestConsumerAgreementIsRecorded(t *testing.T) {
	h, st := newTestNegotiationHandler(t, config.Config{})
	n := seedConsumerNegotiation(t, st, StateRequested)

	deliverContractAgreement(t, h, n.ConsumerPID, "urn:uuid:agreement-7", "urn:dataset:d-1")

	got, ok, err := st.GetAgreement("urn:uuid:agreement-7")
	if err != nil {
		t.Fatalf("GetAgreement: %v", err)
	}
	if !ok {
		t.Fatal("no agreement row was written")
	}
	if got.Origin != store.OriginAgreed {
		t.Errorf("Origin = %q, want %q", got.Origin, store.OriginAgreed)
	}
	if got.DatasetID != "urn:dataset:d-1" {
		t.Errorf("DatasetID = %q", got.DatasetID)
	}
	if got.ConsumerPID != n.ConsumerPID {
		t.Errorf("ConsumerPID = %q, want %q", got.ConsumerPID, n.ConsumerPID)
	}
}
```

`newTestNegotiationHandler`, `seedConsumerNegotiation`, and `deliverContractAgreement` stand for whatever `internal/dsp/negotiation_consumer_handler_test.go` already uses to build a handler, seed a consumer-role negotiation, and drive it to `AGREED`. Read that file first and reuse its helpers under their real names; the file is `package dsp`, so `config.Config` stays qualified and `store.OriginAgreed` does too.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dsp/ -run ConsumerAgreementIsRecorded -v`
Expected: FAIL with "no agreement row was written".

- [ ] **Step 3: Add the third origin**

```go
	// OriginAgreed is an agreement this connector accepted as consumer, from
	// a provider's ContractAgreementMessage. Unlike OriginNegotiated, this
	// connector did not author it; unlike OriginImported, no operator
	// asserted it out of band.
	OriginAgreed = "agreed"
```

Update the `Agreement` doc comment listing the writers: there are now three, and `Origin` has a value for the consumer role. Remove the sentence that says it does not — and remove the matching entry from `docs/follow-ups.md`, per that document's rule that an entry is deleted when it is fixed.

- [ ] **Step 4: Write the row**

In the consumer-role handler that moves the negotiation to `AGREED`, write the agreement in the same step. If the write fails, the transition must fail too: recording that this connector agreed, while losing what it agreed to, is the state Task 4 will later refuse to act on, and a negotiation that silently cannot be transferred under is worse than one that visibly failed.

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/dsp/ -run ConsumerAgreementIsRecorded -v`
Expected: PASS.

- [ ] **Step 6: Verify the negotiation suites did not move**

Run: `go test -race ./...`, then `make tck`.
Expected: `CN_C` still 16 of 16, `CN` still 14 of 15 with `CN:02-07` exempt. This task changes a path `CN_C` exercises, so its suite is the regression check.

- [ ] **Step 7: Commit**

```bash
git add internal/store/store.go internal/dsp/negotiation_consumer_handler.go internal/dsp/negotiation_consumer_handler_test.go docs/follow-ups.md
git commit -m "fix: record the agreement a consumer-role negotiation accepted"
```

---

### Task 7: The harness, the gate, and the claim

**Files:**
- Modify: `test/tck/config.properties`
- Modify: `test/tck/run.sh`
- Modify: `test/tck/dsbox.yaml`
- Modify: `cmd/tckgate/main.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: everything above.
- Produces: a gated `TP_C`.

Every `TP_C` test needs a seeded agreement, not only the four with policies: Task 4's initiate handler refuses an agreement it has no record of, so an unseeded id fails at the first step. Twelve of the sixteen need no *policy*, and they can share one id.

- [ ] **Step 1: Add the config keys**

Five distinct agreement ids. Twelve tests are passive and share one; the four policy-driven tests get their own.

```properties
# TP_C: the agreement each consumer-role test cites. Same key format as TP —
# <TEST METHOD NAME UPPERCASED>_<FIELD NAME UPPERCASED>, no class-level or
# global fallback, so a method with no key of its own silently falls back to
# a fresh random UUID. That fallback is observable here for the same reason
# it is in TP: this connector refuses to start a transfer under an agreement
# it has no record of, and a random UUID is never seeded.
#
# Twelve of the sixteen share urn:uuid:tck-tpc-passive: groups 01 and 03 need
# this connector to stay silent after its initial request, which is what an
# agreement with no consumer_transfer_policies entry gets. Only the four
# consumer-driven tests need an id of their own.
#
# TP_C_02_04 is @Disabled upstream and produces no result. Its key is here
# anyway, so the list does not look like it has a hole.
TP_C_01_01_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_01_02_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_01_03_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_01_04_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_01_05_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_02_01_AGREEMENTID=urn:uuid:tck-tpc-02-01
TP_C_02_02_AGREEMENTID=urn:uuid:tck-tpc-02-02
TP_C_02_03_AGREEMENTID=urn:uuid:tck-tpc-02-03
TP_C_02_04_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_02_05_AGREEMENTID=urn:uuid:tck-tpc-02-05
TP_C_03_01_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_03_02_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_03_03_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_03_04_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_03_05_AGREEMENTID=urn:uuid:tck-tpc-passive
TP_C_03_06_AGREEMENTID=urn:uuid:tck-tpc-passive
```

- [ ] **Step 2: Seed them**

In `run.sh`, beside the existing seven:

```sh
seed_agreement urn:uuid:tck-tpc-passive
seed_agreement urn:uuid:tck-tpc-02-01
seed_agreement urn:uuid:tck-tpc-02-02
seed_agreement urn:uuid:tck-tpc-02-03
seed_agreement urn:uuid:tck-tpc-02-05
echo 'seeded 12 transfer agreements'
```

Update the existing `echo` rather than adding a second one, and update the comment above the block to say it now covers both roles.

- [ ] **Step 3: Configure the four policies**

In `dsbox.yaml`:

```yaml
# The consumer role's autonomous behavior. Only the four TP_C tests that
# require this connector to send a message of its own appear here; the other
# twelve share an agreement with no entry, which is what makes them passive.
consumer_transfer_policies:
  - agreement_id: urn:uuid:tck-tpc-02-01
    after: STARTED
    sequence: [TERMINATED]
  - agreement_id: urn:uuid:tck-tpc-02-02
    after: STARTED
    sequence: [COMPLETED]
  - agreement_id: urn:uuid:tck-tpc-02-03
    after: STARTED
    sequence: [SUSPENDED, TERMINATED]
  - agreement_id: urn:uuid:tck-tpc-02-05
    after: REQUESTED
    sequence: [TERMINATED]
```

- [ ] **Step 4: Run the suite before gating it**

Run: `make tck`
Expected: `TP_C` reports 15 successes. Read the count out of the output rather than trusting the gate, which does not require `TP_C` yet:

```sh
grep -c "SUCCESSFUL: TP_C:" tck-output.txt   # expect 15
grep    "FAILED: TP_C:"     tck-output.txt   # expect no output
```

Do not proceed until both hold. A failure here is a real defect in Tasks 1-6, and the diagnosis is the connector log at `tck-connector.txt`.

- [ ] **Step 5: Gate it**

In `cmd/tckgate/main.go`:

```go
var expected = map[string]int{"MET": 1, "CAT": 3, "CN": 15, "CN_C": 16, "TP": 15, "TP_C": 15}
```

Extend the comment above `expected` so the `TP`/`TP_C` divergence note covers both suites: each declares 16 `@MandatoryTest` methods, each has one carrying JUnit's `@Disabled` (`tp_02_04`, `tp_c_02_04`), and a disabled test produces no result.

- [ ] **Step 6: Verify the gate**

Run: `make tck`
Expected: `64 required tests passed, 0 results outside the gate, 1 known exemption(s)`.

The `0` matters as much as the `64`: every suite the TCK runs is now gated, so a non-zero count means a suite appeared that nobody has accounted for.

- [ ] **Step 7: Update the claim**

`README.md`'s pass rate becomes 64 of 65, with `CN:02-07` named as the single exemption. Keep the claim a control-plane claim: the `TP`/`TP_C` suites move no bytes, and the README must not imply a working data plane.

- [ ] **Step 8: Commit**

```bash
git add test/tck/config.properties test/tck/run.sh test/tck/dsbox.yaml cmd/tckgate/main.go README.md
git commit -m "feat: add the transfer process consumer role to the compliance gate"
```

---

## Done

- `TP_C` reports 15 of 15 and the gate requires it; the total is 64 of 65 with one exemption.
- `TP`, `CN`, `CN_C`, `CAT`, `MET` are unchanged.
- `gofmt -l .` empty, `go vet ./...` clean, `go test -race -count=2 ./...` green.
- `GET /transfers/{id}` resolves a consumer-role row and reports its state.
- A consumer-role negotiation reaching `AGREED` writes a `store.Agreement` row.
- `docs/follow-ups.md` no longer carries the consumer-role agreement-writer entry.
