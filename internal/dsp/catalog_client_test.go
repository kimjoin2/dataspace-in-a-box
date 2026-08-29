package dsp

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The counterparty's document is decoded strictly, the way every inbound
// message in this package is. What makes a lean type work here is omission
// rather than tolerance: @context and @type are the fields whose JSON-LD
// shape varies and that discovery does not need. distribution is needed, for
// the format, and is read at arm's length instead — see the tests below it.
func TestRemoteCatalogDecodesWhatDiscoveryNeeds(t *testing.T) {
	t.Parallel()
	const doc = `{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],
	  "@id":"urn:catalog","@type":"Catalog","participantId":"urn:participant:provider",
	  "dataset":[{"@id":"urn:dataset:sample","@type":"Dataset",
	    "hasPolicy":[{"@id":"urn:dataset:sample#offer","@type":"Offer"}],
	    "distribution":[{"@type":"Distribution","format":"dsbox:unspecified"}]}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if c.ParticipantID != "urn:participant:provider" {
		t.Errorf("participantId = %q", c.ParticipantID)
	}
	pairs, skipped := c.pairs()
	if skipped != 0 {
		t.Errorf("skipped = %d, want none", skipped)
	}
	want := []datasetOffer{{DatasetID: "urn:dataset:sample", OfferID: "urn:dataset:sample#offer", Format: "dsbox:unspecified"}}
	if len(pairs) != len(want) || pairs[0] != want[0] {
		t.Errorf("pairs = %+v, want %+v", pairs, want)
	}
}

// One row per negotiable pair, because one initiate call takes one pair. A
// nested list would blur that correspondence.
func TestRemoteCatalogEmitsARowPerOffer(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"p","dataset":[{"@id":"d",
	  "hasPolicy":[{"@id":"o1"},{"@id":"o2"}]}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pairs, _ := c.pairs()
	if len(pairs) != 2 || pairs[0].OfferID != "o1" || pairs[1].OfferID != "o2" {
		t.Errorf("pairs = %+v", pairs)
	}
}

// A dataset with no offer cannot be negotiated for, so it is omitted -- and
// the count comes back so the caller can log it rather than truncate silently.
func TestRemoteCatalogReportsDatasetsItSkipped(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"p","dataset":[{"@id":"d1","hasPolicy":[{"@id":"o"}]},{"@id":"d2","hasPolicy":[]}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pairs, skipped := c.pairs()
	if len(pairs) != 1 || skipped != 1 {
		t.Errorf("pairs = %+v, skipped = %d", pairs, skipped)
	}
}

// Strict. Each of these is a shape the TCK's own catalog and dataset schemas
// declare invalid -- dataset and hasPolicy are arrays with at least one item,
// and hasPolicy is required -- so tolerating them would buy interoperability
// only with documents the TCK rejects. Tolerance is also worse on null: it
// invents a dataset, and an offer whose identifier is empty, which is a value
// an operator would paste into an initiate call.
func TestRemoteCatalogRefusesShapesTheSchemaForbids(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, doc string }{
		{"a single dataset object", `{"participantId":"p","dataset":{"@id":"d"}}`},
		{"a single policy object", `{"participantId":"p","dataset":[{"@id":"d","hasPolicy":{"@id":"o"}}]}`},
		{"a scalar where the dataset array belongs", `{"participantId":"p","dataset":0}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var rc remoteCatalog
			if err := json.Unmarshal([]byte(c.doc), &rc); err == nil {
				t.Error("Unmarshal accepted a document the schema forbids")
			}
		})
	}
}

