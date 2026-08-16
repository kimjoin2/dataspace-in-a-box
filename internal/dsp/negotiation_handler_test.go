package dsp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// fakeCallback records every push it receives, keyed by the request path it
// arrived on (e.g. "/negotiations/<pid>/offers").
type fakeCallback struct {
	mu     sync.Mutex
	pushes map[string][]map[string]any
	srv    *httptest.Server
}

func newFakeCallback() *fakeCallback {
	fc := &fakeCallback{pushes: make(map[string][]map[string]any)}
	fc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		fc.mu.Lock()
		fc.pushes[r.URL.Path] = append(fc.pushes[r.URL.Path], body)
		fc.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	return fc
}

// wait polls until pathSuffix has received at least one push, or fails the
// test after one second. terminateAfterOfferDelay is overridden to a few
// milliseconds in the tests that need this, so one second is generous.
func (fc *fakeCallback) wait(t *testing.T, pathSuffix string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		for path, pushes := range fc.pushes {
			if strings.HasSuffix(path, pathSuffix) && len(pushes) > 0 {
				body := pushes[0]
				fc.mu.Unlock()
				return body
			}
		}
		fc.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no push received on a path ending %q within the deadline", pathSuffix)
	return nil
}

// waitForState polls until providerPID's stored state equals want, or fails
// the test after one second. dispatch and pushAndStore run in a goroutine
// (`go h.dispatch(...)`, `go h.pushAndStore(...)`) so that the HTTP handler
// can return and its own synchronous response go out on the wire before the
// push is attempted — see dispatch's doc comment. That means observing the
// push via fc.wait does not guarantee the state write pushAndStore makes
// afterward has happened yet; callers that need both must wait for both.
func waitForState(t *testing.T, st *store.Store, providerPID, want string) store.Negotiation {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		n, ok, err := st.Get(providerPID)
		if err == nil && ok && n.State == want {
			return n
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("stored state for %q did not reach %q within the deadline", providerPID, want)
	return store.Negotiation{}
}

func (fc *fakeCallback) neverReceives(t *testing.T, pathSuffix string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		for path := range fc.pushes {
			if strings.HasSuffix(path, pathSuffix) {
				fc.mu.Unlock()
				t.Fatalf("unexpected push received on a path ending %q", pathSuffix)
			}
		}
		fc.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
}

func negotiationTestConfig(publicURL string, datasets ...config.Dataset) config.Config {
	return config.Config{PublicURL: publicURL, Datasets: datasets}
}

func newTestHandler(t *testing.T, cfg config.Config) (negotiationHandler, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// fakeCallback below is an httptest.Server, always bound to loopback —
	// exactly what validateCallbackURL exists to reject in production. These
	// tests are about dispatch/state-machine behavior, not the SSRF filter
	// (which has its own direct table test in callback_test.go), so it is
	// disabled for the duration of this test.
	origValidate := validateOutgoingCallback
	validateOutgoingCallback = func(string) error { return nil }
	t.Cleanup(func() { validateOutgoingCallback = origValidate })

	return negotiationHandler{cfg: cfg, store: st}, st
}

func postJSON(t *testing.T, handler http.HandlerFunc, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(b))
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func requestMessageBody(consumerPID, offerID, datasetID, callbackAddress string) map[string]any {
	return map[string]any{
		"@context":        []string{ContextURL},
		"@type":           ContractRequestMessageType,
		"consumerPid":     consumerPID,
		"offer":           map[string]any{"@id": offerID, "target": datasetID},
		"callbackAddress": callbackAddress,
	}
}

func TestHandleContractRequest_UnknownDataset_NoPush(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:known"}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:unknown#offer", "urn:dataset:unknown", fc.srv.URL))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body)
	}
	var doc NegotiationStateDocument
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if doc.State != StateRequested {
		t.Errorf("State = %q, want %q", doc.State, StateRequested)
	}

	fc.neverReceives(t, "/offers", 100*time.Millisecond)
	fc.neverReceives(t, "/agreement", 100*time.Millisecond)
	fc.neverReceives(t, "/termination", 100*time.Millisecond)

	n, ok, err := st.Get(doc.ProviderPID)
	if err != nil || !ok {
		t.Fatalf("store.Get(%q): ok=%v err=%v", doc.ProviderPID, ok, err)
	}
	if n.State != StateRequested {
		t.Errorf("stored state = %q, want %q", n.State, StateRequested)
	}
}

