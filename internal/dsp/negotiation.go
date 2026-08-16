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

// Every top-level message type below, inbound and outbound, declares
// `@context` as `[]string` and `@type` as an unprefixed term. Neither is a
// style choice:
// the TCK rejects a bare-string `@context` and silently skips validation on a
// prefixed `@type`, then fails expansion. The failure modes are spelled out on
// TransferProcessDoc in transfer.go, with the evidence pointer; they apply
// identically here.

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

// OfferRef is the nested offer object inside an *inbound* RequestMessage.
// Its own @type is deliberately not read: the TCK's own source marks that
// field "@DspTestingWorkaround(Remove @type)", so parsing must not depend on
// it. This type is for decoding only — an outbound offer must carry every
// field the TCK's schema requires, which is what NegotiationOffer is for.
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

// carriesConstraint reports whether any rule in permission carries a
// constraint. It does not look at what the constraint says, because v1
// evaluates none of them (DECISIONS.md §14): presence alone is the whole
// question.
func carriesConstraint(permission []Permission) bool {
	for _, p := range permission {
		if len(p.Constraint) > 0 {
			return true
		}
	}
	return false
}

// decideOfferReaction returns the on_offer action to take for a received
// offer. It is the identity function except in one case: an offer that
// carries a constraint can never be accepted, because this connector
// enforces no constraint at all, and `CLAUDE.md` states the rule without
// exception — "never accept a constraint that is not enforced". Such an
// offer takes the same path as a configured on_offer: reject, which is
// DECISIONS.md §14's "parses successfully but causes the negotiation to be
// rejected" exactly.
//
// The other three actions need no adjustment. counter declines the offer and
// re-proposes this connector's own ask, passive holds OFFERED without
// agreeing to anything, and reject already terminates — none of them adopt
// the counterparty's terms, which is the thing the rule forbids.
func decideOfferReaction(onOffer string, constrained bool) string {
	if constrained && onOffer == "accept" {
		return "reject"
	}
	return onOffer
}

// decideAgreementReaction is decideOfferReaction's counterpart for a
// received agreement, and matters more: an agreement is the binding
// artifact, and verifying one is this connector's strongest possible
// statement that it accepts those terms. It also closes the direct-agreement
// path (`CN_C:01-04`), where a provider sends an agreement with no offer
// ever pushed and decideOfferReaction is therefore never consulted.
func decideAgreementReaction(onAgreement string, constrained bool) string {
	if constrained && onAgreement == "verify" {
		return "reject"
	}
	return onAgreement
}

// NegotiationOffer is the ODRL offer object carried in negotiation protocol
// messages, in either direction. Unlike catalog.go's Offer (which never
// carries a target — the schema forbids it there), a negotiation offer always
// names its target dataset explicitly.
//
// Every field here is load-bearing against the TCK's own
// negotiation/contract-schema.json, where an offer is a MessageOffer:
// allOf[ PolicyClass (@id required), { @type const "Offer", target },
// anyOf[ permission | prohibition ] ] with @type required. permission and
// prohibition are each an array with minItems 1, so a *single* non-empty
// permission satisfies the anyOf and there is deliberately no prohibition
// field: emitting "prohibition": [] would turn a valid message invalid.
type NegotiationOffer struct {
	ID         string       `json:"@id"`
	Type       string       `json:"@type"`
	Target     string       `json:"target"`
	Permission []Permission `json:"permission"`
}

