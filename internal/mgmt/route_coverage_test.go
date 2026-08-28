package mgmt

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type routeUnderTest struct {
	method string
	path   string
}

// openHealthRoute is the one pattern this listener is allowed to mount
// outside the token check. /health is open because a readiness probe must not
// need a credential (DECISIONS.md §25.4); what it discloses in exchange for
// that is recorded beside the route in router.go.
const openHealthRoute = "GET /health"

// managementRoutes builds a router and reports the routes it recorded as it
// mounted them, along with the very handler those registrations built and the
// record of which of them were mounted open.
//
// It reads the router's own registration table rather than parsing router.go,
// which is what this helper did before. Parsing had a measured hole: a route
// registered from any file other than router.go was invisible to it and
// shipped anonymous with nothing failing. Widening the parser to the whole
// package was measured to report an ordinary test helper's own mux and a
// route pattern quoted in a comment, so the parser is gone rather than wider.
// The table also drops the parser's "write the pattern as a string literal"
// constraint, since nothing reads the source any more.
//
// The handler comes back beside the routes so every assertion runs against
// the router whose table it read, rather than against a second construction
// that could differ from it.
func managementRoutes(t *testing.T) (http.Handler, []routeUnderTest, map[string]bool) {
	t.Helper()
	h, tbl := newTestRouterWithTable(t)
	var routes []routeUnderTest
	for _, pattern := range tbl.patterns {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("route pattern %q carries no method, so no request can be built for it", pattern)
		}
		routes = append(routes, routeUnderTest{method, path})
	}
	// A floor, so a router that mounted nothing through the table cannot pass
	// by leaving the assertions with nothing to check — which is what
	// registering on the mux directly, behind the table's back, would do to
	// every route at once. It is not an inventory: routes may be added or
	// removed without touching it.
	if len(routes) < 5 {
		t.Fatalf("the router recorded %d routes through its table, which cannot be right", len(routes))
	}
	return h, routes, tbl.open
}

// Every route this listener serves except the open ones refuses an
// unauthenticated request. This is the management-side counterpart of the DSP
// listener's own route-coverage test: when the initiate hooks moved here, they
// left that test's reach, and a route that is accidentally anonymous is
// exactly what these assertions exist to prevent.
func TestEveryManagementRouteExceptHealthRefusesAnAnonymousRequest(t *testing.T) {
	t.Parallel()
	h, routes, open := managementRoutes(t)

	// What the router recorded as open is checked against what this test
	// expects to be open, rather than used to exempt those routes from the
	// loop below. Used as an exemption it would excuse the very change this
	// guard exists to catch: moving a route to handleOpen would drop it out
	// of the check instead of failing it. That is the property the
	// hand-written exemption list this replaced was carrying, and the table
	// on its own would lose it.
	for pattern := range open {
		if pattern != openHealthRoute {
			t.Errorf("%s is mounted outside the token check, and only %s may be",
				pattern, openHealthRoute)
		}
	}

	for _, rt := range routes {
		if rt.method+" "+rt.path == openHealthRoute {
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
		{http.MethodGet, "/catalog", http.StatusUnauthorized},
		{http.MethodPost, "/catalog", http.StatusMethodNotAllowed},
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

// GET /catalog reaches the catalog lookup, asserted separately from the map
// above because it is the one route here that is not a POST. The reason is
// the same one: it is a further http.Handler in the same positional list, so
// a swap with either initiate hook compiles and changes nothing the compiler
// or a status-code assertion can see.
func TestTheCatalogRouteReachesTheCatalogHandler(t *testing.T) {
	t.Parallel()
	h, _ := newTestRouter(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != catalogHook {
		t.Errorf("GET /catalog reached %q, want %q", body, catalogHook)
	}
}
