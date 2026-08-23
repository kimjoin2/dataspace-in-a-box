package dsp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

const servedBytes = "id,value\n1,hello\n"

// testDataIdleTimeout is what every fixture in this file puts in
// config.DataIdleTimeout. These tests build a config.Config as a struct
// literal, which bypasses config.Load and its defaults, and the zero value is
// not merely "no timeout": handleData rolls the write deadline by it, and
// SetWriteDeadline(time.Now().Add(0)) is a deadline already in the past, so
// every write would fail. Load guarantees a positive value in production, so
// the fixture is where this belongs — handleData must stay undefensive.
const testDataIdleTimeout = 5 * time.Second

// dataFixture wires a provider-role transfer to a dataset with a real file,
// and returns a handler plus the transfer's provider pid.
func dataFixture(t *testing.T, state string, counterparty string, withSource bool) (dataHandler, string) {
	return dataFixtureWithValidity(t, state, counterparty, withSource, nil)
}

// dataFixtureWithValidity is dataFixture plus a dataset validity window, for
// the tests that need one.
func dataFixtureWithValidity(t *testing.T, state string, counterparty string, withSource bool, validityUntil *time.Time) (dataHandler, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ds := config.Dataset{ID: "urn:dataset:a", ValidityUntil: validityUntil}
	if withSource {
		path := filepath.Join(t.TempDir(), "a.csv")
		if err := os.WriteFile(path, []byte(servedBytes), 0o600); err != nil {
			t.Fatalf("write source: %v", err)
		}
		ds.SourceFile = path
	}
	cfg := config.Config{ParticipantID: testSelf, Datasets: []config.Dataset{ds}, DataIdleTimeout: testDataIdleTimeout}

	now := time.Now()
	if err := st.CreateAgreement(store.Agreement{AgreementID: "urn:uuid:a", DatasetID: "urn:dataset:a",
		Origin: store.OriginImported, CreatedAt: now}); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	if err := st.CreateTransfer(store.TransferProcess{ProviderPID: "p1", ConsumerPID: "c1",
		AgreementID: "urn:uuid:a", State: state, CallbackAddress: "http://x", Format: "HTTP-PULL",
		CounterpartyID: counterparty, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	return dataHandler{cfg: cfg, store: st}, "p1"
}

// pullAs makes an authenticated data request as a given participant.
func pullAs(t *testing.T, h dataHandler, id, issuer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, issuer))
	rec := httptest.NewRecorder()
	h.handleData(deadlineRecorder{rec}, req)
	return rec
}

