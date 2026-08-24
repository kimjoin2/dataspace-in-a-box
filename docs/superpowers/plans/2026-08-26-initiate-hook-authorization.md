# Initiate Hook Authorization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the two initiate hooks to the management listener, make them
refuse a `providerId` the roster does not list, and guard every consumer-role
resolver — closing both consequences `DECISIONS.md` §32.3 deferred.

**Architecture:** The hooks stay in package `dsp` as code and move to the
management listener as routes: `dsp.NewRouter` returns them, `cmd/dsbox` hands
them to `mgmt.NewRouter`. The roster reaches the handlers as a nil-able
`func(string) bool` predicate, so a nil value is the check being absent rather
than a check that refuses everyone. With the counterparty then supplied by an
authenticated operator, the existing `refuseIfNotParty` is applied at all six
consumer-role resolvers.

**Tech Stack:** Go standard library only. SQLite via the existing store. No
new dependencies — ask before adding one.

**Spec:** `docs/superpowers/specs/2026-08-24-initiate-hook-authorization-design.md`

## Global Constraints

- Go standard library only. Ask before adding any dependency.
- English for all documentation, code comments, error strings, and commit
  messages.
- No emoji anywhere.
- Final gates, all four required: `go vet ./...`, `go test -race ./...`,
  `make tck` (must stay 65 of 65), `make demo`.
- Never put a count in a code comment. If a sentence would say "three call
  sites" or "seven places", rewrite it without the number. This plan's own
  spec violated that rule five times in its first draft.
- Every documentation edit names the code fact it was checked against. Verify
  the sentence against the code before writing it, not after.
- Work happens directly on `main` — the user authorised this for this session.
  Do not create a worktree. Do not push; the user approves each push
  separately.
- `httptest.ResponseRecorder` does not enforce response framing or routing.
  Anything about routing, status codes from a mux, or `Allow` headers must be
  tested through `httptest.NewServer` or a real `http.ServeMux`.
- Existing tests build `config.Config` as a struct literal, bypassing
  `config.Load`'s defaults. `Config.AuthRequired()` returns true when
  `RequireAuth` is nil, so a bare `config.Config{}` has authentication ON.

## Task ordering is forced

Do not reorder these tasks. Each ordering constraint below was measured, and
violating it leaves `make tck` red at a commit boundary:

- **Task 1 before Tasks 2 and 3.** Until the harness's authenticated identity
  equals the `providerId` it sends, Task 2 refuses every TCK initiate call
  (400) and Task 3 refuses every inbound consumer-role message (403). That is
  the 30-result loss `DECISIONS.md` §32.3 and `docs/milestone-sequence.md`
  both measured.
- **Task 2 before Task 3.** Task 3's guards are only *justified* by Task 2's
  rule. Both are green in either order, so this ordering is about the
  reasoning the code will carry, not the gate.
- **Task 4 is one commit.** Splitting it leaves a commit where either the
  seeding or the initiate calls fail.
- **Task 5 after Task 4.** It reads through a management route that Task 4
  puts the harness in a position to call.

---

## File Structure

**Modified — production code:**

- `internal/dsp/negotiation_handler.go` — `negotiationHandler` gains
  `knownParticipant`; `handleGetNegotiation`'s consumer branch gains a guard.
- `internal/dsp/negotiation_consumer_handler.go` — `handleInitiate` gains the
  roster check; `handleOffers`, `handleAgreement`,
  `handleConsumerFinalizedEvent`, `handleConsumerTermination` gain guards.
- `internal/dsp/transfer_handler.go` — `transferHandler` gains
  `knownParticipant`; `lookup`'s consumer branch gains a guard.
- `internal/dsp/transfer_consumer_handler.go` — `handleTransferInitiate` gains
  the roster check.
- `internal/dsp/router.go` — builds the predicate, populates a new return
  struct at **both** return statements, stops registering the two routes.
- `internal/dsp/auth_middleware.go` — `refuseIfNotParty`'s doc comment.
- `internal/mgmt/router.go` — `NewRouter` takes the two handlers and mounts
  them behind `authenticated`.
- `cmd/dsbox/main.go` — the wiring between the two.
- `internal/store/store.go`, `internal/config/config.go` — doc comments only.

**Modified — harness:**

- `test/tck/run.sh`, `test/tck/compose.yaml`, `test/tck/dsbox.yaml`,
  `test/tck/config.properties`, `demo/run.sh`, `demo/compose.yaml`.

**Created:**

- `internal/mgmt/route_coverage_test.go` — the management-side replacement for
  the guard the DSP routes leave behind.

**Explicitly NOT modified:**

- `internal/dsp/auth_middleware_test.go`. Removing two routes still satisfies
  every assertion in it. A change here would be wrong.

---

## Task 1: Correct the harness's identity

The TCK hardcodes `TCK_PARTICIPANT` as the `providerId` in both initiate
bodies and is not configurable. `test/tck/run.sh` currently mints its
credential with a different name it chose itself. This task makes the two
agree, which is what every later task depends on.