func TestHandleContractRequest_MatchedValid_PushesAgreement(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:a"+offerIDSuffix, "urn:dataset:a", fc.srv.URL))
	var doc NegotiationStateDocument
	json.Unmarshal(rr.Body.Bytes(), &doc)

	push := fc.wait(t, "/agreement")
	if push["providerPid"] != doc.ProviderPID {
		t.Errorf("pushed providerPid = %v, want %v", push["providerPid"], doc.ProviderPID)
	}

	waitForState(t, st, doc.ProviderPID, StateAgreed)
}

func TestHandleContractRequest_MatchedExpired_PushesTermination(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	past := time.Now().Add(-time.Hour)
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a", ValidityUntil: &past}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:a"+offerIDSuffix, "urn:dataset:a", fc.srv.URL))
	var doc NegotiationStateDocument
	json.Unmarshal(rr.Body.Bytes(), &doc)

	fc.wait(t, "/termination")
	fc.neverReceives(t, "/offers", 50*time.Millisecond)
	fc.neverReceives(t, "/agreement", 50*time.Millisecond)

	waitForState(t, st, doc.ProviderPID, StateTerminated)
}

func TestHandleContractRequest_MismatchedValid_PushesOffer(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:a#some-other-offer", "urn:dataset:a", fc.srv.URL))
	var doc NegotiationStateDocument
	json.Unmarshal(rr.Body.Bytes(), &doc)

	push := fc.wait(t, "/offers")
	offer := push["offer"].(map[string]any)
	if offer["@id"] != "urn:dataset:a"+offerIDSuffix {
		t.Errorf("pushed offer @id = %v, want the connector's canonical offer", offer["@id"])
	}

	waitForState(t, st, doc.ProviderPID, StateOffered)
}

func TestHandleContractRequest_MismatchedExpired_OffersThenTerminates(t *testing.T) {
	orig := terminateAfterOfferDelay
	terminateAfterOfferDelay = 10 * time.Millisecond
	defer func() { terminateAfterOfferDelay = orig }()

	fc := newFakeCallback()
	defer fc.srv.Close()
	past := time.Now().Add(-time.Hour)
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a", ValidityUntil: &past}))

	rr := postJSON(t, h.handleContractRequest, "/negotiations/request",
		requestMessageBody("urn:uuid:consumer-1", "urn:dataset:a#some-other-offer", "urn:dataset:a", fc.srv.URL))
	var doc NegotiationStateDocument
	json.Unmarshal(rr.Body.Bytes(), &doc)

	fc.wait(t, "/offers")
	fc.wait(t, "/termination")

	waitForState(t, st, doc.ProviderPID, StateTerminated)
}

func TestHandleReRequest_SameOffer_FirstTime_Returns200AndStaysOffered(t *testing.T) {
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#requested-offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleReRequest, n.ProviderPID,
		map[string]any{
			"@context": []string{ContextURL}, "@type": ContractRequestMessageType,
			"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
			"offer": map[string]any{"@id": n.OfferID, "target": n.DatasetID},
		})
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}

	got, _, _ := st.Get(n.ProviderPID)
	if got.State != StateOffered {
		t.Errorf("stored state = %q, want %q (a matching re-request makes no state change)", got.State, StateOffered)
	}
	if !got.Rerequested {
		t.Error("Rerequested = false, want true after the one allowed re-request")
	}
}

func TestHandleReRequest_Second_Returns400(t *testing.T) {
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#requested-offer",
		CallbackAddress: "https://consumer.example.org/callback",
		Rerequested:     true,
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleReRequest, n.ProviderPID,
		map[string]any{
			"@context": []string{ContextURL}, "@type": ContractRequestMessageType,
			"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
			"offer": map[string]any{"@id": n.OfferID, "target": n.DatasetID},
		})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body)
	}
}

func TestHandleReRequest_DifferentOffer_PushesTermination(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#requested-offer",
		CallbackAddress: fc.srv.URL,
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleReRequest, n.ProviderPID,
		map[string]any{
			"@context": []string{ContextURL}, "@type": ContractRequestMessageType,
			"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
			"offer": map[string]any{"@id": "urn:dataset:a#yet-another-offer", "target": n.DatasetID},
		})
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}
	fc.wait(t, "/termination")
}

func TestHandleEvent_Accept_FromOffered_PushesAgreement(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: fc.srv.URL,
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleEvent, n.ProviderPID, map[string]any{
		"@context": []string{ContextURL}, "@type": ContractNegotiationEventMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID, "eventType": "ACCEPTED",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}
	fc.wait(t, "/agreement")
}

func TestHandleVerification_FromOffered_Returns400(t *testing.T) {
	// CN:03-02: verification is only legal from AGREED.
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleVerification, n.ProviderPID, map[string]any{
		"@context": []string{ContextURL}, "@type": ContractAgreementVerificationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body)
	}
}

