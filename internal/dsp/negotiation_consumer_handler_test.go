// This file holds the tests for the consumer role's negotiation handlers,
// split out of negotiation_handler_test.go alongside their production code
// in negotiation_consumer_handler.go. Shared test helpers used by both
// halves — newTestHandler, waitForConsumerState,
// newConsumerHandlerWithNegotiation, and the rest — stay in
// negotiation_handler_test.go.

package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

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

	// The channel above unblocks when the provider *receives* the request,
	// but startNegotiation then reads the response and records the
	// providerPid — work still in flight at that moment, on a goroutine that
	// would otherwise outlive this test and its deferred st.Close(). Waiting
	// for that write both bounds the goroutine and covers the step nothing
	// else at this level asserted: that startNegotiation stores the pid
	// sendInitialRequest actually returned.
	consumerPID, _ := msg["consumerPid"].(string)
	if consumerPID == "" {
		t.Fatalf("the request carries no consumerPid: %v", msg)
	}
	deadline := time.Now().Add(time.Second)
	for {
		n, ok, err := st.GetConsumer(consumerPID)
		if err != nil {
			t.Fatalf("GetConsumer: %v", err)
		}
		if ok && n.ProviderPID == "urn:uuid:provider-1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored ProviderPID = %q, want the pid the provider returned", n.ProviderPID)
		}
		time.Sleep(5 * time.Millisecond)
	}
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
	// attempts is written on the httptest server's goroutines and read by
	// this test, so it is atomic rather than a plain int — the same
	// cross-goroutine ordering problem TestHandleInitiate_Success solves with
	// a channel, in a shape that counts.
	var attempts atomic.Int64
	// The consumer pid travels the same way, and for a concrete reason: it is
	// generated inside handleInitiate, and this test needs it to wait for the
	// reaction goroutine to finish before returning.
	consumerPID := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/negotiations/request" {
			var msg map[string]any
			if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
				// Errorf, not Fatalf: this runs on the server's goroutine.
				t.Errorf("provider: decode initial request: %v", err)
			}
			pid, _ := msg["consumerPid"].(string)
			consumerPID <- pid
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(NegotiationStateDocument{ProviderPID: "urn:uuid:provider-1"})
			return
		}
		// Every other path is the termination attempt. Reject the first —
		// the registration-order race this policy exists to survive — and
		// accept the second, proving the send goes through pushCallback's
		// retrying path rather than a bespoke one-shot send.
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
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

	var pid string
	select {
	case pid = <-consumerPID:
	case <-time.After(time.Second):
		t.Fatal("the provider never received the initial contract request")
	}

	// Writing TERMINATED is startNegotiation's last act, so waiting for it
	// both proves the abandon policy ran and bounds the goroutine, which
	// would otherwise still be working when this test's deferred provider.Close
	// and st.Close run.
	waitForConsumerState(t, st, pid, StateTerminated)
	if got := attempts.Load(); got < 2 {
		t.Fatalf("provider received %d termination attempt(s), want at least 2 — the first rejection must not be the end of it", got)
	}
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

	// A settle window, for the same reason the sibling
	// ..._NeverAcknowledged_StaysAgreed has one: the state is already OFFERED
	// when the handler returns, so waitForConsumerState below would come back
	// immediately and this test would finish before a wrongly-dispatched
	// reaction could reach the provider above and fail it.
	time.Sleep(50 * time.Millisecond)
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
	// Buffered so the provider handler never blocks, and read from below
	// rather than polled as a shared variable: the counter-request arrives on
	// the goroutine reactToOffer runs on, so the channel is what orders that
	// write against this test's read.
	gotProviderPID := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg CounterRequestMessage
		json.NewDecoder(r.Body).Decode(&msg)
		gotProviderPID <- msg.ProviderPID
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

	select {
	case gotPID := <-gotProviderPID:
		if gotPID != n.ProviderPID {
			t.Errorf("provider received ProviderPID = %q, want %q", gotPID, n.ProviderPID)
		}
	case <-time.After(time.Second):
		t.Fatal("the provider never received a counter-request")
	}
	got := waitForConsumerState(t, st, n.ConsumerPID, StateOffered)
	if got.State != StateOffered {
		t.Error("counter reaction must not change local state — the negotiation stays OFFERED")
	}
}

