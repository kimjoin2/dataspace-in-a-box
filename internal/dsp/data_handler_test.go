package dsp

import (
	"context"
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

const servedBytes = "id,value\n1,hello\n"

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
	cfg := config.Config{ParticipantID: testSelf, Datasets: []config.Dataset{ds}}

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
	h.handleData(rec, req)
	return rec
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