func TestHandleVerification_FromAccepted_Returns400(t *testing.T) {
	// CN:03-03: verifying immediately after ACCEPTED, before AGREED.
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateAccepted, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleVerification, n.ProviderPID, map[string]any{
		"@context": []string{ContextURL}, "@type": ContractAgreementVerificationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body)
	}
}

func TestHandleVerification_FromAgreed_FinalizesAndPushesEvent(t *testing.T) {
	fc := newFakeCallback()
	defer fc.srv.Close()
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateAgreed, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: fc.srv.URL,
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleVerification, n.ProviderPID, map[string]any{
		"@context": []string{ContextURL}, "@type": ContractAgreementVerificationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}
	push := fc.wait(t, "/events")
	if push["eventType"] != "FINALIZED" {
		t.Errorf("pushed eventType = %v, want FINALIZED", push["eventType"])
	}

	waitForState(t, st, n.ProviderPID, StateFinalized)
}

func TestHandleTermination_FromFinalized_Returns400(t *testing.T) {
	// CN:03-01: terminating a FINALIZED negotiation.
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateFinalized, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleTermination, n.ProviderPID, map[string]any{
		"@context": []string{ContextURL}, "@type": ContractNegotiationTerminationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID, "code": "1",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body)
	}
}

func TestHandleTermination_FromOffered_Terminates(t *testing.T) {
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	rr := postJSONWithID(t, h.handleTermination, n.ProviderPID, map[string]any{
		"@context": []string{ContextURL}, "@type": ContractNegotiationTerminationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID, "code": "1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}
	got, _, _ := st.Get(n.ProviderPID)
	if got.State != StateTerminated {
		t.Errorf("stored state = %q, want %q", got.State, StateTerminated)
	}
}

func TestHandleGetNegotiation(t *testing.T) {
	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateAgreed, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/negotiations/"+n.ProviderPID, nil)
	req.SetPathValue("id", n.ProviderPID)
	rr := httptest.NewRecorder()
	h.handleGetNegotiation(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body)
	}
	var doc NegotiationStateDocument
	json.Unmarshal(rr.Body.Bytes(), &doc)
	if doc.State != StateAgreed {
		t.Errorf("State = %q, want %q", doc.State, StateAgreed)
	}
}

func TestHandleGetNegotiation_Missing_Returns404(t *testing.T) {
	h, _ := newTestHandler(t, negotiationTestConfig("https://provider.example.org"))
	req := httptest.NewRequest(http.MethodGet, "/negotiations/does-not-exist", nil)
	req.SetPathValue("id", "does-not-exist")
	rr := httptest.NewRecorder()
	h.handleGetNegotiation(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestNegotiationHandlersValidateTheEnvelope covers the @context/@type check
// on every handler that takes a message body. Each case is posted from a
// state the message is legal in, and with every field but the envelope
// correct, so the only thing a 400 can be about is the envelope itself.
// Structural validation is by direct field check, not a schema library —
// CLAUDE.md's JSON-LD convention and DECISIONS.md section 22.5.
func TestNegotiationHandlersValidateTheEnvelope(t *testing.T) {
	handlers := []struct {
		name string
		// state the negotiation must be in for this message to be legal.
		state string
		// msgType is the @type a valid message of this kind carries.
		msgType string
		pick    func(negotiationHandler) http.HandlerFunc
		// extra holds the non-envelope fields the handler also requires.
		extra map[string]any
		// wantState, when set, is the state a valid message leaves the
		// negotiation in once its dispatch goroutine has run. The test waits
		// for it, so the goroutine is finished before the store closes
		// underneath it.
		wantState string
	}{
		{
			name: "event", state: StateOffered, msgType: ContractNegotiationEventMessageType,
			pick:  func(h negotiationHandler) http.HandlerFunc { return h.handleEvent },
			extra: map[string]any{"eventType": "ACCEPTED"}, wantState: StateAgreed,
		},
		{
			name: "verification", state: StateAgreed, msgType: ContractAgreementVerificationMessageType,
			pick:      func(h negotiationHandler) http.HandlerFunc { return h.handleVerification },
			wantState: StateFinalized,
		},
		{
			name: "termination", state: StateOffered, msgType: ContractNegotiationTerminationMessageType,
			pick:  func(h negotiationHandler) http.HandlerFunc { return h.handleTermination },
			extra: map[string]any{"code": "1"},
		},
		{
			name: "re-request", state: StateOffered, msgType: ContractRequestMessageType,
			pick:  func(h negotiationHandler) http.HandlerFunc { return h.handleReRequest },
			extra: map[string]any{"offer": map[string]any{"@id": "urn:dataset:a#offer", "target": "urn:dataset:a"}},
		},
	}

	cases := []struct {
		name string
		// body builds the request body from a valid one.
		body func(valid map[string]any) any
		want int
	}{
		{"valid envelope", func(valid map[string]any) any { return valid }, http.StatusOK},
		{"wrong @type", func(valid map[string]any) any {
			valid["@type"] = "SomeOtherMessage"
			return valid
		}, http.StatusBadRequest},
		{"missing @type", func(valid map[string]any) any {
			delete(valid, "@type")
			return valid
		}, http.StatusBadRequest},
		{"missing @context", func(valid map[string]any) any {
			delete(valid, "@context")
			return valid
		}, http.StatusBadRequest},
		{"@context without the DSP context", func(valid map[string]any) any {
			valid["@context"] = []string{"https://example.org/some-other-context.jsonld"}
			return valid
		}, http.StatusBadRequest},
		{"body is not an object", func(map[string]any) any { return 5 }, http.StatusBadRequest},
	}

	for _, hc := range handlers {
		for _, tc := range cases {
			t.Run(hc.name+"/"+tc.name, func(t *testing.T) {
				fc := newFakeCallback()
				defer fc.srv.Close()
				h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org",
					config.Dataset{ID: "urn:dataset:a"}))
				n := store.Negotiation{
					ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
					State: hc.state, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a" + offerIDSuffix,
					CallbackAddress: fc.srv.URL,
					CreatedAt:       time.Now(), UpdatedAt: time.Now(),
				}
				if err := st.Create(n); err != nil {
					t.Fatalf("store.Create: %v", err)
				}

				valid := map[string]any{"@context": []string{ContextURL}, "@type": hc.msgType}
				for k, v := range hc.extra {
					valid[k] = v
				}
				rr := postJSONWithID(t, hc.pick(h), n.ProviderPID, tc.body(valid))
				if rr.Code != tc.want {
					t.Errorf("status = %d, want %d; body: %s", rr.Code, tc.want, rr.Body)
				}
				if tc.want == http.StatusOK && hc.wantState != "" {
					waitForState(t, st, n.ProviderPID, hc.wantState)
				}
			})
		}
	}
}

// TestSynchronousResponseDoesNotWaitForTheCallbackPush is the regression test
// for DECISIONS.md section 23.8, the bug that made the real TCK time out:
// net/http holds a small response body in its buffer and puts nothing on the
// wire until the handler function returns, so a handler that writes its
// response and then pushes inline does not actually finish responding until
// the push does. Re-inlining dispatch — dropping the `go` — brings that back.
//
// Nothing built on httptest.NewRecorder can catch it: a recorder is a
// struct, and writing to one says nothing about when bytes reach a client.
// This test therefore runs the real router behind a real server and times a
// real client, against a callback endpoint slow enough that a handler
// waiting for it could not possibly look fast.
func TestSynchronousResponseDoesNotWaitForTheCallbackPush(t *testing.T) {
	const callbackDelay = 500 * time.Millisecond
	const responseBudget = 100 * time.Millisecond

	pushed := make(chan struct{}, 1)
	slowCallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(callbackDelay)
		w.WriteHeader(http.StatusOK)
		select {
		case pushed <- struct{}{}:
		default:
		}
	}))
	defer slowCallback.Close()

	// newTestHandler also opens the store and disables the SSRF filter (the
	// callback server is on loopback), both of which this test needs. Its
	// handler value is unused: this test goes in through the real router.
	cfg := negotiationTestConfig("https://provider.example.org", config.Dataset{ID: "urn:dataset:a"})
	_, st := newTestHandler(t, cfg)
	srv := httptest.NewServer(NewRouter(cfg, st))
	defer srv.Close()

	// A mismatched offer for a valid dataset: one offer push, no follow-up
	// termination, so the only thing between the request and its response is
	// the slow callback.
	b, err := json.Marshal(requestMessageBody("urn:uuid:consumer-1", "urn:dataset:a#some-other-offer",
		"urn:dataset:a", slowCallback.URL))
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	start := time.Now()
	resp, err := http.Post(srv.URL+VersionPath+"/negotiations/request", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /negotiations/request: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", resp.StatusCode, body)
	}
	if elapsed > responseBudget {
		t.Errorf("the synchronous response took %s, want under %s: it is waiting for the %s callback push, "+
			"which means dispatch is running inline instead of in a goroutine", elapsed, responseBudget, callbackDelay)
	}

	// And the push does still happen — otherwise a connector that never
	// pushed at all would pass the assertion above.
	select {
	case <-pushed:
	case <-time.After(5 * time.Second):
		t.Fatal("the callback push never arrived")
	}
}

