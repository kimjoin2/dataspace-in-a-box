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

// waitForTransferState is waitForState's transfer-table counterpart, and
// exists for the same reason: startTransfer runs on its own goroutine
// (`go h.startTransfer(t)`) so the handler can return and its own response go
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
