package dsp

import (
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// The transfer process states, from transfer-process-schema.json's own enum.
// These are deliberately separate constants from the negotiation states even
// where the strings match: the two protocols' state sets overlap by name and
// differ in meaning, and sharing one constant would let a wrong-protocol
// comparison compile silently.
const (
	TransferRequested  = "REQUESTED"
	TransferStarted    = "STARTED"
	TransferCompleted  = "COMPLETED"
	TransferSuspended  = "SUSPENDED"
	TransferTerminated = "TERMINATED"
)

// Message @type values.
const (
	TransferRequestMessageType     = "TransferRequestMessage"
	TransferProcessType            = "TransferProcess"
	TransferStartMessageType       = "TransferStartMessage"
	TransferSuspensionMessageType  = "TransferSuspensionMessage"
	TransferTerminationMessageType = "TransferTerminationMessage"
	TransferCompletionMessageType  = "TransferCompletionMessage"
	TransferErrorType              = "TransferError"
)

// TransferRequestMessage is the body of POST /transfers/request. Only the
// fields this connector inspects are declared, matching the direct-field-check
// approach of DECISIONS.md section 22.5. dataAddress is deliberately absent:
// it is optional in the schema, Phase A has no data plane, and declaring a
// field nothing reads invites someone to believe it is used.
type TransferRequestMessage struct {
	Context         []string `json:"@context"`
	Type            string   `json:"@type"`
	ConsumerPID     string   `json:"consumerPid"`
	AgreementID     string   `json:"agreementId"`
	Format          string   `json:"format"`
	CallbackAddress string   `json:"callbackAddress"`
}

// TransferProcessDoc is the response body for every transfer endpoint that
// returns the process itself.
type TransferProcessDoc struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
	State       string   `json:"state"`
}

// TransferStartMessage is pushed to the consumer's callback address when this
// connector starts a transfer. Phase A carries no dataAddress.
type TransferStartMessage struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

// TransferSuspensionMessage, TransferTerminationMessage, and
// TransferCompletionMessage are the inbound messages this connector accepts on
// a running transfer. The TCK registers no schema validator for these three,
// so their shape is checked only by what its pipeline reads out of them; this
// connector emits and accepts them to the same standard as the rest.
type TransferSuspensionMessage struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

type TransferTerminationMessage struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

type TransferCompletionMessage struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

// startLegalFrom reports whether a transfer in this state may start. SUSPENDED
// is included because resuming a suspended transfer is a start.
func startLegalFrom(state string) bool {
	return state == TransferRequested || state == TransferSuspended
}

// completionLegalFrom reports whether a transfer in this state may complete.
// Only a running transfer can finish.
func completionLegalFrom(state string) bool {
	return state == TransferStarted
}

// suspensionLegalFrom reports whether a transfer in this state may be
// suspended. Only a running transfer can be paused.
func suspensionLegalFrom(state string) bool {
	return state == TransferStarted
}

// terminationLegalFrom reports whether a transfer in this state may be
// terminated. Anything not already in a terminal state can be.
func terminationLegalFrom(state string) bool {
	return state == TransferRequested || state == TransferStarted || state == TransferSuspended
}

func buildTransferProcessDoc(t store.TransferProcess) TransferProcessDoc {
	return TransferProcessDoc{
		Context:     []string{ContextURL},
		Type:        TransferProcessType,
		ProviderPID: t.ProviderPID,
		ConsumerPID: t.ConsumerPID,
		State:       t.State,
	}
}

func buildTransferStartMessage(t store.TransferProcess) TransferStartMessage {
	return TransferStartMessage{
		Context:     []string{ContextURL},
		Type:        TransferStartMessageType,
		ProviderPID: t.ProviderPID,
		ConsumerPID: t.ConsumerPID,
	}
}