// interruptFixture is dataFixture for a dataset whose bytes are cut short by
// the simulated-interrupt knob. The source file is fileSize bytes of filler:
// the tests built on it exercise response shape, not content.
func interruptFixture(t *testing.T, fileSize int64, interruptAfter int64) (dataHandler, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	path := filepath.Join(t.TempDir(), "a.csv")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", int(fileSize))), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ds := config.Dataset{ID: "urn:dataset:a", SourceFile: path, SimulateInterruptAfterBytes: interruptAfter}
	cfg := config.Config{ParticipantID: testSelf, Datasets: []config.Dataset{ds}, DataIdleTimeout: testDataIdleTimeout}

	now := time.Now()
	if err := st.CreateAgreement(store.Agreement{AgreementID: "urn:uuid:a", DatasetID: "urn:dataset:a",
		Origin: store.OriginImported, CreatedAt: now}); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	if err := st.CreateTransfer(store.TransferProcess{ProviderPID: "p1", ConsumerPID: "c1",
		AgreementID: "urn:uuid:a", State: TransferStarted, CallbackAddress: "http://x", Format: "HTTP-PULL",
		CounterpartyID: testPeer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}

	return dataHandler{cfg: cfg, store: st}, "p1"
}

// pullInterrupting is pullAs against an interruptFixture, writing into a
// caller-supplied ResponseWriter so the response headers can be read back.
func pullInterrupting(t *testing.T, w http.ResponseWriter, fileSize int64, interruptAfter int64) {
	t.Helper()
	h, id := interruptFixture(t, fileSize, interruptAfter)
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	h.handleData(w, req)
}

// dataFixtureWithContent is dataFixture with a source file of the caller's
// choosing, for the tests that care about a size rather than a value.
func dataFixtureWithContent(t *testing.T, content string) (dataHandler, string) {
	t.Helper()
	h, id := dataFixture(t, TransferStarted, testPeer, true)
	if err := os.WriteFile(h.cfg.Datasets[0].SourceFile, []byte(content), 0o600); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	return h, id
}

func TestDataPullServesTheConfiguredFile(t *testing.T) {
	h, id := dataFixture(t, TransferStarted, testPeer, true)
	rec := pullAs(t, h, id, testPeer)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != servedBytes {
		t.Errorf("body = %q, want %q", got, servedBytes)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// Each refusal is checked with everything else valid, so it proves the check
// it names. A test that also omitted the credential would pass against a
// handler that never looked at state at all.
func TestDataPullRefusals(t *testing.T) {
	for _, c := range []struct {
		name       string
		state      string
		owner      string
		issuer     string
		withSource bool
		want       int
	}{
		{"not started yet", TransferRequested, testPeer, testPeer, true, http.StatusConflict},
		{"suspended", TransferSuspended, testPeer, testPeer, true, http.StatusConflict},
		{"already completed", TransferCompleted, testPeer, testPeer, true, http.StatusConflict},
		{"terminated", TransferTerminated, testPeer, testPeer, true, http.StatusConflict},
		{"belongs to someone else", TransferStarted, testPeer, testOther, true, http.StatusForbidden},
		{"nothing configured behind the dataset", TransferStarted, testPeer, testPeer, false, http.StatusConflict},
	} {
		h, id := dataFixture(t, c.state, c.owner, c.withSource)
		rec := pullAs(t, h, id, c.issuer)
		if rec.Code != c.want {
			t.Errorf("%s: got %d, want %d: %s", c.name, rec.Code, c.want, rec.Body)
		}
		if rec.Body.String() == servedBytes {
			t.Errorf("%s: served the file anyway", c.name)
		}
	}
}

// TestDataPullRefusesAnAgreementHeldAsConsumer pins datasetFor's own copy of
// the servableAsProvider gate. transfer_handler.go's handleTransferRequest
// refuses to create a transfer under an OriginAgreed agreement at all, but
// that is a separate check on a separate call site: nothing stops a row from
// existing in the transfers table under such an agreement's id (a row
// written before this gate existed, or reached through a path this test
// doesn't need to name), and datasetFor is what handleData asks regardless
// of how the row got there. Without its own check here, that transfer would
// still serve bytes.
func TestDataPullRefusesAnAgreementHeldAsConsumer(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	path := filepath.Join(t.TempDir(), "a.csv")
	if err := os.WriteFile(path, []byte(servedBytes), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ds := config.Dataset{ID: "urn:dataset:a", SourceFile: path}
	cfg := config.Config{ParticipantID: testSelf, Datasets: []config.Dataset{ds}}

	now := time.Now()
	if err := st.CreateAgreement(store.Agreement{AgreementID: "urn:uuid:a", DatasetID: "urn:dataset:a",
		Origin: store.OriginAgreed, CreatedAt: now}); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	if err := st.CreateTransfer(store.TransferProcess{ProviderPID: "p1", ConsumerPID: "c1",
		AgreementID: "urn:uuid:a", State: TransferStarted, CallbackAddress: "http://x", Format: "HTTP-PULL",
		CounterpartyID: testPeer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	h := dataHandler{cfg: cfg, store: st}

	rec := pullAs(t, h, "p1", testPeer)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want %d", rec.Code, http.StatusConflict)
	}
	if rec.Body.String() == servedBytes {
		t.Error("served the file under an agreement this connector holds as consumer")
	}
}

// The three refusals are distinguishable, which is what lets an operator read
// a log and know which one happened.
func TestDataPullUnknownTransferIs404(t *testing.T) {
	h, _ := dataFixture(t, TransferStarted, testPeer, true)
	if rec := pullAs(t, h, "urn:uuid:never", testPeer); rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

// Ownership is checked before state, so probing someone else's transfer
// reveals nothing about how far along it is.
func TestDataPullChecksOwnershipBeforeState(t *testing.T) {
	h, id := dataFixture(t, TransferRequested, testPeer, true)
	if rec := pullAs(t, h, id, testOther); rec.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403 — state was leaked to a stranger", rec.Code)
	}
}

// A STARTED transfer's authorization is not permanent: this pins the one
// check that was entirely missing before this milestone — nothing else
// re-checks anything after the state transition. Without it, an agreement
// whose validity window has closed keeps serving bytes forever once a
// transfer reaches STARTED.
func TestDataPullRefusesAfterTheValidityWindowCloses(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	h, id := dataFixtureWithValidity(t, TransferStarted, testPeer, true, &past)
	rec := pullAs(t, h, id, testPeer)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d, want 409 — the access window has closed", rec.Code)
	}
	if rec.Body.String() == servedBytes {
		t.Error("served the file after the validity window closed")
	}
}

func TestDataPullServesWithinAnOpenValidityWindow(t *testing.T) {
	future := time.Now().Add(time.Hour)
	h, id := dataFixtureWithValidity(t, TransferStarted, testPeer, true, &future)
	rec := pullAs(t, h, id, testPeer)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != servedBytes {
		t.Errorf("body = %q, want %q", got, servedBytes)
	}
}

// A start message carrying an address makes the consumer fetch, and what it
// fetches lands on disk whole. This is the consumer half of the data plane
// and the TCK verifies none of it.
func TestConsumerPullsWhenTheStartCarriesAnAddress(t *testing.T) {
	var pulled string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pulled = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(servedBytes))
	}))
	defer provider.Close()

	dir := t.TempDir()
	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	id := seedConsumerTransfer(t, st, TransferRequested)

	body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferStartMessageType + `",` +
		`"providerPid":"urn:uuid:p","consumerPid":"` + id + `",` +
		`"dataAddress":{"@type":"DataAddress","endpointType":"https://w3id.org/idsa/v4.1/HTTP",` +
		`"endpoint":"` + provider.URL + `"}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/"+id+"/start", strings.NewReader(body))
	req.SetPathValue("id", id)
	h.handleTransferStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start: got %d, want 200: %s", rec.Code, rec.Body)
	}

	path := filepath.Join(dir, downloadDir, id)
	waitForFile(t, path)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	if string(got) != servedBytes {
		t.Errorf("downloaded %q, want %q", got, servedBytes)
	}
	// The pull is an outbound call like any other, so it carries a credential
	// when one is configured. Here the minter is the no-op default, so the
	// assertion is only that the pull happened at all.
	_ = pulled

	// Nothing partial is left behind.
	entries, err := os.ReadDir(filepath.Join(dir, downloadDir))
	if err != nil {
		t.Fatalf("read download dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".partial-") {
			t.Errorf("a partial file was left behind: %s", e.Name())
		}
	}
}

// A start with no address is the control-plane-only case, and it must not
// produce a fetch or a file.
func TestConsumerDoesNotPullWithoutAnAddress(t *testing.T) {
	dir := t.TempDir()
	h, st := newTestTransferHandler(t, config.Config{DataDir: dir})
	id := seedConsumerTransfer(t, st, TransferRequested)

	body := `{"@context":["` + ContextURL + `"],"@type":"` + TransferStartMessageType + `",` +
		`"providerPid":"urn:uuid:p","consumerPid":"` + id + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, VersionPath+"/transfers/"+id+"/start", strings.NewReader(body))
	req.SetPathValue("id", id)
	h.handleTransferStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start: got %d, want 200", rec.Code)
	}

	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, downloadDir)); !os.IsNotExist(err) {
		t.Errorf("a download directory appeared with no address to pull from: %v", err)
	}
}

