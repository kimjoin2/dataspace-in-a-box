package auth

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// pemKeyType is the PEM block type for a PKCS#8 private key, which is the one
// encoding this connector writes and reads. Ed25519 has no legacy alternative
// to support.
const pemKeyType = "PRIVATE KEY"

// GenerateKeyFile writes a new Ed25519 private key to path in PKCS#8 PEM and
// returns the public half, which is what goes in a counterparty's roster.
//
// It refuses to overwrite an existing file. Replacing a key locks out every
// counterparty still holding the old public half, and that is not a thing to
// do as a side effect of re-running a command.
func GenerateKeyFile(path string) (ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("encode key: %w", err)
	}

	// O_EXCL is the refusal: it fails rather than truncating.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create key file %q: %w", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: pemKeyType, Bytes: der}); err != nil {
		return nil, fmt.Errorf("write key file %q: %w", path, err)
	}
	return pub, nil
}

// LoadPrivateKey reads an Ed25519 private key from a PKCS#8 PEM file. A key
// of any other type is an error rather than something to coerce: this
// connector signs EdDSA and nothing else, so an RSA key here is a
// configuration mistake that should surface at startup.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file %q: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("key file %q is not PEM", path)
	}
	if block.Type != pemKeyType {
		return nil, fmt.Errorf("key file %q holds a %q block, want %q", path, block.Type, pemKeyType)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse key file %q: %w", path, err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key file %q holds a %T, want an Ed25519 key", path, parsed)
	}
	return priv, nil
}
