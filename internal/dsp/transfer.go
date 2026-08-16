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
//
// Two things about its shape are execution-verified wire requirements rather
// than style, and both are recorded here because `Context []string` is exactly
// the field a tidy-up turns into a `string`:
//   - `@context` must be a JSON *array* of strings. A bare string fails with
//     `Invalid message: [string found, array expected, ...]`.
//   - `@type` must be the unprefixed term `"TransferProcess"`. The TCK looks
//     its validator up by the raw value, so `"dspace:TransferProcess"` skips
//     validation silently and then breaks JSON-LD expansion with
//     `Property '.../state' was not found`.
//
// The wire contract calls these the single most likely thing to get wrong —
// docs/superpowers/specs/2026-08-16-transfer-process-tck-wire-contract.md
// §1.4. The same two rules govern every other message in this package.
type TransferProcessDoc struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
	State       string   `json:"state"`
}

// TransferStartMessage is pushed to the consumer's callback address when this
// connector starts a transfer. Phase A carries no dataAddress, and that
// omission is a wire-correctness requirement here, not a style choice: adding
// a dataAddress field activates data-address-schema.json, which then demands
// @type: "DataAddress" and an endpointType read in @id form, for no benefit
// since no provider-role TCK test asserts anything about it. Whoever gives
// this connector a real HTTP-PULL data plane and needs to put a dataAddress
// back on this message must satisfy that schema shape or the TCK will reject
// the push. See docs/superpowers/specs/2026-08-16-transfer-process-tck-wire-contract.md
// section 1.7 for the full evidence.
type TransferStartMessage struct {
	Context     []string `json:"@context"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

// TransferSuspensionMessage, TransferTerminationMessage, and
// TransferCompletionMessage are the messages this connector accepts on a
// running transfer — and, where a configured sequence says so, emits on one
// of its own. The TCK registers no schema validator for these three,
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

// The three builders below exist because this connector emits these messages
// as well as accepting them: a configured transfer sequence can suspend,
// complete, or terminate a transfer of its own accord — see
// transfer_handler.go's driveTransfer. Both pids go on the wire because the
// counterparty correlates the push by the consumer pid in the body, not by
// the path it arrived on (the wire contract's section 1.3).
func buildTransferSuspensionMessage(t store.TransferProcess) TransferSuspensionMessage {
	return TransferSuspensionMessage{
		Context:     []string{ContextURL},
		Type:        TransferSuspensionMessageType,
		ProviderPID: t.ProviderPID,
		ConsumerPID: t.ConsumerPID,
	}
}

func buildTransferCompletionMessage(t store.TransferProcess) TransferCompletionMessage {
	return TransferCompletionMessage{
		Context:     []string{ContextURL},
		Type:        TransferCompletionMessageType,
		ProviderPID: t.ProviderPID,
		ConsumerPID: t.ConsumerPID,
	}
}

func buildTransferTerminationMessage(t store.TransferProcess) TransferTerminationMessage {
	return TransferTerminationMessage{
		Context:     []string{ContextURL},
		Type:        TransferTerminationMessageType,
		ProviderPID: t.ProviderPID,
		ConsumerPID: t.ConsumerPID,
	}
}
