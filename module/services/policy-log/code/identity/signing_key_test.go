package identity

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"
)

func TestSigningKeyAcceptsOnlyDeliveredEd25519PKCS8(t *testing.T) {
	want := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	der, err := x509.MarshalPKCS8PrivateKey(want)
	if err != nil {
		t.Fatal(err)
	}
	valid := base64.StdEncoding.EncodeToString(der)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherDER, err := x509.MarshalPKCS8PrivateKey(other)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"valid", valid, true},
		{"missing", "", false},
		{"invalid", "private-invalid-value", false},
		{"oversize", strings.Repeat("x", 4097), false},
		{"wrong_algorithm", base64.StdEncoding.EncodeToString(otherDER), false},
		{"multiple_keys", base64.StdEncoding.EncodeToString(append(bytes.Clone(der), der...)), false},
		{"newline", valid + "\n", false},
		{"raw_seed", base64.StdEncoding.EncodeToString(want.Seed()), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSigningKey(tc.value)
			if tc.valid {
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("valid key: %v", err)
				}
				again, err := ParseSigningKey(tc.value)
				if err != nil || !bytes.Equal(again, got) {
					t.Fatal("same delivered key changed identity")
				}
			} else if err == nil || got != nil {
				t.Fatal("invalid key accepted")
			} else if tc.value != "" && strings.Contains(err.Error(), tc.value) {
				t.Fatal("error exposed key value")
			}
		})
	}
}