func TestParseRangeStart(t *testing.T) {
	cases := []struct {
		header string
		want   int64
		wantOK bool
	}{
		{"", 0, false},
		{"bytes=0-", 0, true},
		{"bytes=42-", 42, true},
		{"bytes=-5", 0, false},         // the excluded suffix form
		{"bytes=5-10", 0, false},       // the excluded closed form
		{"bytes=0-10,20-30", 0, false}, // the excluded multi-range form
		{"bytes=abc-", 0, false},
		{"bytes=-1-", 0, false},
		{"not-bytes=5-", 0, false},
	}
	for _, c := range cases {
		got, gotOK := parseRangeStart(c.header)
		if got != c.want || gotOK != c.wantOK {
			t.Errorf("parseRangeStart(%q) = (%d, %v), want (%d, %v)", c.header, got, gotOK, c.want, c.wantOK)
		}
	}
}

// TestDataPullServesAPartialRange pins the 206 path: a valid open-ended
// range seeks and streams only the requested suffix, with a correct
// Content-Range.
func TestDataPullServesAPartialRange(t *testing.T) {
	h, id := dataFixture(t, TransferStarted, testPeer, true)
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	req.Header.Set("Range", "bytes=3-")
	rec := httptest.NewRecorder()
	h.handleData(deadlineRecorder{rec}, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("got %d, want 206: %s", rec.Code, rec.Body)
	}
	want := servedBytes[3:]
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	wantRange := fmt.Sprintf("bytes 3-%d/%d", len(servedBytes)-1, len(servedBytes))
	if got := rec.Header().Get("Content-Range"); got != wantRange {
		t.Errorf("Content-Range = %q, want %q", got, wantRange)
	}
}

