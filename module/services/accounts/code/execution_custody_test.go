package main

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

	"accounts/pkg/certreload"
)

func TestExecutionCustodyMountDisabledAndRevocationRequired(t *testing.T) {
	t.Setenv("EXECUTION_CUSTODY_CONFIG_FILE", "")
	server, err := configuredExecutionCustody(nil, nil, nil, "", nil, false, true)
	if err != nil || server != nil {
		t.Fatal("absent projection must leave listener disabled")
	}
	t.Setenv("EXECUTION_CUSTODY_CONFIG_FILE", "/not-read-before-revocation-check")
	for _, mode := range [][2]bool{{false, false}, {true, true}} {
		if _, err := configuredExecutionCustody(nil, nil, nil, "", nil, mode[0], mode[1]); err == nil || err.Error() != "execution custody requires fail-closed access-token revocation" {
			t.Fatal("unsafe revocation configuration accepted")
		}
	}
}

func TestCustodyProjectionSupportsPrivateAtomicMount(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "version")
	link := filepath.Join(dir, "config")
	if err := os.WriteFile(target, []byte("{}"), 0440); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := projectedCustodyFile(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := projectedCustodyFile(link); err == nil {
		t.Fatal("world-readable projection accepted")
	}
}

// The reload path must go through the same permission-checking projected reader
// that startup uses, so a rotation is re-validated rather than trusted. A
// replacement the reader refuses must leave the last good material serving.
func TestCustodyReloadRevalidatesRotationsThroughProjectedReader(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "ca.crt")
	issue := func(commonName string, serial int64) (certPEM, keyPEM []byte) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: commonName},
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	}
	place := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	certPEM, keyPEM := issue("leaf-before-rotation", 1)
	caPEM, _ := issue("client-ca", 900)
	place(certFile, certPEM, 0600)
	place(keyFile, keyPEM, 0600)
	place(caFile, caPEM, 0600)

	reloader, err := certreload.New(certFile, keyFile, caFile, projectedCustodyFile)
	if err != nil {
		t.Fatal(err)
	}
	served := func() string {
		t.Helper()
		pair, err := reloader.GetCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		return pair.Leaf.Subject.CommonName
	}
	// settle waits out one full check interval so a refresh is guaranteed to have
	// been attempted, then reports what the listener serves.
	settle := func(want string) string {
		t.Helper()
		deadline := time.Now().Add(4 * certreload.CheckInterval)
		for {
			got := served()
			if got == want || time.Now().After(deadline) {
				return got
			}
			time.Sleep(certreload.CheckInterval / 4)
		}
	}
	if got := served(); got != "leaf-before-rotation" {
		t.Fatalf("initial leaf not served: %q", got)
	}

	rotated, rotatedKey := issue("leaf-after-rotation", 2)
	place(certFile, rotated, 0600)
	place(keyFile, rotatedKey, 0600)
	if got := settle("leaf-after-rotation"); got != "leaf-after-rotation" {
		t.Fatalf("rotation not picked up through the projected reader: %q", got)
	}

	// A replacement mounted world-readable must be refused by the reader, and the
	// listener must keep serving the leaf it already validated.
	loosened, loosenedKey := issue("leaf-world-readable", 3)
	place(certFile, loosened, 0644)
	place(keyFile, loosenedKey, 0644)
	time.Sleep(2 * certreload.CheckInterval)
	if got := served(); got != "leaf-after-rotation" {
		t.Fatalf("world-readable replacement served: %q", got)
	}
}
