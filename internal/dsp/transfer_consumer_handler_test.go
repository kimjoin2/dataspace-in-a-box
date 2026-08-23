package dsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/auth"
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

// Everything this connector sends as consumer carries a credential, and that
// credential names the participant it is addressed to. applyTransition builds
// a store.ConsumerTransfer by hand at both of its consumer-role dispatches —
// the pull a start with an address releases, and the after-STARTED sequence —
// and a CounterpartyID missing from either one sends an unaddressed message.
//
// Nothing else holds those two field assignments in place. The TCK passes 65
// of 65 either way, because its mock endpoints accept whatever arrives without
// inspecting the header, and the regression's only symptom in a real run is
// four warning lines. That is how one of the two came to be missing in the
// first place (commit 2522191).
func TestConsumerFollowUpsAreAddressedToTheCounterparty(t *testing.T) {
	const counterparty = "urn:participant:the-provider"

	// A pure function of its argument, so the assignment below is the only
	// write and the audience is readable straight off the wire. The default
	// minter returns "" and attaches no header at all, which is why
	// TestConsumerPullsWhenTheStartCarriesAnAddress can assert only that the
	// pull happened.
	restore := mintOutboundCredential
	mintOutboundCredential = func(aud string) string { return "Bearer aud=" + aud }
	t.Cleanup(func() { mintOutboundCredential = restore })

	// Buffered and non-blocking, because pushCallback retries: the first
	// arrival is the one under test and later attempts must not block the
	// server goroutine.
	pullAuth := make(chan string, 1)
	pushAuth := make(chan string, 1)
	record := func(into chan string, r *http.Request) {
		select {
		case into <- r.Header.Get("Authorization"):
		default:
		}
	}

	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(pullAuth, r)
		_, _ = w.Write([]byte("some-bytes"))
	}))
	defer data.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(pushAuth, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	dir := t.TempDir()
	h, st := newTestTransferHandler(t, config.Config{
		DataDir: dir,
		ConsumerTransferPolicies: []config.ConsumerTransferPolicy{
			{AgreementID: "urn:uuid:a", After: TransferStarted, Sequence: []string{TransferTerminated}},
		},
	})

	now := time.Now()
	const id = "urn:uuid:c-audience"
	if err := st.CreateConsumerTransfer(store.ConsumerTransfer{
		ConsumerPID: id, ProviderPID: "urn:uuid:p-1", ProviderBaseURL: provider.URL,
		AgreementID: "urn:uuid:a", Format: "HTTP-PULL", State: TransferRequested,
		CounterpartyID: counterparty, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateConsumerTransfer: %v", err)
	}

	// One inbound start releases both dispatches: the address triggers the
	// pull, and reaching STARTED triggers the sequence.
	body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferStartMessageType + `",` +
		`"providerPid":"urn:uuid:p-1","consumerPid":"` + id + `",` +
		`"dataAddress":{"@type":"DataAddress","endpointType":"https://w3id.org/idsa/v4.1/HTTP",` +
		`"endpoint":"` + data.URL + `"}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/"+id+"/start", strings.NewReader(body))
	req.SetPathValue("id", id)
	h.handleTransferStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbound start: got %d, want 200: %s", rec.Code, rec.Body)
	}

	want := "Bearer aud=" + counterparty
	for _, c := range []struct {
		name string
		ch   chan string
	}{
		{"the data pull", pullAuth},
		{"the after-STARTED push", pushAuth},
	} {
		select {
		case got := <-c.ch:
			if got != want {
				t.Errorf("%s carried Authorization %q, want %q — the counterparty is the audience of everything this connector sends",
					c.name, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s never arrived", c.name)
		}
	}

	// Both goroutines are finished before the store closes: the file is the
	// pull's last act, and TERMINATED is the sequence's.
	waitForFile(t, filepath.Join(dir, downloadDir, id))
	deadline := time.Now().Add(2 * time.Second)
	for currentTransferState(t, st, id, true) != TransferTerminated {
		if !time.Now().Before(deadline) {
			t.Fatal("the driven sequence never recorded TERMINATED")
		}
		time.Sleep(5 * time.Millisecond)
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
	if err := os.WriteFile(pullPartialPath(dir, consumerPID), []byte(already), 0o600); err != nil {
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

	// Security regression check: the download file must be owner-only
	// (0600), not group/world-readable (0644, what os.CreateTemp's
	// replacement briefly regressed to). os.Rename preserves the source
	// file's mode, so the resumed pull's final file above must still carry
	// whatever mode the partial was created with — checked here — and a
	// completely fresh pull (no pre-existing partial, so os.OpenFile's
	// O_CREATE actually applies the mode argument) is checked below, since
	// that is the only path where the argument being 0600 vs 0644 matters.
	if info, err := os.Stat(final); err != nil {
		t.Fatalf("stat final file: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("resumed final file mode = %v, want 0600", perm)
	}

	freshPID := "urn:uuid:resume-1-fresh"
	freshProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fresh-bytes"))
	}))
	defer freshProvider.Close()
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: freshPID}, &DataAddress{Endpoint: freshProvider.URL})
	freshFinal := filepath.Join(dir, downloadDir, freshPID)
	waitForFile(t, freshFinal)
	if info, err := os.Stat(freshFinal); err != nil {
		t.Fatalf("stat fresh final file: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("fresh final file mode = %v, want 0600", perm)
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

// TestPullTransferData_206WithWrongContentRangeLeavesThePartialFileUntouched
// pins the check the final review added: a 206 status alone does not prove
// the body picks up where this connector's partial download left off. A
// provider (or a misbehaving proxy) that answers 206 but with a
// Content-Range starting somewhere other than the resume offset must not
// have its body appended — that would silently corrupt the file the same
// way an unexpected 200 would, which the default: case already guards
// against.
func TestPullTransferData_206WithWrongContentRangeLeavesThePartialFileUntouched(t *testing.T) {
	dir := t.TempDir()
	h, _ := newTestTransferHandler(t, config.Config{DataDir: dir})
	consumerPID := "urn:uuid:resume-bad-content-range"
	const already = "id,value\n1,hello\n"

	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(pullPartialPath(dir, consumerPID), []byte(already), 0o600); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Answers 206, but its Content-Range claims the body starts at 0 —
		// not at len(already), the offset this connector actually asked to
		// resume from via its own Range header.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(already)-1, len(already)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(already))
	}))
	defer provider.Close()

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: consumerPID}, &DataAddress{Endpoint: provider.URL})

	got, err := os.ReadFile(pullPartialPath(dir, consumerPID))
	if err != nil {
		t.Fatalf("the partial file was removed after a mismatched Content-Range: %v", err)
	}
	if string(got) != already {
		t.Errorf("partial content = %q, want it untouched at %q (the mismatched 206 must not be appended)", got, already)
	}
	if _, err := os.Stat(filepath.Join(dir, downloadDir, consumerPID)); !os.IsNotExist(err) {
		t.Error("a final file appeared, but the 206's Content-Range did not match the resume offset")
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

// TestPullTransferData_ConcurrentCallsForTheSameTransferDoNotRace pins the
// guard this task adds: a second pullTransferData call for a transfer whose
// first call is still in flight must be dropped, not run alongside it —
// two goroutines writing the same deterministic partial file at once would
// corrupt it.
func TestPullTransferData_ConcurrentCallsForTheSameTransferDoNotRace(t *testing.T) {
	// started is buffered and signaled with a non-blocking send rather than
	// closed: while this test is verifying Step 2's expected failure (the
	// guard not yet in place), a missing guard lets the second call reach
	// this same handler too, and a second close would panic.
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		_, _ = w.Write([]byte(servedBytes))
	}))
	defer provider.Close()

	dir := t.TempDir()
	h, _ := newTestTransferHandler(t, config.Config{DataDir: dir})
	consumerTransfer := store.ConsumerTransfer{ConsumerPID: "urn:uuid:race-consumer"}
	addr := &DataAddress{Endpoint: provider.URL}

	go h.pullTransferData(consumerTransfer, addr)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first call never reached the provider")
	}

	done := make(chan struct{})
	go func() {
		h.pullTransferData(consumerTransfer, addr)
		close(done)
	}()
	select {
	case <-done:
		// Correct: the second call saw the guard and returned immediately,
		// without waiting on `release`.
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("the second call for the same transfer did not return promptly — it ran a second real pull instead of being dropped")
	}

	close(release)
	waitForFile(t, filepath.Join(dir, downloadDir, consumerTransfer.ConsumerPID))
}

func TestParseContentRange(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		first       int64
		hasFirst    bool
		complete    int64
		hasComplete bool
	}{
		{
			name:   "a complete single range yields both values",
			header: "bytes 2000-4095/4096",
			first:  2000, hasFirst: true,
			complete: 4096, hasComplete: true,
		},
		{
			// RFC 9110 section 14.4 permits an unknown complete length. This
			// parses today because the total is discarded; a single ok would
			// make it read as failure and break a resume that works.
			name:   "an unknown complete length is unknown, not a failure",
			header: "bytes 2000-4095/*",
			first:  2000, hasFirst: true,
			complete: 0, hasComplete: false,
		},
		{
			name:   "an absent header yields nothing",
			header: "",
			first:  0, hasFirst: false,
			complete: 0, hasComplete: false,
		},
		{
			name:   "a unit other than bytes yields nothing",
			header: "items 1-2/3",
			first:  0, hasFirst: false,
			complete: 0, hasComplete: false,
		},
		{
			name:   "an unsatisfied-range form carries the total and no first byte",
			header: "bytes */4096",
			first:  0, hasFirst: false,
			complete: 4096, hasComplete: true,
		},
		{
			name:   "a malformed range part does not invalidate a well-formed total",
			header: "bytes -1-5/10",
			first:  0, hasFirst: false,
			complete: 10, hasComplete: true,
		},
		{
			name:   "a malformed total is rejected without losing the first byte",
			header: "bytes 0-5/abc",
			first:  0, hasFirst: true,
			complete: 0, hasComplete: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, hasFirst, complete, hasComplete := parseContentRange(tt.header)
			if hasFirst != tt.hasFirst || (hasFirst && first != tt.first) {
				t.Errorf("first = (%d, %v), want (%d, %v)", first, hasFirst, tt.first, tt.hasFirst)
			}
			if hasComplete != tt.hasComplete || (hasComplete && complete != tt.complete) {
				t.Errorf("complete = (%d, %v), want (%d, %v)", complete, hasComplete, tt.complete, tt.hasComplete)
			}
		})
	}
}

// The pulls below go through newTestTransferHandler like every other test in
// this file, and seed their row with seedConsumerTransfer — a pull that
// records a stated length needs a row to record it against, and that helper
// already writes one. Nothing else is set up, because a pull driven directly
// needs nothing else.

func TestPullRecordsAndPublishesAStatedLength(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("y", 3000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: srv.URL})

	got, err := os.ReadFile(filepath.Join(dir, downloadDir, pid))
	if err != nil {
		t.Fatalf("the download was not published: %v", err)
	}
	if len(got) != len(body) {
		t.Errorf("published %d bytes, want %d", len(got), len(body))
	}
	row, _, err := st.GetConsumerTransfer(pid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.ExpectedBytes != int64(len(body)) {
		t.Errorf("ExpectedBytes = %d, want %d — the stated length was not recorded", row.ExpectedBytes, len(body))
	}
}

// TestPullDoesNotPublishFewerBytesThanStated is the plain-200 half of the
// size contract, and net/http is what enforces it: the client compares the
// body against Content-Length itself and fails the copy with
// io.ErrUnexpectedEOF, so this connector's own comparison of the total
// against the stated length is never reached here. It is reached on a
// resume, where a self-consistent range can still fall short of the total
// the same header states — TestPullDoesNotPublishAResumeShortOfTheStatedTotal
// is that case, and the one that pins the check.
func TestPullDoesNotPublishFewerBytesThanStated(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("z", 100)))
	}))
	defer srv.Close()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: srv.URL})

	if _, err := os.Stat(filepath.Join(dir, downloadDir, pid)); err == nil {
		t.Error("a download short of its stated length was published")
	}
	if _, err := os.Stat(pullPartialPath(dir, pid)); err != nil {
		t.Errorf("the partial was not kept for a later resume: %v", err)
	}
}

// TestPullDoesNotPublishAResumeShortOfTheStatedTotal is the case the stated
// total actually catches. A provider that answers a Range request with a
// valid, self-consistent 206 shorter than the complete length its own
// Content-Range declares leaves the transport nothing to complain about:
// exactly as many bytes arrive as that response promised. Only comparing
// what the file now holds against the total notices that the transfer is
// not finished.
func TestPullDoesNotPublishAResumeShortOfTheStatedTotal(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 100-299/600")
		w.Header().Set("Content-Length", "200")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(strings.Repeat("b", 200)))
	}))
	defer srv.Close()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pullPartialPath(dir, pid), []byte(strings.Repeat("a", 100)), 0o600); err != nil {
		t.Fatal(err)
	}

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid, ExpectedBytes: 600}, &DataAddress{Endpoint: srv.URL})

	if _, err := os.Stat(filepath.Join(dir, downloadDir, pid)); err == nil {
		t.Error("a resume 300 bytes short of the stated complete length was published")
	}
	info, err := os.Stat(pullPartialPath(dir, pid))
	if err != nil {
		t.Fatalf("the partial was not kept for a later resume: %v", err)
	}
	if info.Size() != 300 {
		t.Errorf("partial holds %d bytes, want the 300 that have arrived so far", info.Size())
	}
}

func TestPullPublishesWhenNoLengthIsStated(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("q", 2000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Content-Length: net/http sends this chunked, which is what this
		// connector's own provider did before this milestone and what the
		// TCK's own data endpoint does on every consumer-side pull a gate
		// run makes. This is the branch `make tck` exercises.
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 4; i++ {
			_, _ = w.Write([]byte(body[i*500 : (i+1)*500]))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: srv.URL})

	got, err := os.ReadFile(filepath.Join(dir, downloadDir, pid))
	if err != nil {
		t.Fatalf("a transfer with no stated length was not published: %v", err)
	}
	if len(got) != len(body) {
		t.Errorf("published %d bytes, want %d", len(got), len(body))
	}
	row, _, err := st.GetConsumerTransfer(pid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.ExpectedBytes != 0 {
		t.Errorf("ExpectedBytes = %d, want 0 — nothing was stated", row.ExpectedBytes)
	}
}

func TestResumeDiscardsThePartialWhenTheStatedTotalChanged(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Starts where the partial left off, but the representation is a
		// different length than the one this transfer recorded.
		w.Header().Set("Content-Range", "bytes 100-599/600")
		w.Header().Set("Content-Length", "500")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(strings.Repeat("b", 500)))
	}))
	defer srv.Close()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatal(err)
	}
	partial := pullPartialPath(dir, pid)
	if err := os.WriteFile(partial, []byte(strings.Repeat("a", 100)), 0o600); err != nil {
		t.Fatal(err)
	}

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid, ExpectedBytes: 400}, &DataAddress{Endpoint: srv.URL})

	if _, err := os.Stat(partial); err == nil {
		t.Error("the partial survived a counterparty stating a different complete length; a different representation is not a resumption")
	}
	if _, err := os.Stat(filepath.Join(dir, downloadDir, pid)); err == nil {
		t.Error("the mismatched response was published")
	}
}

func TestResumeAcceptsAnUnknownCompleteLength(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 100-199/*")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(strings.Repeat("b", 100)))
	}))
	defer srv.Close()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pullPartialPath(dir, pid), []byte(strings.Repeat("a", 100)), 0o600); err != nil {
		t.Fatal(err)
	}

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid, ExpectedBytes: 200}, &DataAddress{Endpoint: srv.URL})

	got, err := os.ReadFile(filepath.Join(dir, downloadDir, pid))
	if err != nil {
		t.Fatalf("an unknown complete length was treated as a mismatch: %v", err)
	}
	if len(got) != 200 {
		t.Errorf("published %d bytes, want 200", len(got))
	}
}

func TestPullStopsAtMaxDownloadBytes(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 50; i++ {
			_, _ = w.Write([]byte(strings.Repeat("m", 1000)))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir, MaxDownloadBytes: 2000})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: srv.URL})

	if _, err := os.Stat(filepath.Join(dir, downloadDir, pid)); err == nil {
		t.Error("a download past the ceiling was published")
	}
	info, err := os.Stat(pullPartialPath(dir, pid))
	if err != nil {
		t.Fatalf("stat partial: %v", err)
	}
	// Exactly one byte past the ceiling: that is the byte the LimitReader is
	// given so the overshoot can be detected at all. A looser bound would
	// admit a ceiling applied once per copy buffer instead of once per pull.
	if info.Size() != 2001 {
		t.Errorf("partial holds %d bytes, want 2001 — a 2000-byte ceiling plus the one byte that proves it was exceeded", info.Size())
	}
}

func TestPullIsCutOffWhenProgressStops(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("s", 500)))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir, DataIdleTimeout: 150 * time.Millisecond})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: srv.URL})

	if _, err := os.Stat(filepath.Join(dir, downloadDir, pid)); err == nil {
		t.Error("a pull that stalled was published")
	}
	info, err := os.Stat(pullPartialPath(dir, pid))
	if err != nil {
		t.Fatalf("the partial was not kept after an idle cutoff: %v", err)
	}
	if info.Size() != 500 {
		t.Errorf("partial holds %d bytes, want the 500 that arrived", info.Size())
	}
}

func TestPullCompletesWhileBytesKeepArriving(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Ten chunks, each well inside the idle window, together far beyond
		// it. This is the difference between an idle bound and a total one.
		for i := 0; i < 10; i++ {
			_, _ = w.Write([]byte(strings.Repeat("k", 100)))
			w.(http.Flusher).Flush()
			time.Sleep(30 * time.Millisecond)
		}
	}))
	defer srv.Close()

	// A pull that reached for callbackHTTPClient instead of its own client
	// is caught by TestThePullDoesNotBorrowTheCallbackClientsConnections,
	// which observes it at the call site through the connection pool.
	// TestDataPullClientIsNotTheCallbackClient does not catch it: that one
	// inspects the package variable and never reaches pullTransferData.
	// This test used to catch it by lowering callbackHTTPClient.Timeout in
	// place, which mutated a client every other test in the package shares
	// — docs/follow-ups.md names that pattern as the likeliest source of a
	// future flake — so the coverage moved to a test that mutates nothing
	// rather than being dropped.
	h, st := newTestTransferHandler(t, config.Config{DataDir: dir, DataIdleTimeout: 150 * time.Millisecond})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: srv.URL})

	got, err := os.ReadFile(filepath.Join(dir, downloadDir, pid))
	if err != nil {
		t.Fatalf("a transfer that kept making progress was cut off: %v", err)
	}
	if len(got) != 1000 {
		t.Errorf("published %d bytes, want 1000", len(got))
	}
}

func TestPullRefusesAConnectionThatNeverSendsHeaders(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // never writes a header
	}))
	defer func() { close(release); srv.Close() }()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir, DataIdleTimeout: 150 * time.Millisecond})
	pid := seedConsumerTransfer(t, st, TransferStarted)

	done := make(chan struct{})
	go func() {
		h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: srv.URL})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a counterparty that never sent a response held the pull open; the wait for one is not under the idle timeout")
	}
}

// TestAFreshPullDoesNotInheritALengthFromADiscardedRepresentation walks the
// three attempts that used to end in a permanently blocked transfer. A first
// attempt records the length its counterparty stated and is cut off holding
// a partial; a second is answered about a different representation, which
// discards that partial but leaves the recorded total behind; a third starts
// fresh against a counterparty that states nothing at all. Seeding that
// third attempt from the row would refuse a body that is in fact complete,
// and would say so in a log line attributing a remembered number to a
// counterparty that never sent one.
func TestAFreshPullDoesNotInheritALengthFromADiscardedRepresentation(t *testing.T) {
	dir := t.TempDir()
	stall := make(chan struct{})
	var attempt atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch attempt.Add(1) {
		case 1:
			// States 400, delivers 100, then goes quiet until the idle
			// timeout cuts it off — leaving a partial and a recorded 400.
			w.Header().Set("Content-Length", "400")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(strings.Repeat("a", 100)))
			w.(http.Flusher).Flush()
			<-stall
		case 2:
			// A different representation, so the resume is refused and the
			// partial discarded. The recorded 400 stays in the row.
			w.Header().Set("Content-Range", "bytes 100-599/600")
			w.Header().Set("Content-Length", "500")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(strings.Repeat("b", 500)))
		default:
			// Chunked, so this counterparty states no length at all.
			w.WriteHeader(http.StatusOK)
			for i := 0; i < 5; i++ {
				_, _ = w.Write([]byte(strings.Repeat("c", 50)))
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer func() { close(stall); srv.Close() }()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir, DataIdleTimeout: 150 * time.Millisecond})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	addr := &DataAddress{Endpoint: srv.URL}

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, addr)
	row, _, err := st.GetConsumerTransfer(pid)
	if err != nil {
		t.Fatalf("get after the first attempt: %v", err)
	}
	if row.ExpectedBytes != 400 {
		t.Fatalf("after the first attempt ExpectedBytes = %d, want the stated 400", row.ExpectedBytes)
	}

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid, ExpectedBytes: row.ExpectedBytes}, addr)
	if _, err := os.Stat(pullPartialPath(dir, pid)); err == nil {
		t.Fatal("the second attempt kept a partial belonging to a different representation")
	}
	row, _, err = st.GetConsumerTransfer(pid)
	if err != nil {
		t.Fatalf("get after the second attempt: %v", err)
	}

	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid, ExpectedBytes: row.ExpectedBytes}, addr)
	got, err := os.ReadFile(filepath.Join(dir, downloadDir, pid))
	if err != nil {
		t.Fatalf("a complete body was refused against a length no counterparty stated: %v", err)
	}
	if len(got) != 250 {
		t.Errorf("published %d bytes, want 250", len(got))
	}
	row, _, err = st.GetConsumerTransfer(pid)
	if err != nil {
		t.Fatalf("get after the third attempt: %v", err)
	}
	if row.ExpectedBytes != 0 {
		t.Errorf("ExpectedBytes = %d, want 0 — the attempt that succeeded stated nothing, and the discarded representation's total must not outlive it", row.ExpectedBytes)
	}
}

// TestInboundStartCarriesTheRecordedExpectedBytesToThePull is the wire
// between the stored column and the check that reads it. pullTransferData
// takes ExpectedBytes from the struct its caller assembles rather than from
// a store read, so a recorded total is worth nothing unless lookup projects
// it and the start dispatch passes it on. With that wire cut every resume
// looks like a first attempt and the 206 mismatch check can never fire in
// production, which no direct-call pull test would notice.
func TestInboundStartCarriesTheRecordedExpectedBytesToThePull(t *testing.T) {
	dir := t.TempDir()
	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A valid continuation of the partial seeded below, but of a
		// 600-byte representation rather than the 400-byte one the row
		// recorded.
		w.Header().Set("Content-Range", "bytes 100-599/600")
		w.Header().Set("Content-Length", "500")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(strings.Repeat("b", 500)))
	}))
	defer data.Close()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	now := time.Now()
	const id = "urn:uuid:c-expected"
	if err := st.CreateConsumerTransfer(store.ConsumerTransfer{
		ConsumerPID: id, ProviderPID: "urn:uuid:p-1", ProviderBaseURL: "http://provider.example/2025-1",
		AgreementID: "urn:uuid:a", Format: "HTTP-PULL", State: TransferRequested,
		ExpectedBytes: 400, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateConsumerTransfer: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, downloadDir), 0o755); err != nil {
		t.Fatal(err)
	}
	partial := pullPartialPath(dir, id)
	if err := os.WriteFile(partial, []byte(strings.Repeat("a", 100)), 0o600); err != nil {
		t.Fatal(err)
	}

	body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferStartMessageType + `",` +
		`"providerPid":"urn:uuid:p-1","consumerPid":"` + id + `",` +
		`"dataAddress":{"@type":"DataAddress","endpointType":"https://w3id.org/idsa/v4.1/HTTP",` +
		`"endpoint":"` + data.URL + `"}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/"+id+"/start", strings.NewReader(body))
	req.SetPathValue("id", id)
	h.handleTransferStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbound start: got %d, want 200: %s", rec.Code, rec.Body)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(partial); os.IsNotExist(err) {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the partial survived a counterparty stating a different complete length — the recorded ExpectedBytes never reached the pull")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, downloadDir, id)); err == nil {
		t.Error("the mismatched response was published")
	}
}

// TestPullDoesNotFollowARedirect pins a security property the pull used to
// inherit by borrowing callbackHTTPClient, and which a client of its own is
// free to lose. validateOutgoingCallback checks the endpoint the
// counterparty supplied and nothing a redirect points at, so a client that
// follows one lets that endpoint hop to the management listener, which binds
// to localhost precisely so a firewall mistake cannot expose it.
func TestPullDoesNotFollowARedirect(t *testing.T) {
	dir := t.TempDir()
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		_, _ = w.Write([]byte("bytes from an address no guard ever saw"))
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: srv.URL})

	// The hit count is the assertion that matters: not publishing could
	// happen for any number of reasons, but reaching the target at all means
	// this connector sent a request to an address no guard ever saw.
	if n := reached.Load(); n != 0 {
		t.Errorf("the redirect target was reached %d times; the pull followed a redirect to an address validateOutgoingCallback never checked", n)
	}
	if _, err := os.Stat(filepath.Join(dir, downloadDir, pid)); err == nil {
		t.Error("the pull followed a redirect and published what it found there")
	}
}

// TestDataPullClientIsNotTheCallbackClient pins the properties of the pull's
// client that no behavioral test in this file can reach cheaply. The absent
// overall timeout is the whole reason the client exists, and proving its
// absence behaviorally would mean a test that runs longer than
// callbackHTTPClient's ten seconds; the dial and handshake defaults only
// show up against an endpoint that black-holes packets, which a loopback
// httptest server cannot be.
func TestDataPullClientIsNotTheCallbackClient(t *testing.T) {
	c := dataPullHTTPClient

	if c.Timeout != 0 {
		t.Errorf("Client.Timeout = %v, want none — an overall timeout caps a data pull at whatever the link moves in that time", c.Timeout)
	}
	if c.CheckRedirect == nil {
		t.Error("redirects are enabled; the endpoint guard checked the address the counterparty gave and nothing a redirect points at")
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", c.Transport)
	}
	// These two say the transport is a clone of the default rather than a
	// bare literal. The idle timer around Do bounds the dial and the
	// handshake too, so these are the belt to that suspenders — and what
	// says this client still pools connections like every other one here.
	if tr.DialContext == nil {
		t.Error("DialContext is nil — a bare transport literal, so this client dials without the defaults the rest of the connector uses")
	}
	if tr.TLSHandshakeTimeout <= 0 {
		t.Error("TLSHandshakeTimeout is unset — a bare transport literal rather than a clone of the default")
	}
}

// TestShutdownWaitCoversAnInFlightPull is the covering test for the
// WaitGroup NewRouter returns. It exercises the guarantee rather than the
// plumbing: a pull that is still streaming when the handler returns must
// still be running when Wait is entered, and must have finished — file
// placed, bytes complete — by the time Wait returns.
//
// That ordering is the whole assertion, and it is why the check sits after
// Wait rather than polling for the file the way the other pull tests do.
// Polling would pass whether or not anything waited. Only asserting
// immediately after Wait, with no sleep and no retry, can tell the two
// apart.
//
// Both halves of the wiring are covered because both make Wait return
// early: dropping `pulls: pulls` from NewRouter's transferHandler leaves the
// handler's field nil so nothing is ever counted, and dropping the
// Add/Done pair at the dispatch site leaves the group at zero. Either way
// Wait returns while the provider below is still sleeping, and the file is
// not there yet.
//
// The router is built through NewRouter rather than newTestTransferHandler
// on purpose: the field this pins is populated there, and a handler built
// by hand would skip the line the mutation removes. Building the config as
// a struct literal skips config.Load, so both data bounds are set here for
// the reason testMaxDownloadBytes's comment gives.
func TestShutdownWaitCoversAnInFlightPull(t *testing.T) {
	const payload = "id,value\n1,still-arriving\n"
	// Long enough that the pull is unambiguously in flight when the handler
	// returns, short enough not to slow the suite.
	const bodyDelay = 200 * time.Millisecond

	dir := t.TempDir()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Headers first, then a pause, then the body: this is a response
		// that has started and not finished, which is the state the
		// WaitGroup exists to keep the store open for.
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		time.Sleep(bodyDelay)
		_, _ = w.Write([]byte(payload))
	}))
	defer provider.Close()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	origValidate := validateOutgoingCallback
	validateOutgoingCallback = func(string) error { return nil }
	t.Cleanup(func() { validateOutgoingCallback = origValidate })

	cfg := config.Config{
		PublicURL:        "http://connector.example.org",
		ParticipantID:    testSelf,
		DataDir:          dir,
		DataIdleTimeout:  testDataIdleTimeout,
		MaxDownloadBytes: testMaxDownloadBytes,
		DevMode:          true,
		RequireAuth:      new(bool),
	}
	handler, pulls := NewRouter(cfg, st, auth.Roster{}, nil)

	id := seedConsumerTransferFor(t, st, TransferRequested, "urn:uuid:a-wait", "http://provider.example.org")

	body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferStartMessageType + `",` +
		`"providerPid":"urn:uuid:p-1","consumerPid":"` + id + `",` +
		`"dataAddress":{"@type":"DataAddress","endpointType":"https://w3id.org/idsa/v4.1/HTTP",` +
		`"endpoint":"` + provider.URL + `"}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/"+id+"/start", strings.NewReader(body))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbound start: got %d, want 200: %s", rec.Code, rec.Body)
	}

	final := filepath.Join(dir, downloadDir, id)
	// The handler has returned and the provider is still sleeping, so the
	// pull cannot have finished. If this file already exists, the delay
	// above is not doing its job and everything below would pass for the
	// wrong reason.
	if _, err := os.Stat(final); err == nil {
		t.Fatal("the pull finished before Wait was entered; this test proves nothing about waiting")
	}

	pulls.Wait()

	// No poll and no sleep: Wait is what must have made this true.
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("Wait returned before the in-flight pull placed its file: %v", err)
	}
	if string(got) != payload {
		t.Errorf("downloaded %q, want %q", got, payload)
	}
}

// TestThePullDoesNotBorrowTheCallbackClientsConnections catches at the call
// site what TestDataPullClientIsNotTheCallbackClient can only catch on the
// package variable. That test inspects dataPullHTTPClient and never reaches
// pullTransferData, so changing the Do call to callbackHTTPClient.Do passes
// it — and passed the whole package until this test existed.
//
// The observable difference is the connection pool. callbackHTTPClient
// carries no Transport, so it uses http.DefaultTransport;
// dataPullHTTPClient carries a clone. Two transports mean two pools, so a
// keep-alive connection primed by one client is invisible to the other. This
// test primes the callback client's pool against the same server the pull is
// then pointed at: a pull on its own client must open a second connection,
// and a pull that has borrowed the callback client will reuse the first.
//
// Nothing shared is mutated, which is the point — the check this replaces
// lowered callbackHTTPClient.Timeout in place, and docs/follow-ups.md names
// that pattern as the likeliest source of a future flake.
func TestThePullDoesNotBorrowTheCallbackClientsConnections(t *testing.T) {
	var mu sync.Mutex
	var remoteAddrs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remoteAddrs = append(remoteAddrs, r.RemoteAddr)
		mu.Unlock()
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	// Prime the callback client's pool. The body must be drained and closed
	// or the connection is never returned to it, and then even a borrowed
	// client would open a second connection and this test would pass without
	// proving anything.
	resp, err := callbackHTTPClient.Get(srv.URL)
	if err != nil {
		t.Fatalf("prime the callback client's pool: %v", err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain the priming response: %v", err)
	}
	resp.Body.Close()

	dir := t.TempDir()
	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	pid := seedConsumerTransfer(t, st, TransferStarted)
	h.pullTransferData(store.ConsumerTransfer{ConsumerPID: pid}, &DataAddress{Endpoint: srv.URL})

	mu.Lock()
	defer mu.Unlock()
	if len(remoteAddrs) != 2 {
		t.Fatalf("the server saw %d requests, want 2 (the priming push and the pull)", len(remoteAddrs))
	}
	if remoteAddrs[0] == remoteAddrs[1] {
		t.Error("the data pull reused the callback client's pooled connection, so the call site is using callbackHTTPClient — " +
			"whose ten-second Timeout covers the whole response body and caps a transfer at whatever the link moves in it")
	}
}
