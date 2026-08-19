package dsp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
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

// requireParticipant refuses any request that does not carry a valid
// credential from a roster participant addressed to this connector.
//
// 401, not the 400 the rest of this protocol uses for a structural rejection.
// That rule exists because the TCK's assertion helper throws on a 404; it
// says nothing about 401, and a missing credential is exactly what 401 means.
// A 401 in a TCK run therefore means the harness lost its credential, which
// should look different from a protocol error.
func requireParticipant(roster auth.Roster, self string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := cutBearer(r.Header.Get("Authorization"))
		if !ok {
			refuse(w, r, "no bearer credential presented")
			return
		}
		iss, err := auth.Verify(presented, roster.KeyFor, self, time.Now())
		if err != nil {
			// The reason goes to the log and never to the caller. Telling an
			// anonymous prober which of the six ways its credential was wrong
			// is free reconnaissance; telling the operator is the whole point
			// of having sentinels.
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
