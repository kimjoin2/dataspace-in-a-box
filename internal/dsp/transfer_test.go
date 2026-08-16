package dsp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

func TestTransferTransitionLegality(t *testing.T) {
	states := []string{TransferRequested, TransferStarted, TransferSuspended, TransferCompleted, TransferTerminated}
	cases := []struct {
		name string
		fn   func(string) bool
		want map[string]bool
	}{
		{"start", startLegalFrom, map[string]bool{
			TransferRequested: true, TransferStarted: false, TransferSuspended: true,
			TransferCompleted: false, TransferTerminated: false,
		}},
		{"completion", completionLegalFrom, map[string]bool{
			TransferRequested: false, TransferStarted: true, TransferSuspended: false,
			TransferCompleted: false, TransferTerminated: false,
		}},
		{"suspension", suspensionLegalFrom, map[string]bool{
			TransferRequested: false, TransferStarted: true, TransferSuspended: false,
			TransferCompleted: false, TransferTerminated: false,
		}},
		{"termination", terminationLegalFrom, map[string]bool{
			TransferRequested: true, TransferStarted: true, TransferSuspended: true,
			TransferCompleted: false, TransferTerminated: false,
		}},
	}
	for _, c := range cases {
		for _, s := range states {
			if got := c.fn(s); got != c.want[s] {
				t.Errorf("%sLegalFrom(%s) = %v, want %v", c.name, s, got, c.want[s])
			}
		}
	}
}

func TestBuildTransferProcessDocCarriesTheRequiredFields(t *testing.T) {
	tp := store.TransferProcess{
		ProviderPID: "urn:uuid:p-1", ConsumerPID: "urn:uuid:c-1", State: TransferStarted,
	}
	raw, err := json.Marshal(buildTransferProcessDoc(tp))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// transfer-process-schema.json requires all five.
	for _, k := range []string{"@context", "@type", "providerPid", "consumerPid", "state"} {
		if _, ok := got[k]; !ok {
			t.Errorf("TransferProcess is missing required property %q", k)
		}
	}
	if got["@type"] != "TransferProcess" {
		t.Errorf("@type = %v, want TransferProcess", got["@type"])
	}
	if got["state"] != TransferStarted {
		t.Errorf("state = %v, want %s", got["state"], TransferStarted)
	}
}

func TestBuildTransferStartMessageCarriesTheRequiredFields(t *testing.T) {
	tp := store.TransferProcess{
		ProviderPID: "urn:uuid:p-1", ConsumerPID: "urn:uuid:c-1",
		State: TransferRequested, CreatedAt: time.Now().UTC(),
	}
	raw, err := json.Marshal(buildTransferStartMessage(tp))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// transfer-start-message-schema.json requires these four. dataAddress is
	// optional and Phase A sends none.
	for _, k := range []string{"@context", "@type", "providerPid", "consumerPid"} {
		if _, ok := got[k]; !ok {
			t.Errorf("TransferStartMessage is missing required property %q", k)
		}
	}
	if got["@type"] != "TransferStartMessage" {
		t.Errorf("@type = %v, want TransferStartMessage", got["@type"])
	}
	if _, present := got["dataAddress"]; present {
		t.Error("Phase A must not emit a dataAddress: nothing serves it yet, and announcing an endpoint that serves nothing is a claim this connector cannot keep")
	}
}