// TestReactToOffer_PicksUpAProviderPIDRecordedAfterTheHandlerReadItsRow and
// its agreement twin pin the fix for the window between a negotiation being
// created and startNegotiation recording the provider's pid: a push that
// arrives inside it hands the reaction a row whose ProviderPID is still
// empty, and every outbound path template formats that pid into the URL. The
// stale copy is what the reaction is called with here; the store already has
// the pid, exactly as it would once the initial request's response landed.
func TestReactToOffer_PicksUpAProviderPIDRecordedAfterTheHandlerReadItsRow(t *testing.T) {
	gotPath := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	n := testConsumerNegotiationAt(provider.URL)
	n.State = StateOffered
	stale := n
	stale.ProviderPID = "" // what the handler read before the pid was stored
	h, st := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

	h.reactToOffer(stale, false)

	select {
	case path := <-gotPath:
		want := "/negotiations/" + n.ProviderPID + "/events"
		if path != want {
			t.Errorf("accepted event posted to %q, want %q — the pid recorded after the handler read its row must be picked up", path, want)
		}
	case <-time.After(time.Second):
		t.Fatal("the provider never received the accepted event")
	}
	waitForConsumerState(t, st, n.ConsumerPID, StateAccepted)
}

func TestReactToAgreement_PicksUpAProviderPIDRecordedAfterTheHandlerReadItsRow(t *testing.T) {
	gotPath := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	n := testConsumerNegotiationAt(provider.URL)
	n.State = StateAgreed
	stale := n
	stale.ProviderPID = ""
	h, st := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

	h.reactToAgreement(stale, false)

	select {
	case path := <-gotPath:
		want := "/negotiations/" + n.ProviderPID + "/agreement/verification"
		if path != want {
			t.Errorf("verification posted to %q, want %q", path, want)
		}
	case <-time.After(time.Second):
		t.Fatal("the provider never received the verification")
	}
	waitForConsumerState(t, st, n.ConsumerPID, StateVerified)
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
	return offerMessageJSONWithPermission(n, `[]`)
}

// offerMessageJSONWithPermission is offerMessageJSON with the offer's
// permission array supplied verbatim, so the constraint-guard tests can vary
// the one thing they are about.
func offerMessageJSONWithPermission(n store.ConsumerNegotiation, permission string) string {
	return `{"@context":["` + ContextURL + `"],"@type":"` + ContractOfferMessageType + `",` +
		`"providerPid":"` + n.ProviderPID + `","consumerPid":"` + n.ConsumerPID + `",` +
		`"offer":{"@id":"` + n.OfferID + `","target":"` + n.DatasetID + `","permission":` + permission + `}}`
}

func agreementMessageJSON(n store.ConsumerNegotiation) string {
	return agreementMessageJSONWithPermission(n, `[]`)
}

func agreementMessageJSONWithPermission(n store.ConsumerNegotiation, permission string) string {
	return `{"@context":["` + ContextURL + `"],"@type":"` + ContractAgreementMessageType + `",` +
		`"providerPid":"` + n.ProviderPID + `","consumerPid":"` + n.ConsumerPID + `",` +
		`"agreement":{"@id":"` + n.ProviderPID + `","target":"` + n.DatasetID + `","permission":` + permission +
		`,"assigner":"x","assignee":"y","timestamp":"2026-08-15T00:00:00Z"}}`
}

// The permission arrays the constraint-guard tests vary over. unconstrained*
// are the shapes the TCK's own mock provider sends —
// NegotiationFunctions.createOfferPolicy hard-codes an empty constraint list —
// and are the negative controls that keep the guard from firing on them.
const (
	unconstrainedPermission      = `[{"action":"use"}]`
	unconstrainedEmptyConstraint = `[{"action":"use","constraint":[]}]`
	constrainedPermission        = `[{"action":"use","constraint":[{"leftOperand":"spatial","operator":"eq","rightOperand":"EU"}]}]`
	constrainedSecondRule        = `[{"action":"use"},{"action":"use","constraint":[{"leftOperand":"spatial","operator":"eq","rightOperand":"EU"}]}]`
)

