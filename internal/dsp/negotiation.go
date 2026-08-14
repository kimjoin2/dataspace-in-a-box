// This file holds the contract negotiation state machine's decision logic
// and the shapes of the messages it exchanges — no HTTP.
// negotiation_handler.go wires HTTP onto this. The package doc comment is in
// version.go.
package dsp

import (
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// DSP contract negotiation states, exactly as named in the spec. They are
// also what this project stores and what GET /negotiations/{id} reports:
// there is no separate internal representation.
const (
	StateRequested  = "REQUESTED"
	StateOffered    = "OFFERED"
	StateAccepted   = "ACCEPTED"
	StateAgreed     = "AGREED"
	StateVerified   = "VERIFIED"
	StateFinalized  = "FINALIZED"
	StateTerminated = "TERMINATED"
)

// DSP negotiation message @type names.
const (
	ContractRequestMessageType                = "ContractRequestMessage"
	ContractOfferMessageType                  = "ContractOfferMessage"
	ContractNegotiationEventMessageType       = "ContractNegotiationEventMessage"
	ContractAgreementMessageType              = "ContractAgreementMessage"
	ContractAgreementVerificationMessageType  = "ContractAgreementVerificationMessage"
	ContractNegotiationTerminationMessageType = "ContractNegotiationTerminationMessage"
	ContractNegotiationType                   = "ContractNegotiation"
	ContractNegotiationErrorType              = "ContractNegotiationError"

	// AgreementType is the ODRL @type of the agreement node nested inside a
	// ContractAgreementMessage.
	AgreementType = "Agreement"

	eventTypeAccepted  = "ACCEPTED"
	eventTypeFinalized = "FINALIZED"

	// terminationCode is the value this connector sends as a termination
	// message's "code" field. DSP leaves the value's vocabulary
	// implementation-defined; the TCK's own test code uses the literal "1"
	// for the same field when it plays the consumer role, so this matches
	// that precedent rather than inventing a new one.
	terminationCode = "1"
)

// RequestMessage is the body of POST /negotiations/request and
// POST /negotiations/{id}/request — a ContractRequestMessage, whether it is
// the initial request or a counter-offer/resend. Only the fields this
// connector inspects are declared, matching the direct-field-check approach
// DECISIONS.md section 22.5 established for the catalog protocol.
type RequestMessage struct {
	Context         []string `json:"@context"`
	Type            string   `json:"@type"`
	ConsumerPID     string   `json:"consumerPid"`
	Offer           OfferRef `json:"offer"`
	CallbackAddress string   `json:"callbackAddress"`
}

// OfferRef is the nested offer object inside a RequestMessage. Its own
// @type is deliberately not read: the TCK's own source marks that field
// "@DspTestingWorkaround(Remove @type)", so parsing must not depend on it.
type OfferRef struct {
	ID     string `json:"@id"`
	Target string `json:"target"`
}

// negotiationOutcome is what the provider decides to do in response to a
// contract request or an accept event: what to push to the consumer's
// callback address. pushOffer and pushTermination can both be set — an
// expired, mismatched dataset gets an informational counter-offer followed
// immediately by an unprompted termination, since there is nothing left to
// agree to. The state each push moves the negotiation into is dispatch's,
// written next to the message it pairs with rather than carried here.
type negotiationOutcome struct {
	pushOffer       bool
	pushAgreement   bool
	pushTermination bool
}

var (
	// outcomeNone: the dataset is not advertised at all. The provider has
	// nothing coherent to say about it, so the negotiation stays REQUESTED
	// with no autonomous action.
	outcomeNone = negotiationOutcome{}
	// outcomeOffer: the requested offer does not match what this connector
	// advertises, but the dataset's policy is currently valid.
	outcomeOffer = negotiationOutcome{pushOffer: true}
	// outcomeAgree: the requested offer matches and is currently valid.
	outcomeAgree = negotiationOutcome{pushAgreement: true}
	// outcomeTerminate: either the offer matches but has expired, or an
	// ACCEPTED event arrived for a dataset that is no longer valid or no
	// longer advertised.
	outcomeTerminate = negotiationOutcome{pushTermination: true}
	// outcomeOfferThenTerminate: the offer does not match AND the dataset has
	// expired. The true terms are still worth telling the consumer, so the
	// offer is pushed; then, since there is nothing left to agree to, an
	// unprompted termination follows.
	outcomeOfferThenTerminate = negotiationOutcome{pushOffer: true, pushTermination: true}
)

// findConfiguredDataset returns the advertised dataset configuration with
// the given identifier. Unlike findDataset in catalog.go, this returns the
// raw config.Dataset (for its ValidityUntil), not the built catalog document.
func findConfiguredDataset(cfg config.Config, id string) (config.Dataset, bool) {
	for _, d := range cfg.Datasets {
		if d.ID == id {
			return d, true
		}
	}
	return config.Dataset{}, false
}

// isValid reports whether d's offer is currently valid: it has no
// validity_until, or now is before it.
func isValid(d config.Dataset, now time.Time) bool {
	return d.ValidityUntil == nil || now.Before(*d.ValidityUntil)
}

// decideInitialRequest implements the offer/agreement divergence rule from
// the design spec's "offer/agreement divergence" section: a plain comparison
// against what this connector advertises for datasetID, plus a validity
// check.
func decideInitialRequest(cfg config.Config, datasetID, offerID string, now time.Time) negotiationOutcome {
	ds, ok := findConfiguredDataset(cfg, datasetID)
	if !ok {
		return outcomeNone
	}
	matches := offerID == ds.ID+offerIDSuffix
	valid := isValid(ds, now)
	switch {
	case matches && valid:
		return outcomeAgree
	case matches && !valid:
		return outcomeTerminate
	case !matches && valid:
		return outcomeOffer
	default:
		return outcomeOfferThenTerminate
	}
}

// decideAccept implements the ACCEPTED -> AGREED re-check: an accept only
// advances the negotiation if the dataset is still advertised and still
// valid at the moment of acceptance.
func decideAccept(cfg config.Config, datasetID string, now time.Time) negotiationOutcome {
	ds, ok := findConfiguredDataset(cfg, datasetID)
	if !ok || !isValid(ds, now) {
		return outcomeTerminate
	}
	return outcomeAgree
}

// decideReRequestMatches reports whether a re-request repeats the offer
// already on the table. The design spec's original guess — that a match
// means synchronous rejection — was backwards: the real TCK (CN:03-04) sends
// two re-requests carrying the *identical* offer and expects the first to
// succeed (200, negotiation unchanged, stays OFFERED) and only the second to
// be rejected. handleReRequest enforces that second part with
// store.Negotiation.Rerequested, a flag this function's result has no part
// in; what this result decides is the *other* case (CN:01-02): a mismatched
// re-request is accepted synchronously too, but is a decision to walk away,
// so the provider terminates asynchronously since it has nothing on offer
// that could satisfy it.
func decideReRequestMatches(currentOfferID, requestedOfferID string) bool {
	return requestedOfferID == currentOfferID
}

// NegotiationOffer is the ODRL offer object carried in negotiation protocol
// messages. Unlike catalog.go's Offer (which never carries a target — the
// schema forbids it there), a negotiation offer always names its target
// dataset explicitly.
type NegotiationOffer struct {
	ID         string       `json:"@id"`
	Type       string       `json:"@type"`
	Target     string       `json:"target"`
	Permission []Permission `json:"permission"`
}

// OfferMessage is the ContractOfferMessage pushed to a consumer's callback
// address when the requested offer does not match what this connector
// advertises.
type OfferMessage struct {
	Context     []string         `json:"@context"`
	ID          string           `json:"@id"`
	Type        string           `json:"@type"`
	ProviderPID string           `json:"providerPid"`
	ConsumerPID string           `json:"consumerPid"`
	Offer       NegotiationOffer `json:"offer"`
}

// Agreement is the ODRL agreement node nested inside an AgreementMessage.
// Unlike a catalog Offer, an Agreement is bilateral — the TCK's schema marks
// both assigner and assignee required (confirmed the hard way: the real TCK
// rejected an Agreement missing them with "required property 'assignee' not
// found, required property 'assigner' not found"). assigner is this
// connector's own config.Config.ParticipantID — the party granting the
// rights. assignee is the counterparty being granted them, but v1's
// negotiation messages carry no participant identifier for the consumer
// (ContractRequestMessage has only consumerPid, offer, callbackAddress —
// checked against the TCK's own contract-request-message-schema.json), and
// negotiation is unauthenticated in v1 same as the catalog protocol, so
// there is no participant identity to put here even from a trust boundary.
// n.ConsumerPID is the best available per-negotiation identifier for "this
// specific consumer" and is used as an honest placeholder.
type Agreement struct {
	ID         string       `json:"@id"`
	Type       string       `json:"@type"`
	Target     string       `json:"target"`
	Permission []Permission `json:"permission"`
	Assigner   string       `json:"assigner"`
	Assignee   string       `json:"assignee"`
	Timestamp  string       `json:"timestamp"`
}

// AgreementMessage is the ContractAgreementMessage pushed to a consumer when
// the requested offer matches and is currently valid.
type AgreementMessage struct {
	Context         []string  `json:"@context"`
	ID              string    `json:"@id"`
	Type            string    `json:"@type"`
	ProviderPID     string    `json:"providerPid"`
	ConsumerPID     string    `json:"consumerPid"`
	Agreement       Agreement `json:"agreement"`
	CallbackAddress string    `json:"callbackAddress"`
}

// EventMessage is the ContractNegotiationEventMessage this connector pushes
// for the FINALIZED transition. The ACCEPTED direction is sent by the
// consumer and parsed, not built, by this connector.
type EventMessage struct {
	Context     []string `json:"@context"`
	ID          string   `json:"@id"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
	EventType   string   `json:"eventType"`
}

// TerminationReason is one entry in a TerminationMessage's reason array —
// a different shape from CatalogError.reason, which is an array of plain
// strings. The two are different DSP fields.
type TerminationReason struct {
	Message string `json:"message"`
}

// TerminationMessage is the ContractNegotiationTerminationMessage, pushed by
// the provider or received from the consumer.
type TerminationMessage struct {
	Context     []string            `json:"@context"`
	ID          string              `json:"@id"`
	Type        string              `json:"@type"`
	ProviderPID string              `json:"providerPid"`
	ConsumerPID string              `json:"consumerPid"`
	Code        string              `json:"code"`
	Reason      []TerminationReason `json:"reason,omitempty"`
}

// NegotiationStateDocument is the ContractNegotiation state document served
// by GET /negotiations/{id} and returned synchronously from
// POST /negotiations/request.
type NegotiationStateDocument struct {
	Context     []string `json:"@context"`
	ID          string   `json:"@id"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
	State       string   `json:"state"`
}

// newMessageID generates this message's own @id. A generation failure here
// means the OS's CSPRNG failed, which this project's own principles say not
// to build a fallback path for — the zero value degrades to an empty string
// rather than crashing the handler.
func newMessageID() string {
	id, _ := store.NewUUID()
	return id
}

func buildNegotiationStateDocument(n store.Negotiation) NegotiationStateDocument {
	return NegotiationStateDocument{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		State:       n.State,
	}
}

func buildOfferMessage(n store.Negotiation) OfferMessage {
	return OfferMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractOfferMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		Offer: NegotiationOffer{
			ID:         n.DatasetID + offerIDSuffix,
			Type:       OfferType,
			Target:     n.DatasetID,
			Permission: []Permission{{Action: useAction}},
		},
	}
}

// buildAgreementMessage builds the agreement pushed on the AGREED
// transition. publicURL is this connector's own address (config.Config's
// PublicURL) — the design spec's Risks section notes that whether the wire
// actually requires this field is unconfirmed; it is included on the
// evidence available and the first real TCK run will say if it was
// unnecessary. participantID is config.Config's ParticipantID — see
// Agreement's doc comment for why it becomes the nested agreement's
// assigner.
func buildAgreementMessage(n store.Negotiation, publicURL, participantID string) AgreementMessage {
	return AgreementMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractAgreementMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		Agreement: Agreement{
			ID:         n.ProviderPID,
			Type:       AgreementType,
			Target:     n.DatasetID,
			Permission: []Permission{{Action: useAction}},
			Assigner:   participantID,
			Assignee:   n.ConsumerPID,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
		},
		CallbackAddress: publicURL + VersionPath,
	}
}

func buildFinalizedEventMessage(n store.Negotiation) EventMessage {
	return EventMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationEventMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		EventType:   eventTypeFinalized,
	}
}

func buildTerminationMessage(n store.Negotiation) TerminationMessage {
	return TerminationMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationTerminationMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		Code:        terminationCode,
	}
}
