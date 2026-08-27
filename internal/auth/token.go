// Package auth mints and verifies the credential connectors present to each
// other: a JWT signed EdDSA over Ed25519. Five minutes is what this connector
// mints (DECISIONS.md section 10); what Verify accepts is wider and is a
// separate number, because an issuer's clock is not this connector's — see
// clockLeeway and maxCredentialLifetime below.
//
// Written against the standard library rather than a JWT package, following
// CLAUDE.md's rule that the default answer to a dependency is the standard
// library, and the precedent DECISIONS.md section 22.5 set for JSON-LD — a
// fixed, small format checked directly rather than by a general processor.
// crypto/ed25519, encoding/base64, and encoding/json are the whole of what a
// JWT needs.
//
// Hand-written verification is where the sharp edges live, so Verify's order
// of operations is part of the design and is documented at the point it
// matters rather than only here.
package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Algorithm is the only algorithm this connector will sign or verify with.
// It is a constant compared against, never a value read out of a token to
// decide what to do — see Verify.
const Algorithm = "EdDSA"

// clockLeeway is how far behind this connector's clock an issuer's may run
// before its credentials start being refused. Applied to the expiry
// comparison only: there is no other time claim to apply it to.
//
// Sixty seconds, and the value is measured rather than chosen.
// TestVerifyRefusals mints at a fixed instant with a five-minute life and
// verifies six minutes later, so at sixty seconds that comparison lands
// exactly on the boundary and still refuses. At sixty-one it does not, and
// that case stops testing expiry at all. The constant and that fixture are
// welded: widening one means moving the other, deliberately.
//
// A constant rather than configuration. A configurable leeway is a policy
// nothing signs, so the most generous deployment would be the weak link.
const clockLeeway = 60 * time.Second

// maxCredentialLifetime is how far ahead of now a credential's expiry may
// sit. Without it the lifetime is whatever the issuer wrote, and a
// participant can mint itself a decade with its own key.
//
// Measured against now rather than against iat, which is the whole point: iat
// is the issuer's to choose, so the distance between two claims it signs
// bounds nothing. Against the verifier's own clock, no issuer offset buys
// lifetime.
//
// An hour rather than the five minutes DECISIONS.md section 10 sets for a
// minted credential, because the TCK harness mints with a longer life for a
// recorded reason — it mints before a cold image build. So this refuses an
// absurd lifetime rather than enforcing section 10, and DECISIONS.md section
// 37 is precise about how little that buys.
const maxCredentialLifetime = time.Hour

// Why a token was refused. The middleware logs these and never echoes them:
// telling an anonymous caller which of these its credential tripped is free
// reconnaissance.
//
// Every one of them is a fact about the credential. The middleware also
// refuses a caller whose credential it never reads, because this connector's
// own roster has expired. That is not an authentication failure and does not
// belong in this list: it reads nothing about the caller, so the middleware
// answers it with a 409 that does say why.
var (
	ErrMalformed       = errors.New("token is not three base64url segments")
	ErrBadAlgorithm    = errors.New("token header names an algorithm this connector does not accept")
	ErrUnknownIssuer   = errors.New("token issuer is not in the roster")
	ErrBadSignature    = errors.New("token signature does not verify")
	ErrExpired         = errors.New("token has expired")
	ErrWrongAudience   = errors.New("token is addressed to a different participant")
	ErrLifetimeTooLong = errors.New("token lifetime is longer than this connector accepts")
)

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// claims is the whole credential. No sub: issuer and subject are the same
// party, and a field that always duplicates another is one more thing to keep
// in step. No jti, and none is coming: DECISIONS.md section 28 found that a
// jti-based single-use check would reject the official TCK's own conformant
// behavior — it presents one token for an entire suite run — so replay
// defense for this credential shape needs a different mechanism than storage
// and a sweep, which is not a decision this package can make on its own.
type claims struct {
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// Mint returns a token from iss to aud, valid from now for ttl.
func Mint(priv ed25519.PrivateKey, iss, aud string, now time.Time, ttl time.Duration) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("mint token: private key is %d bytes, want %d", len(priv), ed25519.PrivateKeySize)
	}
	h, err := json.Marshal(header{Alg: Algorithm, Typ: "JWT"})
	if err != nil {
		return "", fmt.Errorf("mint token: marshal header: %w", err)
	}
	c, err := json.Marshal(claims{Iss: iss, Aud: aud, Iat: now.Unix(), Exp: now.Add(ttl).Unix()})
	if err != nil {
		return "", fmt.Errorf("mint token: marshal claims: %w", err)
	}
	signing := encode(h) + "." + encode(c)
	return signing + "." + encode(ed25519.Sign(priv, []byte(signing))), nil
}

// Verify checks a token and returns its issuer. keyFor supplies the public
// key trusted for an issuer — a function rather than a roster type so this
// package never imports one and can be tested without a file.
//
// The order below is the security design, not an implementation detail:
//
//  1. Structure first. Anything that is not three decodable segments is
//     refused before anything is parsed.
//  2. The algorithm is compared against the Algorithm constant. The header's
//     value never selects an algorithm or a key — reading it for that purpose
//     is the alg-confusion family of bugs, and the defense is to not do it.
//  3. iss is read *only* to look up a key. Nothing else in the payload is
//     trusted at this point, because nothing has been authenticated yet.
//  4. The signature is verified.
//  5. Only now are exp, the lifetime it puts on the token, and aud read and
//     checked. The instinct is to parse the payload once at the top and check
//     everything together; that would mean acting on unauthenticated claims.
func Verify(token string, keyFor func(string) (ed25519.PublicKey, bool), wantAud string, now time.Time) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrMalformed
	}
	rawHeader, err := decode(parts[0])
	if err != nil {
		return "", ErrMalformed
	}
	rawClaims, err := decode(parts[1])
	if err != nil {
		return "", ErrMalformed
	}
	sig, err := decode(parts[2])
	if err != nil {
		return "", ErrMalformed
	}

	var h header
	if err := json.Unmarshal(rawHeader, &h); err != nil {
		return "", ErrMalformed
	}
	if h.Alg != Algorithm {
		return "", ErrBadAlgorithm
	}

	var c claims
	if err := json.Unmarshal(rawClaims, &c); err != nil {
		return "", ErrMalformed
	}
	pub, ok := keyFor(c.Iss)
	if !ok {
		return "", ErrUnknownIssuer
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return "", ErrBadSignature
	}

	// Authenticated from here down.
	//
	// Expiry is checked first, and the order decides which refusal a token
	// earns rather than whether it earns one. At an exp of math.MinInt64 the
	// subtraction below wraps positive, so checking the maximum first would
	// call that token over-long where expiry calls it expired. Both refuse,
	// the middleware echoes neither, and no wrap of that kind reaches the
	// accept path: by the time the subtraction runs, expiry has put c.Exp
	// above now less the leeway.
	if now.Add(-clockLeeway).Unix() >= c.Exp {
		return "", ErrExpired
	}
	if c.Exp-now.Unix() > int64(maxCredentialLifetime/time.Second) {
		return "", ErrLifetimeTooLong
	}
	if c.Aud != wantAud {
		return "", ErrWrongAudience
	}
	return c.Iss, nil
}

func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func decode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
