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
