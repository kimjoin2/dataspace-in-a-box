package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

	return transferHandler{cfg: cfg, store: st, stepDelay: transferStepDelay}, st
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

// waitForTransferState is waitForState's transfer-table counterpart, and
// exists for the same reason: driveTransfer runs on its own goroutine
// (`go h.driveTransfer(t)`) so the handler can return and its own response go
// out on the wire before the push is attempted, which means the state it
// writes afterward is not there yet when the handler call returns.
//
// Every test that reaches handleTransferRequest with a known agreement must
// end on this, not only the ones asserting the transition: newTestTransferHandler
// closes the store on cleanup, and a state write landing after that logs a
// "database is closed" error into the output of a test that already passed.
//
// The deadline is longer than waitForState's one second because the push runs
// first and a callback address that does not resolve has to exhaust
// callbackRetryBackoffs (shortened in TestMain) before the state write happens.
func waitForTransferState(t *testing.T, st *store.Store, providerPID, want string) store.TransferProcess {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tp, ok, err := st.GetTransfer(providerPID)
		if err == nil && ok && tp.State == want {
			return tp
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("stored state for %q did not reach %q within the deadline", providerPID, want)
	return store.TransferProcess{}
}

// transferRequestBody is a well-formed TransferRequestMessage. Individual
// tests override one field to exercise a specific guard.
func transferRequestBody(agreementID string) string {
	return transferRequestBodyTo(agreementID, "http://consumer.example/2025-1")
}

// transferRequestBodyTo is transferRequestBody addressed at a callback the
// test is actually listening on, for the tests that assert what was pushed.
func transferRequestBodyTo(agreementID, callbackAddress string) string {
	return `{"@context":["` + ContextURL + `"],"@type":"` + TransferRequestMessageType + `",` +
		`"consumerPid":"urn:uuid:tc-1","agreementId":"` + agreementID + `",` +
		`"format":"HTTP-PULL","callbackAddress":"` + callbackAddress + `"}`
}

// postTransferRequest posts body to the request endpoint and returns the
// provider pid out of the acknowledgment, which is the only handle a test has
// on the transfer the handler just created.
func postTransferRequest(t *testing.T, h transferHandler, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/request", strings.NewReader(body))
	h.handleTransferRequest(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /transfers/request = %d, want 201 (body %q)", rec.Code, rec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("acknowledgment body is not JSON: %v", err)
	}
	providerPID, _ := doc["providerPid"].(string)
	if providerPID == "" {
		t.Fatalf("acknowledgment carried no providerPid: %q", rec.Body.String())
	}
	return providerPID
}

// transferPolicyConfig is the configuration for one agreement's sequence.
func transferPolicyConfig(agreementID string, sequence ...string) config.Config {
	return config.Config{TransferPolicies: []config.TransferPolicy{
		{AgreementID: agreementID, Sequence: sequence},
	}}
}

// transferPush is one push the fake consumer received. at is when it arrived,
// which is what the spacing test measures; assertPushSequence ignores it.
type transferPush struct {
	path    string
	msgType string
	at      time.Time
}

// fakeTransferConsumer is a consumer callback endpoint that records every
// push it receives in arrival order.
//
// It records an ordered log rather than negotiation_handler_test.go's
// fakeCallback, which keys pushes by path and so cannot express order across
// paths. Order is the property that matters here: the TCK registers the
// handler for step N+1 only once step N has arrived, so a driver that pushed
// its steps concurrently would satisfy a per-path assertion and still 404 on
// the wire.
type fakeTransferConsumer struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []transferPush
}

func newFakeTransferConsumer(t *testing.T) *fakeTransferConsumer {
	t.Helper()
	fc := &fakeTransferConsumer{}
	fc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			// t.Errorf, never t.Fatalf: this runs on the server goroutine,
			// where Fatalf calls runtime.Goexit on the wrong stack and hangs
			// the request. See assertEmittedOffer in negotiation_test.go.
			t.Errorf("decode pushed message: %v", err)
		}
		msgType, _ := body["@type"].(string)
		fc.mu.Lock()
		fc.got = append(fc.got, transferPush{path: r.URL.Path, msgType: msgType, at: time.Now()})
		fc.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fc.srv.Close)
	return fc
}

