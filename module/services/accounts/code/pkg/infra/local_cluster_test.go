package infra

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testCertificateAuthorityPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local stand-in"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// A stand-in for the kubelet's projection is a developer's run, and only that:
// the same variable inside a pod is a refusal at boot, never an alternative
// identity. These drive the refusal and the acceptance through the production
// constructors.

func writeLocalCluster(t *testing.T, server string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"server": server, "ca.crt": testCertificateAuthorityPEM(t), "token": "token", "namespace": "host\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAPodMayNotNameALocalStandInForItsCluster(t *testing.T) {
	dir := writeLocalCluster(t, "https://127.0.0.1:6443")
	if _, err := localClusterClient(dir, true); !errors.Is(err, ErrLocalClusterInAPod) {
		t.Fatalf("a pod naming a stand-in was not refused: %v", err)
	}
	client, err := localClusterClient(dir, false)
	if err != nil {
		t.Fatalf("a process outside a pod was refused its stand-in: %v", err)
	}
	if client.endpoint != "https://127.0.0.1:6443" || client.tokenPath != filepath.Join(dir, "token") || client.namespacePath != filepath.Join(dir, "namespace") {
		t.Fatalf("the client reads %q, %q, %q", client.endpoint, client.tokenPath, client.namespacePath)
	}
}

func TestALocalStandInIsAnHTTPSOriginInAnAbsoluteDirectory(t *testing.T) {
	for name, server := range map[string]string{
		"plaintext":   "http://127.0.0.1:6443",
		"a path":      "https://127.0.0.1:6443/api",
		"credentials": "https://user@127.0.0.1:6443",
		"no port":     "https://127.0.0.1",
		"a query":     "https://127.0.0.1:6443?x=1",
		"empty":       "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := localClusterClient(writeLocalCluster(t, server), false); !errors.Is(err, ErrKubernetesUnavailable) {
				t.Fatalf("server %q was accepted: %v", server, err)
			}
		})
	}
	if _, err := localClusterClient("relative/dir", false); !errors.Is(err, ErrKubernetesUnavailable) {
		t.Fatalf("a relative directory was accepted: %v", err)
	}
}

func TestAPodMayNotRelocateItsTrustAnchor(t *testing.T) {
	if dir, err := trustAnchorDir("", true); err != nil || dir != SolutionHostTrustAnchorPath {
		t.Fatalf("a pod naming nothing reads %q, %v", dir, err)
	}
	if _, err := trustAnchorDir("/tmp/anchor", true); !errors.Is(err, ErrLocalTrustAnchorInAPod) {
		t.Fatalf("a pod relocating its anchor was not refused: %v", err)
	}
	if dir, err := trustAnchorDir("/tmp/anchor", false); err != nil || dir != "/tmp/anchor" {
		t.Fatalf("outside a pod the named anchor reads %q, %v", dir, err)
	}
	if _, err := trustAnchorDir("anchor", false); !errors.Is(err, ErrSolutionHostTrustRootUnavailable) {
		t.Fatalf("a relative anchor was accepted: %v", err)
	}
}

func TestARelocatedAnchorMustBeALocalProfile(t *testing.T) {
	release := &keyBundleVerifier{}
	if _, err := requireLocalProfileOffTheAnchor("/tmp/anchor", release); !errors.Is(err, ErrSolutionHostTrustRootUnavailable) {
		t.Fatalf("a release policy read from a relocated anchor was accepted: %v", err)
	}
	if _, err := requireLocalProfileOffTheAnchor(SolutionHostTrustAnchorPath, release); err != nil {
		t.Fatalf("the constant anchor's release policy was refused: %v", err)
	}
	local := &keyBundleVerifier{local: &SolutionHostLocalProfile{}}
	if _, err := requireLocalProfileOffTheAnchor("/tmp/anchor", local); err != nil {
		t.Fatalf("a local profile on a relocated anchor was refused: %v", err)
	}
}
