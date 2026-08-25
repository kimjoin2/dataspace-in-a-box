package dsp

import (
	"crypto/ed25519"
	"encoding/json"
	"github.com/kimjoin2/dataspace-in-a-box/internal/auth"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain shortens callbackRetryBackoffs once, for the whole package, before
// any test runs — what that var's own doc comment says tests may do, but done
// here rather than per-test. The real schedule would spend 5.5s sleeping in
// every test that exhausts it, and what those tests are about is the outcome,
// not the length of the waits.
//
// Once, and never restored, is the load-bearing part. Every push runs on a
// fire-and-forget goroutine (DECISIONS.md §23.8) with no handle to join, so a
// test that assigned this var and restored it in a defer would be writing it
// while a push it started — or one a previous test started — is still reading
// it inside pushCallback's retry loop. That is a data race `go test -race`
// reports, and it is not fixable by waiting: the loop's last read of this var
// happens after the last HTTP response the test could possibly observe. This
// single write, on the main goroutine before m.Run, has nothing to race
// against.
//
// transferStepDelay is shortened here for exactly the same reasons. It is
// read inside driveTransfer's loop, on a goroutine with no handle to join, so
// a per-test assign-and-restore would race a sequence a previous test
// started; and the sequence tests would otherwise pay 200ms per step for a
// pause whose purpose is on the wire, not in the assertion.
func TestMain(m *testing.M) {
	callbackRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond}
	transferStepDelay = time.Millisecond
	os.Exit(m.Run())
}

func TestPushCallbackSendsJSON(t *testing.T) {
	received := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode pushed body: %v", err)
		}
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pushCallback(srv.URL, map[string]string{"hello": "world"}, testAudience)

	select {
	case body := <-received:
		if body["hello"] != "world" {
			t.Errorf("received %v, want {hello: world}", body)
		}
	default:
		t.Fatal("pushCallback did not send a request before returning")
	}
}

// TestPushCallbackToUnreachableURLDoesNotPanic exercises the path where every
// attempt fails. It runs against TestMain's shortened backoff schedule, so
// exhausting the retries costs milliseconds; what is under test is that it
// returns quietly, not how long the waits are.
func TestPushCallbackToUnreachableURLDoesNotPanic(t *testing.T) {
	pushCallback("http://127.0.0.1:1/unreachable", map[string]string{"hello": "world"}, testAudience)
}

// TestValidateCallbackURL is a direct, unfiltered-network table test of the
// SSRF guard: an unauthenticated POST /negotiations/request fully controls
// callbackAddress, and this function is what stops it naming this
// connector's own loopback network. IP literals are used (instead of a real
// public hostname) so the test needs no DNS resolution and cannot flake on
// network access. RFC1918/ULA private ranges are deliberately not
// rejected — see validateCallbackURL's doc comment and DECISIONS.md §23.6.
func TestValidateCallbackURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"public IP literal", "http://8.8.8.8/callback", false},
		{"public IP literal, https", "https://93.184.215.14/callback", false},
		{"loopback IPv4", "http://127.0.0.1/callback", true},
		{"loopback IPv4 with port", "http://127.0.0.1:8081/health", true},
		{"loopback IPv6", "http://[::1]/callback", true},
		{"RFC1918 10/8", "http://10.0.0.1/callback", false},
		{"RFC1918 172.16/12", "http://172.16.0.1/callback", false},
		{"RFC1918 192.168/16", "http://192.168.1.1/callback", false},
		{"link-local", "http://169.254.169.254/callback", true},
		{"unspecified", "http://0.0.0.0/callback", true},
		{"non-http scheme", "ftp://8.8.8.8/callback", true},
		{"malformed url", "http://%zz/callback", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCallbackURL(tt.url)
			if tt.wantErr && err == nil {
				t.Errorf("validateCallbackURL(%q) = nil, want an error", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateCallbackURL(%q) = %v, want nil", tt.url, err)
			}
		})
	}
}

// TestValidateCallbackURLRespectsLookupTimeout proves callbackHostnameLookupTimeout
// actually bounds the DNS resolution step, without depending on a real slow
// or unresponsive nameserver: forcing the timeout to an already-elapsed
// duration makes net.Resolver.LookupIPAddr's context deadline exceeded
// before (or immediately as) the lookup starts, so a hostname (not an IP
// literal, which skips resolution entirely) must return promptly regardless
// of what a real query for it would have done. If this test ever needs
// longer than a couple of seconds to reach that failure, the timeout is not
// actually wired into the lookup call.
func TestValidateCallbackURLRespectsLookupTimeout(t *testing.T) {
	orig := callbackHostnameLookupTimeout
	callbackHostnameLookupTimeout = time.Nanosecond
	defer func() { callbackHostnameLookupTimeout = orig }()

	start := time.Now()
	err := validateCallbackURL("http://callback-timeout-test.invalid/callback")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("validateCallbackURL with an already-expired lookup timeout = nil error, want an error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("validateCallbackURL took %s to return with a %s lookup timeout, want it bounded by the timeout",
			elapsed, callbackHostnameLookupTimeout)
	}
}

func TestPushCallbackReturnsTrueOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !pushCallback(srv.URL, map[string]string{"hello": "world"}, testAudience) {
		t.Error("pushCallback = false, want true for a server that accepts the push")
	}
}

func TestPushCallbackReturnsFalseAfterExhaustingRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if pushCallback(srv.URL, map[string]string{"hello": "world"}, testAudience) {
		t.Error("pushCallback = true, want false once every attempt is rejected")
	}
}

// testAudience is the counterparty these tests pretend to address. The
// default minter is a no-op, so it only matters where a test arms a real one.
const testAudience = "urn:participant:peer"

// Every outbound call carries a credential, and it says who it is for. The
// TCK cannot catch a mistake here — its mock endpoints accept whatever
// arrives without inspecting the header — so this is the only evidence that
// the minting side works at all.
func TestOutboundPushCarriesAMintedCredential(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	restore := mintOutboundCredential
	mintOutboundCredential = func(aud string) (string, bool) {
		tok, err := auth.Mint(priv, "urn:participant:self", aud, time.Now(), time.Minute)
		if err != nil {
			t.Errorf("Mint: %v", err)
			return "", true
		}
		return "Bearer " + tok, true
	}
	defer func() { mintOutboundCredential = restore }()

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !pushCallback(srv.URL, map[string]string{"@type": "X"}, testAudience) {
		t.Fatal("push failed")
	}

	raw, ok := strings.CutPrefix(got, "Bearer ")
	if !ok {
		t.Fatalf("Authorization = %q, want a Bearer credential", got)
	}
	keys := func(id string) (ed25519.PublicKey, bool) {
		return pub, id == "urn:participant:self"
	}
	iss, err := auth.Verify(raw, keys, testAudience, time.Now())
	if err != nil {
		t.Fatalf("the credential this connector sent does not verify: %v", err)
	}
	if iss != "urn:participant:self" {
		t.Errorf("iss = %q", iss)
	}
}

// With authentication off nothing is attached, and the counterparty sees the
// same anonymous request it saw before this milestone.
func TestOutboundPushIsUnsignedWhenAuthenticationIsOff(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !pushCallback(srv.URL, map[string]string{"@type": "X"}, testAudience) {
		t.Fatal("push failed")
	}
	if got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}
