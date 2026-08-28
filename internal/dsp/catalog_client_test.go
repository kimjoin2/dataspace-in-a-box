package dsp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The counterparty's document is decoded strictly, the way every inbound
// message in this package is. What makes a lean type work here is not
// tolerance but omission: @context, @type and distribution are the fields
// whose JSON-LD shape varies, and discovery needs none of them.
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
	want := []datasetOffer{{DatasetID: "urn:dataset:sample", OfferID: "urn:dataset:sample#offer"}}
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
		cfg:              testConfig(),
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"participantId": "urn:participant:someone-else"})
	}))
	t.Cleanup(srv.Close)

	h := catalogLookupHandler{
		cfg:              testConfig(),
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
			h := catalogLookupHandler{cfg: testConfig(), knownParticipant: c.known, providerAddress: c.address}
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