// TestDataPullRangeAtOrPastTheEndIs416 pins the integrity check the spec's
// "Integrity across a resume" section is built on: a range that starts at or
// after the file's current size is refused, not silently served empty or
// served from zero.
func TestDataPullRangeAtOrPastTheEndIs416(t *testing.T) {
	h, id := dataFixture(t, TransferStarted, testPeer, true)
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", len(servedBytes)))
	rec := httptest.NewRecorder()
	h.handleData(rec, req)

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("got %d, want 416: %s", rec.Code, rec.Body)
	}
	wantRange := fmt.Sprintf("bytes */%d", len(servedBytes))
	if got := rec.Header().Get("Content-Range"); got != wantRange {
		t.Errorf("Content-Range = %q, want %q", got, wantRange)
	}
	if rec.Body.String() == servedBytes {
		t.Error("served the file anyway")
	}
}

// TestDataPullRangePastTheEndKeepsItsErrorDocument pins the refusal against a
// real server rather than a recorder, because only a real server enforces
// Content-Length. handleData declares the dataset's length before the Range
// branch so every shape that sends bytes carries it; a 416 sends an error
// document instead, and if it inherited that declaration net/http would
// truncate the document to the file's length and close the connection. The
// consumer would then read a broken response where a reason was written.
func TestDataPullRangePastTheEndKeepsItsErrorDocument(t *testing.T) {
	h, _ := dataFixture(t, TransferStarted, testPeer, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", "p1")
		r = r.WithContext(context.WithValue(r.Context(), issuerContextKey{}, testPeer))
		h.handleData(w, r)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+VersionPath+"/data/p1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", len(servedBytes)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("got %d, want 416", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v — the refusal was truncated by a Content-Length it should not carry", err)
	}
	var doc ErrorResponse
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the refusal is not a readable error document (%v): %q", err, body)
	}
	if doc.Code != strconv.Itoa(http.StatusRequestedRangeNotSatisfiable) {
		t.Errorf("code = %q, want %q", doc.Code, strconv.Itoa(http.StatusRequestedRangeNotSatisfiable))
	}
}

