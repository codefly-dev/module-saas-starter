// Package deliverytest supplies real, reproducible signatures for host tests.
// These public fixture keys must never be used by a deployed trust anchor.
package deliverytest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"

	"github.com/codefly-dev/core/solutionhost"
)

func Signer(t testing.TB, keyID string) *solutionhost.InMemorySigner {
	t.Helper()
	if keyID == solutionhost.FixtureDeliveredBy {
		return solutionhost.FixtureSigner()
	}
	seed := sha256.Sum256([]byte("delivery test key: " + keyID))
	signer, err := solutionhost.NewInMemorySigner(keyID, ed25519.NewKeyFromSeed(seed[:]))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func PublicKey(t testing.TB, keyID string) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(Signer(t, keyID).Public())
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func Verifier(t testing.TB, domains map[string][]string) *solutionhost.Verifier {
	t.Helper()
	var pins []solutionhost.PinnedKey
	for id, allowed := range domains {
		pins = append(pins, solutionhost.PinnedKey{KeyID: id, PublicKeyPEM: PublicKey(t, id), Kind: solutionhost.KeyKindRelease, OwnershipDomains: allowed})
	}
	keys, err := solutionhost.NewPinnedKeySet(pins...)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := solutionhost.NewVerifier(keys)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func Envelope(t testing.TB, payload []byte, keyID string) json.RawMessage {
	t.Helper()
	envelope, err := solutionhost.SignEnvelope(payload, Signer(t, keyID))
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func Carrier(t testing.TB, payload []byte, keyID string) []byte {
	t.Helper()
	carrier, err := solutionhost.Carrier(payload, Envelope(t, payload, keyID))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := solutionhost.MarshalSigned(carrier)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
