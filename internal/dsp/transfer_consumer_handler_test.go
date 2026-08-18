package dsp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// useRealCallbackGuard undoes newTestTransferHandler's stub. Every other
// test in this file wants the stub, because the guard is not their subject;
// this one is about the guard, so it needs the real thing.
func useRealCallbackGuard(t *testing.T) {
	t.Helper()
	stubbed := validateOutgoingCallback
	validateOutgoingCallback = validateCallbackURL
	t.Cleanup(func() { validateOutgoingCallback = stubbed })
}

func initiateBody(fields map[string]string) *bytes.Reader {
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return bytes.NewReader(raw)
}

func fullInitiateFields(providerURL string) map[string]string {
	return map[string]string{
		"providerId":       "urn:connector:tck",
		"agreementId":      "urn:uuid:a-1",
		"format":           "HTTP-PULL",
		"connectorAddress": providerURL,
	}
}

func TestTransferInitiateStartsAConsumerTransfer(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"@context":["` + ContextURL + `"],"@type":"TransferProcess",` +
			`"providerPid":"urn:uuid:p-9","consumerPid":"x","state":"REQUESTED"}`))
	}))
	defer provider.Close()

	h, st := newTestTransferHandler(t, config.Config{})
	seedAgreement(t, st, "urn:uuid:a-1")

	rec := httptest.NewRecorder()
	h.handleTransferInitiate(rec, httptest.NewRequest(http.MethodPost,
		VersionPath+"/transfers/initiate", initiateBody(fullInitiateFields(provider.URL))))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestTransferInitiateRejectsMissingFields(t *testing.T) {
	full := fullInitiateFields("http://provider.example/2025-1")
	for missing := range full {
		h, st := newTestTransferHandler(t, config.Config{})
		seedAgreement(t, st, "urn:uuid:a-1")
		partial := map[string]string{}
		for k, v := range full {
			if k != missing {
				partial[k] = v
			}
		}
		rec := httptest.NewRecorder()
		h.handleTransferInitiate(rec, httptest.NewRequest(http.MethodPost,
			VersionPath+"/transfers/initiate", initiateBody(partial)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("without %s: got %d, want 400", missing, rec.Code)
		}
	}
}

// The decision this milestone takes deliberately: one rule for both roles.
// The provider role already refuses a transfer citing an agreement it has no
// record of; starting one as consumer under a contract this connector never
// held would be the same defect from the other side.
func TestTransferInitiateRejectsAnUnknownAgreement(t *testing.T) {
	h, _ := newTestTransferHandler(t, config.Config{})
	fields := fullInitiateFields("http://provider.example/2025-1")
	fields["agreementId"] = "urn:uuid:never-negotiated"

	rec := httptest.NewRecorder()
	h.handleTransferInitiate(rec, httptest.NewRequest(http.MethodPost,
		VersionPath+"/transfers/initiate", initiateBody(fields)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

// The address is where this connector will send, so it goes through the same
// guard both existing roles use. The reason is logged rather than echoed, so
// the endpoint cannot be used as a name-resolution oracle.
func TestTransferInitiateRejectsAnUnsendableAddress(t *testing.T) {
	h, st := newTestTransferHandler(t, config.Config{})
	useRealCallbackGuard(t)
	seedAgreement(t, st, "urn:uuid:a-1")

	rec := httptest.NewRecorder()
	h.handleTransferInitiate(rec, httptest.NewRequest(http.MethodPost,
		VersionPath+"/transfers/initiate",
		initiateBody(fullInitiateFields("http://127.0.0.1:9999/2025-1"))))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "127.0.0.1") {
		t.Error("the rejection echoed the address back")
	}
}

func TestTransferRequestMessageShape(t *testing.T) {
	msg := buildTransferRequestMessage(store.ConsumerTransfer{
		ConsumerPID: "urn:uuid:c-1",
		AgreementID: "urn:uuid:a-1",
		Format:      "HTTP-PULL",
	}, "http://consumer.example/2025-1")
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Every field transfer-request-message-schema.json requires.
	for _, k := range []string{"@context", "@type", "agreementId", "format", "callbackAddress", "consumerPid"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing required field %q", k)
		}
	}
	if got["@type"] != TransferRequestMessageType {
		t.Errorf("@type = %v", got["@type"])
	}
	// dataAddress is only for push transfers, and this connector pulls.
	if _, ok := got["dataAddress"]; ok {
		t.Error("dataAddress must be absent for a pull transfer")
	}
}