// TestDataPullUnsupportedRangeFormIsIgnored pins RFC 7233's own guidance for
// a range form this connector does not implement: ignore it and serve the
// whole thing, exactly as if no Range header had been sent at all.
func TestDataPullUnsupportedRangeFormIsIgnored(t *testing.T) {
	h, id := dataFixture(t, TransferStarted, testPeer, true)
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	req.Header.Set("Range", "bytes=0-5")
	rec := httptest.NewRecorder()
	h.handleData(deadlineRecorder{rec}, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (the unsupported closed form is ignored): %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != servedBytes {
		t.Errorf("body = %q, want the whole file %q", got, servedBytes)
	}
}

// TestDataPullSimulatedInterruptTruncatesAndSeversTheConnection pins the
// demo-only fault-injection knob: a non-Range request is cut short at the
// configured byte count and the connection is severed, not cleanly closed —
// the client must see a real error, the same as it would against a genuine
// network interruption.
func TestDataPullSimulatedInterruptTruncatesAndSeversTheConnection(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	content := strings.Repeat("x", 100)
	path := filepath.Join(t.TempDir(), "a.csv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ds := config.Dataset{ID: "urn:dataset:a", SourceFile: path, SimulateInterruptAfterBytes: 20}
	cfg := config.Config{ParticipantID: testSelf, Datasets: []config.Dataset{ds}, DataIdleTimeout: testDataIdleTimeout}
	now := time.Now()
	if err := st.CreateAgreement(store.Agreement{AgreementID: "urn:uuid:a", DatasetID: "urn:dataset:a",
		Origin: store.OriginImported, CreatedAt: now}); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	if err := st.CreateTransfer(store.TransferProcess{ProviderPID: "p1", ConsumerPID: "c1",
		AgreementID: "urn:uuid:a", State: TransferStarted, CallbackAddress: "http://x", Format: "HTTP-PULL",
		CounterpartyID: testPeer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	h := dataHandler{cfg: cfg, store: st}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", "p1")
		r = r.WithContext(context.WithValue(r.Context(), issuerContextKey{}, testPeer))
		h.handleData(w, r)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + VersionPath + "/data/p1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	got, readErr := io.ReadAll(resp.Body)
	if readErr == nil {
		t.Fatalf("read: got no error, and %d bytes — the connection should have been severed before the full 100 arrived", len(got))
	}
	if len(got) != 20 {
		t.Errorf("read %d bytes before the error, want exactly the configured 20", len(got))
	}
}

// TestDataPullSimulatedInterruptDoesNotFireOnARangedRequest pins the other
// half: a resumed (ranged) request always completes, which is what lets a
// demo's interrupt-then-resume sequence terminate instead of truncating
// forever.
func TestDataPullSimulatedInterruptDoesNotFireOnARangedRequest(t *testing.T) {
	h, id := dataFixtureWithSimulatedInterrupt(t, 3)
	req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
	req.Header.Set("Range", "bytes=3-")
	rec := httptest.NewRecorder()
	h.handleData(deadlineRecorder{rec}, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("got %d, want 206 — a ranged request must never be truncated: %s", rec.Code, rec.Body)
	}
	want := servedBytes[3:]
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// dataFixtureWithSimulatedInterrupt is dataFixture plus
// SimulateInterruptAfterBytes, for the one test above that needs the field
// set but exercises it through httptest.NewRecorder (which is fine for the
// ranged case: the knob must not fire at all there, so Hijack is never
// reached).
func dataFixtureWithSimulatedInterrupt(t *testing.T, afterBytes int64) (dataHandler, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	path := filepath.Join(t.TempDir(), "a.csv")
	if err := os.WriteFile(path, []byte(servedBytes), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ds := config.Dataset{ID: "urn:dataset:a", SourceFile: path, SimulateInterruptAfterBytes: afterBytes}
	cfg := config.Config{ParticipantID: testSelf, Datasets: []config.Dataset{ds}, DataIdleTimeout: testDataIdleTimeout}

	now := time.Now()
	if err := st.CreateAgreement(store.Agreement{AgreementID: "urn:uuid:a", DatasetID: "urn:dataset:a",
		Origin: store.OriginImported, CreatedAt: now}); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	if err := st.CreateTransfer(store.TransferProcess{ProviderPID: "p1", ConsumerPID: "c1",
		AgreementID: "urn:uuid:a", State: TransferStarted, CallbackAddress: "http://x", Format: "HTTP-PULL",
		CounterpartyID: testPeer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	return dataHandler{cfg: cfg, store: st}, "p1"
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no file appeared at %s", path)
}

// deadlineRecorder is httptest.ResponseRecorder plus the one method
// http.ResponseController needs. The recorder implements neither
// SetWriteDeadline nor Unwrap, so without this every streaming test would
// get the fatal error handleData now raises when deadlines are unsupported
// — and that error is fatal on purpose, so the shim moves rather than the
// production behavior.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
}

func (deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

func TestCopyUnderRollingDeadlineOutlivesAShortWriteTimeout(t *testing.T) {
	// httptest.NewServer builds a server with no timeouts at all, so a test
	// written against it would pass with and without the rolling deadline
	// and its mutation check could never fire. The timeout has to be
	// injected into an unstarted server.
	body := strings.Repeat("x", 64<<10)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		if _, err := copyUnderRollingDeadline(w, rc, &slowReader{s: body, chunk: 4 << 10, pause: 40 * time.Millisecond}, time.Second); err != nil {
			t.Errorf("copy: %v", err)
		}
	}))
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v — the response was cut off, so the deadline was not rolled", err)
	}
	if len(got) != len(body) {
		t.Errorf("got %d bytes, want %d — the write timeout truncated a response that kept making progress", len(got), len(body))
	}
}

func TestCopyUnderRollingDeadlineFailsWhenDeadlinesAreUnsupported(t *testing.T) {
	rec := httptest.NewRecorder()
	rc := http.NewResponseController(rec)
	_, err := copyUnderRollingDeadline(rec, rc, strings.NewReader("hello"), time.Second)
	if err == nil {
		t.Fatal("copy succeeded on a writer with no deadline support; an ignored error leaves the response back under the server-wide WriteTimeout with nothing saying so")
	}
}

// slowReader delivers its string in chunks with a pause between them, so a
// response takes longer than a short WriteTimeout while never going idle.
type slowReader struct {
	s     string
	chunk int
	pause time.Duration
	off   int
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.off >= len(r.s) {
		return 0, io.EOF
	}
	if r.off > 0 {
		time.Sleep(r.pause)
	}
	n := copy(p[:min(len(p), r.chunk)], r.s[r.off:])
	r.off += n
	return n, nil
}

func TestSimulatedInterruptStillDeclaresTheFullLength(t *testing.T) {
	// The interrupt branch is a 200 that returns before the plain-200 path,
	// so a Content-Length set only there would leave this response chunked —
	// and a consumer would record no expected size for the first attempt of
	// exactly the transfer make demo resumes.
	rec := httptest.NewRecorder()
	pullInterrupting(t, deadlineRecorder{rec}, 5000, 2000)
	if got := rec.Result().Header.Get("Content-Length"); got != "5000" {
		t.Errorf("Content-Length = %q, want the file's real length 5000 — an interrupted response must still declare it", got)
	}
}

// countingDeadlineRecorder is deadlineRecorder that also records how many
// times the handler pushed the write deadline out, and refuses a deadline
// that is not actually in the future. deadlineRecorder accepts any deadline,
// which is right for the tests that only need the response to happen, but it
// means a fixture whose DataIdleTimeout is the zero value goes unnoticed
// there — and in production that is SetWriteDeadline(now.Add(0)), a deadline
// already past, which fails every write.
type countingDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines int
}

func (c *countingDeadlineRecorder) SetWriteDeadline(d time.Time) error {
	c.deadlines++
	if !d.After(time.Now()) {
		return fmt.Errorf("write deadline %v is not in the future", d)
	}
	return nil
}

// TestDataPullRollsTheWriteDeadlineOnBothStreamingPaths pins the wiring
// rather than the helper. copyUnderRollingDeadline has its own tests, but
// nothing else notices if handleData goes back to io.Copy: a recorder has no
// deadline to exceed, so every other test in this file passes either way
// while a real transfer is silently capped at the server's WriteTimeout
// again.
//
// The large rows are what make that visible. A count of "at least one push"
// is satisfied by set-the-deadline-once-then-io.Copy, which is the original
// bug exactly — one deadline, then one sendfile the handler is parked in. A
// source larger than copyBufSize forces a correct loop to read more than
// once, and it pushes the deadline on every pass, so only the loop clears
// two.
func TestDataPullRollsTheWriteDeadlineOnBothStreamingPaths(t *testing.T) {
	small := servedBytes
	large := strings.Repeat("x", copyBufSize+copyBufSize/2)
	for _, c := range []struct {
		name         string
		content      string
		rangeHeader  string
		wantCode     int
		wantBody     string
		minDeadlines int
	}{
		{"full 200", small, "", http.StatusOK, small, 1},
		{"partial 206", small, "bytes=3-", http.StatusPartialContent, small[3:], 1},
		{"full 200 past the copy buffer", large, "", http.StatusOK, large, 2},
		{"partial 206 past the copy buffer", large, "bytes=3-", http.StatusPartialContent, large[3:], 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, id := dataFixtureWithContent(t, c.content)
			req := httptest.NewRequest(http.MethodGet, VersionPath+"/data/"+id, nil)
			req.SetPathValue("id", id)
			req = req.WithContext(context.WithValue(req.Context(), issuerContextKey{}, testPeer))
			if c.rangeHeader != "" {
				req.Header.Set("Range", c.rangeHeader)
			}
			rec := &countingDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			h.handleData(rec, req)

			if rec.Code != c.wantCode {
				t.Fatalf("got %d, want %d: %s", rec.Code, c.wantCode, rec.Body)
			}
			if rec.deadlines < c.minDeadlines {
				t.Errorf("pushed the write deadline %d times, want at least %d — a single push before an io.Copy is the sendfile bug this replaced, and the transfer is bounded by the server's WriteTimeout again",
					rec.deadlines, c.minDeadlines)
			}
			if got := rec.Body.String(); got != c.wantBody {
				t.Errorf("body = %d bytes, want %d — nothing was streamed, so the deadline the handler set was refused", len(got), len(c.wantBody))
			}
		})
	}
}

// lateReader delivers its whole string in one chunk, after a pause long
// enough that a short server-wide WriteTimeout has already elapsed. The
// chunk has to be larger than net/http's internal buffers (2048 at
// response.w, 4096 at conn.bufw) or the write never reaches the socket and
// no deadline applies to it — the same reason copyBufSize is large.
type lateReader struct {
	s     string
	pause time.Duration
	done  bool
}

func (r *lateReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	time.Sleep(r.pause)
	r.done = true
	return copy(p, r.s), nil
}

// TestCopyUnderRollingDeadlineRollsBeforeTheFirstWrite pins the ordering,
// which the outlives-a-short-WriteTimeout test above cannot: its first chunk
// arrives immediately, so rolling the deadline after each write would carry
// it too. The server's WriteTimeout is already ticking when the handler is
// entered, so the case that separates the two orderings is a first chunk
// that arrives after it has elapsed.
func TestCopyUnderRollingDeadlineRollsBeforeTheFirstWrite(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		src := &lateReader{s: strings.Repeat("x", 64<<10), pause: 250 * time.Millisecond}
		if _, err := copyUnderRollingDeadline(w, rc, src, time.Second); err != nil {
			t.Errorf("copy: %v — the first write was still governed by the server's WriteTimeout", err)
		}
	}))
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(got) != 64<<10 {
		t.Errorf("got %d bytes, want %d", len(got), 64<<10)
	}
}

