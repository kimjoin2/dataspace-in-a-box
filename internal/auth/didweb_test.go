package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDIDWebURL(t *testing.T) {
	cases := []struct {
		name      string
		id        string
		allowHTTP bool
		want      string
		wantErr   bool
	}{
		{"bare domain", "did:web:example.com", false, "https://example.com/.well-known/did.json", false},
		{"a port", "did:web:example.com%3A3000", false, "https://example.com:3000/.well-known/did.json", false},
		{"path segments", "did:web:example.com:user:alice", false, "https://example.com/user/alice/did.json", false},
		{"a port and dev_mode", "did:web:localhost%3A8080", true, "http://localhost:8080/.well-known/did.json", false},
		{"not did:web at all", "did:key:z6Mk...", false, "", true},
		{"no method prefix", "example.com", false, "", true},
		{"empty domain", "did:web:", false, "", true},
		{"empty path segment", "did:web:example.com::alice", false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := didWebURL(c.id, c.allowHTTP)
			if c.wantErr {
				if err == nil {
					t.Errorf("didWebURL(%q) = %q, want an error", c.id, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("didWebURL(%q): %v", c.id, err)
			}
			if got != c.want {
				t.Errorf("didWebURL(%q) = %q, want %q", c.id, got, c.want)
			}
		})
	}
}

// didDocumentJSON builds a minimal did:web document carrying one
// verificationMethod, in the publicKeyJwk shape resolveAt reads.
func didDocumentJSON(pub ed25519.PublicKey) string {
	return `{"verificationMethod":[{"id":"did:web:example.com#key-1",` +
		`"type":"JsonWebKey2020","publicKeyJwk":{"kty":"OKP","crv":"Ed25519","x":"` +
		base64.RawURLEncoding.EncodeToString(pub) + `"}}]}`
}

func TestResolveAt(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	t.Run("a document with a matching Ed25519 JWK", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(didDocumentJSON(pub)))
		}))
		defer srv.Close()

		got, err := resolveAt(context.Background(), srv.URL, srv.Client())
		if err != nil {
			t.Fatalf("resolveAt: %v", err)
		}
		if !got.Equal(pub) {
			t.Errorf("resolveAt = %v, want %v", got, pub)
		}
	})

	t.Run("a document with no verification method at all", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"verificationMethod":[]}`))
		}))
		defer srv.Close()
		if _, err := resolveAt(context.Background(), srv.URL, srv.Client()); err == nil {
			t.Error("resolveAt: loaded a document with no verification method")
		}
	})

	t.Run("a document carrying only publicKeyMultibase", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"verificationMethod":[{"id":"did:web:example.com#key-1",` +
				`"type":"Ed25519VerificationKey2020","publicKeyMultibase":"z6Mk..."}]}`))
		}))
		defer srv.Close()
		if _, err := resolveAt(context.Background(), srv.URL, srv.Client()); err == nil {
			t.Error("resolveAt: loaded a document with only publicKeyMultibase")
		}
	})

	t.Run("a JWK that is not OKP/Ed25519", func(t *testing.T) {
		// x is otherwise the right length: the failure this proves is the
		// kty/crv mismatch, not an incidental size mismatch downstream.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"verificationMethod":[{"publicKeyJwk":{"kty":"EC","crv":"P-256","x":"` +
				base64.RawURLEncoding.EncodeToString(pub) + `"}}]}`))
		}))
		defer srv.Close()
		if _, err := resolveAt(context.Background(), srv.URL, srv.Client()); err == nil {
			t.Error("resolveAt: loaded a non-Ed25519 JWK")
		}
	})

	t.Run("x is not base64url", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"verificationMethod":[{"publicKeyJwk":{"kty":"OKP","crv":"Ed25519","x":"!!!!"}}]}`))
		}))
		defer srv.Close()
		if _, err := resolveAt(context.Background(), srv.URL, srv.Client()); err == nil {
			t.Error("resolveAt: loaded an x that is not base64url")
		}
	})

	t.Run("x is the wrong size", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"verificationMethod":[{"publicKeyJwk":{"kty":"OKP","crv":"Ed25519","x":"AAAA"}}]}`))
		}))
		defer srv.Close()
		if _, err := resolveAt(context.Background(), srv.URL, srv.Client()); err == nil {
			t.Error("resolveAt: loaded an x of the wrong size")
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{`))
		}))
		defer srv.Close()
		if _, err := resolveAt(context.Background(), srv.URL, srv.Client()); err == nil {
			t.Error("resolveAt: loaded malformed JSON")
		}
	})

	t.Run("a non-200 status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		if _, err := resolveAt(context.Background(), srv.URL, srv.Client()); err == nil {
			t.Error("resolveAt: loaded despite a 404")
		}
	})

	t.Run("the first matching entry wins over a later non-matching one", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"verificationMethod":[` +
				`{"publicKeyJwk":{"kty":"OKP","crv":"Ed25519","x":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}},` +
				`{"publicKeyMultibase":"z6Mk..."}` +
				`]}`))
		}))
		defer srv.Close()
		got, err := resolveAt(context.Background(), srv.URL, srv.Client())
		if err != nil {
			t.Fatalf("resolveAt: %v", err)
		}
		if !got.Equal(pub) {
			t.Errorf("resolveAt = %v, want %v", got, pub)
		}
	})
}