// waitFor polls until at least n pushes have arrived and returns everything
// received, or fails the test after two seconds.
func (fc *fakeTransferConsumer) waitFor(t *testing.T, n int) []transferPush {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		fc.mu.Lock()
		got := append([]transferPush(nil), fc.got...)
		fc.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("received %v, want at least %d pushes", got, n)
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// receivedNothing fails the test if any push arrives within the window. The
// assertion is "nothing on any path", not "nothing on this one", because an
// empty sequence is defined by pushing nothing at all.
func (fc *fakeTransferConsumer) receivedNothing(t *testing.T, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		got := append([]transferPush(nil), fc.got...)
		fc.mu.Unlock()
		if len(got) > 0 {
			t.Fatalf("unexpected pushes: %v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertPushSequence pins what arrived, in the order it arrived. Comparing
// the whole slice rather than checking each message is present is the point:
// a driver that pushed its steps concurrently, or in the wrong order, has to
// fail here rather than in a TCK run. Arrival times are not compared — the
// spacing between pushes is its own test.
func assertPushSequence(t *testing.T, got, want []transferPush) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("received %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i].path != want[i].path || got[i].msgType != want[i].msgType {
			t.Errorf("push %d = %s on %s, want %s on %s",
				i, got[i].msgType, got[i].path, want[i].msgType, want[i].path)
		}
	}
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
	// The value, not just the presence: the schema requires consumerPid to be
	// the one the request carried, and a handler that echoed its own provider
	// pid into it would satisfy a presence check.
	if doc["consumerPid"] != "urn:uuid:tc-1" {
		t.Errorf("consumerPid = %v, want the requested urn:uuid:tc-1", doc["consumerPid"])
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
	// The acknowledgment says REQUESTED; the transfer becomes STARTED once the
	// pushed start message has been attempted. See waitForTransferState.
	waitForTransferState(t, st, providerPID, TransferStarted)
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
		assertTransferErrorBody(t, name, rec)
	}
}

// assertTransferErrorBody pins the @type of a rejection document. Every node
// this connector emits carries a @type, and a rejection that names the wrong
// protocol is worse than an unlabelled one: nothing reads a transfer error
// body today, so only a test can catch a shared helper stamping
// ContractNegotiationError onto a transfer endpoint's rejection.
func assertTransferErrorBody(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Errorf("%s: rejection body is not JSON: %v", name, err)
		return
	}
	if doc["@type"] != TransferErrorType {
		t.Errorf("%s: rejection @type = %v, want %s", name, doc["@type"], TransferErrorType)
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
		if s.wantCode == http.StatusBadRequest {
			assertTransferErrorBody(t, s.endpoint+" from "+s.from, rec)
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
	// The consumerPid in the body is the only thing that correlates this push:
	// the counterparty's callback endpoint registers handlers against a path
	// pattern but never passes the path to them, so it looks the transfer up
	// by this field. The path assertion above is cosmetic by comparison — see
	// the wire contract's section 1.3.
	if msg["consumerPid"] != "urn:uuid:tc-1" {
		t.Errorf("pushed consumerPid = %v, want urn:uuid:tc-1", msg["consumerPid"])
	}
	if _, present := msg["dataAddress"]; present {
		t.Error("Phase A must not emit a dataAddress")
	}

	var ack map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil {
		t.Fatalf("acknowledgment body is not JSON: %v", err)
	}
	providerPID, _ := ack["providerPid"].(string)
	waitForTransferState(t, st, providerPID, TransferStarted)
}

// TestResolveTransferSequenceDefaultsToStarted pins the behavior every test
// written before this configuration existed depends on: an agreement nobody
// configured is still started.
func TestResolveTransferSequenceDefaultsToStarted(t *testing.T) {
	got := resolveTransferSequence(config.Config{}, "urn:uuid:unconfigured")
	if len(got) != 1 || got[0] != TransferStarted {
		t.Errorf("resolveTransferSequence(no entry) = %v, want [%s]", got, TransferStarted)
	}
}

func TestResolveTransferSequenceUsesTheMatchingEntry(t *testing.T) {
	cfg := config.Config{TransferPolicies: []config.TransferPolicy{
		{AgreementID: "urn:uuid:other", Sequence: []string{TransferTerminated}},
		{AgreementID: "urn:uuid:agreement-1", Sequence: []string{TransferStarted, TransferSuspended, TransferTerminated}},
	}}
	got := resolveTransferSequence(cfg, "urn:uuid:agreement-1")
	want := []string{TransferStarted, TransferSuspended, TransferTerminated}
	if len(got) != len(want) {
		t.Fatalf("resolveTransferSequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("resolveTransferSequence = %v, want %v", got, want)
			break
		}
	}
}

// TestResolveTransferSequenceEmptyEntryIsNotTheDefault is the distinction the
// whole mechanism rests on: an entry configured with an empty sequence must
// resolve to nothing, not to the default [STARTED] that having no entry gets.
func TestResolveTransferSequenceEmptyEntryIsNotTheDefault(t *testing.T) {
	cfg := config.Config{TransferPolicies: []config.TransferPolicy{
		{AgreementID: "urn:uuid:agreement-1", Sequence: []string{}},
	}}
	if got := resolveTransferSequence(cfg, "urn:uuid:agreement-1"); len(got) != 0 {
		t.Errorf("resolveTransferSequence(empty entry) = %v, want no steps at all", got)
	}
}

// TestResolveTransferSequenceFromALoadedConfig carries a real YAML document
// through config.Load into the resolver, rather than composing the two from
// separate tests. The four TCK tests that poll for REQUESTED depend on the
// whole path holding: whatever the decoder makes of `sequence: []` — a nil
// slice or an empty one — the entry must still resolve to no steps, while an
// agreement absent from that same document still gets the default.
func TestResolveTransferSequenceFromALoadedConfig(t *testing.T) {
	cfg, err := config.Load([]byte(
		"public_url: https://connector.example.org\n"+
			"participant_id: urn:participant:example\n"+
			"data_dir: ./data\n"+
			"transfer_policies:\n"+
			"  - agreement_id: urn:uuid:agreement-1\n"+
			"    sequence: []\n"), func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := resolveTransferSequence(cfg, "urn:uuid:agreement-1"); len(got) != 0 {
		t.Errorf("resolveTransferSequence(loaded empty sequence) = %v, want no steps at all", got)
	}
	if got := resolveTransferSequence(cfg, "urn:uuid:unconfigured"); len(got) != 1 || got[0] != TransferStarted {
		t.Errorf("resolveTransferSequence(agreement absent from the same document) = %v, want [%s]",
			got, TransferStarted)
	}
}

// TestTransferSequenceEmptyStaysRequestedAndPushesNothing covers the four TCK
// provider tests that carry no "provider started" step and poll for
// REQUESTED. Both halves are asserted: a connector that pushed a start and
// then failed to record it would leave the state at REQUESTED too, so the
// state alone proves nothing.
func TestTransferSequenceEmptyStaysRequestedAndPushesNothing(t *testing.T) {
	fc := newFakeTransferConsumer(t)
	cfg := config.Config{TransferPolicies: []config.TransferPolicy{
		{AgreementID: "urn:uuid:agreement-1", Sequence: []string{}},
	}}
	h, st := newTestTransferHandler(t, cfg)
	seedAgreement(t, st, "urn:uuid:agreement-1")

	providerPID := postTransferRequest(t, h, transferRequestBodyTo("urn:uuid:agreement-1", fc.srv.URL))

	fc.receivedNothing(t, 100*time.Millisecond)
	got, ok, err := st.GetTransfer(providerPID)
	if err != nil {
		t.Fatalf("GetTransfer: %v", err)
	}
	if !ok {
		t.Fatal("the acknowledgment named a providerPid that was never stored")
	}
	if got.State != TransferRequested {
		t.Errorf("stored state = %s, want %s", got.State, TransferRequested)
	}
}

// TestTransferSequenceStartThenTerminate is TP:01-01's shape.
func TestTransferSequenceStartThenTerminate(t *testing.T) {
	fc := newFakeTransferConsumer(t)
	h, st := newTestTransferHandler(t,
		transferPolicyConfig("urn:uuid:agreement-1", TransferStarted, TransferTerminated))
	seedAgreement(t, st, "urn:uuid:agreement-1")

	providerPID := postTransferRequest(t, h, transferRequestBodyTo("urn:uuid:agreement-1", fc.srv.URL))

	assertPushSequence(t, fc.waitFor(t, 2), []transferPush{
		{path: "/transfers/urn:uuid:tc-1/start", msgType: TransferStartMessageType},
		{path: "/transfers/urn:uuid:tc-1/termination", msgType: TransferTerminationMessageType},
	})
	waitForTransferState(t, st, providerPID, TransferTerminated)
}

// TestTransferSequenceSpacesItsSteps measures the pause between two steps.
//
// What it measures is the gap between the two pushes *arriving at the
// consumer*, against a handler given a 60ms stepDelay of its own — every
// other test runs at the millisecond TestMain sets, so none of them can tell
// a spaced driver from an unspaced one.
//
// What it catches is the deletion of `if i > 0 { time.Sleep(h.stepDelay) }`,
// or a refactor that moves the sleep after the last step, or one that pushes
// the steps concurrently: all three land the second push within microseconds
// of the first, an order of magnitude below the threshold. That regression is
// otherwise invisible until a TCK run, where it shows up as a 404 on a
// handler the counterparty had not registered yet, and the transfer stalls
// with nothing pointing at the cause.
//
// The threshold is deliberately well under the delay: time.Sleep guarantees
// *at least* its duration, so the real gap is 60ms plus scheduling, and 30ms
// leaves room for a loaded machine without ever approaching what an unspaced
// driver produces.
func TestTransferSequenceSpacesItsSteps(t *testing.T) {
	const stepDelay = 60 * time.Millisecond

	fc := newFakeTransferConsumer(t)
	h, st := newTestTransferHandler(t,
		transferPolicyConfig("urn:uuid:agreement-1", TransferStarted, TransferTerminated))
	// The handler is a value: this delay belongs to this test's copy alone,
	// so there is no shared state for another test's driver goroutine to race.
	h.stepDelay = stepDelay
	seedAgreement(t, st, "urn:uuid:agreement-1")

	providerPID := postTransferRequest(t, h, transferRequestBodyTo("urn:uuid:agreement-1", fc.srv.URL))

	got := fc.waitFor(t, 2)
	if gap := got[1].at.Sub(got[0].at); gap < stepDelay/2 {
		t.Errorf("the two steps arrived %v apart, want at least %v: the counterparty registers the handler for step N+1 only once step N has arrived",
			gap, stepDelay/2)
	}
	waitForTransferState(t, st, providerPID, TransferTerminated)
}

// TestTransferSequenceStopsWhenTheCounterpartyTakesOver pins what happens
// when a state write is lost to store.ErrStateChanged. The counterparty
// terminates the transfer while the first step's push is still in flight, so
// the write this connector makes afterward no longer holds — and a
// counterparty that has terminated the transfer has taken it over, so the
// remaining steps must not be pushed at all.
func TestTransferSequenceStopsWhenTheCounterpartyTakesOver(t *testing.T) {
	h, st := newTestTransferHandler(t, transferPolicyConfig("urn:uuid:agreement-1",
		TransferStarted, TransferSuspended, TransferTerminated))
	seedAgreement(t, st, "urn:uuid:agreement-1")

	var mu sync.Mutex
	var pushed []string
	consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode pushed message: %v", err)
		}
		msgType, _ := body["@type"].(string)
		mu.Lock()
		pushed = append(pushed, msgType)
		mu.Unlock()
		// The state write, not the handler: what the inbound termination
		// endpoint does to the store is exactly this, and calling the handler
		// from here would need a mutable handler variable captured by this
		// closure.
		if msgType == TransferStartMessageType {
			providerPID, _ := body["providerPid"].(string)
			if err := st.SetTransferState(providerPID, TransferRequested, TransferTerminated, time.Now()); err != nil {
				t.Errorf("terminate from the counterparty's side: %v", err)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer consumer.Close()

	providerPID := postTransferRequest(t, h, transferRequestBodyTo("urn:uuid:agreement-1", consumer.URL))

	// Long enough for the two remaining steps to have been pushed, had the
	// sequence continued: transferStepDelay is a millisecond under test.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	got := append([]string(nil), pushed...)
	mu.Unlock()
	if len(got) != 1 || got[0] != TransferStartMessageType {
		t.Errorf("pushed %v, want only the start message before the sequence stopped", got)
	}
	stored, ok, err := st.GetTransfer(providerPID)
	if err != nil || !ok {
		t.Fatalf("GetTransfer: %v (found %v)", err, ok)
	}
	if stored.State != TransferTerminated {
		t.Errorf("stored state = %s, want %s — the counterparty's termination must stand",
			stored.State, TransferTerminated)
	}
}

// TestTransferSequenceSuspendResumeComplete is TP:01-04: the longest sequence
// the TCK asks for, and the one that revisits STARTED.
func TestTransferSequenceSuspendResumeComplete(t *testing.T) {
	fc := newFakeTransferConsumer(t)
	h, st := newTestTransferHandler(t, transferPolicyConfig("urn:uuid:agreement-1",
		TransferStarted, TransferSuspended, TransferStarted, TransferCompleted))
	seedAgreement(t, st, "urn:uuid:agreement-1")

	providerPID := postTransferRequest(t, h, transferRequestBodyTo("urn:uuid:agreement-1", fc.srv.URL))

	assertPushSequence(t, fc.waitFor(t, 4), []transferPush{
		{path: "/transfers/urn:uuid:tc-1/start", msgType: TransferStartMessageType},
		{path: "/transfers/urn:uuid:tc-1/suspension", msgType: TransferSuspensionMessageType},
		{path: "/transfers/urn:uuid:tc-1/start", msgType: TransferStartMessageType},
		{path: "/transfers/urn:uuid:tc-1/completion", msgType: TransferCompletionMessageType},
	})
	waitForTransferState(t, st, providerPID, TransferCompleted)
}