// TestDataPullSimulatedInterruptSeversPastTheSniffThreshold covers the branch
// this milestone's own Content-Length made reachable by sendfile. The
// existing real-server interrupt test cuts at 20 bytes, which stays inside
// (*http.response).ReadFrom's 512-byte sniff and so never reaches
// *net.TCPConn.ReadFrom; the 2000-byte case runs on a recorder, which has no
// ReadFrom at all. Between them the newly-live path was untested in both
// directions. This cuts well past 512 against a real server, so a regression
// to io.CopyN here streams under one sendfile call.
func TestDataPullSimulatedInterruptSeversPastTheSniffThreshold(t *testing.T) {
	const fileSize, interruptAfter = 8000, 4000
	h, id := interruptFixture(t, fileSize, interruptAfter)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", id)
		r = r.WithContext(context.WithValue(r.Context(), issuerContextKey{}, testPeer))
		h.handleData(w, r)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + VersionPath + "/data/" + id)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(fileSize) {
		t.Errorf("Content-Length = %q, want %d", got, fileSize)
	}

	got, readErr := io.ReadAll(resp.Body)
	if readErr == nil {
		t.Fatalf("read: got no error, and %d bytes — the connection should have been severed before the full %d arrived", len(got), fileSize)
	}
	if len(got) != interruptAfter {
		t.Errorf("read %d bytes before the error, want exactly the configured %d", len(got), interruptAfter)
	}
}

// TestDataPullSimulatedInterruptRollsTheWriteDeadline is the wiring test for
// the third path that writes dataset bytes. It is separate from the truncation
// test above because truncation happens either way: io.CopyN cuts at the same
// byte, so only the deadline pushes tell the two implementations apart.
func TestDataPullSimulatedInterruptRollsTheWriteDeadline(t *testing.T) {
	rec := &countingDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	pullInterrupting(t, rec, 3*copyBufSize, 2*copyBufSize)

	if rec.deadlines < 2 {
		t.Errorf("pushed the write deadline %d times, want at least 2 — io.CopyN is back on the interrupt branch, and with a Content-Length declared that is one sendfile under the server's WriteTimeout", rec.deadlines)
	}
	if got := rec.Body.Len(); got != 2*copyBufSize {
		t.Errorf("wrote %d bytes, want the configured %d", got, 2*copyBufSize)
	}
}
