package dsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// seedConsumerTransferFor writes a consumer-role transfer under a chosen
// agreement, pointed at a chosen provider base URL, and returns its consumer
// pid — the id its endpoints are addressed by.
func seedConsumerTransferFor(t *testing.T, st *store.Store, state, agreementID, providerBaseURL string) string {
	t.Helper()
	now := time.Now()
	id := "urn:uuid:c-" + agreementID + "-" + state
	if err := st.CreateConsumerTransfer(store.ConsumerTransfer{
		ConsumerPID:     id,
		ProviderPID:     "urn:uuid:p-1",
		ProviderBaseURL: providerBaseURL,
		AgreementID:     agreementID,
		Format:          "HTTP-PULL",
		State:           state,
		CreatedAt:       now,
		UpdatedAt:       now,
	}); err != nil {
		t.Fatalf("CreateConsumerTransfer: %v", err)
	}
	return id
}

// deliverInboundStart posts a TransferStartMessage to the consumer-role
// transfer with the given id, which is how the provider starts it.
func deliverInboundStart(t *testing.T, h transferHandler, id string) {
	t.Helper()
	body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferStartMessageType + `",` +
		`"providerPid":"urn:uuid:p-1","consumerPid":"` + id + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		VersionPath+"/transfers/"+id+"/start", strings.NewReader(body))
	req.SetPathValue("id", id)
	h.handleTransferStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbound start: got %d, want 200: %s", rec.Code, rec.Body)
	}
}

// TP_C:02-01's shape, and the test that justifies the `after` field. A
// driver that fired as soon as its step became legal would send this
// termination from REQUESTED — termination is legal there — and the
// provider's start would then land on a terminated transfer.
func TestConsumerDriverWaitsForTheTriggerState(t *testing.T) {
	fc := newFakeTransferConsumer(t)
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", After: TransferStarted, Sequence: []string{TransferTerminated}},
	}}
	h, st := newTestTransferHandler(t, cfg)
	id := seedConsumerTransferFor(t, st, TransferRequested, "urn:uuid:a", fc.srv.URL)
	tr, _, err := st.GetConsumerTransfer(id)
	if err != nil {
		t.Fatalf("GetConsumerTransfer: %v", err)
	}

	// Drive the REQUESTED trigger point for real. Reaching it is not enough:
	// this policy waits for STARTED, so the ACK must release nothing. Seeding
	// the row and simply not calling this would prove only that an untaken
	// branch is quiet.
	h.onTransferRequestAcknowledged(tr, "urn:uuid:p-ack")
	fc.receivedNothing(t, 50*time.Millisecond)

	deliverInboundStart(t, h, id)

	got := fc.waitFor(t, 1)
	if len(got) != 1 || got[0].msgType != TransferTerminationMessageType {
		t.Fatalf("after the start, pushed %v, want one termination", got)
	}
}

// TP_C:02-05: the sequence is released by the ACK, not by a provider
// message, because no provider message ever arrives in that test.
func TestConsumerDriverFiresFromRequestedAfterTheAck(t *testing.T) {
	fc := newFakeTransferConsumer(t)
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", After: TransferRequested, Sequence: []string{TransferTerminated}},
	}}
	h, st := newTestTransferHandler(t, cfg)
	id := seedConsumerTransferFor(t, st, TransferRequested, "urn:uuid:a", fc.srv.URL)
	tr, _, err := st.GetConsumerTransfer(id)
	if err != nil {
		t.Fatalf("GetConsumerTransfer: %v", err)
	}

	h.onTransferRequestAcknowledged(tr, "urn:uuid:p-ack")

	got := fc.waitFor(t, 1)
	if len(got) != 1 || got[0].msgType != TransferTerminationMessageType {
		t.Fatalf("pushed %v, want one termination", got)
	}
	// The URL must carry the provider pid the ACK supplied, not the empty
	// value the row held before it.
	if !strings.Contains(got[0].path, "urn:uuid:p-ack") {
		t.Errorf("pushed to %q, which omits the provider pid the ACK supplied", got[0].path)
	}
}

// TP_C:02-03: two steps, in order.
func TestConsumerDriverWalksTheWholeSequence(t *testing.T) {
	fc := newFakeTransferConsumer(t)
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", After: TransferStarted,
			Sequence: []string{TransferSuspended, TransferTerminated}},
	}}
	h, st := newTestTransferHandler(t, cfg)
	id := seedConsumerTransferFor(t, st, TransferRequested, "urn:uuid:a", fc.srv.URL)

	deliverInboundStart(t, h, id)

	got := fc.waitFor(t, 2)
	if len(got) != 2 ||
		got[0].msgType != TransferSuspensionMessageType ||
		got[1].msgType != TransferTerminationMessageType {
		t.Fatalf("pushed %v, want suspension then termination", got)
	}
}

