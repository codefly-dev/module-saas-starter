// Package identity owns the witness signing-key trust boundary.
package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"errors"
)

const SigningKeySetting = "POLICY_LOG_SIGNING_KEY"

// ParseSigningKey accepts the persistent Ed25519 PKCS#8 key delivered as a
// standard-base64 secret value. It never reads a file or generates a key.
// Startup errors contain no supplied value or parser detail.
func ParseSigningKey(value string) (ed25519.PrivateKey, error) {
	if len(value) == 0 || len(value) > 4096 {
		return nil, errors.New("policy-log: a signing-key secret of at most 4096 bytes is required")
	}
	body, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || base64.StdEncoding.EncodeToString(body) != value {
		return nil, errors.New("policy-log: signing-key secret must be canonical standard base64")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(body)
	if err != nil {
		return nil, errors.New("policy-log: signing-key secret must contain exactly one PKCS#8 private key")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("policy-log: signing-key secret is not Ed25519")
	}
	canonical, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil || !bytes.Equal(canonical, body) {
		return nil, errors.New("policy-log: signing-key secret must contain exactly one canonical PKCS#8 key")
	}
	return key, nil
}
