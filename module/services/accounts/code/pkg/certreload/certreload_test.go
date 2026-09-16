package certreload

import (
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

func leafPEM(t *testing.T, commonName string, serial int64) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
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

func caPEM(t *testing.T, commonName string) []byte {
	t.Helper()
	certPEM, _ := leafPEM(t, commonName, 900)
	return certPEM
}

// mount writes a projected-style directory and returns its three paths.
func mount(t *testing.T, certPEM, keyPEM, clientCA []byte) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	certFile, keyFile, caFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "ca.crt")
	for path, data := range map[string][]byte{certFile: certPEM, keyFile: keyPEM, caFile: clientCA} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return certFile, keyFile, caFile
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// immediate removes the check throttle so a test observes a rotation on the very
// next handshake instead of waiting out CheckInterval.
func immediate(r *Reloader) *Reloader {
	r.interval = 0
	return r
}

func servedCommonName(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	tlsListener := tls.NewListener(listener, cfg)
	go func() {
		conn, err := tlsListener.Accept()
		if err == nil {
			_ = conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()
	// Dial the literal address. crypto/tls omits SNI for an IP literal, which is
	// exactly the case that a Certificates snapshot would silently take over.
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if state.ServerName != "" {
		t.Fatalf("client unexpectedly sent SNI %q; the no-SNI path is not under test", state.ServerName)
	}
	return state.PeerCertificates[0].Subject.CommonName
}

// leafConfig serves the reloader's leaf through GetCertificate alone, which is
// how the hardened listener config resolves an identity once the client CA hook
// has run. Certificates stays empty; see
// TestCertificatesSnapshotSuppressesGetCertificateWithoutSNI for why.
func leafConfig(r *Reloader) *tls.Config {
	return &tls.Config{GetCertificate: r.GetCertificate, ClientCAs: r.ClientCAs(), MinVersion: tls.VersionTLS13}
}

// This pins the crypto/tls rule the whole package depends on: GetCertificate is
// consulted only when Certificates is empty or the ClientHello carries SNI. A
// listener that keeps a startup snapshot in Certificates therefore serves that
// snapshot forever to every client that dials by IP, which is what makes
// "populate both, belt and braces" a silent failure rather than a safe default.
// If a future Go release drops this rule, this test fails and the empty-
// Certificates requirement documented on GetCertificate can be revisited.
func TestCertificatesSnapshotSuppressesGetCertificateWithoutSNI(t *testing.T) {
	snapshotPEM, snapshotKeyPEM := leafPEM(t, "startup-snapshot", 10)
	snapshot, err := tls.X509KeyPair(snapshotPEM, snapshotKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := leafPEM(t, "reloaded-leaf", 11)
	certFile, keyFile, caFile := mount(t, certPEM, keyPEM, caPEM(t, "client-ca"))
	r, err := New(certFile, keyFile, caFile, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	withSnapshot := leafConfig(immediate(r))
	withSnapshot.Certificates = []tls.Certificate{snapshot}
	if got := servedCommonName(t, withSnapshot); got != "startup-snapshot" {
		t.Fatalf("crypto/tls no longer prefers Certificates without SNI (served %q); revisit the GetCertificate contract", got)
	}
	if got := servedCommonName(t, leafConfig(r)); got != "reloaded-leaf" {
		t.Fatalf("GetCertificate not consulted with Certificates empty: %q", got)
	}
}

// A client that dials by IP sends no SNI. With a startup snapshot left in
// tls.Config.Certificates crypto/tls never calls GetCertificate for such a
// client, so the rotated leaf is never served and the listener presents the
// startup leaf until it expires.
func TestReloaderServesRotatedLeafToClientsWithoutSNI(t *testing.T) {
	certPEM, keyPEM := leafPEM(t, "leaf-before-rotation", 1)
	certFile, keyFile, caFile := mount(t, certPEM, keyPEM, caPEM(t, "client-ca"))
	r, err := New(certFile, keyFile, caFile, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := leafConfig(immediate(r))
	if got := servedCommonName(t, cfg); got != "leaf-before-rotation" {
		t.Fatalf("initial leaf not served: %q", got)
	}

	rotated, rotatedKey := leafPEM(t, "leaf-after-rotation", 2)
	write(t, certFile, rotated)
	write(t, keyFile, rotatedKey)
	if got := servedCommonName(t, cfg); got != "leaf-after-rotation" {
		t.Fatalf("rotated leaf not served to a client that sends no SNI: %q", got)
	}
}

// A rotation is a content change, not necessarily a forward step in modification
// time: a projected volume swaps in a different inode whose timestamp carries no
// ordering guarantee against the file it replaced. Detection must not depend on
// the timestamp advancing.
func TestReloaderDetectsRotationWhenModTimeDoesNotAdvance(t *testing.T) {
	certPEM, keyPEM := leafPEM(t, "leaf-before-rotation", 1)
	certFile, keyFile, caFile := mount(t, certPEM, keyPEM, caPEM(t, "client-ca"))
	r, err := New(certFile, keyFile, caFile, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	immediate(r)

	rotated, rotatedKey := leafPEM(t, "leaf-after-rotation", 2)
	write(t, certFile, rotated)
	write(t, keyFile, rotatedKey)
	stale := time.Now().Add(-time.Hour)
	for _, path := range []string{certFile, keyFile} {
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Leaf.Subject.CommonName != "leaf-after-rotation" {
		t.Fatalf("rotation missed because the replacement was not newer: %q", got.Leaf.Subject.CommonName)
	}
}

// The Reloader must observe files only through the injected reader, so the host's
// permission-checking projected reader re-validates every rotation. A reader
// backed by no filesystem at all proves there is no second, unchecked path.
func TestReloaderObservesFilesOnlyThroughInjectedReader(t *testing.T) {
	certPEM, keyPEM := leafPEM(t, "leaf-before-rotation", 1)
	files := map[string][]byte{"/custody/tls.crt": certPEM, "/custody/tls.key": keyPEM, "/custody/ca.crt": caPEM(t, "client-ca")}
	reads := 0
	read := func(path string) ([]byte, error) {
		reads++
		data, ok := files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return data, nil
	}
	r, err := New("/custody/tls.crt", "/custody/tls.key", "/custody/ca.crt", read)
	if err != nil {
		t.Fatal(err)
	}
	immediate(r)
	rotated, rotatedKey := leafPEM(t, "leaf-after-rotation", 2)
	files["/custody/tls.crt"], files["/custody/tls.key"] = rotated, rotatedKey
	got, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Leaf.Subject.CommonName != "leaf-after-rotation" {
		t.Fatalf("rotation not observed through the injected reader: %q", got.Leaf.Subject.CommonName)
	}
	if reads == 0 {
		t.Fatal("injected reader was never used")
	}
}

// Handshakes must not re-read the mount on every connection, and a persistently
// malformed replacement must not turn each handshake into a fresh read storm.
func TestReloaderThrottlesMountReads(t *testing.T) {
	certPEM, keyPEM := leafPEM(t, "leaf-before-rotation", 1)
	certFile, keyFile, caFile := mount(t, certPEM, keyPEM, caPEM(t, "client-ca"))
	reads := 0
	read := func(path string) ([]byte, error) {
		reads++
		return os.ReadFile(path)
	}
	r, err := New(certFile, keyFile, caFile, read)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	r.now = func() time.Time { return clock }
	write(t, certFile, []byte("not a certificate"))

	for i := 0; i < 50; i++ {
		if _, err := r.GetCertificate(nil); err != nil {
			t.Fatal(err)
		}
	}
	after := reads
	if after > 3+3 {
		t.Fatalf("handshakes re-read the mount %d times within one interval", after-3)
	}
	clock = clock.Add(2 * CheckInterval)
	if _, err := r.GetCertificate(nil); err != nil {
		t.Fatal(err)
	}
	if reads == after {
		t.Fatal("reloader stopped checking the mount after the interval elapsed")
	}
}

// A re-issued client CA must reach a live listener: workers presenting leaves
// from the new CA are refused until the pool is replaced, which is the same
// outage as a stale leaf, arriving from the other side of the handshake.
func TestReloaderRotatesClientCAs(t *testing.T) {
	certPEM, keyPEM := leafPEM(t, "leaf", 1)
	certFile, keyFile, caFile := mount(t, certPEM, keyPEM, caPEM(t, "client-ca-before"))
	r, err := New(certFile, keyFile, caFile, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	immediate(r)

	replacement := caPEM(t, "client-ca-after")
	write(t, caFile, replacement)
	fresh, err := r.GetConfigForClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := x509.NewCertPool()
	if !expected.AppendCertsFromPEM(replacement) {
		t.Fatal("test CA not usable")
	}
	if !fresh.ClientCAs.Equal(expected) {
		t.Fatal("re-issued client CA bundle not served to the listener")
	}
	if fresh.GetCertificate == nil {
		t.Fatal("rotated trust config dropped the server identity")
	}
}

func TestReloaderKeepsLastGoodOnMalformedReplacement(t *testing.T) {
	certPEM, keyPEM := leafPEM(t, "leaf-before-rotation", 1)
	certFile, keyFile, caFile := mount(t, certPEM, keyPEM, caPEM(t, "client-ca"))
	r, err := New(certFile, keyFile, caFile, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := leafConfig(immediate(r))

	write(t, certFile, []byte("not a certificate"))
	if got := servedCommonName(t, cfg); got != "leaf-before-rotation" {
		t.Fatalf("malformed replacement served instead of the last good leaf: %q", got)
	}
	write(t, caFile, []byte("not a CA bundle"))
	if pool := r.ClientCAs(); pool == nil {
		t.Fatal("malformed CA replacement dropped the client CA pool")
	}

	recovered, recoveredKey := leafPEM(t, "leaf-after-recovery", 3)
	write(t, certFile, recovered)
	write(t, keyFile, recoveredKey)
	write(t, caFile, caPEM(t, "client-ca"))
	if got := servedCommonName(t, cfg); got != "leaf-after-recovery" {
		t.Fatalf("reloader did not recover once a valid pair landed: %q", got)
	}
}

// A torn read during a rotation yields a certificate and key that do not pair.
// It must be rejected without recording the replacement, so the next check
// retries rather than pinning the stale material forever.
func TestReloaderRetriesAfterMismatchedPair(t *testing.T) {
	certPEM, keyPEM := leafPEM(t, "leaf-before-rotation", 1)
	certFile, keyFile, caFile := mount(t, certPEM, keyPEM, caPEM(t, "client-ca"))
	r, err := New(certFile, keyFile, caFile, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := leafConfig(immediate(r))

	rotated, rotatedKey := leafPEM(t, "leaf-after-rotation", 2)
	write(t, certFile, rotated) // key still belongs to the previous leaf
	if got := servedCommonName(t, cfg); got != "leaf-before-rotation" {
		t.Fatalf("mismatched pair served: %q", got)
	}
	write(t, keyFile, rotatedKey) // the rotation settles
	if got := servedCommonName(t, cfg); got != "leaf-after-rotation" {
		t.Fatalf("reloader pinned stale material after a torn read: %q", got)
	}
}

func TestReloaderRejectsIncompleteMaterialAtStartup(t *testing.T) {
	certPEM, keyPEM := leafPEM(t, "leaf", 1)
	certFile, keyFile, caFile := mount(t, certPEM, keyPEM, caPEM(t, "client-ca"))
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent")
	for _, material := range [][3]string{{absent, keyFile, caFile}, {certFile, absent, caFile}, {certFile, keyFile, absent}} {
		if _, err := New(material[0], material[1], material[2], os.ReadFile); err == nil {
			t.Fatalf("listener started without complete TLS material: %v", material)
		}
	}
	if _, err := New(certFile, keyFile, caFile, nil); err == nil {
		t.Fatal("reloader accepted a nil projected reader")
	}
	invalid := filepath.Join(dir, "ca.crt")
	write(t, invalid, []byte("not a CA bundle"))
	if _, err := New(certFile, keyFile, invalid, os.ReadFile); err == nil {
		t.Fatal("listener started with an unusable client CA bundle")
	}
}

// A refused rotation must be observable. Without a signal it is indistinguishable
// from no rotation, and the operator first learns of it when the leaf expires.
// A standing failure must report once, not once per check.
func TestReloaderReportsReloadOutcomes(t *testing.T) {
	certPEM, keyPEM := leafPEM(t, "leaf-before-rotation", 1)
	certFile, keyFile, caFile := mount(t, certPEM, keyPEM, caPEM(t, "client-ca"))
	r, err := New(certFile, keyFile, caFile, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	immediate(r)
	var outcomes []string
	r.Observe(func(err error) {
		if err != nil {
			outcomes = append(outcomes, "refused")
			return
		}
		outcomes = append(outcomes, "reloaded")
	})

	// A check that finds nothing changed must stay silent.
	if _, err := r.GetCertificate(nil); err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 0 {
		t.Fatalf("an unchanged mount reported %v", outcomes)
	}

	rotated, rotatedKey := leafPEM(t, "leaf-after-rotation", 2)
	write(t, certFile, rotated)
	write(t, keyFile, rotatedKey)
	if _, err := r.GetCertificate(nil); err != nil {
		t.Fatal(err)
	}

	write(t, certFile, []byte("not a certificate"))
	for i := 0; i < 5; i++ {
		if _, err := r.GetCertificate(nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(outcomes) != 2 || outcomes[0] != "reloaded" || outcomes[1] != "refused" {
		t.Fatalf("expected one reload then one suppressed-repeat refusal, got %v", outcomes)
	}

	recovered, recoveredKey := leafPEM(t, "leaf-after-recovery", 3)
	write(t, certFile, recovered)
	write(t, keyFile, recoveredKey)
	if _, err := r.GetCertificate(nil); err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 3 || outcomes[2] != "reloaded" {
		t.Fatalf("recovery was not reported: %v", outcomes)
	}
}
