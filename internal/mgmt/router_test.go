// This file is `package mgmt` (an internal test package), so NewRouter is
// referenced unqualified while store and config keep their package prefixes.
package mgmt

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

const testToken = "0123456789abcdef"

// stubInitiate stands in for one of the handlers package dsp supplies to this
// listener, answering with the name it was given.
//
// Non-nil on purpose: a nil handler compiles and is still wrapped by
// authenticated, which is non-nil, so registration succeeds and the panic
// waits until a caller gets past the token check. Named on purpose too:
// NewRouter takes the hooks as positional http.Handler values, so a swapped
// pair is invisible to the compiler and to every assertion that only reads a
// status code.
func stubInitiate(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(name))
	})
}

// The names the stubs answer with, shared so the routing assertions in
// route_coverage_test.go compare against the same strings wired in here.
const (
	negotiationHook = "negotiation-initiate"
	transferHook    = "transfer-initiate"
	catalogHook     = "catalog-lookup"
)

func newTestRouter(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	return newTestRouterWithRoster(t, func() bool { return true })
}

// newTestRouterWithRoster builds a router whose roster answers usable or
// not. newTestRouter delegates to it with a usable one, so every existing
// caller — including the route-coverage assertions — is untouched.
func newTestRouterWithRoster(t *testing.T, usable func() bool) (http.Handler, *store.Store) {
	t.Helper()
	st := testStore(t)
	return NewRouter(config.Config{MgmtToken: testToken}, st, usable,
		stubInitiate(negotiationHook), stubInitiate(transferHook), stubInitiate(catalogHook)), st
}

// newTestRouterWithTable builds the same router the helpers above do and hands
// back the record of what it mounted, which is what the route-coverage
// assertions read instead of parsing router.go. Each call constructs its own
// table, so the tests in this package keep running in parallel.
func newTestRouterWithTable(t *testing.T) (http.Handler, *routeTable) {
	t.Helper()
	return newRouterWithTable(config.Config{MgmtToken: testToken}, testStore(t), func() bool { return true },
		stubInitiate(negotiationHook), stubInitiate(transferHook), stubInitiate(catalogHook))
}