**Files:**
- Modify: `test/tck/run.sh` (two roster heredocs, the mint's `-iss`, comments)
- Modify: `test/tck/dsbox.yaml` (the roster comment naming the old identity)

**Interfaces:**
- Consumes: nothing.
- Produces: a TCK harness whose credential issuer is `TCK_PARTICIPANT` and
  whose generated roster lists that id. Tasks 2 and 3 depend on this.

- [ ] **Step 1: Change the first roster heredoc**

In `test/tck/run.sh`, the unsigned heredoc currently reads:

```sh
cat >"$identity/roster.json" <<EOF
{
  "participants": [
    {"id": "urn:participant:dsbox-test", "public_key": "$connector_pub"},
    {"id": "urn:participant:tck", "public_key": "$tck_pub"}
  ]
}
EOF
```

Change `urn:participant:tck` to `TCK_PARTICIPANT`, and add a comment
immediately above the `cat` explaining the coupling:

```sh
# The TCK's id is TCK_PARTICIPANT because that is the string the harness
# hardcodes as the providerId in both initiate bodies
# (DspConstants.TCK_PARTICIPANT_ID in the pinned image, inlined at every call
# site and not configurable). Its authenticated identity has to equal the
# name it claims, or the connector records a counterparty no inbound request
# will ever present. That coupling is to a constant in an upstream image; it
# is safe because compose.yaml pins that image by digest, and if the pin ever
# moves and the constant changes, the symptom is every CN_C and TP_C result
# failing on a refusal that reads like a protocol bug.
cat >"$identity/roster.json" <<EOF
```

- [ ] **Step 2: Change the second roster heredoc**

The signed heredoc a few lines below repeats the same participant list. Change
`urn:participant:tck` to `TCK_PARTICIPANT` there too.

**Both heredocs must change.** The signature is computed over the participant
list from the first file and pasted into the second; if the two lists differ,
`LoadRoster` fails the signature check and the connector refuses to start —
the run dies at the readiness probe with no useful message.

- [ ] **Step 3: Change the mint's issuer**

Find the `dsops token` invocation and change `-iss urn:participant:tck` to
`-iss TCK_PARTICIPANT`. Leave `-aud` alone.

- [ ] **Step 4: Correct the roster comment in `test/tck/dsbox.yaml`**

That file's comment above `roster:` says the roster carries this connector and
"the TCK, which authenticates as `urn:participant:tck`". Replace the identity
with `TCK_PARTICIPANT`. **Verify before writing:** open `test/tck/run.sh` and
confirm the id you write matches both heredocs and the `-iss` you just
changed.

- [ ] **Step 5: Run the TCK**

Run: `make tck`
Expected: 65 of 65, exactly as before.

The one thing this changes on the wire is the `assignee` on agreements the CN
suite concludes, because the provider role fills it from the verified issuer.
Nothing asserts that value — `docs/superpowers/specs/2026-08-23-exchange-authorization-design.md`
records CN passing with a bare UUID there, which is not even an IRI.

If the run fails at the readiness probe, Step 2 was missed.

- [ ] **Step 6: Commit**

```bash
git add test/tck/run.sh test/tck/dsbox.yaml
git commit -m "test: the harness authenticates as the name it already claims"
```

---

## Task 2: Refuse a providerId the roster does not list

**Files:**
- Modify: `internal/dsp/negotiation_handler.go` (struct field)
- Modify: `internal/dsp/transfer_handler.go` (struct field)
- Modify: `internal/dsp/negotiation_consumer_handler.go` (`handleInitiate`)
- Modify: `internal/dsp/transfer_consumer_handler.go` (`handleTransferInitiate`)
- Modify: `internal/dsp/router.go` (build the predicate, set it at both returns)
- Test: `internal/dsp/negotiation_consumer_handler_test.go`,
  `internal/dsp/transfer_consumer_handler_test.go`

**Interfaces:**
- Consumes: Task 1's harness identity.
- Produces: `knownParticipant func(string) bool` as a field on both
  `negotiationHandler` and `transferHandler`. Task 3 does not use it; Task 4
  moves the handlers that read it.

- [ ] **Step 1: Write the failing tests**

Add to `internal/dsp/negotiation_consumer_handler_test.go`:

```go
func TestHandleInitiateRefusesAnUnlistedProviderID(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	h := negotiationHandler{
		cfg:   config.Config{ParticipantID: testSelf, PublicURL: "http://consumer:8080"},
		store: st,
		// Lists one participant, and it is not the one the body names.
		knownParticipant: func(id string) bool { return id == "urn:participant:known" },
	}

	rec := httptest.NewRecorder()
	body := `{"providerId":"urn:participant:stranger","offerId":"o","datasetId":"d","connectorAddress":"http://provider:8080/2025-1"}`
	req := httptest.NewRequest(http.MethodPost, "/negotiations/initiate", strings.NewReader(body))
	h.handleInitiate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "urn:participant:stranger") {
		t.Errorf("body does not name the rejected id: %s", rec.Body.String())
	}
}

func TestHandleInitiateSkipsTheRosterCheckWhenItIsAbsent(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	// knownParticipant left nil: authentication is off, so there is no
	// roster and the check is absent rather than refusing everyone.
	h := negotiationHandler{
		cfg:   config.Config{ParticipantID: testSelf, PublicURL: "http://consumer:8080"},
		store: st,
	}

	rec := httptest.NewRecorder()
	body := `{"providerId":"anything at all","offerId":"o","datasetId":"d","connectorAddress":"http://provider:8080/2025-1"}`
	req := httptest.NewRequest(http.MethodPost, "/negotiations/initiate", strings.NewReader(body))
	h.handleInitiate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d — a nil predicate is the check being absent", rec.Code, http.StatusOK)
	}
}
```

Add to `internal/dsp/transfer_consumer_handler_test.go`:

```go
func TestTransferInitiateRefusesAnUnlistedProviderID(t *testing.T) {
	t.Parallel()
	h, st := newTestTransferHandlerWithStore(t)
	seedAgreementForInitiate(t, st, "urn:uuid:a")
	h.knownParticipant = func(id string) bool { return id == "urn:participant:known" }

	rec := httptest.NewRecorder()
	body := `{"providerId":"urn:participant:stranger","agreementId":"urn:uuid:a","format":"HTTP-PULL","connectorAddress":"http://provider:8080/2025-1"}`
	req := httptest.NewRequest(http.MethodPost, "/transfers/initiate", strings.NewReader(body))
	h.handleTransferInitiate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "urn:participant:stranger") {
		t.Errorf("body does not name the rejected id: %s", rec.Body.String())
	}
}
```

If `newTestTransferHandlerWithStore` and `seedAgreementForInitiate` do not
exist, use whatever the file's existing tests use to build a handler with a
store and seed an agreement — read
`TestTransferInitiateStartsAConsumerTransfer` and follow it exactly rather
than inventing a helper.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/dsp/ -run 'RefusesAnUnlistedProviderID|SkipsTheRosterCheck' -v`
Expected: compile failure — `knownParticipant` is not a field of either
struct. That is the correct first failure.

- [ ] **Step 3: Add the field to both handler structs**

In `internal/dsp/negotiation_handler.go`:

```go
type negotiationHandler struct {
	cfg   config.Config
	store *store.Store
	// knownParticipant reports whether an id is one the roster lists. It is
	// how an initiate call's providerId is checked without this handler
	// holding a roster.
	//
	// Nil disables the check rather than refusing everyone, and that
	// direction is the whole point: the roster is loaded only when
	// authentication is on, so with it off there is nothing to consult, and
	// a disabled check is absent rather than silently false. The same
	// convention pulling uses on transferHandler.
	knownParticipant func(string) bool
}
```

In `internal/dsp/transfer_handler.go`, add the same field to
`transferHandler` with the same comment.

- [ ] **Step 4: Add the check to `handleInitiate`, last**

In `internal/dsp/negotiation_consumer_handler.go`, `handleInitiate` currently
validates in this order: decode, required fields, `validateOutgoingCallback`.
Add the roster check **after** `validateOutgoingCallback`, immediately before
`store.NewUUID()`:

```go
	// Last of the validations, deliberately. Every branch here answers 400,
	// so the order is invisible to a caller — but two sibling tests in the
	// transfer hook assert only a status code, and a check that ran earlier
	// would answer their requests first and leave them passing without
	// testing their own rule.
	//
	// Unlike validateOutgoingCallback's reason, this one is echoed. That
	// silence exists because the address check reports what name resolution
	// told this connector, which the caller did not know. This reports back
	// a string the caller just sent, and an operator debugging a typo needs
	// to see which name was refused.
	if h.knownParticipant != nil && !h.knownParticipant(body.ProviderID) {
		writeError(w, ContractNegotiationErrorType, http.StatusBadRequest,
			"providerId "+body.ProviderID+" is not a participant this connector's roster lists")
		return
	}
```

- [ ] **Step 5: Add the check to `handleTransferInitiate`, last**

In `internal/dsp/transfer_consumer_handler.go`, that handler validates:
decode, required fields, `validateOutgoingCallback`, then the agreement
lookup. Add the roster check **after the agreement lookup**, immediately
before `store.NewUUID()`:

```go
	// Last, for the reason handleInitiate's equivalent gives — and here it
	// matters concretely: the agreement lookup above is pinned by a test
	// that asserts only a status code.
	if h.knownParticipant != nil && !h.knownParticipant(body.ProviderID) {
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"providerId "+body.ProviderID+" is not a participant this connector's roster lists")
		return
	}
