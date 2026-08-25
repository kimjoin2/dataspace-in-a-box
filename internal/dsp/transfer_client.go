// Package dsp: this file holds every outbound transfer-protocol HTTP call
// this connector makes as consumer. Everything in transfer_handler.go and
// transfer_consumer_handler.go answers an inbound request; this file
// initiates one — the same split negotiation_client.go makes, for the same
// reason.
package dsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// Path templates for transfer calls this connector makes as consumer,
// formatted against a provider's base URL
// (store.ConsumerTransfer.ProviderBaseURL) and the provider's own pid.
const (
	consumerTransferRequestPath     = "/transfers/request"
	consumerTransferStartPath       = "/transfers/%s/start"
	consumerTransferSuspensionPath  = "/transfers/%s/suspension"
	consumerTransferCompletionPath  = "/transfers/%s/completion"
	consumerTransferTerminationPath = "/transfers/%s/termination"
)

// sendTransferRequest POSTs the initial TransferRequestMessage and returns
// the providerPid from the provider's synchronous TransferProcess response.
//
// Not retried, for the reason sendInitialRequest gives: a retry against a
// provider that already accepted the first attempt creates a second
// transfer, and there is no way to tell that case from a lost request.
func sendTransferRequest(providerBaseURL string, msg TransferRequestMessage, aud string) (string, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal transfer request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, providerBaseURL+consumerTransferRequestPath, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build transfer request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// An error to the caller, for the reason sendInitialRequest gives.
	authorization, maySend := mintOutboundCredential(aud)
	if !maySend {
		return "", fmt.Errorf("post transfer request: %w", errRosterExpired)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := callbackHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("post transfer request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("post transfer request: provider responded %d", resp.StatusCode)
	}
	var doc TransferProcessDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("decode transfer request response: %w", err)
	}
	if doc.ProviderPID == "" {
		return "", fmt.Errorf("transfer request response carries no providerPid")
	}
	return doc.ProviderPID, nil
}

// buildTransferRequestMessage is this connector's opening message as
// consumer. callbackAddress is where the provider sends everything after
// this: config.PublicURL + VersionPath.
//
// No dataAddress: it is required only when the format calls for a push
// transfer, and it is this connector that would be pushed to. Sending an
// empty one would also trip the endpointProperties schema's minItems, the
// same trap DECISIONS.md section 24.7 records for MessageOffer.
func buildTransferRequestMessage(t store.ConsumerTransfer, callbackAddress string) TransferRequestMessage {
	return TransferRequestMessage{
		Context:         []string{ContextURL},
		Type:            TransferRequestMessageType,
		ConsumerPID:     t.ConsumerPID,
		AgreementID:     t.AgreementID,
		Format:          t.Format,
		CallbackAddress: callbackAddress,
	}
}