// newNegotiationOffer builds the offer node this connector sends for
// offerID/datasetID. Every builder that emits one goes through it, in either
// role, so there is exactly one place where the shape NegotiationOffer's doc
// comment describes is actually produced.
func newNegotiationOffer(offerID, datasetID string) NegotiationOffer {
	return NegotiationOffer{
		ID:         offerID,
		Type:       OfferType,
		Target:     datasetID,
		Permission: []Permission{{Action: useAction}},
	}
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
		Offer:       newNegotiationOffer(n.DatasetID+offerIDSuffix, n.DatasetID),
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

// ConsumerRequestMessage is the initial ContractRequestMessage this
// connector sends as consumer — POST {provider}/negotiations/request.
//
// It is a separate type from RequestMessage, which decodes the same DSP
// message inbound, because the two directions have opposite obligations.
// Inbound, DECISIONS.md section 22.5 declares only the fields this connector
// reads, and OfferRef's doc comment forbids depending on the offer's @type
// at all. Outbound, the TCK validates what this connector emits against its
// own schema, so the offer must be a complete NegotiationOffer. Sharing one
// struct would force one of those two rules to bend; CounterRequestMessage
// is already separate for the same kind of reason.
type ConsumerRequestMessage struct {
	Context         []string         `json:"@context"`
	Type            string           `json:"@type"`
	ConsumerPID     string           `json:"consumerPid"`
	Offer           NegotiationOffer `json:"offer"`
	CallbackAddress string           `json:"callbackAddress"`
}

// CounterRequestMessage is the body of the consumer role's counter-request
// — POST {provider}/negotiations/{providerPid}/request — sent when this
// connector's on_offer:counter policy decides to repeat its original ask
// rather than accept a provider's counter-offer. Unlike
// ConsumerRequestMessage (the very first request, which has no providerPid
// yet), this carries the providerPid the synchronous response to that first
// request returned: without it, the TCK's own reference provider treats the
// message as a duplicate initial request rather than a counter, and the test
// that expects it hangs. See the design spec's "The 01-02 counter-request
// shape".
type CounterRequestMessage struct {
	Context     []string         `json:"@context"`
	Type        string           `json:"@type"`
	ProviderPID string           `json:"providerPid"`
	ConsumerPID string           `json:"consumerPid"`
	Offer       NegotiationOffer `json:"offer"`
}

// VerificationMessage is the ContractAgreementVerificationMessage this
// connector sends once its on_agreement:verify policy decides to verify a
// received agreement.
type VerificationMessage struct {
	Context     []string `json:"@context"`
	ID          string   `json:"@id"`
	Type        string   `json:"@type"`
	ProviderPID string   `json:"providerPid"`
	ConsumerPID string   `json:"consumerPid"`
}

// buildConsumerRequestMessage is the initial ContractRequestMessage this
// connector sends as consumer. datasetID and offerID are echoed verbatim
// from what POST /negotiations/initiate received — never regenerated. The
// TCK's own mock provider recovers datasetID from offerID via its own
// "offer"+datasetID convention, a different shape from this connector's
// own provider-role offerIDSuffix convention; conflating the two would
// break the request the TCK's mock provider needs to parse.
func buildConsumerRequestMessage(consumerPID, datasetID, offerID, callbackAddress string) ConsumerRequestMessage {
	return ConsumerRequestMessage{
		Context:         []string{ContextURL},
		Type:            ContractRequestMessageType,
		ConsumerPID:     consumerPID,
		Offer:           newNegotiationOffer(offerID, datasetID),
		CallbackAddress: callbackAddress,
	}
}

func buildCounterRequestMessage(n store.ConsumerNegotiation) CounterRequestMessage {
	return CounterRequestMessage{
		Context:     []string{ContextURL},
		Type:        ContractRequestMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		Offer:       newNegotiationOffer(n.OfferID, n.DatasetID),
	}
}

func buildAcceptedEventMessage(n store.ConsumerNegotiation) EventMessage {
	return EventMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationEventMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		EventType:   eventTypeAccepted,
	}
}

func buildVerificationMessage(n store.ConsumerNegotiation) VerificationMessage {
	return VerificationMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractAgreementVerificationMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
	}
}

func buildConsumerTerminationMessage(n store.ConsumerNegotiation) TerminationMessage {
	return TerminationMessage{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationTerminationMessageType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		Code:        terminationCode,
	}
}

func buildConsumerNegotiationStateDocument(n store.ConsumerNegotiation) NegotiationStateDocument {
	return NegotiationStateDocument{
		Context:     []string{ContextURL},
		ID:          newMessageID(),
		Type:        ContractNegotiationType,
		ProviderPID: n.ProviderPID,
		ConsumerPID: n.ConsumerPID,
		State:       n.State,
	}
}

// resolvePolicy returns the consumer policy for datasetID, with every field
// defaulted to this connector's sane, real-world behavior: accept an offer,
// verify an agreement, wait if nothing arrives. See the design spec's "Why
// a policy configuration, not a content rule".
func resolvePolicy(cfg config.Config, datasetID string) config.ConsumerPolicy {
	for _, p := range cfg.ConsumerPolicies {
		if p.DatasetID == datasetID {
			return normalizedPolicy(p)
		}
	}
	return normalizedPolicy(config.ConsumerPolicy{DatasetID: datasetID})
}

func normalizedPolicy(p config.ConsumerPolicy) config.ConsumerPolicy {
	if p.OnOffer == "" {
		p.OnOffer = "accept"
	}
	if p.OnAgreement == "" {
		p.OnAgreement = "verify"
	}
	if p.OnIdle == "" {
		p.OnIdle = "wait"
	}
	return p
}

// offerLegalFrom reports whether an incoming ContractOfferMessage is a
// legal transition from state — the consumer-role mirror of the provider's
// own CN:03 structural checks. Only REQUESTED accepts an offer; CN_C:03-05
// confirms a second offer is illegal once ACCEPTED, and there is no test
// that ever sends a second offer while already OFFERED either.
func offerLegalFrom(state string) bool {
	return state == StateRequested
}

// agreementLegalFrom reports whether an incoming ContractAgreementMessage
// is a legal transition from state. Legal from REQUESTED (the
// direct-agreement path with no offer ever pushed, CN_C:01-04) or ACCEPTED
// (the normal path after this connector accepted an offer); illegal from
// OFFERED (CN_C:03-02).
func agreementLegalFrom(state string) bool {
	return state == StateRequested || state == StateAccepted
}

// finalizedEventLegalFrom reports whether an incoming FINALIZED event is a
// legal transition from state. Legal only from VERIFIED — CN_C:03-01,
// 03-03, 03-04, and 03-06 each require rejection from a different other
// state.
func finalizedEventLegalFrom(state string) bool {
	return state == StateVerified
}
