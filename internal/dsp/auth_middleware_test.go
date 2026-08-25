package dsp

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/auth"
	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

const (
	testSelf  = "urn:participant:self"
	testPeer  = "urn:participant:peer"
	testOther = "urn:participant:stranger"
)

// authedRouter returns a router with authentication on, plus the peer's
// private key so a test can mint what it needs.
func authedRouter(t *testing.T) (http.Handler, ed25519.PrivateKey) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signerPub, signerPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	path := filepath.Join(t.TempDir(), "roster.json")
	// Held in one variable and interpolated into both copies of the
	// document: the signature covers the expiry, so a second reading of the
	// clock would sign one value and load another.
	fields := `,"version":1,"expires_at":"` + time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339) + `"`
	participants := `[{"id":"` + testPeer + `","public_key":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}]`
	body := `{"participants":` + participants + fields + `}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}
	sig, err := auth.SignRoster(path, signerPriv, time.Now())
	if err != nil {
		t.Fatalf("SignRoster: %v", err)
	}
	signed := `{"participants":` + participants + fields + `,"signature":"` + sig + `"}`
	if err := os.WriteFile(path, []byte(signed), 0o600); err != nil {
		t.Fatalf("write signed roster: %v", err)
	}
	roster, err := auth.LoadRoster(path, signerPub, time.Now())
	if err != nil {
		t.Fatalf("LoadRoster: %v", err)
	}

	// NewRouter assigns mintOutboundCredential on the authenticated path and
	// never puts it back, so without this the closure built below outlives
	// the test and stands in for the package default in everything that runs
	// after it. This roster is alive, so that closure permits — which is why
	// the leak was harmless until the minter could refuse, and why leaving
	// it now shows up as a test waiting forever on a send.
	restoreMinter := mintOutboundCredential
	t.Cleanup(func() { mintOutboundCredential = restoreMinter })

	cfg := config.Config{
		PublicURL:     "https://connector.example.org",
		ParticipantID: testSelf,
		Datasets:      []config.Dataset{{ID: "urn:dataset:a"}},
	}
	return NewRouter(cfg, st, roster, nil).Protocol, priv
}

type routeUnderTest struct {
	method string
	path   string
}

// dspRoutes reads the routes straight out of router.go rather than repeating
// them here. net/http's mux does not expose its patterns, and a hand-kept
// list is exactly the thing that goes stale — a route added later would
// simply be missing from it, and the "every route is closed" test below would
// pass while leaving the new one open. Parsing the source keeps one source of
// truth.
func dspRoutes(t *testing.T) []routeUnderTest {
	t.Helper()
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	versioned := regexp.MustCompile(`HandleFunc\("([A-Z]+) "\+VersionPath\+"([^"]+)"`)
	literal := regexp.MustCompile(`HandleFunc\("([A-Z]+) (/[^"]+)"`)

	var routes []routeUnderTest
	for _, m := range versioned.FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, routeUnderTest{m[1], VersionPath + fillPathParams(m[2])})
	}
	for _, m := range literal.FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, routeUnderTest{m[1], fillPathParams(m[2])})
	}
	// A method-less pattern, which the version endpoint uses so a non-GET
	// gets 405 rather than being swallowed by the catch-all. GET stands in
	// for "any method" here; the path is what matters.
	methodless := regexp.MustCompile(`HandleFunc\("(/[^" ]+)"`)
	for _, m := range methodless.FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, routeUnderTest{"GET", fillPathParams(m[1])})
	}
	if len(routes) < 15 {
		t.Fatalf("parsed only %d routes out of router.go, which cannot be right", len(routes))
	}
	// Every mounted route must be one this parser can see, or it is not
	// checked for authentication and ships open. A pattern built from a
	// constant instead of a literal slips past silently — that happened once
	// with the data endpoint — so the count is compared against every
	// HandleFunc call in the file rather than trusted.
	calls := regexp.MustCompile(`HandleFunc\(`).FindAllString(string(src), -1)
	if len(calls) != len(routes) {
		t.Fatalf("router.go makes %d HandleFunc calls but only %d parse into routes; "+
			"write the pattern as a string literal so it is checked for authentication", len(calls), len(routes))
	}
	return routes
}

// fillPathParams turns a mux pattern into a concrete path. The value does not
// matter: these requests are meant to reach the middleware, not a handler.
func fillPathParams(pattern string) string {
	return regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(pattern, "x")
}

// openRoutes are the paths deliberately mounted outside the wrap. Listing
// them here rather than skipping them silently means adding one is a visible
// edit to a test.
var openRoutes = map[string]bool{"/.well-known/dspace-version": true}

// Every DSP route is closed.
func TestEveryDSPRouteRefusesAnAnonymousRequest(t *testing.T) {
	handler, _ := authedRouter(t)
	for _, rt := range dspRoutes(t) {
		if openRoutes[rt.path] {
			continue
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d, want 401", rt.method, rt.path, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("%s %s: challenge = %q, want Bearer", rt.method, rt.path, got)
		}
	}
}

