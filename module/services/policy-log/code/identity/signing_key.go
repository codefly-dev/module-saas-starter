// Package identity owns the fixed witness signing-key trust boundary.
package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

const SigningKeyPath = "/etc/obin/policy-log/signing_key"

// LoadSigningKey has no path argument and no environment override. Delivery
// mounts a PKCS#8 PEM Ed25519 private key at the fixed path, mode 0400 or 0600,
// readable by the service uid. No key is generated at boot.
func LoadSigningKey() (ed25519.PrivateKey, error) {
	return loadSigningKey(func(path string) (fs.File, error) { return os.Open(path) })
}

func loadSigningKey(open func(string) (fs.File, error)) (ed25519.PrivateKey, error) {
	f, err := open(SigningKeyPath)
	if err != nil {
		return nil, fmt.Errorf("policy-log: signing key required: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return nil, errors.New("policy-log: signing key must be a private regular file of at most 4096 bytes")
	}
	body, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return nil, err
	}
	if len(body) > 4096 {
		return nil, errors.New("policy-log: signing key too large")
	}
	block, rest := pem.Decode(body)
	if block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 ||
		!bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace(pem.EncodeToMemory(block))) {
		return nil, errors.New("policy-log: signing key must be exactly one PKCS#8 private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("policy-log: parse signing key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("policy-log: signing key is not Ed25519")
	}
	return key, nil
}
