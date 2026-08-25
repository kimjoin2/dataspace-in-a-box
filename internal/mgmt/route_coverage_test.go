package mgmt

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
)

type routeUnderTest struct {
	method string
	path   string
}

// managementRoutes reads the routes straight out of router.go rather than
// repeating them here. net/http's mux does not expose its patterns, and a
// hand-kept list is exactly the thing that goes stale — a route added later
// would simply be missing from it, and the coverage test below would pass
// while leaving the new one open. Parsing the source keeps one source of
// truth, the way the DSP listener's own route-coverage test does.
//
// Both registration forms are read, and that is the difference from the DSP
// side. This listener mounts /health with HandleFunc and everything behind
// the token with Handle, so a parser that saw only one form would prove
// nothing about the other — and the Handle form is precisely the one that
// carries the token.
func managementRoutes(t *testing.T) []routeUnderTest {
	t.Helper()
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	registration := regexp.MustCompile(`mux\.Handle(?:Func)?\("([A-Z]+) (/[^"]+)"`)

	var routes []routeUnderTest
	for _, m := range registration.FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, routeUnderTest{m[1], m[2]})
	}
	// A floor, so a parser that has quietly stopped matching cannot pass by
	// finding nothing to check. It is not an inventory: routes may be added
	// or removed without touching it.
	if len(routes) < 5 {
		t.Fatalf("parsed only %d routes out of router.go, which cannot be right", len(routes))
	}
	// Every mounted route must be one this parser can see, or it is not
	// checked for authentication and ships open. A pattern built from a
	// constant instead of a literal slips past silently — that happened once
	// on the DSP side with the data endpoint — so the number parsed is
	// compared against every registration call in the file rather than
	// trusted.
	calls := regexp.MustCompile(`mux\.Handle(?:Func)?\(`).FindAllString(string(src), -1)
	if len(calls) != len(routes) {
		t.Fatalf("router.go makes %d registration calls but only %d parse into routes; "+
			"write the pattern as a string literal so it is checked for authentication", len(calls), len(routes))
	}
	return routes
}

// openRoutes are the paths deliberately mounted outside the token check.
// Listing them here rather than skipping them silently means opening another
// one is a visible edit to a test. /health is open because a readiness probe
// must not need a credential (DECISIONS.md §25.4); what it discloses in
// exchange for that is recorded beside the route in router.go.
var openRoutes = map[string]bool{"/health": true}

// Every route this listener serves except the open ones refuses an
// unauthenticated request. This is the management-side counterpart of the DSP
// listener's own route-coverage test: when the initiate hooks moved here, they
// left that test's reach, and a route that is accidentally anonymous is
// exactly what these assertions exist to prevent.
func TestEveryManagementRouteExceptHealthRefusesAnAnonymousRequest(t *testing.T) {
	t.Parallel()
	h, _ := newTestRouter(t)
	for _, rt := range managementRoutes(t) {
		if openRoutes[rt.path] {
			continue
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want %d", rt.method, rt.path, rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("%s %s: WWW-Authenticate = %q, want %q", rt.method, rt.path, got, "Bearer")
		}
	}
}

// The new POST routes and the existing GET /transfers do not shadow each
// other. Asserted through a real mux rather than a recorder, because the
// thing under test is routing.
func TestManagementRoutePatternsDoNotShadowEachOther(t *testing.T) {
	t.Parallel()
	h, _ := newTestRouter(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/transfers", http.StatusUnauthorized},
		{http.MethodPost, "/transfers/initiate", http.StatusUnauthorized},
		{http.MethodPost, "/transfers", http.StatusMethodNotAllowed},
		{http.MethodGet, "/transfers/initiate", http.StatusMethodNotAllowed},
		// A trailing slash is a different path to this mux, and no pattern
		// matches it. 404 rather than 405 is what tells a stale caller it has
		// the URL wrong rather than the method.
		{http.MethodPost, "/transfers/initiate/", http.StatusNotFound},
	}
	for _, c := range cases {
		req, err := http.NewRequest(c.method, srv.URL+c.path, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}
}

// Each initiate route reaches the hook it belongs to. NewRouter takes the
// hooks as positional http.Handler values of the same type, so a swapped pair
// compiles, satisfies every other assertion in this package, and surfaces
// only as the TCK failing its consumer-role suites for what reads like a
// protocol fault.
func TestEachInitiateRouteReachesItsOwnHook(t *testing.T) {
	t.Parallel()
	h, _ := newTestRouter(t)
	for path, want := range map[string]string{
		"/negotiations/initiate": negotiationHook,
		"/transfers/initiate":    transferHook,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s = %d, want 200", path, rec.Code)
		}
		body, err := io.ReadAll(rec.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != want {
			t.Errorf("POST %s reached %q, want %q", path, body, want)
		}
	}
}
