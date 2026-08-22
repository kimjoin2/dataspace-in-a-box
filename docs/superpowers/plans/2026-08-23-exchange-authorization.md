# Exchange Authorization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the participant identity §27 established load-bearing, so an
authenticated caller can only act on exchanges and agreements it is party to.

**Architecture:** Comparisons only, plus one column. The verified issuer is
already on every request (`issuerFrom`) and already stored on all four exchange
tables; this adds the checks that were never written, a shared origin gate that
closes the agreement-forging path, and a fifth `counterparty_id` column on
`agreements`. No new dependency, no new credential, no new endpoint.

**Tech Stack:** Go standard library only. SQLite via `modernc.org/sqlite`.

**Spec:** `docs/superpowers/specs/2026-08-23-exchange-authorization-design.md`

## Global Constraints

- **Go standard library only.** Ask before adding a dependency.
- **English** for all docs, comments, and user-facing error strings. Match
  `"this transfer is not yours"` (`internal/dsp/data_handler.go:80`).
- **A refusal is `403`, never `404`** (spec §4.1, DECISIONS §25.1). A `404`
  aborts the counterparty's exchange instead of reading as a refusal.
- **The ownership check runs before the state check**, so a prober learns
  nothing about progress.
- **Exchange checks carry no empty clause**: `cfg.AuthRequired() && issuer != stored`.
  **The agreement check does**: `stored != "" && issuer != stored`. Spec §4.2
  explains why they differ; do not unify them into one helper.
- **Never add a check against a counterparty this connector did not verify**
  (spec §4.3). That means consumer-role rows are never compared — their
  `CounterpartyID` came from an operator's initiate body.
- **`handleData` is not modified by any task.** Its stricter form is deliberate.
- **After every task:** `go build ./...`, `go vet ./...`, `go test -race ./...`.
- **Before the final commit of the last task:** `make tck` must print
  `65 required tests passed, 0 results outside the gate`, and `make demo` must
  print its success line.
- **Mutation testing** per this project's convention: after a check is green,
  delete it, confirm a *named* test fails, restore it.

---

### Task 1: Sign consumer-driven follow-up messages

An unrelated one-field bug in a file later tasks touch. Doing it first keeps it
out of their diffs.

`maybeDriveConsumerTransfer` builds a `store.ConsumerTransfer` without
`CounterpartyID`, so every consumer-driven follow-up goes out unsigned — the
sibling call three lines above sets it.

