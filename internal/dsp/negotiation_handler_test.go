package dsp

import (
	"bytes"
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
		"@context": ContextURL, "@type": ContractNegotiationEventMessageType,
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
		"@context": ContextURL, "@type": ContractAgreementVerificationMessageType,
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
		"@context": ContextURL, "@type": ContractAgreementVerificationMessageType,
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
		"@context": ContextURL, "@type": ContractAgreementVerificationMessageType,
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
		"@context": ContextURL, "@type": ContractNegotiationTerminationMessageType,
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
		"@context": ContextURL, "@type": ContractNegotiationTerminationMessageType,
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
