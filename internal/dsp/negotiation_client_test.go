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
		// Decoded as a map, not into RequestMessage: a typed decode would
		// silently discard anything the schema requires and this connector
		// failed to send, which is exactly how the missing @type reached the
		// real TCK.
		var msg map[string]any
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Fatalf("provider: decode request: %v", err)
		}
		assertEmittedOffer(t, msg, "urn:dataset:a#offer", "urn:dataset:a")
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
	// A map, not CounterRequestMessage, for the reason given in
	// TestSendInitialRequest_Success: the counter-request's offer is
	// validated by the same TCK schema as the initial request's.
	var gotBody map[string]any
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
	if gotBody["providerPid"] != n.ProviderPID {
		t.Errorf("providerPid = %v, want %q — without it the TCK's mock provider reads a counter-request as a duplicate initial request",
			gotBody["providerPid"], n.ProviderPID)
	}
	assertEmittedOffer(t, gotBody, n.OfferID, n.DatasetID)
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