// The same stop-not-skip rule the provider driver enforces, checked from the
// consumer side. The third step would be legal again from where the refused
// second step leaves the transfer, so a driver that skipped would push it.
func TestConsumerDriverStopsAtAnIllegalStep(t *testing.T) {
	fc := newFakeTransferConsumer(t)
	cfg := config.Config{ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
		{AgreementID: "urn:uuid:a", After: TransferStarted,
			Sequence: []string{TransferTerminated, TransferCompleted, TransferSuspended}},
	}}
	h, st := newTestTransferHandler(t, cfg)
	id := seedConsumerTransferFor(t, st, TransferRequested, "urn:uuid:a", fc.srv.URL)

	deliverInboundStart(t, h, id)

	got := fc.waitFor(t, 1)
	time.Sleep(50 * time.Millisecond)
	if len(got) != 1 || got[0].msgType != TransferTerminationMessageType {
		t.Fatalf("pushed %v, want exactly one termination", got)
	}
}

// pullPartialPath is the deterministic name pullTransferData now uses,
// exposed here so tests can seed and inspect it directly.
func pullPartialPath(dir, consumerPID string) string {
	return filepath.Join(dir, downloadDir, ".partial-"+consumerPID)
}

// TestPullTransferData_ResumesFromAnExistingPartialFile seeds a partial
// download by hand and points the mock provider at a server that asserts
// the resulting request carries the matching Range and answers 206 with
// only the remaining bytes — then checks the two pieces landed concatenated
// in the final file.
func TestPullTransferData_ResumesFromAnExistingPartialFile(t *testing.T) {
	const already = "id,value\n1,hel"
	const rest = "lo\n"
	dir := t.TempDir()
	h, _ := newTestTransferHandler(t, config.Config{DataDir: dir})
	consumerPID := "urn:uuid:resume-1"

	partialDir := filepath.Join(dir, downloadDir)
	if err := os.MkdirAll(partialDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(pullPartialPath(dir, consumerPID), []byte(already), 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := fmt.Sprintf("bytes=%d-", len(already))
		if got := r.Header.Get("Range"); got != want {
			t.Errorf("Range = %q, want %q", got, want)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", len(already), len(already)+len(rest)-1, len(already)+len(rest)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(rest))
	}))
	defer provider.Close()

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: consumerPID}, &DataAddress{Endpoint: provider.URL})

	final := filepath.Join(dir, downloadDir, consumerPID)
	waitForFile(t, final)
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if string(got) != already+rest {
		t.Errorf("final content = %q, want %q", got, already+rest)
	}
	if _, err := os.Stat(pullPartialPath(dir, consumerPID)); !os.IsNotExist(err) {
		t.Error("the partial file was not renamed away")
	}
}

// TestPullTransferData_416DiscardsThePartialFile pins the integrity check:
// a 416 answer to a resumed pull means the provider's file is no longer
// long enough to be a valid continuation, so the partial is deleted rather
// than kept or appended to.
func TestPullTransferData_416DiscardsThePartialFile(t *testing.T) {
	dir := t.TempDir()
	h, _ := newTestTransferHandler(t, config.Config{DataDir: dir})
	consumerPID := "urn:uuid:resume-416"

	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(pullPartialPath(dir, consumerPID), []byte("stale-bytes"), 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer provider.Close()

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: consumerPID}, &DataAddress{Endpoint: provider.URL})

	if _, err := os.Stat(pullPartialPath(dir, consumerPID)); !os.IsNotExist(err) {
		t.Error("the stale partial file was not removed after a 416")
	}
	if _, err := os.Stat(filepath.Join(dir, downloadDir, consumerPID)); !os.IsNotExist(err) {
		t.Error("a final file appeared, but nothing was ever successfully downloaded")
	}
}

// TestPullTransferData_OrdinaryFailureDuringAResumeKeepsThePartialFile pins
// the one behavior change from before this milestone: an ordinary failure
// (here, a 500) on a resumed pull must not discard bytes already received.
func TestPullTransferData_OrdinaryFailureDuringAResumeKeepsThePartialFile(t *testing.T) {
	dir := t.TempDir()
	h, _ := newTestTransferHandler(t, config.Config{DataDir: dir})
	consumerPID := "urn:uuid:resume-500"
	const already = "id,value\n"

	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(pullPartialPath(dir, consumerPID), []byte(already), 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: consumerPID}, &DataAddress{Endpoint: provider.URL})

	got, err := os.ReadFile(pullPartialPath(dir, consumerPID))
	if err != nil {
		t.Fatalf("the partial file was removed after an ordinary failure: %v", err)
	}
	if string(got) != already {
		t.Errorf("partial content = %q, want it untouched at %q", got, already)
	}
}