```

- [ ] **Step 6: Build the predicate in `router.go`, at both returns**

`NewRouter` has **two** return statements. Build the predicate once, before
the handlers are constructed, and set it on both `neg` and `tr` — which are
built before either return, so a single assignment at construction covers
both paths:

```go
	// Non-nil only when there is a roster to consult. NewRouter's own rule
	// applies: with authentication off the check is absent, not silently
	// false.
	var knownParticipant func(string) bool
	if cfg.AuthRequired() {
		knownParticipant = func(id string) bool {
			_, ok := roster.KeyFor(id)
			return ok
		}
	}
```

Add `knownParticipant: knownParticipant` to the `negotiationHandler` and
`transferHandler` literals.

**Verify this is genuinely on both paths:** the two literals are constructed
above the `if !cfg.AuthRequired()` early return, so both returns see the
field set. Read the function top to bottom and confirm that is still true
after your edit.

- [ ] **Step 7: Run the new tests**

Run: `go test ./internal/dsp/ -run 'RefusesAnUnlistedProviderID|SkipsTheRosterCheck' -v`
Expected: PASS, all three.

- [ ] **Step 8: Verify the four existing initiate tests still pass**

Run: `go test ./internal/dsp/ -run 'TestHandleInitiate_|TestTransferInitiateStartsAConsumerTransfer' -v`
Expected: PASS.

These four build their handler from `config.Config{}` — authentication ON —
and name a `providerId` no roster lists. They survive **because** the
predicate is nil in a struct literal that does not set it. If any of them
fails, the field was made a roster rather than a predicate, and Step 3 needs
redoing. Do not "fix" these tests.

- [ ] **Step 9: Strengthen the two tests the check could have voided**

`TestTransferInitiateRejectsAnUnknownAgreement` and
`TestTransferInitiateRejectsAnUnsendableAddress` in
`internal/dsp/transfer_consumer_handler_test.go` assert only `400`. Add a
body assertion to each so a future reordering cannot leave them passing on
the wrong branch:

```go
	if !strings.Contains(rec.Body.String(), "no agreement with id") {
		t.Errorf("refused for the wrong reason: %s", rec.Body.String())
	}
```

and, for the address test:

```go
	if !strings.Contains(rec.Body.String(), "connectorAddress is not an address") {
		t.Errorf("refused for the wrong reason: %s", rec.Body.String())
	}
