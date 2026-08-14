package dsp

import (
	"bytes"
	"context"
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

// callbackHostnameLookupTimeout bounds validateCallbackURL's DNS resolution
// step. callbackHTTPClient's Timeout above does not cover this earlier
// step at all — it only starts once the POST itself begins — so without a
// separate bound, a callbackAddress hostname whose authoritative nameserver
// is slow or unresponsive could stall pushAndStore (called inline, not from
// a goroutine, by four of the unauthenticated dispatch paths) for as long
// as the resolver is willing to wait. A var, not a const, so a test can
// force an immediate deadline without depending on a real slow resolver.
var callbackHostnameLookupTimeout = 5 * time.Second

// callbackRetryBackoffs is how long pushCallback waits between attempts
// after a failed push, up to len(callbackRetryBackoffs)+1 attempts total.
// This is not a retry queue — nothing persists across a restart, and a
// still-failing push after the last backoff is dropped exactly as it always
// was.
//
// It exists because the real TCK's pipeline queues async-listener
// registration as a stage that runs only once the *previous* stage's
// synchronous call returns — sequential, on one thread, not parallel with
// this connector's near-instant push — confirmed from the TCK's own source
// (fetched per CLAUDE.md's "official TCK" allowed-input rule). What is NOT
// yet confirmed is that retrying for any bounded window actually closes
// that race: a real run extending this schedule to 10 attempts spanning
// ~54s still saw every async push (offer, agreement, termination) rejected
// 404 on every single attempt, for every CN test that needs one — see
// DECISIONS.md §23.7 for the open question this leaves and why the
// schedule was left here rather than removed outright. A var, not a
// const, so tests can shorten it.
var callbackRetryBackoffs = []time.Duration{300 * time.Millisecond, 700 * time.Millisecond, 1500 * time.Millisecond, 3 * time.Second}

// pushCallback sends v as a JSON POST to url, retrying on failure per
// callbackRetryBackoffs. Still best-effort overall: if every attempt fails,
// it is logged and never returned to the caller. The provider is
// authoritative over negotiation state in this protocol, so a dropped push
// does not corrupt anything a consumer cannot recover from
// GET /negotiations/{id}.
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
	for attempt := 0; ; attempt++ {
		if attemptPush(url, body, attempt) {
			return
		}
		if attempt >= len(callbackRetryBackoffs) {
			return
		}
		time.Sleep(callbackRetryBackoffs[attempt])
	}
}

// attemptPush makes one POST attempt and reports whether it succeeded.
func attemptPush(url string, body []byte, attempt int) bool {
	resp, err := callbackHTTPClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("push callback", "url", url, "attempt", attempt, "error", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Error("callback endpoint rejected push", "url", url, "attempt", attempt, "status", resp.StatusCode)
		return false
	}
	return true
}

// validateCallbackURL rejects a callback address that could turn this
// connector's own process into a request proxy against itself. POST
// /negotiations/request has no authentication in v1 (matching the catalog
// protocol's posture), so an unauthenticated caller fully controls the
// callbackAddress this function's result is used to gate — without it,
// "callbackAddress": "http://127.0.0.1:8081/health" would let anyone reach
// the management API, which binds to localhost specifically so a firewall
// mistake can't expose it (DECISIONS.md §12). pushCallback runs inside this
// same process, so that boundary means nothing to it unless this check
// stops it first.
//
// It does not reject RFC1918/ULA private-range addresses: DECISIONS.md
// §23.6 records why that check was tried and then deliberately narrowed —
// the deployment network a private address might reach is the operator's
// own, not this process's.
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
		ctx, cancel := context.WithTimeout(context.Background(), callbackHostnameLookupTimeout)
		defer cancel()
		addrs, err := (&net.Resolver{}).LookupIPAddr(ctx, host)
		if err != nil {
			return fmt.Errorf("resolve callback host %q: %w", host, err)
		}
		ips = make([]net.IP, len(addrs))
		for i, a := range addrs {
			ips[i] = a.IP
		}
	}
	for _, ip := range ips {
		if isDisallowedCallbackIP(ip) {
			return fmt.Errorf("callback host %q resolves to disallowed address %s", host, ip)
		}
	}
	return nil
}

// isDisallowedCallbackIP reports whether ip is loopback, link-local, or
// unspecified — addresses that only ever reach this connector's own host,
// regardless of what network it is deployed on. RFC1918/ULA private-range
// addresses are deliberately not included — see validateCallbackURL's doc
// comment and DECISIONS.md §23.6.
func isDisallowedCallbackIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
