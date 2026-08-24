package mgmt

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every route this listener serves except /health refuses an unauthenticated
// request. This is the management-side counterpart of the DSP listener's own
// route-coverage test: when the initiate hooks moved here, they left that
// test's reach, and a route that is accidentally anonymous is exactly what
// these assertions exist to prevent.
func TestEveryManagementRouteExceptHealthRefusesAnAnonymousRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/agreements"},
		{http.MethodGet, "/agreements"},
		{http.MethodGet, "/transfers"},
		{http.MethodPost, "/negotiations/initiate"},
		{http.MethodPost, "/transfers/initiate"},
	}
	h, _ := newTestRouter(t)
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("%s %s: WWW-Authenticate = %q, want %q", c.method, c.path, got, "Bearer")
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