```

Leave that test's existing assertion that the address itself is absent from
the body — it still holds and is the point of that test.

- [ ] **Step 10: Mutation-test the ordering**

Move the roster check in `handleTransferInitiate` from after the agreement
lookup to before it, then run:

Run: `go test ./internal/dsp/ -run TestTransferInitiateRejectsAnUnknownAgreement -v`
Expected: **FAIL** — the body now says "is not a participant this connector's
roster lists" instead of "no agreement with id", so Step 9's new assertion
catches it. Without Step 9 this mutation would pass, which is why Step 9
exists.

Restore the check to its correct position with `git diff` review, not
`git checkout` — `git checkout` would discard the other uncommitted work in
this task.

- [ ] **Step 11: Mutation-test the nil guard**

Change `h.knownParticipant != nil && !h.knownParticipant(...)` to
`!h.knownParticipant(...)` in `handleInitiate`, then run:

Run: `go test ./internal/dsp/ -run TestHandleInitiate_Success -v`
Expected: **FAIL** with a nil-pointer panic — that test's handler literal
leaves the field nil, so calling it unconditionally dereferences nil.

Restore.

- [ ] **Step 12: Full suite and TCK**

Run: `go vet ./... && go test -race ./...`
Expected: PASS.

Run: `make tck`
Expected: 65 of 65. The harness's `providerId` is `TCK_PARTICIPANT`, and
Task 1 put that id in the roster.

- [ ] **Step 13: Commit**

```bash
git add internal/dsp/
git commit -m "feat: an initiate call may only name a participant the roster lists"
```

---

## Task 3: Guard every consumer-role resolver

Six places resolve a consumer-role row from an inbound DSP request. Three are
named in `DECISIONS.md` §32.3; three are not, because they are reached by
role dispatch rather than through a `lookup` helper.

**Files:**
- Modify: `internal/dsp/negotiation_consumer_handler.go` (`handleOffers`,
  `handleAgreement`, `handleConsumerFinalizedEvent`,
  `handleConsumerTermination`)
- Modify: `internal/dsp/negotiation_handler.go` (`handleGetNegotiation`'s
  consumer branch)
- Modify: `internal/dsp/transfer_handler.go` (`lookup`'s consumer branch, and
  its doc comment)
- Modify: `internal/dsp/auth_middleware.go` (`refuseIfNotParty`'s doc comment)
- Test: `internal/dsp/negotiation_consumer_handler_test.go`,
  `internal/dsp/negotiation_handler_test.go`,
  `internal/dsp/transfer_handler_test.go`,
  `internal/dsp/transfer_consumer_handler_test.go`

**Interfaces:**
- Consumes: Task 1's harness identity; Task 2's rule as justification.
- Produces: nothing later tasks call.

- [ ] **Step 1: Write a mismatch test for each of the three newly-found resolvers**

These three have no existing coverage of this behavior. The other
consumer-role tests pass the comparison only because their fixtures leave
`CounterpartyID` empty, so it compares `"" == ""`.

In `internal/dsp/negotiation_consumer_handler_test.go`:

```go
// The three resolvers below are reached by role dispatch rather than through
// a lookup helper, which is why DECISIONS.md section 32.3's list of
// consumer-role resolvers does not name them.
func TestHandleEventRefusesAConsumerRowFromAnotherParty(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	if err := st.CreateConsumer(store.ConsumerNegotiation{
		ConsumerPID: "c1", ProviderPID: "p1", ProviderBaseURL: "http://provider",
		CounterpartyID: "urn:participant:the-provider", State: StateVerified,
		DatasetID: "d", OfferID: "o", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}
	h := negotiationHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

	rec := httptest.NewRecorder()
	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"ContractNegotiationEventMessage","eventType":"FINALIZED"}`
	req := httptest.NewRequest(http.MethodPost, "/negotiations/c1/events", strings.NewReader(body))
	req.SetPathValue("id", "c1")
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, "urn:participant:stranger"))
	h.handleEvent(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandleTerminationRefusesAConsumerRowFromAnotherParty(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	if err := st.CreateConsumer(store.ConsumerNegotiation{
		ConsumerPID: "c1", ProviderPID: "p1", ProviderBaseURL: "http://provider",
		CounterpartyID: "urn:participant:the-provider", State: StateRequested,
		DatasetID: "d", OfferID: "o", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}
	h := negotiationHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

	rec := httptest.NewRecorder()
	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"ContractNegotiationTerminationMessage"}`
	req := httptest.NewRequest(http.MethodPost, "/negotiations/c1/termination", strings.NewReader(body))
	req.SetPathValue("id", "c1")
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, "urn:participant:stranger"))
	h.handleTermination(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandleGetNegotiationRefusesAConsumerRowFromAnotherParty(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	if err := st.CreateConsumer(store.ConsumerNegotiation{
		ConsumerPID: "c1", ProviderPID: "p1", ProviderBaseURL: "http://provider",
		CounterpartyID: "urn:participant:the-provider", State: StateRequested,
		DatasetID: "d", OfferID: "o", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}
	h := negotiationHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/negotiations/c1", nil)
	req.SetPathValue("id", "c1")
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, "urn:participant:stranger"))
	h.handleGetNegotiation(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d — the provider branch of this same function already carries the check", rec.Code, http.StatusForbidden)
	}
}
```

If `StateVerified` / `StateRequested` are named differently, use the constants
the file's existing tests use. Read `testConsumerNegotiation()` in
`internal/dsp/negotiation_test.go` and match its field names exactly.

- [ ] **Step 2: Write mismatch tests for `handleOffers` and `handleAgreement`**

Same shape as above, calling `h.handleOffers` and `h.handleAgreement`, each
with a stored `CounterpartyID` of `urn:participant:the-provider` and an
issuer of `urn:participant:stranger`, asserting 403. Name them
`TestHandleOffersRefusesAMessageFromAnotherParty` and
`TestHandleAgreementRefusesAMessageFromAnotherParty`.

- [ ] **Step 3: Write the empty-counterparty test**

```go
// A consumer-role row written before counterparty_id existed carries an
// empty string. refuseIfNotParty has no empty-stored clause, deliberately:
// a row nobody can authorize is refused rather than served.
//
// This needs no migration fixture. counterparty_id appears in no CREATE
// literal for the consumer tables, so every fresh database already runs the
// ALTER that adds it, and leaving the field unset is exactly the state an
// upgraded row is in.
func TestHandleOffersRefusesARowWithNoCounterparty(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	if err := st.CreateConsumer(store.ConsumerNegotiation{
		ConsumerPID: "c1", ProviderPID: "p1", ProviderBaseURL: "http://provider",
		State: StateRequested, DatasetID: "d", OfferID: "o",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateConsumer: %v", err)
	}
	h := negotiationHandler{cfg: config.Config{ParticipantID: testSelf}, store: st}

	rec := httptest.NewRecorder()
	body := `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@type":"ContractOfferMessage"}`
	req := httptest.NewRequest(http.MethodPost, "/negotiations/c1/offers", strings.NewReader(body))
	req.SetPathValue("id", "c1")
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, "urn:participant:the-provider"))
	h.handleOffers(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test ./internal/dsp/ -run 'RefusesAConsumerRowFromAnotherParty|RefusesAMessageFromAnotherParty|RefusesARowWithNoCounterparty' -v`
Expected: all FAIL with `status = 200, want 403`.

- [ ] **Step 5: Add the guard at all six points**

Insert, at each site, immediately after the row is resolved and before the
body is read or the response is written:

```go
	if refuseIfNotParty(w, r, ContractNegotiationErrorType, n.CounterpartyID, h.cfg.AuthRequired()) {
		return
	}
```

using the row variable and error type each site actually has:

| site | file | variable | error type |
|---|---|---|---|
| `handleOffers` | `negotiation_consumer_handler.go` | `n` | `ContractNegotiationErrorType` |
| `handleAgreement` | `negotiation_consumer_handler.go` | `n` | `ContractNegotiationErrorType` |
| `handleConsumerFinalizedEvent` | `negotiation_consumer_handler.go` | `n` | `ContractNegotiationErrorType` |
| `handleConsumerTermination` | `negotiation_consumer_handler.go` | `n` | `ContractNegotiationErrorType` |
| `handleGetNegotiation` consumer branch | `negotiation_handler.go` | `cn` | `ContractNegotiationErrorType` |
| `transferHandler.lookup` consumer branch | `transfer_handler.go` | `c` | `TransferErrorType` |

For `handleGetNegotiation`, the provider branch of that same function already
has this exact call. Put the consumer one in the `else if ok` branch that
builds the consumer state document, before `writeJSON`.

For `transferHandler.lookup`, put it inside the `if ok` block that returns the
consumer `resolvedTransfer`, before the `return`. **Do not** move the existing
call that sits after `GetTransfer`, and **do not** write the check against the
value `lookup` returns — see Step 6.

- [ ] **Step 6: Rewrite `transferHandler.lookup`'s doc comment**

Its current comment explains why the check sits after the consumer branch has
returned. Half of that is now obsolete and half is a warning that must
survive. Replace the obsolete half; keep the warning:

```go
// Both branches now carry the refusal, and they carry it separately on
// purpose. resolvedTransfer carries CounterpartyID for consumer rows too, so
// a single comparison written against the value this function returns — or
// hoisted above the branch split — would apply the provider-role rule to
// consumer rows. That happens to be correct now and was catastrophically
// wrong before the initiate hooks moved behind the operator's token: it would
// have refused every consumer-role transfer the TCK drives. The placement is
// deliberate, not incidental.
```

**Verify before writing:** read the function and confirm both branches carry a
call after your Step 5 edit.

- [ ] **Step 7: Rewrite `refuseIfNotParty`'s doc comment**

Its current comment says `stored` must come from a verified issuer and that a
consumer-role row's counterparty is not one, using the TCK's two names as the
example. Both halves are now false. Replace with:

```go
// stored is the participant this row's exchange is with. Provider-role rows
// take it from the verified issuer of the request that created them.
// Consumer-role rows take it from the providerId of an initiate call — which
// is an authorization anchor now that only the operator can make one and only
// a roster participant may be named. Before that it was a string any caller
// could choose, which is why DECISIONS.md section 32.3 recorded the consumer
// role's resolvers as deliberately unguarded.
//
// Every resolver that reaches a row of either role carries this call. That is
// load-bearing rather than tidy: a comment saying a consumer counterparty is
// an authorization anchor, next to a resolver that does not compare against
// it, would be worse than the documented asymmetry it replaced.
```

Keep the existing paragraphs about 403-not-404 and about there being no
empty-stored clause — both still hold.

- [ ] **Step 8: Run the new tests**

Run: `go test ./internal/dsp/ -run 'RefusesAConsumerRowFromAnotherParty|RefusesAMessageFromAnotherParty|RefusesARowWithNoCounterparty' -v`
Expected: PASS.

- [ ] **Step 9: Invert the test that pinned the old behavior**

`TestTransferLookupDoesNotCheckConsumerRows` in
`internal/dsp/transfer_handler_test.go` exists to pin the absence of the check
Step 5 added. It stores `CounterpartyID: "SOME_OTHER_NAME"`, presents
`testPeer`, and asserts 200. **Invert it, do not patch it** — rename to
`TestTransferLookupChecksConsumerRows`, assert `http.StatusForbidden`, and
replace its comment:

```go
// A consumer-role row is compared like a provider-role one. Its counterparty
// came from an initiate call that only the operator can make and that only
// accepts a participant the roster lists, so it is an identity. Until the
// initiate hooks moved behind the operator's token this test asserted the
// opposite, and the assertion was correct then.
```

- [ ] **Step 10: Repair the two fixture-only failures**

`TestHandleAgreement_RecordsTheProviderAsCounterparty`
(`negotiation_consumer_handler_test.go`) and
`TestConsumerFollowUpsAreAddressedToTheCounterparty`
(`transfer_consumer_handler_test.go`) set a counterparty and present no issuer
at all, against a config whose `AuthRequired()` is true. Add the matching
issuer to each request:

```go
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, "urn:participant:the-provider"))
```

using whichever counterparty string that test already stores. These are
fixture repairs — do not change what either test asserts.

- [ ] **Step 11: Mutation-test one of the newly-found resolvers**

Delete the `refuseIfNotParty` call from `handleGetNegotiation`'s consumer
branch, then run:

Run: `go test ./internal/dsp/ -run TestHandleGetNegotiationRefusesAConsumerRowFromAnotherParty -v`
Expected: **FAIL** with `status = 200, want 403` — that test calls
`handleGetNegotiation` directly with a consumer-role row and a mismatched
issuer, so the deleted branch is the only thing that could have refused it.

Restore by re-adding the call, not by `git checkout`.

- [ ] **Step 12: Full suite and both harnesses**

Run: `go vet ./... && go test -race ./...`
Expected: PASS.

Run: `make tck`
Expected: 65 of 65. Every inbound consumer-role message in `CN_C` and `TP_C`
now arrives from `TCK_PARTICIPANT` and matches the row Task 1 made consistent.
If `CN_C` and `TP_C` fail wholesale, Task 1 did not land.

Run: `make demo`
Expected: both rounds pass, the resumed transfer matches byte for byte.

- [ ] **Step 13: Commit**

```bash
git add internal/dsp/
git commit -m "feat: every consumer-role resolver checks who is asking"
```

---

## Task 4: Move both hooks to the management listener

**This task is one commit.** Splitting it leaves a commit where the TCK's
seeding or its initiate calls fail.

**Files:**
- Modify: `internal/dsp/router.go` (return struct, stop registering two routes)
- Modify: `internal/mgmt/router.go` (`NewRouter` signature, mount two routes)
- Modify: `cmd/dsbox/main.go` (wiring)
- Modify: `internal/dsp/negotiation_handler_test.go`,
  `internal/dsp/auth_middleware_test.go` (call site only),
  `internal/dsp/catalog_handler_test.go`,
  `internal/dsp/transfer_consumer_handler_test.go` (call sites)
- Modify: `internal/mgmt/router_test.go` (three call sites)
- Create: `internal/mgmt/route_coverage_test.go`
- Modify: `test/tck/run.sh`, `test/tck/compose.yaml`, `test/tck/dsbox.yaml`,
  `test/tck/config.properties`
- Modify: `demo/run.sh`

**Interfaces:**
- Consumes: the handlers Tasks 2 and 3 modified.
- Produces: `dsp.Routers` (below), and `mgmt.NewRouter(cfg, st, initiate dsp.InitiateHandlers)`.

- [ ] **Step 1: Define the return struct in `internal/dsp/router.go`**

```go
// Routers is what NewRouter returns. It is a struct rather than a longer
// return list because two of its fields are http.Handler and a call site
// would have nothing to tell them apart by.
type Routers struct {
	// Protocol serves the DSP listener.
	Protocol http.Handler
	// Initiate holds the two hooks that belong on the management listener:
	// they ask this connector to start an exchange, which is an operator
	// action, not a message from a counterparty. cmd/dsbox hands them to
	// internal/mgmt, so that package needs no opinion about this one.
	Initiate InitiateHandlers
	// Pulls counts the data pulls the protocol handler has in flight, and
	// CancelPulls ends them. The caller uses them in one order: cancel, then
	// wait. DECISIONS.md section 34.3 has the argument.
	Pulls       *sync.WaitGroup
	CancelPulls context.CancelFunc
}

