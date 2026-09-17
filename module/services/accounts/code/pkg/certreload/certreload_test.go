package certreload

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLeaf(t *testing.T, certFile, keyFile string, serial int64) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "custody-reload-test"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0600); err != nil {
		t.Fatal(err)
	}
	return der
}

func bump(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

func TestReloaderServesRotatedLeaf(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	first := writeLeaf(t, certFile, keyFile, 1)

	r, err := New(certFile, keyFile, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Current(); string(got.Certificate[0]) != string(first) {
		t.Fatal("initial leaf not served")
	}

	second := writeLeaf(t, certFile, keyFile, 2)
	bump(t, certFile)
	bump(t, keyFile)
	if got := r.Current(); string(got.Certificate[0]) != string(second) {
		t.Fatal("rotated leaf not served after file change")
	}
}

func TestReloaderKeepsLastGoodOnMalformedReplacement(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	good := writeLeaf(t, certFile, keyFile, 1)

	r, err := New(certFile, keyFile, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	bump(t, certFile)
	if got := r.Current(); string(got.Certificate[0]) != string(good) {
		t.Fatal("malformed replacement served instead of last good leaf")
	}

	third := writeLeaf(t, certFile, keyFile, 3)
	bump(t, certFile)
	bump(t, keyFile)
	if got := r.Current(); string(got.Certificate[0]) != string(third) {
		t.Fatal("reloader did not recover after a valid pair landed")
	}
}

func TestReloaderRejectsMissingFilesAtStartup(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(filepath.Join(dir, "absent.crt"), filepath.Join(dir, "absent.key"), os.ReadFile); err == nil {
		t.Fatal("reloader started without an initial leaf")
	}
}
