package dsp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// assertEmittedOffer checks the offer node msg serializes to against the
// MessageOffer definition in the TCK's own negotiation/contract-schema.json,
// which is what validates every message this connector sends:
//
//	allOf[ PolicyClass (@id required),
//	       { @type const "Offer", target },
//	       anyOf[ required permission | required prohibition ] ]
//
// with @type required, permission/prohibition arrays of minItems 1, and Rule
// requiring action.
//
// It deliberately inspects serialized JSON rather than the Go struct. The
// defect this guards against was a builder filling in an offer type that had
// no @type and no permission field at all: every struct-level assertion in
// this file passed, because a field that does not exist cannot be asserted
// wrong, and the first real TCK run rejected all sixteen consumer tests.
//
// It reports through t.Errorf only, never t.Fatalf, because two of its
// callers run it inside an httptest handler — on a goroutine where t.Fatalf
// would call runtime.Goexit on the wrong stack instead of failing the test.
func assertEmittedOffer(t *testing.T, msg any, wantOfferID, wantTarget string) {
	t.Helper()
	b, err := json.Marshal(msg)
	if err != nil {
		t.Errorf("marshal message: %v", err)
		return
	}
	var envelope struct {
		Offer map[string]any `json:"offer"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Errorf("unmarshal message: %v", err)
		return
	}
	offer := envelope.Offer
	if offer == nil {
		t.Errorf("the emitted message carries no offer node: %s", b)
		return
	}
	if offer["@type"] != OfferType {
		t.Errorf("offer @type = %v, want %q — MessageOffer requires @type", offer["@type"], OfferType)
	}
	if offer["@id"] != wantOfferID {
		t.Errorf("offer @id = %v, want %q — PolicyClass requires @id", offer["@id"], wantOfferID)
	}
	if offer["target"] != wantTarget {
		t.Errorf("offer target = %v, want %q", offer["target"], wantTarget)
	}
	perms, ok := offer["permission"].([]any)
	switch {
	case !ok || len(perms) == 0:
		t.Errorf("offer permission = %v, want a non-empty array — it is what satisfies MessageOffer's anyOf, and minItems is 1",
			offer["permission"])
	default:
		rule, ok := perms[0].(map[string]any)
		if !ok || rule["action"] != useAction {
			t.Errorf("offer permission[0] = %v, want a rule with action %q — Rule requires action", perms[0], useAction)
		}
	}
	if v, present := offer["prohibition"]; present {
		t.Errorf("offer carries prohibition = %v; the anyOf is already satisfied by permission, and minItems 1 makes an empty prohibition invalid", v)
	}
}

func cfgWithDataset(id string, validityUntil *time.Time) config.Config {
	return config.Config{Datasets: []config.Dataset{{ID: id, ValidityUntil: validityUntil}}}
}

func TestDecideInitialRequest_UnknownDataset_TakesNoAction(t *testing.T) {
	cfg := cfgWithDataset("urn:dataset:known", nil)
	got := decideInitialRequest(cfg, "urn:dataset:unknown", "urn:dataset:unknown#offer", time.Now())
	if got != outcomeNone {
		t.Errorf("decideInitialRequest = %+v, want outcomeNone", got)
	}
}

func TestDecideInitialRequest_MatchedValid_Agrees(t *testing.T) {
	cfg := cfgWithDataset("urn:dataset:a", nil)
	got := decideInitialRequest(cfg, "urn:dataset:a", "urn:dataset:a"+offerIDSuffix, time.Now())
	if got != outcomeAgree {
		t.Errorf("decideInitialRequest = %+v, want outcomeAgree", got)
	}
}

func TestDecideInitialRequest_MatchedExpired_Terminates(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cfg := cfgWithDataset("urn:dataset:a", &past)
	got := decideInitialRequest(cfg, "urn:dataset:a", "urn:dataset:a"+offerIDSuffix, time.Now())
	if got != outcomeTerminate {
		t.Errorf("decideInitialRequest = %+v, want outcomeTerminate", got)
	}
}

func TestDecideInitialRequest_MismatchedValid_Offers(t *testing.T) {
	cfg := cfgWithDataset("urn:dataset:a", nil)
	got := decideInitialRequest(cfg, "urn:dataset:a", "urn:dataset:a#some-other-offer", time.Now())
	if got != outcomeOffer {
		t.Errorf("decideInitialRequest = %+v, want outcomeOffer", got)
	}
}

func TestDecideInitialRequest_MismatchedExpired_OffersThenTerminates(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cfg := cfgWithDataset("urn:dataset:a", &past)
	got := decideInitialRequest(cfg, "urn:dataset:a", "urn:dataset:a#some-other-offer", time.Now())
	if got != outcomeOfferThenTerminate {
		t.Errorf("decideInitialRequest = %+v, want outcomeOfferThenTerminate", got)
	}
}

func TestDecideAccept_Valid_Agrees(t *testing.T) {
	cfg := cfgWithDataset("urn:dataset:a", nil)
	got := decideAccept(cfg, "urn:dataset:a", time.Now())
	if got != outcomeAgree {
		t.Errorf("decideAccept = %+v, want outcomeAgree", got)
	}
}

func TestDecideAccept_Expired_Terminates(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cfg := cfgWithDataset("urn:dataset:a", &past)
	got := decideAccept(cfg, "urn:dataset:a", time.Now())
	if got != outcomeTerminate {
		t.Errorf("decideAccept = %+v, want outcomeTerminate", got)
	}
}

func TestDecideAccept_NoLongerAdvertised_Terminates(t *testing.T) {
	cfg := cfgWithDataset("urn:dataset:other", nil)
	got := decideAccept(cfg, "urn:dataset:a", time.Now())
	if got != outcomeTerminate {
		t.Errorf("decideAccept = %+v, want outcomeTerminate", got)
	}
}

func TestDecideReRequestMatches_SameOffer(t *testing.T) {
	if !decideReRequestMatches("urn:dataset:a#offer", "urn:dataset:a#offer") {
		t.Error("decideReRequestMatches = false, want true for an identical offer")
	}
}

func TestDecideReRequestMatches_DifferentOffer(t *testing.T) {
	if decideReRequestMatches("urn:dataset:a#offer", "urn:dataset:a#different-offer") {
		t.Error("decideReRequestMatches = true, want false for a different offer")
	}
}

func testStoredNegotiation() store.Negotiation {
	return store.Negotiation{
		ProviderPID:     "urn:uuid:provider-1",
		ConsumerPID:     "urn:uuid:consumer-1",
		State:           StateOffered,
		DatasetID:       "urn:dataset:a",
		OfferID:         "urn:dataset:a#offer",
		CallbackAddress: "https://consumer.example.org/callback",
	}
}

func TestBuildNegotiationStateDocument(t *testing.T) {
	n := testStoredNegotiation()
	doc := buildNegotiationStateDocument(n)
	if doc.Type != ContractNegotiationType {
		t.Errorf("Type = %q, want %q", doc.Type, ContractNegotiationType)
	}
	if doc.ProviderPID != n.ProviderPID || doc.ConsumerPID != n.ConsumerPID || doc.State != n.State {
		t.Errorf("doc = %+v, want it to carry n's identifiers and state", doc)
	}
	if doc.ID == "" {
		t.Error("ID is empty, want a generated message id")
	}
	if len(doc.Context) == 0 || doc.Context[0] != ContextURL {
		t.Errorf("Context = %v, want it to contain %q", doc.Context, ContextURL)
	}
}

func TestBuildOfferMessage(t *testing.T) {
	n := testStoredNegotiation()
	msg := buildOfferMessage(config.Config{}, n)
	if msg.Type != ContractOfferMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractOfferMessageType)
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
	// The canonical offer this connector advertises, not the requested one.
	assertEmittedOffer(t, msg, n.DatasetID+offerIDSuffix, n.DatasetID)
}

func TestBuildAgreementMessage(t *testing.T) {
	n := testStoredNegotiation()
	cfg := config.Config{PublicURL: "https://provider.example.org", ParticipantID: "urn:participant:provider"}
	msg := buildAgreementMessage(cfg, n)
	if msg.Type != ContractAgreementMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractAgreementMessageType)
	}
	if msg.Agreement.Target != n.DatasetID {
		t.Errorf("Agreement.Target = %q, want %q", msg.Agreement.Target, n.DatasetID)
	}
	if msg.Agreement.Type != AgreementType {
		t.Errorf("Agreement.Type = %q, want %q", msg.Agreement.Type, AgreementType)
	}
	if msg.Agreement.Assigner != "urn:participant:provider" {
		t.Errorf("Agreement.Assigner = %q, want the connector's own participant id", msg.Agreement.Assigner)
	}
	if msg.Agreement.Assignee != n.ConsumerPID {
		t.Errorf("Agreement.Assignee = %q, want %q", msg.Agreement.Assignee, n.ConsumerPID)
	}
	if msg.Agreement.Timestamp == "" {
		t.Error("Agreement.Timestamp is empty")
	}
	if msg.CallbackAddress != "https://provider.example.org"+VersionPath {
		t.Errorf("CallbackAddress = %q, want the provider's own address", msg.CallbackAddress)
	}
}

func TestBuildOfferMessageAttachesTheConfiguredValidityConstraint(t *testing.T) {
	n := testStoredNegotiation()
	until := time.Now().Add(time.Hour)
	cfg := cfgWithDataset(n.DatasetID, &until)
	msg := buildOfferMessage(cfg, n)
	if hasUnenforceableConstraint(msg.Offer.Permission) {
		t.Error("the offer's own recognized constraint reads back as unenforceable")
	}
	if len(msg.Offer.Permission) == 0 || len(msg.Offer.Permission[0].Constraint) == 0 {
		t.Errorf("Offer.Permission = %+v, want the dataset's ValidityUntil attached", msg.Offer.Permission)
	}
}

func TestBuildOfferMessageForAnUnconfiguredDatasetStaysUnrestricted(t *testing.T) {
	// n.DatasetID names no dataset in an empty config — the shape a consumer-
	// role offer echo also produces, and buildOfferMessage must not invent a
	// constraint for a dataset this connector has no configuration for.
	n := testStoredNegotiation()
	msg := buildOfferMessage(config.Config{}, n)
	if len(msg.Offer.Permission) == 0 || len(msg.Offer.Permission[0].Constraint) != 0 {
		t.Errorf("Offer.Permission = %+v, want unrestricted use", msg.Offer.Permission)
	}
}

func TestBuildAgreementMessageAttachesTheConfiguredValidityConstraint(t *testing.T) {
	n := testStoredNegotiation()
	until := time.Now().Add(time.Hour)
	cfg := cfgWithDataset(n.DatasetID, &until)
	cfg.PublicURL, cfg.ParticipantID = "https://provider.example.org", "urn:participant:provider"
	msg := buildAgreementMessage(cfg, n)
	if hasUnenforceableConstraint(msg.Agreement.Permission) {
		t.Error("the agreement's own recognized constraint reads back as unenforceable")
	}
	if len(msg.Agreement.Permission) == 0 || len(msg.Agreement.Permission[0].Constraint) == 0 {
		t.Errorf("Agreement.Permission = %+v, want the dataset's ValidityUntil attached", msg.Agreement.Permission)
	}
}

func TestBuildFinalizedEventMessage(t *testing.T) {
	n := testStoredNegotiation()
	msg := buildFinalizedEventMessage(n)
	if msg.Type != ContractNegotiationEventMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractNegotiationEventMessageType)
	}
	if msg.EventType != eventTypeFinalized {
		t.Errorf("EventType = %q, want %q", msg.EventType, eventTypeFinalized)
	}
}

func TestBuildTerminationMessage(t *testing.T) {
	n := testStoredNegotiation()
	msg := buildTerminationMessage(n)
	if msg.Type != ContractNegotiationTerminationMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractNegotiationTerminationMessageType)
	}
	if msg.Code == "" {
		t.Error("Code is empty")
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
}

func testConsumerNegotiation() store.ConsumerNegotiation {
	return store.ConsumerNegotiation{
		ConsumerPID:     "urn:uuid:consumer-1",
		ProviderPID:     "urn:uuid:provider-1",
		ProviderBaseURL: "https://provider.example.org",
		State:           StateOffered,
		DatasetID:       "urn:dataset:a",
		OfferID:         "urn:dataset:a#offer",
	}
}

func TestBuildConsumerRequestMessage(t *testing.T) {
	msg := buildConsumerRequestMessage("urn:uuid:consumer-1", "urn:dataset:a", "urn:dataset:a#offer", "https://connector.example.org/2025-1")
	if msg.Type != ContractRequestMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractRequestMessageType)
	}
	if msg.ConsumerPID != "urn:uuid:consumer-1" {
		t.Errorf("ConsumerPID = %q, want urn:uuid:consumer-1", msg.ConsumerPID)
	}
	// The ids are echoed verbatim from the initiate call, never regenerated.
	assertEmittedOffer(t, msg, "urn:dataset:a#offer", "urn:dataset:a")
	if msg.CallbackAddress != "https://connector.example.org/2025-1" {
		t.Errorf("CallbackAddress = %q, want the address passed in", msg.CallbackAddress)
	}
}

func TestBuildCounterRequestMessage(t *testing.T) {
	n := testConsumerNegotiation()
	msg := buildCounterRequestMessage(n)
	if msg.Type != ContractRequestMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractRequestMessageType)
	}
	if msg.ProviderPID != n.ProviderPID {
		t.Errorf("ProviderPID = %q, want %q — a counter-request must carry it or the TCK's mock provider treats it as a duplicate initial request", msg.ProviderPID, n.ProviderPID)
	}
	if msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("ConsumerPID = %q, want %q", msg.ConsumerPID, n.ConsumerPID)
	}
	// The negotiation's original ask, repeated.
	assertEmittedOffer(t, msg, n.OfferID, n.DatasetID)
}

func TestBuildAcceptedEventMessage(t *testing.T) {
	n := testConsumerNegotiation()
	msg := buildAcceptedEventMessage(n)
	if msg.Type != ContractNegotiationEventMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractNegotiationEventMessageType)
	}
	if msg.EventType != eventTypeAccepted {
		t.Errorf("EventType = %q, want %q", msg.EventType, eventTypeAccepted)
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
}

func TestBuildVerificationMessage(t *testing.T) {
	n := testConsumerNegotiation()
	msg := buildVerificationMessage(n)
	if msg.Type != ContractAgreementVerificationMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractAgreementVerificationMessageType)
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
	if msg.ID == "" {
		t.Error("ID is empty, want a generated message id")
	}
}

func TestBuildConsumerTerminationMessage(t *testing.T) {
	n := testConsumerNegotiation()
	msg := buildConsumerTerminationMessage(n)
	if msg.Type != ContractNegotiationTerminationMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractNegotiationTerminationMessageType)
	}
	if msg.Code != terminationCode {
		t.Errorf("Code = %q, want %q", msg.Code, terminationCode)
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
}

func TestBuildConsumerNegotiationStateDocument(t *testing.T) {
	n := testConsumerNegotiation()
	doc := buildConsumerNegotiationStateDocument(n)
	if doc.Type != ContractNegotiationType {
		t.Errorf("Type = %q, want %q", doc.Type, ContractNegotiationType)
	}
	if doc.ProviderPID != n.ProviderPID || doc.ConsumerPID != n.ConsumerPID || doc.State != n.State {
		t.Errorf("doc = %+v, want it to carry n's identifiers and state", doc)
	}
}

func TestResolvePolicy_UnmatchedDatasetGetsEveryDefault(t *testing.T) {
	cfg := config.Config{}
	p := resolvePolicy(cfg, "urn:dataset:unmatched")
	if p.OnOffer != "accept" || p.OnAgreement != "verify" || p.OnIdle != "wait" {
		t.Errorf("resolvePolicy = %+v, want accept/verify/wait for an unmatched dataset", p)
	}
}

func TestResolvePolicy_UsesTheMatchingEntry(t *testing.T) {
	cfg := config.Config{ConsumerPolicies: []config.ConsumerPolicy{
		{DatasetID: "urn:dataset:a", OnOffer: "passive"},
		{DatasetID: "urn:dataset:b", OnOffer: "reject"},
	}}
	p := resolvePolicy(cfg, "urn:dataset:b")
	if p.OnOffer != "reject" {
		t.Errorf("resolvePolicy(...,\"urn:dataset:b\") = %+v, want on_offer reject", p)
	}
}

func TestResolvePolicy_UnsetFieldsOnAMatchedEntryStillDefault(t *testing.T) {
	cfg := config.Config{ConsumerPolicies: []config.ConsumerPolicy{
		{DatasetID: "urn:dataset:a", OnOffer: "passive"},
	}}
	p := resolvePolicy(cfg, "urn:dataset:a")
	if p.OnOffer != "passive" {
		t.Errorf("OnOffer = %q, want passive (the configured value)", p.OnOffer)
	}
	if p.OnAgreement != "verify" || p.OnIdle != "wait" {
		t.Errorf("OnAgreement/OnIdle = %q/%q, want the defaults for fields the entry left unset", p.OnAgreement, p.OnIdle)
	}
}

func TestIsValidityPeriodConstraint(t *testing.T) {
	recognized := json.RawMessage(`{"leftOperand":"dateTime","operator":"lteq","rightOperand":"2027-01-01T00:00:00Z"}`)
	cases := []struct {
		name string
		cs   []json.RawMessage
		want bool
	}{
		{"the recognized shape", []json.RawMessage{recognized}, true},
		{"no elements", nil, false},
		{"two elements, each the recognized shape", []json.RawMessage{recognized, recognized}, false},
		{"an unrecognized leftOperand", []json.RawMessage{json.RawMessage(`{"leftOperand":"spatial","operator":"eq","rightOperand":"EU"}`)}, false},
		{"an unrecognized operator", []json.RawMessage{json.RawMessage(`{"leftOperand":"dateTime","operator":"gteq","rightOperand":"2027-01-01T00:00:00Z"}`)}, false},
		{"a rightOperand that does not parse as RFC 3339", []json.RawMessage{json.RawMessage(`{"leftOperand":"dateTime","operator":"lteq","rightOperand":"not-a-time"}`)}, false},
		{"malformed JSON", []json.RawMessage{json.RawMessage(`{`)}, false},
	}
	for _, c := range cases {
		if got := isValidityPeriodConstraint(c.cs); got != c.want {
			t.Errorf("isValidityPeriodConstraint(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHasUnenforceableConstraint(t *testing.T) {
	validity := []json.RawMessage{json.RawMessage(`{"leftOperand":"dateTime","operator":"lteq","rightOperand":"2027-01-01T00:00:00Z"}`)}
	spatial := []json.RawMessage{json.RawMessage(`{"leftOperand":"spatial","operator":"eq","rightOperand":"EU"}`)}
	cases := []struct {
		name       string
		permission []Permission
		want       bool
	}{
		{"no rules", nil, false},
		{"an empty rule list", []Permission{}, false},
		{"a rule with no constraint", []Permission{{Action: useAction}}, false},
		{"a rule with an empty constraint list", []Permission{{Action: useAction, Constraint: []json.RawMessage{}}}, false},
		{"a rule with the recognized validity-period constraint", []Permission{{Action: useAction, Constraint: validity}}, false},
		{"a rule with an unrecognized constraint", []Permission{{Action: useAction, Constraint: spatial}}, true},
		{"an unrecognized constraint on the second rule only", []Permission{{Action: useAction}, {Action: useAction, Constraint: spatial}}, true},
		{"the recognized constraint on one rule, unrecognized on another", []Permission{{Action: useAction, Constraint: validity}, {Action: useAction, Constraint: spatial}}, true},
	}
	for _, c := range cases {
		if got := hasUnenforceableConstraint(c.permission); got != c.want {
			t.Errorf("hasUnenforceableConstraint(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDecideOfferReaction(t *testing.T) {
	cases := []struct {
		onOffer       string
		unenforceable bool
		want          string
	}{
		// An offer with nothing unenforceable is never adjusted, whatever
		// the policy says — this includes an offer whose only constraint is
		// the recognized validity-period shape.
		{"accept", false, "accept"},
		{"reject", false, "reject"},
		{"counter", false, "counter"},
		{"passive", false, "passive"},
		// Only accept is unsafe for an unenforceable offer: it is the one
		// action that adopts the counterparty's terms.
		{"accept", true, "reject"},
		{"reject", true, "reject"},
		{"counter", true, "counter"},
		{"passive", true, "passive"},
	}
	for _, c := range cases {
		if got := decideOfferReaction(c.onOffer, c.unenforceable); got != c.want {
			t.Errorf("decideOfferReaction(%q, %v) = %q, want %q", c.onOffer, c.unenforceable, got, c.want)
		}
	}
}

func TestDecideAgreementReaction(t *testing.T) {
	cases := []struct {
		onAgreement   string
		unenforceable bool
		want          string
	}{
		{"verify", false, "verify"},
		{"reject", false, "reject"},
		{"verify", true, "reject"},
		{"reject", true, "reject"},
	}
	for _, c := range cases {
		if got := decideAgreementReaction(c.onAgreement, c.unenforceable); got != c.want {
			t.Errorf("decideAgreementReaction(%q, %v) = %q, want %q", c.onAgreement, c.unenforceable, got, c.want)
		}
	}
}

func TestOfferLegalFrom(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{StateRequested, true},
		{StateOffered, false},
		{StateAccepted, false},
		{StateAgreed, false},
		{StateVerified, false},
	}
	for _, c := range cases {
		if got := offerLegalFrom(c.state); got != c.want {
			t.Errorf("offerLegalFrom(%q) = %v, want %v", c.state, got, c.want)
		}
	}
}

func TestAgreementLegalFrom(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{StateRequested, true},
		{StateAccepted, true},
		{StateOffered, false},
		{StateAgreed, false},
		{StateVerified, false},
	}
	for _, c := range cases {
		if got := agreementLegalFrom(c.state); got != c.want {
			t.Errorf("agreementLegalFrom(%q) = %v, want %v", c.state, got, c.want)
		}
	}
}

func TestFinalizedEventLegalFrom(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{StateVerified, true},
		{StateRequested, false},
		{StateOffered, false},
		{StateAccepted, false},
		{StateAgreed, false},
	}
	for _, c := range cases {
		if got := finalizedEventLegalFrom(c.state); got != c.want {
			t.Errorf("finalizedEventLegalFrom(%q) = %v, want %v", c.state, got, c.want)
		}
	}
}