// testStore opens the in-memory store the helpers above share and closes it
// when the test ends.
func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestHealthReturnsOK sends no Authorization header, so it also pins that
// /health is unauthenticated (DECISIONS.md §25.4): a readiness probe must not
// need a credential.
func TestHealthReturnsOK(t *testing.T) {
	h, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), `{"status":"ok"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// A readiness probe that cannot see this keeps a connector in rotation when
// it can serve no counterparty. 503 and not the 409 the DSP listener answers
// with: the standing rule that keeps a rejection out of the 5xx range —
// DECISIONS.md §25.1, which the section states in passing rather than as its
// subject — governs what a DSP endpoint emits, and this is a probe on the
// management listener. Not §25.4, cited above for a different property of
// this same route: that one is about the token, this one about the status.
func TestHealthReportsAnExpiredRoster(t *testing.T) {
	t.Parallel()
	h, _ := newTestRouterWithRoster(t, func() bool { return false })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "roster") {
		t.Errorf("body %q does not say what is wrong", rec.Body)
	}
}

func TestHealthStaysOKWithAUsableRoster(t *testing.T) {
	t.Parallel()
	h, _ := newTestRouterWithRoster(t, func() bool { return true })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// With authentication off there is no roster and nothing to be expired.
// A nil predicate must read as "no check", not as "not usable".
func TestHealthIsOKWithNoRosterAtAll(t *testing.T) {
	t.Parallel()
	h, _ := newTestRouterWithRoster(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — a connector with authentication off is healthy", rec.Code)
	}
}

// TestDSPRouteIsNotServedByManagement makes the listener split (DECISIONS.md
// §12) a permanent test rather than a manually verified claim: the management
// listener must not also answer public DSP routes.
func TestDSPRouteIsNotServedByManagement(t *testing.T) {
	h, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/dspace-version", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: the management listener must not serve DSP endpoints", rec.Code)
	}
}

func TestPostAgreementsRecordsIt(t *testing.T) {
	h, st := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /agreements = %d, want 201 (body %q)", rec.Code, rec.Body.String())
	}
	got, ok, err := st.GetAgreement("urn:uuid:a-1")
	if err != nil {
		t.Fatalf("GetAgreement: %v", err)
	}
	if !ok {
		t.Fatal("POST /agreements returned 201 but stored no agreement")
	}
	if got.DatasetID != "urn:dataset:a" {
		t.Errorf("DatasetID = %q, want urn:dataset:a", got.DatasetID)
	}
	if got.Origin != store.OriginImported {
		t.Errorf("Origin = %q, want %q", got.Origin, store.OriginImported)
	}
}

func TestPostAgreementsWithoutTokenIs401(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agreements", body))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /agreements with no Authorization header = %d, want 401", rec.Code)
	}
}

func TestPostAgreementsWithWrongTokenIs401(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	// One character off testToken, not an unrelated literal, so the
	// relationship to the real token stays visible if testToken ever changes.
	wrongToken := testToken[:len(testToken)-1] + "0"
	req.Header.Set("Authorization", "Bearer "+wrongToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /agreements with a wrong token = %d, want 401", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("401 body = %q, want empty — a rejection must not say why it was rejected", rec.Body.String())
	}
}

// TestPostAgreementsAcceptsAnyCaseOfTheScheme pins RFC 9110 §11.1: the auth
// scheme is a case-insensitive token, so a client that writes `bearer` or
// `BEARER` is presenting the same credential. The token after it is not
// folded, which the wrong-token test above still proves.
func TestPostAgreementsAcceptsAnyCaseOfTheScheme(t *testing.T) {
	for _, scheme := range []string{"Bearer", "bearer", "BEARER", "BeArEr"} {
		h, _ := newTestRouter(t)
		body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
		req := httptest.NewRequest(http.MethodPost, "/agreements", body)
		req.Header.Set("Authorization", scheme+" "+testToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Errorf("POST /agreements with scheme %q = %d, want 201", scheme, rec.Code)
		}
	}
}

// TestPostAgreementsRejectsAnotherScheme and its bare-token sibling below are
// the other half of the case-folding above: folding the scheme must not turn
// into accepting the credential under any scheme, or under none.
func TestPostAgreementsRejectsAnotherScheme(t *testing.T) {
	for name, header := range map[string]string{
		"Basic":         "Basic " + testToken,
		"a made-up one": "Token " + testToken,
		// "Bearerx <t>" — "Bearer" with a letter glued on, so a comparison
		// that matched the scheme without its trailing space would accept it.
		"a scheme that merely starts with Bearer": "Bearerx " + testToken,
	} {
		h, _ := newTestRouter(t)
		body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
		req := httptest.NewRequest(http.MethodPost, "/agreements", body)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST /agreements with %s = %d, want 401", name, rec.Code)
		}
	}
}

func TestPostAgreementsRejectsABareToken(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", testToken) // no scheme at all
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /agreements with a bare token and no scheme = %d, want 401", rec.Code)
	}
}

// TestUnauthorizedCarriesAChallenge pins RFC 9110 §15.5.2, which makes a
// WWW-Authenticate challenge a MUST on a 401.
func TestUnauthorizedCarriesAChallenge(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agreements", body))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /agreements with no Authorization header = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want %q", got, "Bearer")
	}
}

func TestPostAgreementsIs401WhenNoTokenIsConfigured(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	// no token configured
	// nil roster predicate: this is about the token, not the roster.
	h := NewRouter(config.Config{}, st, nil,
		stubInitiate(negotiationHook), stubInitiate(transferHook), stubInitiate(catalogHook))
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /agreements with no configured token = %d, want 401 — an unset token must never mean open access", rec.Code)
	}
}

func TestPostAgreementsMissingFieldIs400(t *testing.T) {
	h, _ := newTestRouter(t)
	body := strings.NewReader(`{"agreementId":"urn:uuid:a-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/agreements", body)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /agreements without datasetId = %d, want 400", rec.Code)
	}
}