// TestVerificationIsRejectedWhileTheAgreementIsStillInFlight is CN:03-03 in
// miniature, and it exists to stop pushAndStore's push-then-store order from
// being "cleaned up" into store-then-push. That reordering looks like an
// improvement — GET would stop trailing a push that already landed — and it
// fails the real TCK, because it makes the negotiation AGREED a millisecond
// after the accept, while the consumer still has no agreement. The state
// check in handleVerification is only a real guard as long as "not AGREED
// yet" means "the consumer has not been told yet". See DECISIONS.md
// section 23.12.
func TestVerificationIsRejectedWhileTheAgreementIsStillInFlight(t *testing.T) {
	// The callback signals that the push has started and only then blocks, so
	// the verification below happens at a known point — after the push began,
	// long before it finished — rather than at whatever point a sleep happens
	// to land on.
	const callbackDelay = 500 * time.Millisecond
	pushStarted := make(chan struct{}, 1)

	slowCallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case pushStarted <- struct{}{}:
		default:
		}
		time.Sleep(callbackDelay)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowCallback.Close()

	h, st := newTestHandler(t, negotiationTestConfig("https://provider.example.org",
		config.Dataset{ID: "urn:dataset:a"}))
	n := store.Negotiation{
		ProviderPID: "urn:uuid:provider-1", ConsumerPID: "urn:uuid:consumer-1",
		State: StateOffered, DatasetID: "urn:dataset:a", OfferID: "urn:dataset:a" + offerIDSuffix,
		CallbackAddress: slowCallback.URL,
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.Create(n); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	accept := postJSONWithID(t, h.handleEvent, n.ProviderPID, map[string]any{
		"@context": []string{ContextURL}, "@type": ContractNegotiationEventMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID, "eventType": "ACCEPTED",
	})
	if accept.Code != http.StatusOK {
		t.Fatalf("accept status = %d, want 200; body: %s", accept.Code, accept.Body)
	}

	select {
	case <-pushStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the agreement push never started")
	}

	// The agreement push is in flight and will not be delivered for
	// callbackDelay yet. A consumer that verifies during that window is
	// verifying an agreement it cannot have. Store-then-push would already
	// have recorded AGREED here and accepted this.
	verify := postJSONWithID(t, h.handleVerification, n.ProviderPID, map[string]any{
		"@context": []string{ContextURL}, "@type": ContractAgreementVerificationMessageType,
		"providerPid": n.ProviderPID, "consumerPid": n.ConsumerPID,
	})
	if verify.Code != http.StatusBadRequest {
		t.Errorf("verification during agreement delivery: status = %d, want 400; body: %s",
			verify.Code, verify.Body)
	}

	// Once delivery finishes the negotiation does reach AGREED, so the
	// rejection above is about timing, not a stuck state machine.
	waitForState(t, st, n.ProviderPID, StateAgreed)
}