// InitiateHandlers carries the two hooks by name. They are handlers on
// unexported types, which is fine: a method value is assignable to
// http.Handler without exporting anything.
type InitiateHandlers struct {
	Negotiation http.Handler
	Transfer    http.Handler
}
```

- [ ] **Step 2: Change `NewRouter`'s signature and both returns**

Signature becomes:

```go
func NewRouter(cfg config.Config, st *store.Store, roster auth.Roster, signKey ed25519.PrivateKey) Routers {
```

Build the initiate handlers once, above the `if !cfg.AuthRequired()` early
return:

```go
	initiate := InitiateHandlers{
		Negotiation: http.HandlerFunc(neg.handleInitiate),
		Transfer:    http.HandlerFunc(tr.handleTransferInitiate),
	}
```

Both return statements become `return Routers{Protocol: outer, Initiate:
initiate, Pulls: pulls, CancelPulls: cancelPulls}`.

**Both returns must carry `Initiate`.** The first is the
`!cfg.AuthRequired()` path. If only the second sets it, a development
instance ships nil handlers: the management listener wraps them in
`authenticated`, which is non-nil, so registration succeeds and the panic
arrives at request time — after the token check passes — as a connection
reset with no error document. Neither harness runs with authentication off,
so nothing in CI would catch it. Step 9 adds the test that does.

- [ ] **Step 3: Remove the two route registrations**

Delete these two lines from `NewRouter`:

```go
	mux.HandleFunc("POST "+VersionPath+"/negotiations/initiate", neg.handleInitiate)
	mux.HandleFunc("POST "+VersionPath+"/transfers/initiate", tr.handleTransferInitiate)
```

Add a comment where the negotiation one was:

```go
	// The two initiate hooks are not registered here. They are operator
	// actions and live on the management listener; NewRouter returns them so
	// cmd/dsbox can mount them there. Note what the removal leaves behind: a
	// POST to either old path now matches the GET route with a path
	// parameter above and answers 405, not 404. The TCK retries a 405 three
	// times and fails immediately on a 404, so a stale URL in its
	// configuration produces the slow diagnosis rather than the fast one.
```

- [ ] **Step 4: Update the four in-package `NewRouter` call sites**

Each currently destructures three values. They become:

```go
	r := NewRouter(cfg, st, roster, signKey)
```

and use `r.Protocol` where the handler was. Two of them ignore the WaitGroup
and cancel func with `_`; those blanks simply disappear. The files are
`negotiation_handler_test.go`, `auth_middleware_test.go`,
`catalog_handler_test.go`, `transfer_consumer_handler_test.go`.

**Nothing else in `auth_middleware_test.go` changes.** That file parses
`router.go` as text to prove every DSP route is authenticated; removing two
routes still satisfies its floor and its calls-versus-routes equality. Do not
adjust any number in it.

- [ ] **Step 5: Change `mgmt.NewRouter` and mount the routes**

```go
func NewRouter(cfg config.Config, st *store.Store, initiate dsp.InitiateHandlers) http.Handler {
```

and inside, beside the existing routes:

```go
	// The two hooks that ask this connector to start an exchange as consumer.
	// They live in package dsp as code and here as routes: starting an
	// exchange is an operator action, and before this they sat on the
	// protocol listener where any roster participant could call them.
	// DECISIONS.md section 35 has the argument.
	mux.Handle("POST /negotiations/initiate", authenticated(cfg.MgmtToken, initiate.Negotiation))
	mux.Handle("POST /transfers/initiate", authenticated(cfg.MgmtToken, initiate.Transfer))
```

No `/2025-1` prefix: this listener carries no protocol version on any route.

`POST /transfers/initiate` and the existing `GET /transfers` coexist without
shadowing — that was measured against a real `ServeMux`, along with 405 for
`POST /transfers` and `GET /transfers/initiate`, and 404 for a trailing
slash. Step 8 pins it.

- [ ] **Step 6: Add the comment about what the token is**

In `internal/mgmt/router.go`, inside `authenticated`, add:

```go
	// This compares a shared secret; it does not verify a credential. The
	// distinction matters because the TCK harness sets this token to a
	// minted participant credential — one string has to satisfy this
	// listener and the protocol listener, and the TCK can express only one
	// Authorization value. A string that looks like a credential is still
	// compared byte for byte here, so it keeps being accepted after the
	// credential inside it has expired.
```

- [ ] **Step 7: Wire it in `cmd/dsbox/main.go`**

```go
	routers := dsp.NewRouter(cfg, st, roster, signKey)
```

then use `routers.Protocol` for the DSP server, `routers.Pulls` and
`routers.CancelPulls` where the old values were, and pass
`routers.Initiate` into `mgmt.NewRouter(cfg, st, routers.Initiate)`.

- [ ] **Step 8: Write the routing test**

Create `internal/mgmt/route_coverage_test.go`:

```go
// Every route this listener serves except /health refuses an unauthenticated
// request. This is the management-side counterpart of the DSP listener's own
// route-coverage test: when the two initiate hooks moved here, they left that
// test's reach, and a route that is accidentally anonymous is exactly what
// these assertions exist to prevent.
func TestEveryManagementRouteExceptHealthRefusesAnAnonymousRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/agreements"},
		{http.MethodGet, "/agreements"},
		{http.MethodGet, "/transfers"},
		{http.MethodPost, "/negotiations/initiate"},
		{http.MethodPost, "/transfers/initiate"},
	}
	h := newTestRouter(t)
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("%s %s: WWW-Authenticate = %q, want %q", c.method, c.path, got, "Bearer")
		}
	}
}