// A JSON null for dataset is not one of the cases above: Go decodes it to an
// empty slice without error, and an empty slice is a real answer -- a
// counterparty may genuinely advertise nothing. What distinguishes that from a
// document which is not a catalog at all is the participantId check in
// fetchCatalog, not the decode.
func TestANullDatasetListIsAnEmptyCatalogRatherThanAnError(t *testing.T) {
	t.Parallel()
	var c remoteCatalog
	if err := json.Unmarshal([]byte(`{"participantId":"p","dataset":null}`), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if pairs, _ := c.pairs(); len(pairs) != 0 {
		t.Errorf("pairs = %+v, want none", pairs)
	}
}

// admitLoopback replaces the address guard handleCatalogLookup runs on the
// roster's address, so a test can point the handler at an httptest server.
// That server listens on a loopback address, which the real guard refuses --
// the property TestCatalogLookupRefusesARosterAddressItWillNotDialTo holds --
// and every test below is about what the route does once the guard has passed.
//
// A caller must not run in parallel: validateOutgoingCallback is a package
// variable.
func admitLoopback(t *testing.T) {
	t.Helper()
	restore := validateOutgoingCallback
	validateOutgoingCallback = func(string) error { return nil }
	t.Cleanup(func() { validateOutgoingCallback = restore })
}

// The request carries no filter. This connector's own provider side refuses
// one -- DSP leaves the filter expression implementation-defined, so serving a
// full catalog to a consumer that asked for a subset is a worse failure than a
// rejection -- and that argument holds for what it sends.
func TestFetchCatalogSendsNoFilter(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		writeJSON(w, http.StatusOK, map[string]any{"participantId": "p"})
	}))
	t.Cleanup(srv.Close)

	if _, err := fetchCatalog(srv.URL, ""); err != nil {
		t.Fatalf("fetchCatalog: %v", err)
	}
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatalf("the request body does not parse: %v", err)
	}
	if _, ok := msg["filter"]; ok {
		t.Errorf("the request carries a filter: %s", body)
	}
}

// Where the request goes and how it is framed, pinned the way the sibling
// clients pin theirs -- negotiation_client_test.go captures r.URL.Path and
// asserts it. A wrong path, a GET where DSP defines a POST, and a missing
// content type each fail against a real counterparty in a way that reads like
// an unreachable one, and no other test here observes any of them.
//
// The path is written out rather than compared against consumerCatalogPath:
// asserting a constant against itself holds nothing.
func TestFetchCatalogPostsWhereTheProviderServesItsCatalog(t *testing.T) {
	var method, path, contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, contentType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		writeJSON(w, http.StatusOK, map[string]any{"participantId": "p"})
	}))
	t.Cleanup(srv.Close)

	if _, err := fetchCatalog(srv.URL, ""); err != nil {
		t.Fatalf("fetchCatalog: %v", err)
	}
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if want := "/catalog/request"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if want := "application/json"; contentType != want {
		t.Errorf("Content-Type = %q, want %q", contentType, want)
	}
}

// An empty participantId is fatal. Without this check an empty object, a DSP
// error document, an unrelated document and a bare null all decode without
// error into a catalog with no datasets, and the operator is told the
// counterparty advertises nothing rather than that the request failed. The
// precedent is sendInitialRequest, which refuses a response carrying no
// providerPid.
func TestFetchCatalogRefusesADocumentThatIsNotACatalog(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"an empty object", `{}`},
		{"a DSP error document", `{"@type":"CatalogError","code":"400"}`},
		{"an unrelated document", `{"Contents":[{"Key":"a"}]}`},
		{"null", `null`},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(c.body))
			}))
			t.Cleanup(srv.Close)
			if _, err := fetchCatalog(srv.URL, ""); err == nil {
				t.Error("fetchCatalog accepted a document that is not a catalog")
			}
		})
	}
}

// A type error is fatal too: encoding/json populates what it can before
// returning one, so a document with a malformed policy list decodes into a
// structurally valid value with offers missing.
func TestFetchCatalogRefusesAHalfDecodedDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"participantId":"p","dataset":[{"@id":"d","hasPolicy":7}]}`))
	}))
	t.Cleanup(srv.Close)
	if _, err := fetchCatalog(srv.URL, ""); err == nil {
		t.Error("fetchCatalog accepted a half-decoded document")
	}
}

// The provider's own status reaches the operator: a refused credential, a
// missing endpoint and a broken provider are each a different next action.
func TestFetchCatalogReportsTheProvidersStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		t.Cleanup(srv.Close)
		_, err := fetchCatalog(srv.URL, "")
		if err == nil || !strings.Contains(err.Error(), strconv.Itoa(status)) {
			t.Errorf("status %d: err = %v, want it to name the status", status, err)
		}
	}
}

// The response is bounded. The client's timeout covers the body, so a hostile
// provider is bounded in time -- but a streamed response can allocate a great
// deal inside that window, and a catalog is the one DSP body whose size scales
// with the counterparty's holdings.
//
// The document the provider sends is well formed and simply larger than the
// bound, which is what makes this assertion about the bound: a truncated
// document errors at its own truncation whether or not anything limits the
// read, so the error it produces would say nothing.
func TestFetchCatalogBoundsTheResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"participantId":"p","dataset":[`))
		for i := 0; i < maxCatalogResponseBytes/8; i++ {
			w.Write([]byte(`{"@id":"x"},`))
		}
		w.Write([]byte(`{"@id":"x"}]}`))
	}))
	t.Cleanup(srv.Close)
	if _, err := fetchCatalog(srv.URL, ""); err == nil {
		t.Error("fetchCatalog read an unbounded response")
	}
}

