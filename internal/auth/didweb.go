// This file resolves a did:web identifier to an Ed25519 public key. It is
// an operator tool, invoked by `dsops resolve` — never called from the
// authentication path. The roster already maps identifier to key; resolving
// on every request would add a network dependency to authentication and
// change nothing about who ends up trusted (see the design spec's "Scope").
package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// didWebTimeout bounds a resolution — an operator running a one-shot
// command, not a request in a hot path, so a slow or hung server should
// time out and say so rather than leave the command hanging.
const didWebTimeout = 10 * time.Second

const didWebPrefix = "did:web:"

// didWebURL turns a did:web identifier into the HTTPS URL its DID document
// lives at, per the did:web method spec. No path segments after the domain:
// https://<domain>/.well-known/did.json. With path segments: each is
// percent-decoded and joined, and the document lives directly under them —
// no .well-known: https://<domain>/<segments.../>did.json. %3A immediately
// after the domain decodes to a port, exactly the form the method spec
// documents for resolving something running on localhost.
//
// allowHTTP builds an http:// URL instead. This package takes no dependency
// on internal/config, so it is a plain bool rather than config.DevMode
// itself — dsops resolve -allow-http is the caller that sets it, for local
// demos and tests with no TLS to terminate, the same reasoning
// config.DevMode already applies to public_url's https requirement
// (DECISIONS.md section 13). Production resolution stays HTTPS-only.
func didWebURL(id string, allowHTTP bool) (string, error) {
	if !strings.HasPrefix(id, didWebPrefix) {
		return "", fmt.Errorf("%q is not a did:web identifier", id)
	}
	rest := id[len(didWebPrefix):]
	if rest == "" {
		return "", fmt.Errorf("%q names no domain", id)
	}
	parts := strings.Split(rest, ":")
	domain, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", fmt.Errorf("%q: domain %q: %w", id, parts[0], err)
	}
	if domain == "" {
		return "", fmt.Errorf("%q names no domain", id)
	}
	scheme := "https"
	if allowHTTP {
		scheme = "http"
	}
	if len(parts) == 1 {
		return scheme + "://" + domain + "/.well-known/did.json", nil
	}
	segments := make([]string, 0, len(parts)-1)
	for _, seg := range parts[1:] {
		decoded, err := url.PathUnescape(seg)
		if err != nil {
			return "", fmt.Errorf("%q: path segment %q: %w", id, seg, err)
		}
		if decoded == "" {
			return "", fmt.Errorf("%q: empty path segment", id)
		}
		segments = append(segments, decoded)
	}
	return scheme + "://" + domain + "/" + strings.Join(segments, "/") + "/did.json", nil
}

// didDocument is the subset of a DID document this connector reads: which
// verification methods it declares. Everything else — @context, id,
// authentication, assertionMethod — is present on a real document and
// ignored here, the same "only the fields this connector inspects" approach
// DECISIONS.md section 22.5 established for DSP messages.
type didDocument struct {
	VerificationMethod []verificationMethod `json:"verificationMethod"`
}

type verificationMethod struct {
	// PublicKeyJWK is the one key representation this connector reads. A
	// verification method carrying only publicKeyMultibase is a resolution
	// failure with that reason, not a silent skip to the next entry's
	// fallback — see the design spec's "Key representation".
	PublicKeyJWK *jsonWebKey `json:"publicKeyJwk"`
}

// jsonWebKey is RFC 7517's JWK, narrowed to RFC 8037's OKP/Ed25519 case: the
// one shape ResolveDIDWeb accepts.
type jsonWebKey struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
}

// resolveAt fetches the DID document at docURL and returns the first
// Ed25519 verification key it finds. Split from ResolveDIDWeb so the
// fetch-and-parse step can be tested against an httptest.Server without
// needing a real did:web identifier to reach it.
func resolveAt(ctx context.Context, docURL string, client *http.Client) (ed25519.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %q: %w", docURL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %q: %w", docURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %q: got status %d", docURL, resp.StatusCode)
	}
	var doc didDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse DID document at %q: %w", docURL, err)
	}
	for _, vm := range doc.VerificationMethod {
		if vm.PublicKeyJWK == nil {
			continue
		}
		if vm.PublicKeyJWK.Kty != "OKP" || vm.PublicKeyJWK.Crv != "Ed25519" {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(vm.PublicKeyJWK.X)
		if err != nil {
			return nil, fmt.Errorf("DID document at %q: publicKeyJwk.x is not base64url: %w", docURL, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("DID document at %q: publicKeyJwk.x is %d bytes, want %d",
				docURL, len(raw), ed25519.PublicKeySize)
		}
		return ed25519.PublicKey(raw), nil
	}
	return nil, fmt.Errorf("DID document at %q carries no Ed25519 publicKeyJwk verification method", docURL)
}

// ResolveDIDWeb resolves id to the Ed25519 public key its DID document
// publishes. allowHTTP is didWebURL's relaxation; see its doc comment.
func ResolveDIDWeb(id string, allowHTTP bool) (ed25519.PublicKey, error) {
	docURL, err := didWebURL(id, allowHTTP)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), didWebTimeout)
	defer cancel()
	return resolveAt(ctx, docURL, http.DefaultClient)
}
