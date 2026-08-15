package dsp

import (
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

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
	msg := buildOfferMessage(n)
	if msg.Type != ContractOfferMessageType {
		t.Errorf("Type = %q, want %q", msg.Type, ContractOfferMessageType)
	}
	if msg.ProviderPID != n.ProviderPID || msg.ConsumerPID != n.ConsumerPID {
		t.Errorf("msg = %+v, want it to carry n's identifiers", msg)
	}
	wantOfferID := n.DatasetID + offerIDSuffix
	if msg.Offer.ID != wantOfferID {
		t.Errorf("Offer.ID = %q, want %q (the connector's canonical offer, not the requested one)", msg.Offer.ID, wantOfferID)
	}
	if msg.Offer.Target != n.DatasetID {
		t.Errorf("Offer.Target = %q, want %q", msg.Offer.Target, n.DatasetID)
	}
	if len(msg.Offer.Permission) != 1 || msg.Offer.Permission[0].Action != useAction {
		t.Errorf("Offer.Permission = %v, want one permission with action %q", msg.Offer.Permission, useAction)
	}
}

func TestBuildAgreementMessage(t *testing.T) {
	n := testStoredNegotiation()
	msg := buildAgreementMessage(n, "https://provider.example.org", "urn:participant:provider")
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
	if msg.Offer.ID != "urn:dataset:a#offer" || msg.Offer.Target != "urn:dataset:a" {
		t.Errorf("Offer = %+v, want the exact ids passed in, not regenerated", msg.Offer)
	}
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
	if msg.Offer.ID != n.OfferID || msg.Offer.Target != n.DatasetID {
		t.Errorf("Offer = %+v, want the negotiation's original ask repeated", msg.Offer)
	}
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