// TestHandleOffers_ConstraintGuard pins both directions of the rule
// CLAUDE.md states without exception — "never accept a constraint that is not
// enforced". This connector enforces none, so an offer carrying any
// constraint must take the reject path instead of the configured accept one,
// while the unconstrained shapes the TCK actually sends must still accept.
func TestHandleOffers_ConstraintGuard(t *testing.T) {
	cases := []struct {
		name       string
		permission string
		want       string
	}{
		{"no rules at all", `[]`, StateAccepted},
		{"a rule with no constraint key", unconstrainedPermission, StateAccepted},
		{"a rule with an empty constraint list", unconstrainedEmptyConstraint, StateAccepted},
		{"a rule carrying a constraint", constrainedPermission, StateTerminated},
		{"a constraint on the second rule only", constrainedSecondRule, StateTerminated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer provider.Close()

			n := testConsumerNegotiationAt(provider.URL)
			n.State = StateRequested
			// No consumer_policies entry: on_offer defaults to accept, so
			// anything other than ACCEPTED here is the guard firing.
			h, st := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

			req := httptest.NewRequest("POST", "/x", strings.NewReader(offerMessageJSONWithPermission(n, c.permission)))
			req.SetPathValue("id", n.ConsumerPID)
			w := httptest.NewRecorder()
			h.handleOffers(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 — the guard rejects the negotiation, not the message", w.Code)
			}

			waitForConsumerState(t, st, n.ConsumerPID, c.want)
		})
	}
}

// TestHandleAgreement_ConstraintGuard is the same rule on the binding
// artifact, and covers the direct-agreement path (CN_C:01-04) where no offer
// is ever pushed and the offer-side guard is therefore never consulted.
func TestHandleAgreement_ConstraintGuard(t *testing.T) {
	cases := []struct {
		name       string
		permission string
		want       string
	}{
		{"a rule with no constraint key", unconstrainedPermission, StateVerified},
		{"a rule with an empty constraint list", unconstrainedEmptyConstraint, StateVerified},
		{"a rule carrying a constraint", constrainedPermission, StateTerminated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer provider.Close()

			n := testConsumerNegotiationAt(provider.URL)
			n.State = StateRequested
			h, st := newConsumerHandlerWithNegotiation(t, config.Config{}, n)

			req := httptest.NewRequest("POST", "/x", strings.NewReader(agreementMessageJSONWithPermission(n, c.permission)))
			req.SetPathValue("id", n.ConsumerPID)
			w := httptest.NewRecorder()
			h.handleAgreement(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}

			waitForConsumerState(t, st, n.ConsumerPID, c.want)
		})
	}
}

// A consumer-role negotiation that reaches AGREED must leave behind the
// agreement it agreed to, or this connector can never transfer under it —
// POST /transfers/initiate refuses an agreement it has no row for. No TCK
// test covers this: the TP_C suite cites seeded agreements and never
// negotiates one. So this unit test is the only evidence, and a green suite
// says nothing either way.
func TestHandleAgreement_RecordsTheAgreement(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	// agreementMessageJSON puts the provider pid in the agreement's @id.
	got, ok, err := st.GetAgreement(n.ProviderPID)
	if err != nil {
		t.Fatalf("GetAgreement: %v", err)
	}
	if !ok {
		t.Fatal("no agreement row was written")
	}
	if got.Origin != store.OriginAgreed {
		t.Errorf("Origin = %q, want %q", got.Origin, store.OriginAgreed)
	}
	if got.DatasetID != n.DatasetID {
		t.Errorf("DatasetID = %q, want %q", got.DatasetID, n.DatasetID)
	}
	if got.ConsumerPID != n.ConsumerPID {
		t.Errorf("ConsumerPID = %q, want %q", got.ConsumerPID, n.ConsumerPID)
	}
}
