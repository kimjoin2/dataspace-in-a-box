package dsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"
)

// callbackHTTPClient is used for every callback push. A finite timeout keeps
// a consumer that accepts the TCP connection and never responds from
// blocking the caller indefinitely — three of the four dispatch paths in
// negotiation_handler.go call pushCallback inline, not from a goroutine, so
// an unbounded call would stall the HTTP handler goroutine that made it.
// Redirects are disabled: a DSP callback endpoint has no legitimate reason
// to redirect, and following one would let a URL that passed
// validateCallbackURL hop to an address that check never saw.
var callbackHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// pushCallback sends v as a JSON POST to url, best-effort: a failure is
// logged and never returned to the caller. The provider is authoritative
// over negotiation state in this protocol, so a dropped push does not
// corrupt anything a consumer cannot recover from GET /negotiations/{id};
// no retry queue is built for v1.
//
// pushCallback itself does not filter url — callers must run it through
// validateCallbackURL first (negotiation_handler.go's pushAndStore does).
// Keeping the filter out of this function is what lets
// TestPushCallbackSendsJSON exercise a real POST against an
// httptest.Server, which is itself bound to loopback.
func pushCallback(url string, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal callback push", "url", url, "error", err)
		return
	}
	resp, err := callbackHTTPClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("push callback", "url", url, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Error("callback endpoint rejected push", "url", url, "status", resp.StatusCode)
	}
}

// validateCallbackURL rejects a callback address that could turn this
// connector's own process into a request proxy against itself or its
// private network. POST /negotiations/request has no authentication in v1
// (matching the catalog protocol's posture), so an unauthenticated caller
// fully controls the callbackAddress this function's result is used to
// gate — without it, "callbackAddress": "http://127.0.0.1:8081/health"
// would let anyone reach the management API, which binds to localhost
// specifically so a firewall mistake can't expose it (DECISIONS.md §12).
// pushCallback runs inside this same process, so that boundary means
// nothing to it unless this check stops it first.
//
// This resolves the host once, at validation time, and does not re-check
// the address actually dialed — it does not defend against DNS rebinding
// between this call and pushCallback's request. Closing that gap needs a
// custom dialer that validates the address it is about to connect to,
// which is more machinery than this milestone covers; the fixed set of
// disallowed ranges below is the sized-for-v1 defense.
func validateCallbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse callback url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("callback url scheme must be http or https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("callback url has no host")
	}

	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = net.LookupIP(host)
		if err != nil {
			return fmt.Errorf("resolve callback host %q: %w", host, err)
		}
	}
	for _, ip := range ips {
		if isDisallowedCallbackIP(ip) {
			return fmt.Errorf("callback host %q resolves to disallowed address %s", host, ip)
		}
	}
	return nil
}

// isDisallowedCallbackIP reports whether ip is loopback, an RFC1918/ULA
// private range, link-local, or unspecified — none of which a DSP
// consumer's public callback endpoint should ever resolve to.
func isDisallowedCallbackIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
