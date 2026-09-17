package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// custodyLeaf mints a self-signed leaf valid for the loopback address and the
// custody.example name, writes it as a private pair to certFile/keyFile, and
// returns its DER, its PEM and a pool trusting it.
func custodyLeaf(t *testing.T, certFile, keyFile string, serial int64) ([]byte, []byte, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "custody-rotation-test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"custody.example"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return der, certPEM, pool
}

// rotate makes a rotation observable: the reloader compares modification
// times, so each rotation moves both files further into the future than the
// previous one, however close together the writes land.
func rotate(t *testing.T, step time.Duration, paths ...string) {
	t.Helper()
	future := time.Now().Add(step)
	for _, path := range paths {
		if err := os.Chtimes(path, future, future); err != nil {
			t.Fatal(err)
		}
	}
}

// servedLeaf completes one handshake against a listener serving tc and returns
// the leaf the server presented and the SNI name it saw. An empty serverName
// dials the loopback address by IP, which carries no SNI — the path Go's
// GetCertificate is skipped on when a static Certificates entry is present.
func servedLeaf(t *testing.T, tc *tls.Config, trust *x509.CertPool, serverName string) ([]byte, string) {
	t.Helper()
	var seen string
	serve := tc.Clone()
	inner := serve.GetCertificate
	serve.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		seen = hello.ServerName
		return inner(hello)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", serve)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.(*tls.Conn).Handshake()
		conn.Close()
	}()
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: trust, MinVersion: tls.VersionTLS13, ServerName: serverName})
	if err != nil {
		<-done
		t.Fatalf("handshake against the custody listener failed: %v", err)
	}
	defer conn.Close()
	<-done
	return conn.ConnectionState().PeerCertificates[0].Raw, seen
}

// The custody identity must serve a rotated leaf to every peer, including one
// that dials by IP and so sends no SNI: a kubelet probe, an in-cluster IP dial.
// The client trusts only the leaf it expects, so a stale leaf fails the
// handshake rather than passing unnoticed.
func TestCustodyListenerServesRotatedLeafWithAndWithoutSNI(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	first, firstPEM, trustFirst := custodyLeaf(t, certFile, keyFile, 1)
	tc, err := custodyServerTLS(certFile, keyFile, firstPEM)
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Certificates) != 0 {
		t.Fatal("custody identity carries a static leaf, which would shadow the reloader for peers without SNI")
	}
	if got, sni := servedLeaf(t, tc, trustFirst, ""); !bytes.Equal(got, first) || sni != "" {
		t.Fatalf("boot-time leaf not served to a peer dialing by IP (sni=%q)", sni)
	}

	second, _, trustSecond := custodyLeaf(t, certFile, keyFile, 2)
	rotate(t, time.Minute, certFile, keyFile)
	if got, sni := servedLeaf(t, tc, trustSecond, ""); !bytes.Equal(got, second) || sni != "" {
		t.Fatalf("rotated leaf not served to a peer dialing by IP (sni=%q)", sni)
	}
	if got, sni := servedLeaf(t, tc, trustSecond, "custody.example"); !bytes.Equal(got, second) || sni != "custody.example" {
		t.Fatalf("rotated leaf not served to a peer sending SNI (sni=%q)", sni)
	}
}

// A rotation the projected reader refuses — here, a pair that landed
// world-readable — is not served: the last good leaf keeps serving until the
// files satisfy the same checks the boot-time pair did.
func TestCustodyListenerKeepsLastGoodLeafWhenRotationIsNotPrivate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	first, firstPEM, trustFirst := custodyLeaf(t, certFile, keyFile, 1)
	tc, err := custodyServerTLS(certFile, keyFile, firstPEM)
	if err != nil {
		t.Fatal(err)
	}

	second, _, trustSecond := custodyLeaf(t, certFile, keyFile, 2)
	for _, path := range []string{certFile, keyFile} {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
	}
	rotate(t, time.Minute, certFile, keyFile)
	if got, _ := servedLeaf(t, tc, trustFirst, ""); !bytes.Equal(got, first) {
		t.Fatal("a world-readable rotation was served instead of the last good leaf")
	}

	for _, path := range []string{certFile, keyFile} {
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	rotate(t, 2*time.Minute, certFile, keyFile)
	if got, _ := servedLeaf(t, tc, trustSecond, ""); !bytes.Equal(got, second) {
		t.Fatal("rotated leaf not served once the pair became private again")
	}
}

// A listener must never come up without a valid, private, matching pair and a
// parseable client CA; the failure names which half is wrong and nothing else.
func TestCustodyTLSIdentityRequiresPrivateMatchingPair(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	_, caPEM, _ := custodyLeaf(t, certFile, keyFile, 1)
	other := t.TempDir()
	custodyLeaf(t, filepath.Join(other, "tls.crt"), filepath.Join(other, "tls.key"), 2)

	identity := "invalid custody TLS identity"
	if _, err := custodyServerTLS(filepath.Join(dir, "absent.crt"), filepath.Join(dir, "absent.key"), caPEM); err == nil || err.Error() != identity {
		t.Fatalf("listener identity accepted without a mounted pair: %v", err)
	}
	if _, err := custodyServerTLS(certFile, filepath.Join(other, "tls.key"), caPEM); err == nil || err.Error() != identity {
		t.Fatalf("mismatched certificate and key accepted: %v", err)
	}
	if err := os.Chmod(keyFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := custodyServerTLS(certFile, keyFile, caPEM); err == nil || err.Error() != identity {
		t.Fatalf("world-readable key accepted at startup: %v", err)
	}
	if err := os.Chmod(keyFile, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := custodyServerTLS(certFile, keyFile, []byte("not a certificate")); err == nil || err.Error() != "invalid custody client CA" {
		t.Fatalf("unparseable client CA accepted: %v", err)
	}
	if _, err := custodyServerTLS(certFile, keyFile, caPEM); err != nil {
		t.Fatal(err)
	}
}
