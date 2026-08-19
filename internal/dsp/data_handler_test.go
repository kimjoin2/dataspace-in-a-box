package dsp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

const servedBytes = "id,value\n1,hello\n"

// dataFixture wires a provider-role transfer to a dataset with a real file,
// and returns a handler plus the transfer's provider pid.
func dataFixture(t *testing.T, state string, counterparty string, withSource bool) (dataHandler, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ds := config.Dataset{ID: "urn:dataset:a"}
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