**Files:**
- Modify: `internal/dsp/transfer_handler.go:507-514`
- Test: `internal/dsp/transfer_handler_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing later tasks rely on.

- [ ] **Step 1: Read the two neighbouring call sites**

```bash
sed -n '495,516p' internal/dsp/transfer_handler.go
```

The call at `:499-505` passes `CounterpartyID: t.CounterpartyID`. The one at
`:507-514` does not. That is the whole bug.

- [ ] **Step 2: Write the failing test**

Add to `internal/dsp/transfer_handler_test.go`:

```go
// A consumer-driven follow-up must be addressable, or it goes out unsigned.
// The row the driver is handed is the only place its audience can come from.
func TestConsumerTransferDriverCarriesCounterparty(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	if err := st.CreateConsumerTransfer(store.ConsumerTransfer{
		ConsumerPID: "c1", ProviderPID: "p1", ProviderBaseURL: "http://provider",
		AgreementID: "urn:uuid:a", Format: "HTTP-PULL", State: TransferStarted,
		CounterpartyID: testPeer, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateConsumerTransfer: %v", err)
	}
	got, ok, err := st.GetConsumerTransfer("c1")
	if err != nil || !ok {
		t.Fatalf("GetConsumerTransfer: %v ok=%t", err, ok)
	}
	if got.CounterpartyID != testPeer {
		t.Fatalf("stored counterparty = %q, want %q", got.CounterpartyID, testPeer)
	}
}
```

- [ ] **Step 3: Run it — it passes, and that is the point**

```bash
go test ./internal/dsp/ -run TestConsumerTransferDriverCarriesCounterparty -v
```

Expected: PASS. The store round-trips fine; the bug is in the *caller*, which
this test cannot reach. Delete this test — it does not pin the defect. Instead
verify the fix by reading, and by the TCK's warning count in Step 5.

- [ ] **Step 4: Apply the fix**

In `internal/dsp/transfer_handler.go`, in the `store.ConsumerTransfer` literal
at `:507-514`, add the field so it matches its sibling:

```go
		CounterpartyID:  t.CounterpartyID,
```

- [ ] **Step 5: Verify against the TCK's own log**

```bash
go build ./... && go vet ./... && go test -race ./...
make tck
grep -c "no counterparty to address" tck-connector.txt
```

Expected: the gate prints `65 required tests passed`, and the grep prints `0`.
Before the fix it prints `4`.

- [ ] **Step 6: Commit**

```bash
git add internal/dsp/transfer_handler.go
git commit -m "fix: address consumer-driven transfer follow-ups

maybeDriveConsumerTransfer built its ConsumerTransfer without CounterpartyID
while the sibling call three lines above set it, so every consumer-driven
follow-up went out unsigned — four such warnings in a TCK run, matching the
four after-STARTED steps the harness configures. The counterparty is the
audience of the outbound credential; without it there is nobody to address."
```

---

### Task 2: Refuse an exchange message from a participant that is not party to it

The largest gap, and it needs no schema change: all four exchange tables
already carry `counterparty_id` from the verified issuer.

Five enforcement points. `handleOffers` and `handleAgreement` get **no** check —
they resolve only through `GetConsumer`, so they can never produce a
provider-role row (spec §4.3).

**Files:**
- Modify: `internal/dsp/transfer_handler.go` (`lookup`, between `:593` and `:594`)
- Modify: `internal/dsp/negotiation_handler.go` (`lookup`; `handleProviderAcceptedEvent`; `handleProviderTermination`; `handleGetNegotiation`'s provider branch)
- Test: `internal/dsp/transfer_handler_test.go`, `internal/dsp/negotiation_handler_test.go`

**Interfaces:**
- Consumes: `issuerFrom(r) string` (`internal/dsp/auth_middleware.go:22`).
- Produces: `func refuseIfNotParty(w http.ResponseWriter, r *http.Request, errType, stored string, authRequired bool) bool` in `internal/dsp/auth_middleware.go` — returns true when it wrote a refusal and the caller must return.

- [ ] **Step 1: Write the failing test for the transfer side**

Add to `internal/dsp/transfer_handler_test.go`:

```go
// A roster participant that is not party to a transfer may not move it. The
// credential admits them to the connector; it does not admit them to someone
// else's exchange.
func TestTransferLookupRefusesAStranger(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	if err := st.CreateTransfer(store.TransferProcess{
		ProviderPID: "p1", ConsumerPID: "c1", AgreementID: "urn:uuid:a",
		State: TransferStarted, CallbackAddress: "http://x", Format: "HTTP-PULL",
		CounterpartyID: testPeer, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	h := transferHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/transfers/p1", nil)
	req.SetPathValue("id", "p1")
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, "urn:participant:stranger"))
	h.handleGetTransfer(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// The party itself is unaffected.
func TestTransferLookupAllowsTheParty(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	if err := st.CreateTransfer(store.TransferProcess{
		ProviderPID: "p1", ConsumerPID: "c1", AgreementID: "urn:uuid:a",
		State: TransferStarted, CallbackAddress: "http://x", Format: "HTTP-PULL",
		CounterpartyID: testPeer, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	h := transferHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/transfers/p1", nil)
	req.SetPathValue("id", "p1")
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	h.handleGetTransfer(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// A consumer-role row is never compared: its counterparty came from an
// operator's initiate body, not from a credential this connector verified.
// The TCK depends on this — it authenticates as urn:participant:tck while
// naming itself TCK_PARTICIPANT in that body.
func TestTransferLookupDoesNotCheckConsumerRows(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	if err := st.CreateConsumerTransfer(store.ConsumerTransfer{
		ConsumerPID: "c1", ProviderPID: "p1", ProviderBaseURL: "http://provider",
		AgreementID: "urn:uuid:a", Format: "HTTP-PULL", State: TransferStarted,
		CounterpartyID: "SOME_OTHER_NAME", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateConsumerTransfer: %v", err)
	}
	h := transferHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/transfers/c1", nil)
	req.SetPathValue("id", "c1")
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	h.handleGetTransfer(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d — a consumer row must not be checked", rec.Code, http.StatusOK)
	}
}
```

- [ ] **Step 2: Run them to verify the first fails**

```bash
go test ./internal/dsp/ -run 'TestTransferLookup' -v
```

Expected: `TestTransferLookupRefusesAStranger` FAILs with `status = 200, want 403`.
The other two PASS.

- [ ] **Step 3: Add the shared helper**

Append to `internal/dsp/auth_middleware.go`:

```go
// refuseIfNotParty writes a 403 and reports true when the authenticated
// caller is not the participant this row is with. Callers return immediately
// on true.
//
// stored must come from a counterparty this connector verified — a
// provider-role row, filled from issuerFrom at creation. A consumer-role row's
// counterparty came from the request body of an operator's own initiate call,
// which is a string the caller chose; comparing against it is not
// authorization, and the DSP TCK demonstrates why (it authenticates as
// urn:participant:tck while naming itself TCK_PARTICIPANT in that body).
//
// 403, not 404: DECISIONS.md section 25.1 makes every DSP rejection
// [400, 500) and never 404, because the counterparty's client checks for 404
// before it checks whether an error was expected and aborts the whole
// exchange on one. This is the same answer, and the same wording, the data
// endpoint already gives.
//
// No empty-stored clause, matching handleData: a row with no counterparty
// predates authentication and is served to nobody. The agreement check in
// handleTransferRequest deliberately differs — see the design spec's section
// 4.2.
func refuseIfNotParty(w http.ResponseWriter, r *http.Request, errType, stored string, authRequired bool) bool {
	if issuer := issuerFrom(r); authRequired && issuer != stored {
		slog.Warn("refuse a message about an exchange the sender is not party to",
			"issuer", issuer, "expected", stored, "path", r.URL.Path)
		writeError(w, errType, http.StatusForbidden, "this exchange is not yours")
		return true
	}
	return false
}
```

Add `"log/slog"` and `"net/http"` to that file's imports if absent.

- [ ] **Step 4: Wire the transfer side**

In `internal/dsp/transfer_handler.go`, inside `lookup`, replace the final
return (currently `:594`) so the check sits after `GetTransfer` succeeded and
after the consumer branch has already returned:

```go
	if refuseIfNotParty(w, r, TransferErrorType, t.CounterpartyID, h.cfg.AuthRequired()) {
		return resolvedTransfer{}, false
	}
	return resolvedTransfer{TransferProcess: t}, true
```

**Do not** put this in `handleGetTransfer`, in `applyTransition`, or against
the returned struct: `resolvedTransfer` carries `CounterpartyID` for consumer
rows too (`:571`), and any of those placements silently refuses all fifteen
TP_C results.

- [ ] **Step 5: Run the transfer tests**

```bash
go test ./internal/dsp/ -run 'TestTransferLookup' -v
```

Expected: all three PASS.

- [ ] **Step 6: Write the failing test for the negotiation side**

Add to `internal/dsp/negotiation_handler_test.go`:

```go
// The four provider-role negotiation resolvers each refuse a stranger. They
// are listed individually because three of them do their own two-table
// dispatch and are not reached through lookup.
func TestNegotiationResolversRefuseAStranger(t *testing.T) {
	t.Parallel()
	const stranger = "urn:participant:stranger"
	cases := []struct {
		name   string
		method string
		path   string
		call   func(h negotiationHandler, w http.ResponseWriter, r *http.Request)
	}{
		{"get", http.MethodGet, "/negotiations/p1",
			func(h negotiationHandler, w http.ResponseWriter, r *http.Request) { h.handleGetNegotiation(w, r) }},
		{"events", http.MethodPost, "/negotiations/p1/events",
			func(h negotiationHandler, w http.ResponseWriter, r *http.Request) { h.handleEvent(w, r) }},
		{"termination", http.MethodPost, "/negotiations/p1/termination",
			func(h negotiationHandler, w http.ResponseWriter, r *http.Request) { h.handleTermination(w, r) }},
		{"re-request", http.MethodPost, "/negotiations/p1/request",
			func(h negotiationHandler, w http.ResponseWriter, r *http.Request) { h.handleReRequest(w, r) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
			now := time.Now()
			if err := st.Create(store.Negotiation{
				ProviderPID: "p1", ConsumerPID: "c1", State: StateOffered,
				DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
				CallbackAddress: "http://x", CounterpartyID: testPeer,
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			h := negotiationHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			req.SetPathValue("id", "p1")
			req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, stranger))
			tc.call(h, rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
			}
		})
	}
}
```

- [ ] **Step 7: Run it to verify it fails**

```bash
go test ./internal/dsp/ -run TestNegotiationResolversRefuseAStranger -v
```

Expected: all four subtests FAIL — none returns 403 today.

- [ ] **Step 8: Wire the four negotiation points**

In `internal/dsp/negotiation_handler.go`:

`lookup` — after the `!ok` block, before `return n, true, nil`:

```go
	if refuseIfNotParty(w, r, ContractNegotiationErrorType, n.CounterpartyID, h.cfg.AuthRequired()) {
		return store.Negotiation{}, false, nil
	}
```

`handleProviderAcceptedEvent` — as the first statement of the function body:

```go
	if refuseIfNotParty(w, r, ContractNegotiationErrorType, n.CounterpartyID, h.cfg.AuthRequired()) {
		return
	}
```

`handleProviderTermination` — as the first statement of the function body, the
same three lines.

`handleGetNegotiation` — inline in the provider branch, before `writeJSON`.
There is no branch function to hold it:

```go
	} else if ok {
		if refuseIfNotParty(w, r, ContractNegotiationErrorType, n.CounterpartyID, h.cfg.AuthRequired()) {
			return
		}
		writeJSON(w, http.StatusOK, buildNegotiationStateDocument(n))
		return
	}
```

Leave `handleOffers` and `handleAgreement`
(`internal/dsp/negotiation_consumer_handler.go`) untouched.

- [ ] **Step 9: Run the full suite**

```bash
go build ./... && go vet ./... && go test -race ./...
```

Expected: all pass. If an existing test fails, check whether it seeds a row
with a non-empty `CounterpartyID` and drives the handler without an issuer —
that combination is now a refusal and the test's fixture needs an issuer.

- [ ] **Step 10: Mutation-test each of the five points**

For each of the five insertion points in turn: delete the check, run
`go test ./internal/dsp/ -run 'RefusesAStranger'`, confirm a **named** subtest
fails, restore it. Five deletions, five named failures.

- [ ] **Step 11: Confirm the TCK is unmoved**

```bash
make tck
```

Expected: `65 required tests passed, 0 results outside the gate`. If TP_C
results fail, the check was placed where it sees consumer rows — re-read
Step 4.

- [ ] **Step 12: Commit**

```bash
git add internal/dsp/auth_middleware.go internal/dsp/transfer_handler.go \
        internal/dsp/negotiation_handler.go internal/dsp/transfer_handler_test.go \
        internal/dsp/negotiation_handler_test.go
git commit -m "feat: refuse an exchange message from a participant not party to it

DECISIONS §23.11 predicted this was closed by enforcing the connector-to-
connector JWT rather than by patching handlers one at a time. The JWT shipped
and narrowed the attacker set from anyone on the network to any roster
participant, which is not closure: a roster is shared by strangers.

Five provider-role resolvers, not the two lookup helpers — handleEvent,
handleTermination and handleGetNegotiation each dispatch on their own, and
handleTermination is the harm §23.11 named. Consumer-role rows are never
compared: their counterparty came from an operator's initiate body, which is a
string the caller chose."
```

---

### Task 3: Refuse to serve data as provider under an agreement held as consumer

Closes the agreement-forging path's byte exit. A peer can initiate a
negotiation naming itself as provider, forge the agreement that comes back,
and cite it in a transfer request. The forged row is not detectably forged —
it is exactly what an honest provider sends — so the defect is caught at
consumption, where the role confusion is visible.

**Files:**
- Modify: `internal/dsp/transfer_handler.go` (`handleTransferRequest`, `hasSourceFor`, `driveTransfer`)
- Modify: `internal/dsp/data_handler.go` (`datasetFor`)
- Test: `internal/dsp/transfer_handler_test.go`

**Interfaces:**
- Consumes: `store.OriginAgreed` (`internal/store/store.go:99-109`).
- Produces: `func servableAsProvider(a store.Agreement) bool` in `internal/dsp/transfer_handler.go`.

- [ ] **Step 1: Write the failing test**

Add to `internal/dsp/transfer_handler_test.go`:

```go
// An agreement this connector accepted as consumer never authorizes it to
// serve as provider. That is the only exit a forged agreement has to bytes:
// handleAgreement is the sole writer of OriginAgreed, and an attacker cannot
// reach the other two origins.
func TestTransferRequestRefusesAConsumerRoleAgreement(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateAgreement(store.Agreement{
		AgreementID: "urn:agreement:forged", DatasetID: "urn:dataset:a",
		Origin: store.OriginAgreed, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	h := transferHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

	body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferRequestMessageType +
		`","consumerPid":"c1","agreementId":"urn:agreement:forged",` +
		`"format":"HTTP-PULL","callbackAddress":"http://consumer"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/transfers/request", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	h.handleTransferRequest(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// The two origins this connector may serve under are unaffected.
func TestTransferRequestAcceptsProviderRoleAgreements(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{store.OriginNegotiated, store.OriginImported} {
		t.Run(origin, func(t *testing.T) {
			st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
			if err := st.CreateAgreement(store.Agreement{
				AgreementID: "urn:uuid:a", DatasetID: "urn:dataset:a",
				Origin: origin, CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("CreateAgreement: %v", err)
			}
			h := transferHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

			body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferRequestMessageType +
				`","consumerPid":"c1","agreementId":"urn:uuid:a",` +
				`"format":"HTTP-PULL","callbackAddress":"http://consumer"}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/transfers/request", strings.NewReader(body))
			req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
			h.handleTransferRequest(rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify the first fails**

```bash
go test ./internal/dsp/ -run 'TestTransferRequest(Refuses|Accepts)' -v
```

Expected: `...RefusesAConsumerRoleAgreement` FAILs with `status = 201, want 403`.
Both subtests of the second PASS.

- [ ] **Step 3: Add the predicate**

In `internal/dsp/transfer_handler.go`, above `hasSourceFor`:

```go
// servableAsProvider reports whether this connector may serve data as the
// provider under a. OriginAgreed means the opposite of what a transfer
// request asks for: this connector accepted that agreement as the *consumer*,
// from a counterparty's ContractAgreementMessage, so serving bytes under it
// would be role confusion regardless of who asked.
//
// It is also the only exit a forged agreement has. handleAgreement writes the
// message body verbatim and is the sole writer of OriginAgreed, so a peer that
// initiates a negotiation naming itself as provider can mint a row — but it
// cannot reach OriginImported, which is behind the management token on a
// localhost listener, or OriginNegotiated, whose id this connector generates.
// The forged row itself is not detectably forged: it is exactly the message an
// honest provider sends in a negotiation the peer legitimately owns.
func servableAsProvider(a store.Agreement) bool {
	return a.Origin != store.OriginAgreed
}
```

- [ ] **Step 4: Gate the four provider-role readers**

`handleTransferRequest` — bind the agreement instead of discarding it, and add
the branch after the `!ok` branch:

```go
	a, ok, err := h.store.GetAgreement(msg.AgreementID)
	if err != nil {
		slog.Error("get agreement", "agreement_id", msg.AgreementID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if !ok {
		slog.Warn("refuse transfer request citing an agreement this connector has no record of",
			"agreement_id", msg.AgreementID)
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"no agreement with id "+msg.AgreementID)
		return
	}
	if !servableAsProvider(a) {
		slog.Warn("refuse transfer request citing an agreement this connector holds as consumer",
			"agreement_id", msg.AgreementID, "origin", a.Origin)
		writeError(w, TransferErrorType, http.StatusForbidden,
			"this connector is not the provider on that agreement")
		return
	}
```

`hasSourceFor` — after the `GetAgreement` error/ok check:

```go
	if !servableAsProvider(a) {
		return false
	}
```

`driveTransfer` — inside the `else if ok` branch, leave `datasetID` empty when
the agreement is not servable:

```go
	} else if ok && servableAsProvider(a) {
		datasetID = a.DatasetID
	}
```

`datasetFor` in `internal/dsp/data_handler.go` — after the `err != nil || !ok`
guard:

```go
	if !servableAsProvider(a) {
		return config.Dataset{}, false
	}
```

- [ ] **Step 5: Run the tests**

```bash
go test ./internal/dsp/ -run 'TestTransferRequest(Refuses|Accepts)' -v
go build ./... && go vet ./... && go test -race ./...
```

Expected: all pass.

- [ ] **Step 6: Mutation-test**

Change `servableAsProvider` to `return true`, run
`go test ./internal/dsp/ -run TestTransferRequestRefusesAConsumerRoleAgreement`,
confirm it fails by name, restore.

- [ ] **Step 7: Confirm the TCK and demo**

```bash
make tck && make demo
```

Expected: `65 required tests passed`; the demo prints its success line. The
TCK's twelve seeded agreements are `OriginImported`, and the demo's provider
holds `OriginNegotiated`, so neither meets the new refusal.

- [ ] **Step 8: Commit**

```bash
git add internal/dsp/transfer_handler.go internal/dsp/data_handler.go \
        internal/dsp/transfer_handler_test.go
git commit -m "feat: refuse to serve as provider under an agreement held as consumer

A roster participant can initiate a negotiation naming itself as provider, read
this connector's own consumer pid out of the request that arrives, post a forged
ContractAgreementMessage to it, and cite the resulting row in a transfer
request. handleAgreement writes the body verbatim, so the row is real.

The forged message is not detectably forged — it is exactly what an honest
provider sends in a negotiation the peer legitimately owns — so no intake check
catches it. What is visible at consumption is the role: OriginAgreed means this
connector is the consumer on that contract, and serving bytes under it is
always wrong. transfer_processes has one writer and this gate sits in front of
it, which closes the byte exit completely.

What it does not close is recorded in the spec: the row survives, so id
squatting, the duplicate-id oracle, and a forged row indistinguishable in
GET /agreements all remain."
```

---

### Task 4: Publish the verified counterparty in `Agreement.assignee`

A correctness fix with no security content. The placeholder's justification was
falsified by §27.

**Files:**
- Modify: `internal/dsp/negotiation.go:316-327` (comment), `:450` (the field)
- Test: `internal/dsp/negotiation_test.go`

**Interfaces:**
- Consumes: `store.Negotiation.CounterpartyID`.
- Produces: nothing later tasks rely on.

- [ ] **Step 1: Write the failing test**

Add to `internal/dsp/negotiation_test.go`:

```go
// assignee names the party the rights are granted to. Since connector
// authentication, that party has a verified identity on the negotiation row.
func TestAgreementAssigneeIsTheVerifiedCounterparty(t *testing.T) {
	t.Parallel()
	n := testStoredNegotiation()
	n.CounterpartyID = testPeer
	msg := buildAgreementMessage(config.Config{ParticipantID: testSelf}, n)
	if msg.Agreement.Assignee != testPeer {
		t.Fatalf("assignee = %q, want %q", msg.Agreement.Assignee, testPeer)
	}
}

// With authentication off there is no verified identity, and the field is
// required to be present. The consumer pid remains the honest placeholder.
func TestAgreementAssigneeFallsBackWithoutAuth(t *testing.T) {
	t.Parallel()
	n := testStoredNegotiation()
	n.CounterpartyID = ""
	msg := buildAgreementMessage(config.Config{ParticipantID: testSelf}, n)
	if msg.Agreement.Assignee != n.ConsumerPID {
		t.Fatalf("assignee = %q, want %q", msg.Agreement.Assignee, n.ConsumerPID)
	}
}
```

- [ ] **Step 2: Run to verify the first fails**

```bash
go test ./internal/dsp/ -run TestAgreementAssignee -v
```

Expected: `...IsTheVerifiedCounterparty` FAILs — it gets the consumer pid.
`...FallsBackWithoutAuth` PASSes already.

- [ ] **Step 3: Change the field**

In `buildAgreementMessage` (`internal/dsp/negotiation.go`), replace
`Assignee: n.ConsumerPID` with a value computed just above the literal:

```go
	// The party the rights are granted to. Since connector authentication
	// (DECISIONS.md section 27) that party has a verified identity on the
	// negotiation row; with authentication off there is none, and the consumer
	// pid remains the placeholder it always was.
	assignee := n.CounterpartyID
	if assignee == "" {
		assignee = n.ConsumerPID
	}
```

and use `Assignee: assignee` in the literal.

- [ ] **Step 4: Rewrite the stale comment**

In `internal/dsp/negotiation.go:316-327`, replace the clause beginning "and
negotiation is unauthenticated in v1" through "used as an honest placeholder"
with:

```go
// assignee is the counterparty being granted them. v1's negotiation messages
// still carry no participant identifier for the consumer
// (ContractRequestMessage has only consumerPid, offer, callbackAddress —
// checked against the TCK's own contract-request-message-schema.json), but
// since connector authentication (DECISIONS.md section 27) the identity is
// available from the transport: the request that opened the negotiation was
// verified against the roster and its issuer is on the row. n.ConsumerPID
// survives only as the fallback for a connector running with authentication
// off, where there is no verified identity to name.
```

- [ ] **Step 5: Run the tests**

```bash
go test ./internal/dsp/ -run TestAgreementAssignee -v
go build ./... && go vet ./... && go test -race ./...
```

Expected: all pass. `negotiation_test.go:216`'s existing assertion still holds
because `testStoredNegotiation()` leaves `CounterpartyID` empty.

- [ ] **Step 6: Confirm the wire**

```bash
make tck
```

Expected: `65 required tests passed`. CN passes today with a bare UUID in that
field, so nothing asserts its value; a `urn:participant:` string is strictly
better formed.

- [ ] **Step 7: Commit**

```bash
git add internal/dsp/negotiation.go internal/dsp/negotiation_test.go
git commit -m "feat: name the verified counterparty in an agreement's assignee

The placeholder's justification said negotiation is unauthenticated in v1 'so
there is no participant identity to put here even from a trust boundary'. §27
falsified that: handleContractRequest records the verified issuer on the very
row buildAgreementMessage receives. The consumer pid stays as the fallback for
a connector running with authentication off, which is also what keeps the
existing assertion meaningful."
```

---

### Task 5: Record who an agreement is with

**Files:**
- Modify: `internal/store/store.go` (struct, schema literal, `migrate` loop, four SQL statements)
- Modify: `internal/dsp/negotiation_handler.go` (the `OriginNegotiated` writer)
- Modify: `internal/dsp/negotiation_consumer_handler.go` (the `OriginAgreed` writer)
- Modify: `internal/mgmt/router.go` (import body, `agreementView`)
- Test: `internal/store/store_test.go`, `internal/mgmt/router_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `store.Agreement.CounterpartyID string`, persisted; `agreementView.CounterpartyID string` with JSON tag `counterpartyId`, **declared last**.

- [ ] **Step 1: Write the failing store test**

Add to `internal/store/store_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/store/ -run TestAgreementRoundTripsItsCounterparty -v
```

Expected: compile error — `Agreement` has no field `CounterpartyID`.

- [ ] **Step 3: Add the field and the column**

In `internal/store/store.go`:

Add to the `Agreement` struct, after `Origin`:

```go
	// CounterpartyID is the participant this agreement is with, recorded so a
	// message citing it can be checked against who sent it. Role-relative like
	// the same column on the exchange tables: for a negotiated agreement it is
	// the consumer, for one this connector accepted as consumer it is the
	// provider, and for an imported one it is whoever the operator named.
	// Empty means not known — an agreement imported before this column existed,
	// or one imported without naming a counterparty — and is permitted rather
	// than refused. See the design spec's section 4.2.
	CounterpartyID string
```

Add `counterparty_id TEXT NOT NULL DEFAULT ''` to `agreementSchema`.

Add `"agreements"` to `migrate`'s table loop.

Update the four SQL statements to carry the column:
`CreateAgreement`'s INSERT (`:548`), `GetAgreement`'s SELECT (`:565`),
`CreateAgreementIfNegotiationAgreed`'s INSERT (`:621`), `ListAgreements`'
SELECT (`:838`) — each gains `counterparty_id` in its column list, its
placeholder list, and its `Scan` target list.

- [ ] **Step 4: Run the store tests**

```bash
go test ./internal/store/ -v
```

Expected: all pass.

- [ ] **Step 5: Populate it from both writers**

`internal/dsp/negotiation_handler.go`, the `store.Agreement` literal in the
`OriginNegotiated` path: add `CounterpartyID: n.CounterpartyID,`.

`internal/dsp/negotiation_consumer_handler.go:266`, the `OriginAgreed` literal:
add `CounterpartyID: n.CounterpartyID,`.

- [ ] **Step 6: Accept it on import**

In `internal/mgmt/router.go`, add to `importAgreement`'s body struct:

```go
		CounterpartyID string `json:"counterpartyId"`
```

and to the `store.Agreement` literal: `CounterpartyID: body.CounterpartyID,`.
It stays optional — required would break `test/tck/run.sh` and every existing
operator import in lockstep.

- [ ] **Step 7: Expose it, last**

Add to `agreementView`, **after** `CreatedAt`:

```go
	CounterpartyID string `json:"counterpartyId"`
```

and set it in the literal that builds the view.

**The position is load-bearing.** Go emits struct fields in declaration order,
and `demo/run.sh`'s resume round extracts its agreement with a `sed` that
requires `agreementId` and `datasetId` to be adjacent in the JSON. Grouping the
new field beside `agreementId` breaks the demo.

- [ ] **Step 8: Write the management-API test**

Add to `internal/mgmt/router_test.go`:

```go
func TestImportAgreementRecordsAnOptionalCounterparty(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body, want string }{
		{"named", `{"agreementId":"urn:uuid:a","datasetId":"urn:dataset:a","counterpartyId":"urn:participant:peer"}`, "urn:participant:peer"},
		{"omitted", `{"agreementId":"urn:uuid:b","datasetId":"urn:dataset:a"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
			h := NewRouter(config.Config{MgmtToken: "t"}, st)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/agreements", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer t")
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
			}
			var id string
			if tc.name == "named" {
				id = "urn:uuid:a"
			} else {
				id = "urn:uuid:b"
			}
			a, ok, err := st.GetAgreement(id)
			if err != nil || !ok {
				t.Fatalf("GetAgreement: %v ok=%t", err, ok)
			}
			if a.CounterpartyID != tc.want {
				t.Fatalf("counterparty = %q, want %q", a.CounterpartyID, tc.want)
			}
		})
	}
}
```

- [ ] **Step 9: Run everything, including the demo**

```bash
go build ./... && go vet ./... && go test -race ./...
make tck && make demo
```

Expected: all green. If the demo fails at `no resume-scenario agreement was
concluded`, the new field was declared before `datasetId` — move it after
`createdAt`.

- [ ] **Step 10: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go \
        internal/dsp/negotiation_handler.go internal/dsp/negotiation_consumer_handler.go \
        internal/mgmt/router.go internal/mgmt/router_test.go
git commit -m "feat: record who an agreement is with

The fifth table gets the column the other four got with connector
authentication. Both writers fill it from the negotiation row's verified
counterparty; POST /agreements takes an optional one, because required would
break the TCK's twelve seeded imports and every existing operator import in
lockstep, and an empty owner has to keep meaning 'not known'.

agreementView exposes it after createdAt rather than beside agreementId: Go
emits fields in declaration order and demo/run.sh's resume sed requires
agreementId and datasetId to stay adjacent."
```

---

### Task 6: Refuse a transfer request citing someone else's agreement

**Files:**
- Modify: `internal/dsp/transfer_handler.go` (`handleTransferRequest`)
- Test: `internal/dsp/transfer_handler_test.go`

**Interfaces:**
- Consumes: `store.Agreement.CounterpartyID` (Task 5).
- Produces: nothing later tasks rely on.

- [ ] **Step 1: Write the failing test**

Add to `internal/dsp/transfer_handler_test.go`:

```go
func TestTransferRequestChecksAgreementOwnership(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		owner string
		issuer string
		want  int
	}{
		{"the party", testPeer, testPeer, http.StatusCreated},
		{"a stranger", testPeer, "urn:participant:stranger", http.StatusForbidden},
		{"no owner recorded", "", "urn:participant:anyone", http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
			if err := st.CreateAgreement(store.Agreement{
				AgreementID: "urn:uuid:a", DatasetID: "urn:dataset:a",
				Origin: store.OriginImported, CounterpartyID: tc.owner,
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("CreateAgreement: %v", err)
			}
			h := transferHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

			body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferRequestMessageType +
				`","consumerPid":"c1","agreementId":"urn:uuid:a",` +
				`"format":"HTTP-PULL","callbackAddress":"http://consumer"}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/transfers/request", strings.NewReader(body))
			req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, tc.issuer))
			h.handleTransferRequest(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify the stranger case fails**

```bash
go test ./internal/dsp/ -run TestTransferRequestChecksAgreementOwnership -v
```

Expected: `a stranger` FAILs with `status = 201, want 403`. The other two PASS.

- [ ] **Step 3: Add the check**

In `handleTransferRequest`, after the `servableAsProvider` branch from Task 3:

```go
	// The empty clause is what the exchange checks deliberately lack. An
	// imported agreement may legitimately have no owner — POST /agreements
	// takes it optionally, and the TCK seeds twelve without one — so an
	// unnamed owner has to keep meaning "not known" rather than "nobody". That
	// leaves imports without a named counterparty exactly as open as they were.
	if issuer := issuerFrom(r); h.cfg.AuthRequired() && a.CounterpartyID != "" && issuer != a.CounterpartyID {
		slog.Warn("refuse transfer request citing an agreement with another participant",
			"agreement_id", msg.AgreementID, "issuer", issuer, "expected", a.CounterpartyID)
		writeError(w, TransferErrorType, http.StatusForbidden,
			"that agreement is not yours")
		return
	}
```

`handleTransferInitiate` gets **no** check: its caller is this connector's own
operator, and comparing the initiate body's `providerId` against a counterparty
that was itself filled from a `providerId` compares two unverified strings.

- [ ] **Step 4: Run the tests**

```bash
go test ./internal/dsp/ -run TestTransferRequestChecksAgreementOwnership -v
go build ./... && go vet ./... && go test -race ./...
```

Expected: all pass.

- [ ] **Step 5: Mutation-test**

Delete the `a.CounterpartyID != ""` clause, run the test, confirm
`no owner recorded` fails by name. Restore. Then delete the whole check,
confirm `a stranger` fails by name. Restore.

- [ ] **Step 6: Confirm the TCK and demo**

```bash
make tck && make demo
```

Expected: `65 required tests passed` — the twelve seeded agreements have no
owner and take the permitted path. The demo's agreements carry
`urn:participant:consumer`, which is what its transfers present.

- [ ] **Step 7: Commit**

```bash
git add internal/dsp/transfer_handler.go internal/dsp/transfer_handler_test.go
git commit -m "feat: refuse a transfer request citing another participant's agreement

Knowing an agreement id was enough to open a transfer under it and pass the
data endpoint's ownership check, because the transfer's counterparty is
whoever asked. Imported ids are operator-chosen and this repo's own fixtures
use guessable ones.

An empty owner stays permitted, unlike the exchange checks: POST /agreements
takes the counterparty optionally and the TCK seeds twelve without one, so
refusing would fail all thirty TP results. Imports without a named owner are
therefore exactly as open as before — stated in the spec rather than implied."
```

---

### Task 7: Record the decision and correct what it falsifies

**Files:**
- Modify: `DECISIONS.md` (new §32; corrections to §23.11, §24.2, §25.1, §25.2, §25.3)
- Modify: `README.md` (auth paragraph)
- Modify: `docs/follow-ups.md` (delete the three closed entries)
- Modify: `docs/milestone-sequence.md`
- Modify: code comments listed in spec §7

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Write §32**

Append to `DECISIONS.md` a section `## 32. An agreement records who it is with,
and the exchange endpoints check it`, with subsections carrying: §23.11's
falsified prediction as the justification; the four decisions of spec §4; and
four trade-offs — an imported agreement with no owner stays open; there is no
update path (§25.3) so an operator who names the wrong participant on import
has locked the real one out; the residuals §4.4 leaves (id squatting, the
duplicate-id oracle, the forged audit row); and that a connector initiating
against its own public address, which today half-works by accident, is now
refused outright.

- [ ] **Step 2: Make the five in-place corrections**

Facts, not reasoning — a superseding section is the wrong instrument:

- §25.2 "exactly two writers" → three, naming `OriginAgreed`.
- §25.3 "takes two strings" → two required and one optional. Keep "there is no
  update path and no delete path" **verbatim** — `importAgreement`'s duplicate
  re-query depends on it and it stays true.
- §25.3 "records an agreement and nothing more" → note `GET /agreements`.
- §23.11 and §24.2 → the prediction shipped and did not close the gap.
- §25.1 "matched by id alone" → conditional.

- [ ] **Step 3: Correct the code comments**

Per spec §7: the four `CounterpartyID` doc comments
(`internal/store/store.go:40-45, 67-72, 122-127, 727-731`) which call it an
addressing field; `Agreement`'s `ConsumerPID` comment (`:91-93`); both `lookup`
doc comments; `handleTransferRequest`'s "looked up by its id alone"; the two
initiate-hook comments still saying "open to anonymous callers"; and
`internal/dsp/transfer_handler.go:548-551`, which claims the negotiation
resolvers try the consumer table first — they try the provider table first.

- [ ] **Step 4: Update README and delete the follow-ups**

README's auth paragraph currently implies ownership is checked at one endpoint.
Delete all three entries under "From the policy cross-check (2026-08)" in
`docs/follow-ups.md`, per that file's own rule that an entry goes when it is
fixed — but keep whatever part of the third entry describes the residuals §4.4
does not close, moved to a fresh entry.

- [ ] **Step 5: Correct `docs/milestone-sequence.md`**

Stale independently of this work: milestones 3 and 4 shipped (§26, §29), and
two shipped milestones are absent (§27 roster signing, §31 range/resumption).
Mark 3 and 4 done with DECISIONS pointers, add §27 and §31, then add this
milestone with its ordering argument — no TCK test sends a message about
another participant's exchange, so the TCK is a regression gate and evidence is
unit tests, which is milestone 2's situation.

- [ ] **Step 6: Final gate**

```bash
go build ./... && go vet ./... && go test -race ./...
make tck && make demo
```

Expected: `65 required tests passed, 0 results outside the gate`, and the demo
prints its success line.

- [ ] **Step 7: Commit**

```bash
git add DECISIONS.md README.md docs/ internal/
git commit -m "docs: record exchange authorization as §32

§23.11 predicted these gaps were closed by enforcing the connector-to-connector
JWT rather than by patching handlers one at a time. It shipped and narrowed the
attacker set without closing anything, which is why §23.11 rather than §25.2 is
the decision this overturns.

Five in-place corrections alongside, all facts rather than reasoning: §25.2's
writer count, §25.3's two strings and its 'nothing more', §24.2's inherited
posture, and §25.1's 'by id alone'. §25.3's no-update-no-delete clause stays
verbatim — importAgreement's duplicate re-query depends on it and it stays true.

milestone-sequence.md is corrected in the same pass; it had stopped being the
authority two milestones ago."
```