func TestPostAgreementsDuplicateIs409(t *testing.T) {
	h, _ := newTestRouter(t)
	post := func() int {
		body := strings.NewReader(`{"agreementId":"urn:uuid:a-1","datasetId":"urn:dataset:a"}`)
		req := httptest.NewRequest(http.MethodPost, "/agreements", body)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(); code != http.StatusCreated {
		t.Fatalf("first POST = %d, want 201", code)
	}
	if code := post(); code != http.StatusConflict {
		t.Errorf("second POST with the same id = %d, want 409", code)
	}
}

func TestImportAgreementRecordsAnOptionalCounterparty(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body, want string }{
		{"named", `{"agreementId":"urn:uuid:a","datasetId":"urn:dataset:a","counterpartyId":"urn:participant:peer"}`, "urn:participant:peer"},
		{"omitted", `{"agreementId":"urn:uuid:b","datasetId":"urn:dataset:a"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(":memory:")
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			t.Cleanup(func() { st.Close() })
			h := NewRouter(config.Config{MgmtToken: "t"}, st, nil,
				stubInitiate(negotiationHook), stubInitiate(transferHook), stubInitiate(catalogHook))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/agreements", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer t")
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
			}
			var id string
			if tc.name == "named" {
				id = "urn:uuid:a"
			} else {
				id = "urn:uuid:b"
			}
			a, ok, err := st.GetAgreement(id)
			if err != nil || !ok {
				t.Fatalf("GetAgreement: %v ok=%t", err, ok)
			}
			if a.CounterpartyID != tc.want {
				t.Fatalf("counterparty = %q, want %q", a.CounterpartyID, tc.want)
			}
		})
	}
}

// TestGetAgreementsKeepsAgreementIdAdjacentToDatasetId pins agreementView's
// JSON field order. demo/run.sh's resume round extracts its agreement id
// with a sed that requires "agreementId" and "datasetId" to sit back-to-back
// in the response body:
//
//	sed -n 's/.*"agreementId":"\([^"]*\)","datasetId":"...".*/\1/p'
//
// Nothing else in this codebase would catch a future reordering of
// agreementView's fields (such as inserting counterpartyId between them) —
// the only symptom would be `make demo` failing with "no resume-scenario
// agreement was concluded", a message that points nowhere near this file.
func TestGetAgreementsKeepsAgreementIdAdjacentToDatasetId(t *testing.T) {
	h, st := newTestRouter(t)
	if err := st.CreateAgreement(store.Agreement{
		AgreementID: "urn:uuid:order", DatasetID: "urn:dataset:order",
		Origin: store.OriginImported, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateAgreement: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/agreements", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /agreements = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	matched, err := regexp.MatchString(`"agreementId":"[^"]*","datasetId":"[^"]*"`, rec.Body.String())
	if err != nil {
		t.Fatalf("regexp: %v", err)
	}
	if !matched {
		t.Fatalf("agreementId and datasetId are not adjacent in the response body: %s", rec.Body.String())
	}
}

// The brief's own mutation table seeds only a consumer row and notes that a
// handler returning consumer rows alone survives it — so this test seeds a
// provider-role row too, or "return only the consumer rows" is a mutation
// this test cannot catch.
func TestListTransfersReturnsBothRolesWithARoleField(t *testing.T) {
	h, st := newTestRouter(t)
	now := time.Now().UTC()
	if err := st.CreateConsumerTransfer(store.ConsumerTransfer{
		ConsumerPID: "urn:uuid:c-1", ProviderBaseURL: "http://p", AgreementID: "urn:uuid:a",
		Format: "HttpData-PULL", State: "COMPLETED", CreatedAt: now, UpdatedAt: now,
		ExpectedBytes: 4096, ReceivedBytes: 4096, DataPath: "/d/urn:uuid:c-1", DataCompletedAt: now,
	}); err != nil {
		t.Fatalf("seed consumer transfer: %v", err)
	}
	if err := st.CreateTransfer(store.TransferProcess{
		ProviderPID: "urn:uuid:p-1", ConsumerPID: "urn:uuid:remote-c", AgreementID: "urn:uuid:a",
		State: "STARTED", CallbackAddress: "http://consumer", Format: "HttpData-PULL", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed provider transfer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/transfers", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /transfers = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"role":"consumer"`, `"consumerPid":"urn:uuid:c-1"`, `"receivedBytes":4096`, `"dataPath":"/d/urn:uuid:c-1"`,
		`"role":"provider"`, `"providerPid":"urn:uuid:p-1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %s\nbody: %s", want, body)
		}
	}
}

func TestListTransfersRequiresTheToken(t *testing.T) {
	h, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/transfers", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /transfers with no token = %d, want 401", rec.Code)
	}
}