// The version endpoint is how a counterparty learns what to speak before it
// has any context, and it discloses only a protocol version.
func TestVersionEndpointStaysOpen(t *testing.T) {
	handler, _ := authedRouter(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/dspace-version", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rec.Code)
	}
}

// The rejection says a credential is required and nothing else. Which of the
// six ways it was wrong goes to the log, where an operator can see it and a
// prober cannot.
func TestRejectionDoesNotExplainWhy(t *testing.T) {
	handler, priv := authedRouter(t)
	now := time.Now()
	expired, err := auth.Mint(priv, testPeer, testSelf, now.Add(-time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	misaddressed, err := auth.Mint(priv, testPeer, testOther, now, time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	for _, header := range []string{"", "Bearer garbage", "Bearer " + expired, "Bearer " + misaddressed} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", VersionPath+"/catalog/request", strings.NewReader("{}"))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%q: got %d, want 401", header, rec.Code)
		}
		body := strings.ToLower(rec.Body.String())
		for _, leak := range []string{"expired", "audience", "signature", "issuer", "roster"} {
			if strings.Contains(body, leak) {
				t.Errorf("%q: body leaks %q: %s", header, leak, rec.Body.String())
			}
		}
	}
}

func TestValidCredentialIsAdmitted(t *testing.T) {
	handler, priv := authedRouter(t)
	tok, err := auth.Mint(priv, testPeer, testSelf, time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	body := `{"@context":["` + ContextURL + `"],"@type":"CatalogRequestMessage"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", VersionPath+"/catalog/request", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("a valid credential was refused: %s", rec.Body)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc["@type"] != "dcat:Catalog" && doc["@type"] != "Catalog" {
		t.Errorf("@type = %v, want a catalog", doc["@type"])
	}
}

// The scheme is compared case-insensitively, as RFC 9110 section 11.1
// requires, and an empty token is not a credential.
func TestCutBearer(t *testing.T) {
	for header, want := range map[string]string{
		"Bearer abc":    "abc",
		"bearer abc":    "abc",
		"BEARER abc":    "abc",
		"Bearer  abc  ": "abc",
	} {
		if got, ok := cutBearer(header); !ok || got != want {
			t.Errorf("cutBearer(%q) = %q, %v; want %q, true", header, got, ok, want)
		}
	}
	for _, header := range []string{"", "abc", "Basic abc", "Bearer", "Bearer   "} {
		if _, ok := cutBearer(header); ok {
			t.Errorf("cutBearer(%q) accepted", header)
		}
	}
}

// The initiate hooks are not registered on this listener, and this is the
// assertion that keeps them off it. Every other test in this file would pass
// if they came back: a re-added registration parses into dspRoutes, sits
// behind requireParticipant, and answers 401 to an anonymous request like
// everything else — which is exactly what "every DSP route is closed" checks
// for. Closed is not the property that matters here. DECISIONS.md section 35
// moved these two because a roster participant holding a valid credential is
// not the caller they are meant to have; only the operator is.
func TestTheInitiateHooksAreNotRegisteredOnTheDSPListener(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	registration := regexp.MustCompile(`mux\.Handle(?:Func)?\([^)]*initiate`)
	if found := registration.FindAllString(string(src), -1); len(found) != 0 {
		t.Errorf("router.go registers an initiate route on the DSP listener: %q; "+
			"these belong on the management listener (DECISIONS.md section 35)", found)
	}
}

// What a caller who still has the old URL is told. Not 404: the TCK's own
// assertion helper throws immediately on a 404 and retries anything else, so
// a stale URL in a harness configuration produces a slow diagnosis rather
// than an abrupt one. The 405 comes from the addressed GET route matching
// with id="initiate", which is why Allow names GET and HEAD rather than POST.
//
// Authenticated on purpose: unauthenticated, the wrap answers 401 and the
// mux is never consulted, so the anonymous form would pin the middleware
// instead of the routing this test is about.
//
// Through a real server rather than a recorder, for the same reason the
// management listener's shadowing test uses one: a recorder reports what a
// handler wrote, and what is under test here is which pattern the mux chose.
func TestTheOldInitiatePathsAnswerMethodNotAllowed(t *testing.T) {
	handler, priv := authedRouter(t)
	tok, err := auth.Mint(priv, testPeer, testSelf, time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	for _, path := range []string{
		VersionPath + "/negotiations/initiate",
		VersionPath + "/transfers/initiate",
	} {
		req, err := http.NewRequest("POST", srv.URL+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: got %d, want 405", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Allow"); got != "GET, HEAD" {
			t.Errorf("POST %s: Allow = %q, want %q", path, got, "GET, HEAD")
		}
	}
}
