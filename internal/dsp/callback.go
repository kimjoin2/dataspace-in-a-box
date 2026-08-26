package dsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"
)

// callbackHTTPClient is used for every callback push. A finite timeout keeps
// a consumer that accepts the TCP connection and never responds from
// blocking the caller indefinitely. Pushes run in their own goroutines
// (DECISIONS.md section 23.8), so nothing waits on this — but a goroutine
// that never returns is never collected either, and one per push against an
// unauthenticated endpoint is a leak with a public trigger. The timeout is
// what bounds that lifetime; multiplied by the retry schedule below, it is
// also what bounds the whole push. Redirects are disabled: a DSP callback
// endpoint has no legitimate reason to redirect, and following one would let
// a URL that passed validateCallbackURL hop to an address that check never
// saw.
var callbackHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// callbackHostnameLookupTimeout bounds validateCallbackURL's DNS resolution
// step. callbackHTTPClient's Timeout above does not cover this earlier step
// at all — it only starts once the POST itself begins — so without a
// separate bound, a callbackAddress hostname whose authoritative nameserver
// is slow or unresponsive would hold a push goroutine open for as long as
// the resolver is willing to wait, on a hostname an unauthenticated caller
// chose. A var, not a const, so a test can force an immediate deadline
// without depending on a real slow resolver.
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
// (fetched per CLAUDE.md's "official TCK" allowed-input rule). A real
// CN:02-06 run needed 2 of these 5 attempts for a push that raced that
// window, which is the evidence the schedule is sized against.
//
// An intermediate run once widened this to 10 attempts over ~54s and still
// saw every async push rejected 404 every time, which looked like proof the
// retry idea was useless. It was not: that failure had a different,
// structural cause — pushes were running inline in the handler goroutine, so
// the synchronous response the TCK needed before it could register a
// listener was itself stuck behind the push (DECISIONS.md §23.8). With that
// fixed, the race shrank back to the occasional single retry this schedule
// was originally sized for. What remains genuinely unproven is only the
// margin: whether 5 attempts is enough for network conditions other than
// this project's own (§23.7). A var, not a const, so tests can shorten it —
// once, in TestMain, and never restored. Pushes run on goroutines that
// outlive the test that started them and read this inside the retry loop, so
// a per-test assign-and-restore is a data race `go test -race` reports; see
// callback_test.go's TestMain.
var callbackRetryBackoffs = []time.Duration{300 * time.Millisecond, 700 * time.Millisecond, 1500 * time.Millisecond, 3 * time.Second}

// pushCallback sends v as a JSON POST to url, retrying on failure per
// callbackRetryBackoffs, and reports whether it ultimately succeeded. Most
// callers discard the return value: the provider role's own pushes, and
// most of this connector's consumer-role reactions, write their state
// unconditionally once the push is dispatched (DECISIONS.md section 23.12).
// The one exception is the consumer role's verify-on-agreement reaction,
// which must not advance to VERIFIED unless this returns true — see the
// design spec's "03-06 verification-ack rule".
//
// pushCallback itself does not filter url — callers must run it through
// validateCallbackURL first (negotiation_handler.go's pushAndStore does).
func pushCallback(url string, v any, aud string) bool {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal callback push", "url", url, "error", err)
		return false
	}
	for attempt := 0; ; attempt++ {
		// Minted per attempt, not once per call: a retry schedule that runs
		// past the credential's five-minute life — and past the leeway
		// internal/auth allows on top of it — would otherwise present an
		// expired token on its last try, which is the hardest kind of
		// intermittent failure to read from a log. Minting per attempt is
		// also what makes an expiry landing mid-schedule observable: it is
		// seen on the next attempt, which abandons the schedule through the
		// same exit an exhausted one takes.
		authorization, maySend := mintOutboundCredential(aud)
		if !maySend {
			return false
		}
		if attemptPush(url, body, attempt, authorization) {
			return true
		}
		if attempt >= len(callbackRetryBackoffs) {
			return false
		}
		time.Sleep(callbackRetryBackoffs[attempt])
	}
}

// attemptPush makes one POST attempt and reports whether it succeeded.
func attemptPush(url string, body []byte, attempt int, authorization string) bool {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.Error("build callback push", "url", url, "attempt", attempt, "error", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := callbackHTTPClient.Do(req)
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

// mintOutboundCredential returns the Authorization header value for a
// message to aud, and whether the message may be sent at all.
//
// The second return is not the first being empty. An empty audience and a
// minting error both proceed without a credential, exactly as they did
// before this; only an expired roster stops a send. Merging the two would
// silently change what happens when a counterparty has no address.
//
// The package default permits. A connector with authentication off never
// reaches the assignment in NewRouter, and a refusing default would leave it
// sending nothing.
//
// A package variable, set by NewRouter, following the shape
// validateOutgoingCallback already established here. The trade-off is the
// same one: two routers in one process would share it. Nothing in this
// connector runs two, and the alternative — threading a credential through
// every client function and every handler that calls one — buys nothing
// until something does.
var mintOutboundCredential = defaultMintOutboundCredential

// defaultMintOutboundCredential attaches nothing and permits the send: with
// authentication off there is no key to sign with and no roster to expire,
// so the counterparty sees the same anonymous request it saw before
// outbound credentials existed.
//
// Named rather than written as a literal at the assignment above, so a test
// can install it back over whatever a router left behind and assert what it
// answers.
func defaultMintOutboundCredential(string) (string, bool) { return "", true }

// errRosterExpired is what an outbound call reports to its caller once the
// minter refuses. The sentence names this connector's own roster because
// the counterparty is fine and the fault is entirely local — the same thing
// refuseExpiredRoster tells an inbound caller.
var errRosterExpired = errors.New("this connector's roster has expired")
