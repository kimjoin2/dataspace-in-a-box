package dsp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/auth"
)

// issuerContextKey carries the authenticated counterparty's participant
// identifier from the middleware to the handlers. Its own type, so nothing
// else in the process can collide with it.
type issuerContextKey struct{}

// issuerFrom returns the participant identifier of the counterparty that made
// this request, or "" when authentication is off. Handlers use it to record
// who they are talking to — the only honest source for that, since the row
// exists because this party made an authenticated request.
func issuerFrom(r *http.Request) string {
	iss, _ := r.Context().Value(issuerContextKey{}).(string)
	return iss
}

// rosterGuard answers whether the roster is still usable, and carries the
// warning that says so — once, however many surfaces refuse. A per-request
// warning is the log firehose cmd/dsbox/main.go's authentication-off comment
// exists to prevent, and its reason applies here unchanged.
//
// check is nil exactly when there is no roster to expire, which is what makes
// the zero guard usable: a connector with authentication off holds no roster,
// and absence must not read as expiry. That is the convention knownParticipant
// already uses, and the reason a zero auth.Roster is never handed to
// UsableAt.
//
// The predicate is a field and the answer is a method because they answer
// different questions. The field is what NewRouter returns on Routers, where
// nil still has to mean "there is no check"; the method is the reading every
// surface that refuses wants, in which absence is usable. A method value is
// never nil, so one cannot stand in for the other. The warning is
// a *sync.Once and not a value so that copying this struct — which every
// handler holding one does — does not copy a lock, which go vet reports and
// which would give each copy its own first time.
type rosterGuard struct {
	check func() bool
	warn  *sync.Once
}

// usable reports whether the roster may still be acted on. The zero guard
// reports true: there is no roster, so there is nothing to have expired.
func (g rosterGuard) usable() bool { return g.check == nil || g.check() }

// warnExpired says the roster has expired, the first time any surface
// refuses for that reason and not again. Reached only past usable reporting
// false, which the zero guard never does, so warn is set wherever this runs.
func (g rosterGuard) warnExpired() {
	g.warn.Do(func() {
		slog.Warn("the roster has expired; this connector refuses every counterparty and every initiate call until it is replaced")
	})
}

// refuseExpiredRoster writes the answer every HTTP surface gives once the
// roster has expired, so the status and the sentence have one home. It does
// not warn; its callers do, through the guard, which is where the
// once-per-connector rule lives and where a surface that writes no response
// can reach it.
//
// 409 and not 401. The caller's credential may be perfect and the fault is
// entirely local, so answering "your credential is required" sends their
// operator hunting across an organizational boundary, where they cannot read
// this connector's log.
//
// 409 and not 503 either. This repository's own wire contract records that a
// 5xx raises immediately on the TCK's negative paths, exactly like the 404
// DECISIONS.md section 25.1 forbids, so a 503 would sit outside that rule
// rather than amend it.
func refuseExpiredRoster(w http.ResponseWriter, errType string) {
	writeError(w, errType, http.StatusConflict,
		"this connector's roster has expired and it cannot act until the roster is replaced")
}