// The expiry guard answers before anything is sent. Asserting the fake
// provider was never contacted is the only way the guard's position is
// observable.
func TestCatalogLookupRefusesAnExpiredRosterWithoutDialing(t *testing.T) {
	dialed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialed = true
	}))
	t.Cleanup(srv.Close)

	h := catalogLookupHandler{
		guard:            rosterGuard{check: func() bool { return false }, warn: &sync.Once{}},
		knownParticipant: func(string) bool { return true },
		providerAddress:  func(string) (string, bool) { return srv.URL, true },
	}
	rec := httptest.NewRecorder()
	h.handleCatalogLookup(rec, httptest.NewRequest(http.MethodGet, "/catalog?providerId=p", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if dialed {
		t.Error("the expired connector contacted the provider before refusing")
	}
}

// A catalog that declares a different participant than the one asked for is
// refused. The declared value is an unauthenticated claim, and refusing on one
// is fail-closed -- a different thing from acting on one, the line LoadRoster's
// own comment draws. It is also the one place where evidence about what an
// address actually serves can contradict the roster.
func TestCatalogLookupRefusesAMismatchedParticipant(t *testing.T) {
	admitLoopback(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"participantId": "urn:participant:someone-else"})
	}))
	t.Cleanup(srv.Close)

	h := catalogLookupHandler{
		knownParticipant: func(string) bool { return true },
		providerAddress:  func(string) (string, bool) { return srv.URL, true },
	}
	rec := httptest.NewRecorder()
	h.handleCatalogLookup(rec, httptest.NewRequest(http.MethodGet,
		"/catalog?providerId=urn:participant:provider", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "urn:participant:someone-else") {
		t.Errorf("the refusal does not name what the catalog declared: %s", rec.Body)
	}
}

func TestCatalogLookupRefusesWhatItCannotAddress(t *testing.T) {
	for _, c := range []struct {
		name    string
		query   string
		known   func(string) bool
		address func(string) (string, bool)
		wantIn  string
	}{
		{"no providerId", "/catalog", func(string) bool { return true },
			func(string) (string, bool) { return "http://x", true }, "providerId"},
		{"a participant the roster does not list", "/catalog?providerId=p",
			func(string) bool { return false },
			func(string) (string, bool) { return "http://x", true }, "roster lists"},
		{"a participant with no address", "/catalog?providerId=p",
			func(string) bool { return true },
			func(string) (string, bool) { return "", false }, "connector_address"},
		{"authentication is off", "/catalog?providerId=p", nil, nil, "roster"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := catalogLookupHandler{knownParticipant: c.known, providerAddress: c.address}
			rec := httptest.NewRecorder()
			h.handleCatalogLookup(rec, httptest.NewRequest(http.MethodGet, c.query, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), c.wantIn) {
				t.Errorf("body %s does not mention %q", rec.Body, c.wantIn)
			}
		})
	}
}

// The roster's address is the address this connector dials, so it is guarded
// where every other outbound site guards -- both initiate hooks run this on
// the address they derive, and so do the consumer messages that follow. It
// cannot run in LoadRoster: internal/auth cannot import this package, and a
// counterparty's host does not resolve while this connector is booting. So a
// roster entry naming a loopback or link-local host is admitted at boot, and
// without this check discovery would dial it and hand the operator pairs for
// an exchange the initiate hooks then refuse.
//
// No t.Parallel: this is the one test here that leaves validateOutgoingCallback
// alone, and its siblings replace it.
func TestCatalogLookupRefusesARosterAddressItWillNotDialTo(t *testing.T) {
	dialed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialed = true
	}))
	t.Cleanup(srv.Close)

	h := catalogLookupHandler{
		knownParticipant: func(string) bool { return true },
		// The httptest server's own URL. It is a loopback address, which is
		// exactly the entry an operator can sign into a roster and the real
		// guard refuses.
		providerAddress: func(string) (string, bool) { return srv.URL, true },
	}
	rec := httptest.NewRecorder()
	h.handleCatalogLookup(rec, httptest.NewRequest(http.MethodGet,
		"/catalog?providerId=urn:participant:provider", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "the roster's connector_address") {
		t.Errorf("the refusal does not name the roster's address as what is at fault: %s", rec.Body)
	}
	if dialed {
		t.Error("the connector dialed an address it will not send to")
	}
}