// The new POST routes and the existing GET /transfers do not shadow each
// other. Asserted through a real mux rather than a recorder, because the
// thing under test is routing.
func TestManagementRoutePatternsDoNotShadowEachOther(t *testing.T) {
	t.Parallel()
	h := newTestRouter(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/transfers", http.StatusUnauthorized},
		{http.MethodPost, "/transfers/initiate", http.StatusUnauthorized},
		{http.MethodPost, "/transfers", http.StatusMethodNotAllowed},
		{http.MethodGet, "/transfers/initiate", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		req, err := http.NewRequest(c.method, srv.URL+c.path, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}
}
```

`newTestRouter` is a helper this file adds: it opens an in-memory store, builds
a `dsp.InitiateHandlers` whose two fields are non-nil stub handlers writing
200, and returns `NewRouter(config.Config{MgmtToken: "a-management-token-16+"}, st, initiate)`.
Follow whatever `internal/mgmt/router_test.go` already does to build a store
and a config; do not invent a second style.

- [ ] **Step 9: Write the nil-handler regression test**

In `internal/dsp/router_test.go` (create it if absent):

```go
// NewRouter has two return statements and the authentication-off one is easy
// to miss. A nil Initiate handler is not caught by the management listener's
// route-coverage test, because a nil handler behind authenticated still
// answers 401 to an anonymous request — the panic only arrives once a caller
// authenticates.
func TestNewRouterReturnsInitiateHandlersWithAuthenticationOff(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	off := false
	cfg := config.Config{
		ParticipantID: "urn:participant:self",
		PublicURL:     "http://self:8080",
		DevMode:       true,
		RequireAuth:   &off,
	}

	r := NewRouter(cfg, st, auth.Roster{}, nil)
	if r.Initiate.Negotiation == nil || r.Initiate.Transfer == nil {
		t.Fatal("NewRouter returned a nil initiate handler on the authentication-off path")
	}
}
```

- [ ] **Step 10: Update the three `mgmt.NewRouter` call sites**

All three are in `internal/mgmt/router_test.go`. Pass a `dsp.InitiateHandlers`
with two non-nil stub handlers, not `nil` — `nil` compiles and then panics the
moment a request authenticates. Reuse the `newTestRouter` helper from Step 8
where the test does not need its own config.

- [ ] **Step 11: Run the Go gates**

Run: `go vet ./... && go test -race ./...`
Expected: PASS.

- [ ] **Step 12: Move the mint in `test/tck/run.sh`**

Relocate the `dsops token` invocation and the two lines that copy and append
to `config.properties` so they run **after** the `tck_pub=$(...)` keygen and
**before** `$compose up -d --build dsbox`. Add `-ttl 30m`, and export the
token:

```sh
# Minted before the build, not after, because one string now has to satisfy
# both listeners: the TCK registers a single Authorization value as a
# process-wide interceptor and sends it everywhere, including to the two
# initiate hooks, which now live on the management listener. That listener
# compares it against mgmt_token, so the two must be the same string.
#
# 30m rather than the five minutes DECISIONS.md section 10 sets for a real
# credential: a cold image build plus the pull of the pinned TCK image now
# sits inside this token's life. The connector's own credentialTTL is
# unchanged; what is relaxed is only what this harness mints for itself.
#
# No stray whitespace around the value below. The two listeners are not
# equally forgiving: the protocol listener trims the credential after the
# scheme and the management listener compares it byte for byte, so a trailing
# space would pass one and 401 the other.
token=$("$identity/dsops" token -key "$identity/tck.key" \
	-iss TCK_PARTICIPANT -aud urn:participant:dsbox-test -ttl 30m)
export DSBOX_MGMT_TOKEN="$token"
cat "$dir/config.properties" >"$identity/config.properties"
printf '\ndataspacetck.dsp.connector.http.headers.authorization=Bearer %s\n' \
	"$token" >>"$identity/config.properties"
```

Delete the old comment block that explained minting after the build, and the
old mint. Also update the comment near the top of the identities section that
says the credential is minted later.

- [ ] **Step 13: Change the seeding header in `test/tck/run.sh`**

In `seed_agreement`, replace `-H 'Authorization: Bearer tck-harness-token-0'`
with `-H "Authorization: Bearer $token"`. Note the quote change from single to
double — the value is now a variable.

- [ ] **Step 14: Pass the token through `test/tck/compose.yaml`**

Add to the `dsbox` service's `environment:` list, beside
`DSBOX_ROSTER_SIGNER`:

```yaml
      - DSBOX_MGMT_TOKEN=${DSBOX_MGMT_TOKEN}
```

Also correct that file's ports comment: it says two things reach the
management port and names an outdated number of seeding calls. Say instead
that the readiness probe and the agreement seeding reach it from the host,
and the TCK reaches the two initiate hooks on it over the Compose network.
**Verify before writing:** count nothing — describe what reaches it.

- [ ] **Step 15: Remove `mgmt_token` from `test/tck/dsbox.yaml`**

Delete the key and replace its comment with:

```yaml
# mgmt_token is not set here. run.sh mints one credential and passes it in
# through DSBOX_MGMT_TOKEN, because the TCK sends a single Authorization
# value to every endpoint and two of them are now on this listener. Leaving
# the key out is deliberate: an absent token rejects every authenticated
# request, so a missing environment passthrough fails at the first seeded
# agreement instead of silently, much later, as a suite result.
```

- [ ] **Step 16: Repoint the two initiate URLs in `test/tck/config.properties`**

```properties
dataspacetck.dsp.connector.negotiation.initiate.url=http://dsbox:8081/negotiations/initiate
dataspacetck.dsp.connector.transfer.initiate.url=http://dsbox:8081/transfers/initiate
```

**Leave `dataspacetck.dsp.connector.http.base.url` and
`...http.url` on 8080.** Moving those would take the catalog and every DSP
message with them.

Rewrite the comment block above the two URLs: it currently says neither
endpoint exists yet and both 404. Say instead that these are the management
listener's hooks, that the connector reaches that listener by service name on
the Compose network, and that the credential below satisfies both listeners.

- [ ] **Step 17: Move the demo's four initiate calls**

In `demo/run.sh`, change all four `curl` invocations from
`http://127.0.0.1:9280/2025-1/negotiations/initiate` and
`.../2025-1/transfers/initiate` to `http://127.0.0.1:9281/negotiations/initiate`
and `http://127.0.0.1:9281/transfers/initiate`, and change
`-H "Authorization: Bearer $operator"` to
`-H "Authorization: Bearer demo-management-token"` in each.

Delete the `operator=$(...)` mint and the comment block above it that explains
the self-issued credential. `$gen/dsops` is still needed for the keygens and
for `roster sign`, so do not remove the build.

Also correct `demo/compose.yaml`'s note about why both listeners are
published: after this, nothing on the host drives the consumer's DSP port.

- [ ] **Step 18: Run both harnesses**

Run: `make tck`
Expected: 65 of 65.

If seeding fails with HTTP 401 at the first agreement, Step 14 was missed —
which is what Step 15 is for.

Run: `make demo`
Expected: both rounds pass.

- [ ] **Step 19: Commit**

```bash
git add internal/ cmd/ test/tck/ demo/
git commit -m "feat: the initiate hooks move to the management listener"
```

---

## Task 5: Make the harness's counterparty observable

`make tck` passing at 65 of 65 depends on the connector recording
`TCK_PARTICIPANT` as the counterparty, and nothing in a run shows that it did:
the initiate handlers log nothing on success, the script never queries the
transfers, and the container's database dies with it. A silent mismatch would
present as a protocol bug.

**Files:**
- Modify: `test/tck/run.sh`

**Interfaces:**
- Consumes: Task 4's management listener and its token.
- Produces: nothing.

- [ ] **Step 1: Add the read-back after the suite**

After the suite runs and before the gate, add:

```sh
# The suite's result depends on the connector having recorded TCK_PARTICIPANT
# as the counterparty of every consumer-role exchange: that is what the
# roster check accepts at initiate time and what the inbound guards compare
# against. Nothing else in this run would show a mismatch — the initiate
# handlers log nothing on success and this container's database dies with it
# — and the symptom of one is suite failures that read like protocol bugs.
transfers=$(curl -sf http://127.0.0.1:8081/transfers \
	-H "Authorization: Bearer $token" || true)
if [ -z "$transfers" ]; then
	echo "could not read back the transfers to confirm the recorded counterparty" >&2
	exit 1
fi
case "$transfers" in
	*'"counterpartyId":"TCK_PARTICIPANT"'*) ;;
	*)
		echo "the connector did not record TCK_PARTICIPANT as a counterparty; the harness identity and the providerId the TCK sends have diverged" >&2
		exit 1
		;;
esac
echo 'confirmed the recorded counterparty'
```

Place it before the container is torn down. Read the script's existing
teardown and put this above it.

- [ ] **Step 2: Run the TCK**

Run: `make tck`
Expected: 65 of 65, plus the line `confirmed the recorded counterparty`.

- [ ] **Step 3: Mutation-test the read-back**

Temporarily change the `case` pattern to
`*'"counterpartyId":"urn:participant:tck"'*` and run `make tck`.
Expected: the run fails with the divergence message, after the suite passes.
This proves the check reads real data rather than always matching.

Restore the pattern.

- [ ] **Step 4: Commit**

```bash
git add test/tck/run.sh
git commit -m "test: the harness confirms the counterparty it depends on"
```

---

## Task 6: Correct what is no longer true

Every edit below names the code fact it was checked against. Open the code
first; write the sentence second.

**Files:**
- Modify: `internal/dsp/negotiation_consumer_handler.go`,
  `internal/dsp/transfer_consumer_handler.go`,
  `internal/dsp/negotiation_handler.go`, `internal/dsp/negotiation.go`,
  `internal/dsp/transfer_handler.go`, `internal/store/store.go`,
  `internal/config/config.go`, `config.example.yaml`
- Modify: `DECISIONS.md`, `docs/follow-ups.md`,
  `docs/milestone-sequence.md`, `docs/goal-gap-analysis.md`, `SECURITY.md`,
  `README.md`

**Interfaces:**
- Consumes: everything Tasks 1-5 landed.
- Produces: nothing.

- [ ] **Step 1: Code comments**

- `handleTransferInitiate`'s doc comment describes the public listener and the
  absence of an ownership check. Both are false. Rewrite: it is on the
  management listener, its caller is the operator, and its `providerId` must
  name a roster participant.
- `handleInitiate`'s doc comment says **neither** of those things — check it
  before editing. What needs rewriting in that file is the inline comment
  saying the endpoint is reachable by any roster participant.
- `negotiationHandler.lookup`'s doc comment says `handleOffers` and
  `handleAgreement` "carry no ownership check, deliberately". False now.
- `store.ConsumerNegotiation.CounterpartyID` and
  `store.ConsumerTransfer.CounterpartyID` say nothing authorizes an inbound
  request against them. False now.
- `store.ConsumerNegotiation.ProviderBaseURL` and `store.Agreement`'s type doc
  name the old initiate paths.
- `internal/dsp/negotiation.go` and `internal/dsp/transfer_handler.go` have doc
  comments naming the old paths.
- `config.Config.MgmtToken`'s doc comment calls the token optional. An absent
  token now disables the only way to start an exchange as consumer.
- `config.Config.ConsumerPolicies`'s doc comment names the old path.
- `config.example.yaml` documents `mgmt_token` as optional and names the old
  path. That file is load-guarded by a test — run
  `go test ./internal/config/` after editing it.

- [ ] **Step 2: `DECISIONS.md` §24.2 and §32.3**

§24.2's headline says the hook is on the public listener, and its "Corrected
by §32" block ends by saying what §32 does not change is this endpoint's lack
of an ownership check. Both are now false; append a correction block naming
§35, in the style §24.2 already uses.

§32.3 stays as the record of what was true then. Mark both deferred
consequences closed and point at §35.

- [ ] **Step 3: Write `DECISIONS.md` §35**

A new numbered section, following the per-milestone pattern §21 through §34
use. Cover, one sub-section each:

- **35.1** The hooks move to the management listener. Why the question was
  which listener rather than what a call may name.
- **35.2** `providerId` must name a roster participant, and the check is a
  nil-able predicate because a roster-shaped field fails closed against every
  handler literal that does not set it.
- **35.3** Six consumer-role resolvers, not the three §32.3 enumerates, and
  why closing three would have been worse than closing none.
- **35.4** The harness's identity is corrected rather than worked around, and
  that is what dissolves the 30-result cost §32.3 measured.
- **35.5** The answer §34.4 demanded: why the management API is still small
  with two write routes added. These are not new capability — they are an
  existing capability moved to the listener it belonged on. What is new is
  that nobody else can use it.

Then the trade-offs, from the spec's §9: a roster participant is not
necessarily the participant at `connectorAddress`; the harness no longer
demonstrates the five-minute lifetime; the management token is a string the
DSP listener also accepts; the harness identity is coupled to an upstream
constant; `make demo` loses its only self-issued operator credential; an
upgraded deployment's in-flight consumer exchanges stop working.

- [ ] **Step 4: `docs/follow-ups.md`**

Delete the two entries §32.3 deferred — that file's own rule is to delete an
entry when it is fixed. The third entry in that section survives, but its
bullet claiming `handleTransferInitiate`'s agreement gate is decorative
pending verified-`providerId` work is now false; amend that bullet. **Verify
before writing:** read the third entry and confirm which of its bullets the
milestone actually invalidates.

- [ ] **Step 5: `docs/milestone-sequence.md`**

Two edits: the forward section, whose framing this milestone settles, and the
earlier note saying the dispute should be settled by whoever starts the work.
Record the milestone as done in the "What has been done since this was
written" section, in the style the entries there already use, and say what
the verification situation turned out to be.

- [ ] **Step 6: `docs/goal-gap-analysis.md`**

Mark ordered item 2 done. That document is the only place in the repository
that counts management routes and already carries a dated correction of that
count — update it, and do not introduce a new count anywhere else.

- [ ] **Step 7: `SECURITY.md`**

Its published-gaps section names the initiate hooks as the sharpest item
currently open and says whoever closes it should correct that section. Replace
the claim, and name what is now sharpest. The candidates are the forged-row
follow-up entry, the gap where a roster participant is not necessarily the
participant at `connectorAddress`, and the absent rate limiting. Pick one and
say why.

Also re-read the `require_auth: false` scope bullet: the initiate hooks are no
longer reachable through it, and that bullet should say what is still true.

- [ ] **Step 8: `README.md`**

It states the consumer-role gap as open and calls it the sharpest of the open
items, describes the management API as read routes behind a token, and
reports a credential-removal measurement taken when both hooks were on the
DSP listener. Correct all three, and add a milestone paragraph in the style
the file already uses for previous milestones.

- [ ] **Step 9: Verify every claim you wrote**

For each file above, re-read the sentence you wrote and the code it describes,
side by side. This is the step the last two milestones skipped, and both were
blocked by documentation asserting things about the code that were not so.

Run: `go test ./internal/config/`
Expected: PASS — `config.example.yaml` is load-guarded.

- [ ] **Step 10: Final gates**

Run: `go vet ./... && go test -race ./... && make tck && make demo`
Expected: all four pass; TCK at 65 of 65.

- [ ] **Step 11: Commit**

```bash
git add -A
git commit -m "docs: record the initiate-hook milestone and correct what it made false"
```

---

## Mutation table

Each row names why the mutation breaks that specific test. A row whose reason
cannot be written is a wrong mutation — two milestones in a row shipped
prescribed mutations that tested nothing.

| # | Mutation | Named test that must fail | Why it fails |
|---|---|---|---|
| 1 | Move the roster check in `handleTransferInitiate` above the agreement lookup | `TestTransferInitiateRejectsAnUnknownAgreement` | The test's request names a `providerId` the predicate rejects, so the roster branch answers first and the body says "not a participant" instead of "no agreement with id" — caught only by the body assertion Task 2 Step 9 adds. |
| 2 | Drop the `h.knownParticipant != nil` guard in `handleInitiate` | `TestHandleInitiate_Success` | That test's handler literal leaves the field nil, so an unconditional call dereferences nil and panics. |
| 3 | Delete `refuseIfNotParty` from `handleGetNegotiation`'s consumer branch | `TestHandleGetNegotiationRefusesAConsumerRowFromAnotherParty` | The test calls that function directly with a consumer-role row and a mismatched issuer; the deleted call is the only thing that could refuse it, so the handler falls through to `writeJSON` and answers 200. |
| 4 | Set `Initiate` only at `NewRouter`'s authenticated return | `TestNewRouterReturnsInitiateHandlersWithAuthenticationOff` | That test builds a config with `RequireAuth` false, which takes the early return; the struct then carries two nil handlers and the test's nil check fires. |
| 5 | Change the `case` pattern in `run.sh`'s read-back to the old identity | the `make tck` run itself | The connector records `TCK_PARTICIPANT`, so the pattern no longer matches and the script exits with the divergence message after the suite has already passed. |

Mutations deliberately **not** prescribed, with the reason each would be
bogus:

- Removing a route registration from `router.go` to check
  `auth_middleware_test.go` — that test parses the file it is given, so
  removing a route removes it from both sides of the comparison and the test
  stays green. It tests coverage, not presence.
- Deleting the `strings.TrimSpace` in the DSP bearer helper — no test presents
  a padded header, and adding one would test the helper rather than anything
  this milestone changes.

---

## Self-review

**Spec coverage.** §3.1-§3.2 → Task 4 Steps 3, 5. §3.3 → Task 4 Step 3's
comment and the 405 assertions in Step 8. §3.4 → Task 4 Steps 1-2, 4, 8-10.
§4.1, §4.4 → Task 2 Steps 4-5. §4.2 → Task 2 Steps 3, 6. §4.3 → Task 2 Steps
4-5, 9-10. §4.5 → Task 6 Step 3's trade-offs. §5.1 → Task 3 Step 5. §5.2 →
Task 3 Step 6. §5.3 → Task 3 Step 3. §5.4 → Task 3 Step 7's comment. §5.5 →
Task 3 Steps 9-10. §6.1 → Task 4 Steps 6, 12-16. §6.2 → Task 1. §6.3 → Task 4
Steps 14-15. §6.4 → Task 4 Step 17. §7 → Task 6. §8 → Task 5. §9 → Task 6
Step 3.

**One spec item is deliberately not a task.** §6.1's note that the two bearer
helpers differ on whitespace trimming changes no code — they are unshared on
purpose. It is recorded where the harness writes the header, in Task 4
Step 12's comment.

**Type consistency.** `knownParticipant func(string) bool` is the field name
in Tasks 2 and 3. `dsp.Routers` with fields `Protocol`, `Initiate`, `Pulls`,
`CancelPulls`, and `dsp.InitiateHandlers` with fields `Negotiation`,
`Transfer`, are used identically in Task 4 Steps 1, 2, 5, 7, 9, 10.
`mgmt.NewRouter(cfg, st, initiate dsp.InitiateHandlers)` is the signature in
Steps 5, 7, 10.