// postJSONWithID posts body to a handler that reads r.PathValue("id"), with
// id set the way http.ServeMux would set it after routing.
func postJSONWithID(t *testing.T, handler http.HandlerFunc, id string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/negotiations/"+id, bytes.NewReader(b))
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

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
	// Buffered so the provider handler never blocks, and read from below
	// instead of polling a shared variable: the request arrives on the
	// goroutine handleInitiate dispatches, so the channel is what orders the
	// write against this test's read.
	received := make(chan map[string]any, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A map, not RequestMessage: what matters here is what actually went
		// on the wire, since the TCK validates the request against its own
		// schema before the negotiation is allowed to start.
		var msg map[string]any
		json.NewDecoder(r.Body).Decode(&msg)
		received <- msg
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

	var msg map[string]any
	select {
	case msg = <-received:
	case <-time.After(time.Second):
		t.Fatal("the provider never received the initial contract request")
	}
	if msg["@type"] != ContractRequestMessageType {
		t.Errorf("@type = %v, want %q", msg["@type"], ContractRequestMessageType)
	}
	if msg["callbackAddress"] != cfg.PublicURL+VersionPath {
		t.Errorf("callbackAddress = %v, want %q", msg["callbackAddress"], cfg.PublicURL+VersionPath)
	}
	// The exact ids the initiate call supplied, echoed rather than regenerated.
	assertEmittedOffer(t, msg, "urn:dataset:a#offer", "urn:dataset:a")
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