// requireParticipant refuses any request that does not carry a valid
// credential from a roster participant addressed to this connector.
//
// 401, not the 400 the rest of this protocol uses for a structural rejection.
// That rule exists because the TCK's assertion helper throws on a 404; it
// says nothing about 401, and a missing credential is exactly what 401 means.
// A 401 in a TCK run therefore means the harness lost its credential, which
// should look different from a protocol error.
//
// An expired roster is refused here too, with a 409 that says so. That
// refusal is not an authentication failure and reads nothing about the
// caller — see refuseExpiredRoster above.
func requireParticipant(roster auth.Roster, self string, guard rosterGuard, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Before the credential is read. The answer does not depend on it,
		// and verifying a credential against a roster this connector has
		// declared unusable is work that cannot mean anything. It also keeps
		// the refusal honest: the connector reports its own roster rather
		// than making any claim about the caller.
		//
		// TransferErrorType, matching refuse below: this middleware wraps
		// every protocol's routes and cannot tell which one the caller was
		// speaking, and the document is otherwise identical whichever name
		// it carries.
		if !guard.usable() {
			guard.warnExpired()
			refuseExpiredRoster(w, TransferErrorType)
			return
		}
		presented, ok := cutBearer(r.Header.Get("Authorization"))
		if !ok {
			refuse(w, r, "no bearer credential presented")
			return
		}
		iss, err := auth.Verify(presented, roster.KeyFor, self, time.Now())
		if err != nil {
			// The reason goes to the log and never to the caller. Telling an
			// anonymous prober which way its credential was wrong is free
			// reconnaissance; telling the operator is the whole point of
			// having sentinels. The expiry refusal above is the one rejection
			// on this path that does say why, because it reads nothing about
			// the caller to disclose.
			refuse(w, r, err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), issuerContextKey{}, iss)))
	})
}

func refuse(w http.ResponseWriter, r *http.Request, reason string) {
	slog.Warn("refuse unauthenticated DSP request",
		"path", r.URL.Path, "remote_addr", r.RemoteAddr, "reason", reason)
	// RFC 9110 section 15.5.2 makes a challenge a MUST on a 401. No realm:
	// this listener has exactly one protection space, so naming it would add
	// a string an operator has to keep in step with nothing.
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, TransferErrorType, http.StatusUnauthorized,
		"a valid participant credential is required")
}

// cutBearer strips the scheme case-insensitively, as RFC 9110 section 11.1
// requires — mirroring internal/mgmt's own helper rather than sharing it,
// because the two listeners accept different credentials and a shared helper
// would invite sharing the check as well.
func cutBearer(header string) (string, bool) {
	const scheme = "bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	token := strings.TrimSpace(header[len(scheme):])
	return token, token != ""
}

// refuseIfNotParty writes a 403 and reports true when the authenticated
// caller is not the participant this row is with. Callers return immediately
// on true.
//
// stored is the participant this row's exchange is with. Provider-role rows
// take it from the verified issuer of the request that created them.
// Consumer-role rows take it from the providerId of an initiate call — which
// is an authorization anchor because only the operator can make an initiate
// call, and it may only name a participant the roster lists, so the stored
// counterparty is a name this connector can verify a message from and no
// counterparty chose it. Before those checks existed, providerId was a
// string any caller could choose, which is why DECISIONS.md section 32.3
// recorded the consumer role's resolvers as deliberately unguarded.
//
// Every control-plane resolver that reaches a row of either role carries
// this call — handleData resolves a transfer row too, but keeps its own
// comparison deliberately unshared (see its own doc comment for why). That
// is load-bearing rather than tidy: a comment saying a consumer counterparty
// is an authorization anchor, next to a resolver that does not compare
// against it, would be worse than the documented asymmetry it replaced.
//
// 403, not 404: DECISIONS.md section 25.1 makes every DSP rejection
// [400, 500) and never 404, because the counterparty's client checks for 404
// before it checks whether an error was expected and aborts the whole
// exchange on one. This is the same answer, and the same register, the data
// endpoint already gives — its own string is "this transfer is not yours".
//
// No empty-stored clause, matching handleData: a row with no counterparty
// predates authentication and is served to nobody. The agreement check in
// handleTransferRequest deliberately differs — see the design spec's section
// 4.2.
func refuseIfNotParty(w http.ResponseWriter, r *http.Request, errType, stored string, authRequired bool) bool {
	if issuer := issuerFrom(r); authRequired && issuer != stored {
		slog.Warn("refuse a message about an exchange the sender is not party to",
			"issuer", issuer, "expected", stored, "path", r.URL.Path)
		writeError(w, errType, http.StatusForbidden, "this exchange is not yours")
		return true
	}
	return false
}
