// Package auth mints and verifies the credential connectors present to each
// other: a JWT signed EdDSA over Ed25519, valid for five minutes
// (DECISIONS.md section 10).
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

// Why a token was refused. The middleware logs these and never echoes them:
// telling an anonymous caller which of the six ways its credential was wrong
// is free reconnaissance.
var (
	ErrMalformed     = errors.New("token is not three base64url segments")
	ErrBadAlgorithm  = errors.New("token header names an algorithm this connector does not accept")
	ErrUnknownIssuer = errors.New("token issuer is not in the roster")
	ErrBadSignature  = errors.New("token signature does not verify")
	ErrExpired       = errors.New("token has expired")
	ErrWrongAudience = errors.New("token is addressed to a different participant")
)

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// claims is the whole credential. No sub: issuer and subject are the same
// party, and a field that always duplicates another is one more thing to keep
// in step. No jti: it would only earn its place alongside replay detection,
// which needs storage and a sweep and is not in this milestone — see the
// design spec's accepted trade-offs.
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
//  5. Only now are exp and aud read and checked. The instinct is to parse the
//     payload once at the top and check everything together; that would mean
//     acting on unauthenticated claims.
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
	if now.Unix() >= c.Exp {
		return "", ErrExpired
	}
	if c.Aud != wantAud {
		return "", ErrWrongAudience
	}
	return c.Iss, nil
}

func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func decode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
