package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

	pushCallback(srv.URL, map[string]string{"hello": "world"})

	select {
	case body := <-received:
		if body["hello"] != "world" {
			t.Errorf("received %v, want {hello: world}", body)
		}
	default:
		t.Fatal("pushCallback did not send a request before returning")
	}
}

func TestPushCallbackToUnreachableURLDoesNotPanic(t *testing.T) {
	pushCallback("http://127.0.0.1:1/unreachable", map[string]string{"hello": "world"})
}

// TestValidateCallbackURL is a direct, unfiltered-network table test of the
// SSRF guard: an unauthenticated POST /negotiations/request fully controls
// callbackAddress, and this function is what stops it naming this
// connector's own loopback or private network. IP literals are used
// (instead of a real public hostname) so the test needs no DNS resolution
// and cannot flake on network access.
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
		{"RFC1918 10/8", "http://10.0.0.1/callback", true},
		{"RFC1918 172.16/12", "http://172.16.0.1/callback", true},
		{"RFC1918 192.168/16", "http://192.168.1.1/callback", true},
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