// The success body is the wire contract the management route answers with, and
// nothing else in this package holds it: demo/run.sh reads connectorAddress and
// offerId straight out of this document with sed, and a renamed or transposed
// field would reach an operator's terminal before it reached a test.
//
// So the assertion is on the JSON the route emitted, not on a value decoded
// back through catalogLookupResponse. Decoding through the struct that encoded
// it moves both sides of a tag together, and the rename this test exists to
// catch is invisible to it -- which is the mechanism its own comment used to
// name as the fix.
//
// connectorAddress is the address this connector resolved and dialed, which is
// why it is asserted against the fake provider's own URL rather than against
// anything the query carried.
//
// No t.Parallel: admitLoopback replaces a package variable.
func TestCatalogLookupAnswersWithThePairsAnInitiateCallNeeds(t *testing.T) {
	admitLoopback(t)
	const provider = "urn:participant:provider"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"@context":      []string{ContextURL},
			"@type":         CatalogType,
			"participantId": provider,
			"dataset": []map[string]any{{
				"@id":       "urn:dataset:one",
				"@type":     DatasetType,
				"hasPolicy": []map[string]any{{"@id": "urn:dataset:one#offer"}},
			}, {
				"@id":       "urn:dataset:two",
				"@type":     DatasetType,
				"hasPolicy": []map[string]any{{"@id": "urn:dataset:two#offer"}},
			}},
		})
	}))
	t.Cleanup(srv.Close)

	h := catalogLookupHandler{
		knownParticipant: func(string) bool { return true },
		providerAddress:  func(string) (string, bool) { return srv.URL, true },
	}
	rec := httptest.NewRecorder()
	h.handleCatalogLookup(rec, httptest.NewRequest(http.MethodGet, "/catalog?providerId="+provider, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the response does not decode: %v: %s", err, rec.Body)
	}
	want := map[string]any{
		"participantId":    provider,
		"connectorAddress": srv.URL,
		"datasets": []any{
			map[string]any{"id": "urn:dataset:one", "offerId": "urn:dataset:one#offer"},
			map[string]any{"id": "urn:dataset:two", "offerId": "urn:dataset:two#offer"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the response document is not what an operator and demo/run.sh read:\n got %s\nwant %+v",
			rec.Body, want)
	}
}

// The credential is addressed to the participant the route was asked about.
// Address it to anything else and a counterparty running with authentication on
// answers 401, which reaches the operator as nothing more specific than the
// request having failed -- so no status this connector reports would say what
// is wrong.
//
// The minter is armed the way TestConsumerFollowUpsAreAddressedToTheCounterparty
// arms it, and restored the same way: a pure function of its argument, so the
// audience is readable straight off the wire.
//
// No t.Parallel: mintOutboundCredential is a package variable.
func TestCatalogLookupAddressesItsCredentialToTheProvider(t *testing.T) {
	admitLoopback(t)
	const provider = "urn:participant:provider"
	restore := mintOutboundCredential
	mintOutboundCredential = func(aud string) (string, bool) { return "Bearer aud=" + aud, true }
	t.Cleanup(func() { mintOutboundCredential = restore })

	// Buffered and non-blocking, so the assertion reads what arrived without
	// sharing a variable with the server's goroutine.
	auth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case auth <- r.Header.Get("Authorization"):
		default:
		}
		writeJSON(w, http.StatusOK, map[string]any{"participantId": provider})
	}))
	t.Cleanup(srv.Close)

	h := catalogLookupHandler{
		knownParticipant: func(string) bool { return true },
		providerAddress:  func(string) (string, bool) { return srv.URL, true },
	}
	rec := httptest.NewRecorder()
	h.handleCatalogLookup(rec, httptest.NewRequest(http.MethodGet, "/catalog?providerId="+provider, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	select {
	case got := <-auth:
		if want := "Bearer aud=" + provider; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
	default:
		t.Fatal("the provider was never contacted")
	}
}

// A dataset advertising no offer cannot be negotiated for, so it is left out of
// the response -- and saying so in the log is what keeps that from being a
// silent truncation. The pairs the catalog does carry still come back, which is
// the other half: omitting one dataset must not cost the others.
//
// No t.Parallel: slog.Default is process-global, so a parallel sibling would be
// logging into this test's buffer.
func TestCatalogLookupSaysWhichDatasetsItLeftOut(t *testing.T) {
	admitLoopback(t)
	const provider = "urn:participant:provider"
	var buf bytes.Buffer
	restoreLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(restoreLogger) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"participantId":"` + provider + `","dataset":[` +
			`{"@id":"urn:dataset:negotiable","hasPolicy":[{"@id":"urn:dataset:negotiable#offer"}]},` +
			`{"@id":"urn:dataset:no-offer","hasPolicy":[]}]}`))
	}))
	t.Cleanup(srv.Close)

	h := catalogLookupHandler{
		knownParticipant: func(string) bool { return true },
		providerAddress:  func(string) (string, bool) { return srv.URL, true },
	}
	rec := httptest.NewRecorder()
	h.handleCatalogLookup(rec, httptest.NewRequest(http.MethodGet, "/catalog?providerId="+provider, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	var got catalogLookupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the response does not decode: %v: %s", err, rec.Body)
	}
	want := []datasetOffer{{DatasetID: "urn:dataset:negotiable", OfferID: "urn:dataset:negotiable#offer"}}
	if !slices.Equal(got.Datasets, want) {
		t.Errorf("datasets = %+v, want %+v", got.Datasets, want)
	}
	if !strings.Contains(buf.String(), "omitted") {
		t.Errorf("a dataset was left out of the response and the log does not say so: %s", buf.String())
	}
}

// A catalog of sub-catalogs is how a federated broker advertises. This
// connector does not walk them, and an operator reading a short list of pairs
// has no way to tell that from a counterparty holding nothing -- so the log
// says which of the two happened.
//
// No t.Parallel: slog.Default is process-global.
func TestCatalogLookupSaysItDidNotWalkTheSubCatalogs(t *testing.T) {
	admitLoopback(t)
	const provider = "urn:participant:broker"
	var buf bytes.Buffer
	restoreLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(restoreLogger) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"participantId":"` + provider + `","dataset":[],` +
			`"catalog":[{"@id":"urn:catalog:downstream","@type":"Catalog"}]}`))
	}))
	t.Cleanup(srv.Close)

	h := catalogLookupHandler{
		knownParticipant: func(string) bool { return true },
		providerAddress:  func(string) (string, bool) { return srv.URL, true },
	}
	rec := httptest.NewRecorder()
	h.handleCatalogLookup(rec, httptest.NewRequest(http.MethodGet, "/catalog?providerId="+provider, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(buf.String(), "sub_catalogs") {
		t.Errorf("the catalog advertised sub-catalogs and the log does not say they went unwalked: %s", buf.String())
	}
}

// The format is what POST /transfers/initiate takes, and reading it here is
// the difference between a reader who is told the value and one who is not.
func TestTheFormatIsReadFromTheDistribution(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"urn:participant:provider",
	  "dataset":[{"@id":"urn:dataset:sample",
	    "hasPolicy":[{"@id":"urn:dataset:sample#offer"}],
	    "distribution":[{"@type":"Distribution","format":"HTTP-PULL"}]}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pairs, _ := c.pairs()
	if len(pairs) != 1 || pairs[0].Format != "HTTP-PULL" {
		t.Errorf("pairs = %+v, want one carrying format HTTP-PULL", pairs)
	}
}

// The entries are where the JSON-LD variation lives, so one this connector
// cannot read costs that entry rather than the document. A counterparty that
// writes accessService as a bare string and a distribution as something else
// entirely still yields a negotiable pair.
func TestAnUnreadableDistributionEntryDoesNotCostTheLookup(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"urn:participant:provider",
	  "dataset":[{"@id":"urn:dataset:sample",
	    "hasPolicy":[{"@id":"urn:dataset:sample#offer"}],
	    "distribution":["urn:some-reference",{"format":"HTTP-PULL"}]}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("a distribution entry this connector cannot read failed the whole document: %v", err)
	}
	pairs, _ := c.pairs()
	if len(pairs) != 1 || pairs[0].Format != "HTTP-PULL" {
		t.Errorf("pairs = %+v, want the readable entry's format", pairs)
	}
}

// Absence is not a failure. A counterparty is not obliged to advertise a
// format this connector can read, and a dataset whose format is missing is
// still negotiable — the operator supplies the value, exactly as every
// operator did before any of this was decoded.
func TestADatasetWithNoReadableFormatIsStillNegotiable(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"urn:participant:provider",
	  "dataset":[{"@id":"urn:dataset:sample",
	    "hasPolicy":[{"@id":"urn:dataset:sample#offer"}],
	    "distribution":[{"@type":"Distribution"}]}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pairs, skipped := c.pairs()
	if skipped != 0 {
		t.Errorf("skipped = %d; a missing format is not a reason to drop a dataset", skipped)
	}
	if len(pairs) != 1 || pairs[0].Format != "" {
		t.Fatalf("pairs = %+v, want one carrying no format", pairs)
	}
	// Omitted rather than blank, so an operator reading the response sees a
	// value that is missing instead of one that is empty.
	body, err := json.Marshal(pairs[0])
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(body), "format") {
		t.Errorf("response carries an empty format: %s", body)
	}
}

// Tolerated here, unlike dataset and hasPolicy. The DSP context scopes
// distribution's @container: @set to a node typed Dataset, so a dataset
// written without @type collapses a lone distribution to a bare object -- and
// @type is what this decode type declines to read.
func TestASingleDistributionObjectIsRead(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"urn:participant:provider",
	  "dataset":[{"@id":"urn:dataset:sample",
	    "hasPolicy":[{"@id":"urn:dataset:sample#offer"}],
	    "distribution":{"format":"HTTP-PULL"}}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("a collapsed distribution refused the whole catalog: %v", err)
	}
	pairs, _ := c.pairs()
	if len(pairs) != 1 || pairs[0].Format != "HTTP-PULL" {
		t.Errorf("pairs = %+v, want the collapsed entry's format", pairs)
	}
}

// The cost of refusing the shape above is not one dataset: it is the
// document. This is the case that made the array strict version a regression.
func TestOneCollapsedDatasetDoesNotVoidItsSiblings(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"urn:participant:provider",
	  "dataset":[{"@id":"urn:dataset:collapsed",
	              "hasPolicy":[{"@id":"urn:dataset:collapsed#offer"}],
	              "distribution":{"format":"HTTP-PULL"}},
	             {"@id":"urn:dataset:ordinary","@type":"Dataset",
	              "hasPolicy":[{"@id":"urn:dataset:ordinary#offer"}],
	              "distribution":[{"@type":"Distribution","format":"HTTP-PULL"}]}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("one collapsed dataset refused the whole catalog: %v", err)
	}
	pairs, _ := c.pairs()
	if len(pairs) != 2 {
		t.Errorf("pairs = %+v, want both datasets", pairs)
	}
}

// A shape with nothing to read is not a refusal either: the format is absent,
// which pairs() already reports by omitting it.
func TestADistributionThatIsNeitherArrayNorObjectIsSurvivable(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{`"urn:some-reference"`, `1`, `null`, `{}`} {
		doc := `{"participantId":"urn:participant:provider",
		  "dataset":[{"@id":"urn:dataset:sample",
		    "hasPolicy":[{"@id":"urn:dataset:sample#offer"}],
		    "distribution":` + shape + `}]}`
		var c remoteCatalog
		if err := json.Unmarshal([]byte(doc), &c); err != nil {
			t.Errorf("distribution %s refused the catalog: %v", shape, err)
			continue
		}
		pairs, _ := c.pairs()
		if len(pairs) != 1 || pairs[0].Format != "" {
			t.Errorf("distribution %s: pairs = %+v, want one carrying no format", shape, pairs)
		}
	}
}

// Given a choice, report the one this connector can carry out. Reporting the
// first would hand an operator a token the transfer fails on while a usable
// one sat beside it.
func TestAUsableFormatWinsOverAnAdvertisedOne(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"urn:participant:provider",
	  "dataset":[{"@id":"urn:dataset:sample",
	    "hasPolicy":[{"@id":"urn:dataset:sample#offer"}],
	    "distribution":[{"format":"AmazonS3-PUSH"},{"format":"HTTP-PULL"}]}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pairs, _ := c.pairs()
	if len(pairs) != 1 || pairs[0].Format != "HTTP-PULL" {
		t.Errorf("pairs = %+v, want the format this connector can carry", pairs)
	}
}

// And when none of it is usable, say what is on offer rather than nothing.
func TestAnUnusableFormatIsStillReported(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"urn:participant:provider",
	  "dataset":[{"@id":"urn:dataset:sample",
	    "hasPolicy":[{"@id":"urn:dataset:sample#offer"}],
	    "distribution":[{"format":"AmazonS3-PUSH"}]}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pairs, _ := c.pairs()
	if len(pairs) != 1 || pairs[0].Format != "AmazonS3-PUSH" {
		t.Errorf("pairs = %+v, want the advertised format reported", pairs)
	}
}

// The first entry wins only if it has a format. A distribution that describes
// how to reach the data without naming a format is legitimate, and the one
// after it is where the answer is.
func TestAnEntryWithoutAFormatYieldsToTheNextOne(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"urn:participant:provider",
	  "dataset":[{"@id":"urn:dataset:sample",
	    "hasPolicy":[{"@id":"urn:dataset:sample#offer"}],
	    "distribution":[{"@type":"Distribution","accessService":"urn:service"},
	                    {"@type":"Distribution","format":"HTTP-PULL"}]}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pairs, _ := c.pairs()
	if len(pairs) != 1 || pairs[0].Format != "HTTP-PULL" {
		t.Errorf("pairs = %+v, want the later entry's format", pairs)
	}
}

// A node carrying the term twice is what a producer emits when it has both a
// string form and an @id form. The last is what the duplicate means, so an
// unreadable last value is no format -- not the earlier one, which the
// document itself overrode. Reporting the earlier one would be the worst
// answer available here: it is servedFormat, so an operator would be told
// this connector can carry a transfer the document does not describe.
func TestADuplicatedFormatTakesTheLastAndReportsNoneIfItCannotBeRead(t *testing.T) {
	t.Parallel()
	const doc = `{"participantId":"urn:participant:provider",
	  "dataset":[{"@id":"urn:dataset:sample",
	    "hasPolicy":[{"@id":"urn:dataset:sample#offer"}],
	    "distribution":{"format":"HTTP-PULL","format":{"@id":"HttpData-PULL"}}}]}`
	var c remoteCatalog
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pairs, _ := c.pairs()
	if len(pairs) != 1 || pairs[0].Format != "" {
		t.Errorf("pairs = %+v, want one carrying no format", pairs)
	}
}

// JSON-LD terms are case-sensitive, and Go's struct decoding is not. A node
// keyed Format or FORMAT does not carry the DSP term and must not be read as
// though it did -- least of all when it would outrank a correctly keyed one.
func TestAMiscasedFormatKeyIsNotTheDSPTerm(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{
		`{"Format":"AmazonS3-PUSH"}`,
		`{"FORMAT":"HTTP-PULL"}`,
		`[{"format":"AmazonS3-PUSH"},{"Format":"HTTP-PULL"}]`,
	} {
		doc := `{"participantId":"urn:participant:provider",
		  "dataset":[{"@id":"urn:dataset:sample",
		    "hasPolicy":[{"@id":"urn:dataset:sample#offer"}],
		    "distribution":` + shape + `}]}`
		var c remoteCatalog
		if err := json.Unmarshal([]byte(doc), &c); err != nil {
			t.Errorf("distribution %s refused the catalog: %v", shape, err)
			continue
		}
		pairs, _ := c.pairs()
		if len(pairs) != 1 {
			t.Fatalf("distribution %s: pairs = %+v", shape, pairs)
		}
		if pairs[0].Format == "HTTP-PULL" {
			t.Errorf("distribution %s: read a miscased key as the DSP term", shape)
		}
	}
}

// Both shell harnesses read this response with sed, anchored on the dataset
// identifier, and that pattern depends on these three fields appearing in
// this order. Reordering the struct would break demo/run.sh and
// docs/quickstart.md silently: they would read an empty value and report that
// discovery returned nothing, which names the wrong cause. Nothing else in
// the repository would notice, because Go never reads this response back.
func TestTheLookupResponseKeepsTheFieldOrderTheHarnessesRead(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(datasetOffer{
		DatasetID: "urn:dataset:sample",
		OfferID:   "urn:dataset:sample#offer",
		Format:    "HTTP-PULL",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	const want = `{"id":"urn:dataset:sample","offerId":"urn:dataset:sample#offer","format":"HTTP-PULL"}`
	if string(body) != want {
		t.Errorf("pair marshalled as\n  %s\nwant\n  %s", body, want)
	}
}
