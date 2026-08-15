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
