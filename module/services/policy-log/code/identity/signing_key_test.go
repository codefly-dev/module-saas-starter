package identity

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestMissingSigningKeyRefusesBoot(t *testing.T) {
	t.Setenv("POLICY_LOG_SIGNING_KEY", "ignored")
	t.Setenv("POLICY_LOG_SIGNING_KEY_PATH", "/tmp/example-key")
	var requested string
	key, err := loadSigningKey(func(path string) (fs.File, error) { requested = path; return nil, fs.ErrNotExist })
	if len(key) != 0 || !errors.Is(err, fs.ErrNotExist) || requested != SigningKeyPath {
		t.Fatalf("missing fixed key did not refuse: requested=%q key length=%d err=%v", requested, len(key), err)
	}
}

func TestSigningKeyAcceptsOnlyPrivateEd25519PKCS8(t *testing.T) {
	want := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	der, err := x509.MarshalPKCS8PrivateKey(want)
	if err != nil {
		t.Fatal(err)
	}
	valid := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherDER, err := x509.MarshalPKCS8PrivateKey(other)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		body  []byte
		mode  fs.FileMode
		valid bool
	}{
		{"private", valid, 0400, true}, {"owner_write", valid, 0600, true},
		{"public", valid, 0444, false}, {"group", valid, 0440, false},
		{"empty", nil, 0400, false}, {"invalid", []byte("invalid"), 0400, false},
		{"leading_junk", append([]byte("untrusted prefix\n"), valid...), 0400, false},
		{"oversize", bytes.Repeat([]byte("x"), 4097), 0400, false},
		{"wrong_algorithm", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: otherDER}), 0400, false},
		{"multiple_keys", append(bytes.Clone(valid), valid...), 0400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fstest.MapFS{"key": &fstest.MapFile{Data: tc.body, Mode: tc.mode}}
			got, err := loadSigningKey(func(string) (fs.File, error) { return files.Open("key") })
			if tc.valid {
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("valid key: %v", err)
				}
			} else if err == nil || got != nil {
				t.Fatal("invalid key accepted")
			}
		})
	}
}
