package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const gatewayTestSPIFFE = "spiffe://acme.test/ns/acme-prod/sa/gateway"
const callerTestSPIFFE = "spiffe://acme.test/ns/acme-prod/sa/worker"

type workloadTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newWorkloadTestCA(t *testing.T) *workloadTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Example CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &workloadTestCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *workloadTestCA) leaf(t *testing.T, ids ...string) (tls.Certificate, []byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	for _, id := range ids {
		u, err := url.Parse(id)
		require.NoError(t, err)
		tmpl.URIs = append(tmpl.URIs, u)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	return cert, certPEM, keyPEM
}

func workloadTestIdentity(t *testing.T, ca *workloadTestCA, id, allowed string) (*workloadIdentity, map[string]string) {
	t.Helper()
	_, cert, key := ca.leaf(t, id)
	paths := map[string]string{}
	dir := t.TempDir()
	for name, body := range map[string][]byte{"CERT_FILE": cert, "KEY_FILE": key, "TRUST_BUNDLE_FILE": ca.pem, "ALLOWED_CALLERS_FILE": []byte(allowed), "TOKEN_FILE": []byte("projected-token")} {
		paths[name] = filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(paths[name], body, 0600))
	}
	identity, err := loadWorkloadIdentity(func(key string) (string, error) { return paths[key], nil })
	require.NoError(t, err)
	return identity, paths
}

// net.Pipe preserves the temporary deadline errors TLS requires when HTTP
// cancels a background read between keep-alive requests. bufconn's deadline
// error is permanent, causing TLS to close after the first request.
type workloadTestListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *workloadTestListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *workloadTestListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *workloadTestListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (l *workloadTestListener) DialContext(ctx context.Context) (net.Conn, error) {
	server, client := net.Pipe()
	select {
	case l.connections <- server:
		return client, nil
	case <-ctx.Done():
		_ = server.Close()
		_ = client.Close()
		return nil, ctx.Err()
	case <-l.done:
		_ = server.Close()
		_ = client.Close()
		return nil, net.ErrClosed
	}
}

// Real TLS and HTTP over an in-memory listener: these security checks also run
// where the sandbox forbids binding a loopback socket.
func workloadMemoryServer(t *testing.T, config *tls.Config, handler http.Handler) *workloadTestListener {
	t.Helper()
	listener := &workloadTestListener{connections: make(chan net.Conn), done: make(chan struct{})}
	server := &http.Server{Handler: handler, TLSConfig: config}
	go func() { _ = server.ServeTLS(listener, "", "") }()
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	return listener
}

func workloadMemoryClient(t *testing.T, identity *workloadIdentity, listener *workloadTestListener, expected string) *http.Client {
	t.Helper()
	transport, err := identity.upstreamTransport(expected)
	require.NoError(t, err)
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) { return listener.DialContext(ctx) }
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}

func TestWorkloadListenerAdmissionAndRevocation(t *testing.T) {
	ca := newWorkloadTestCA(t)
	gateway, paths := workloadTestIdentity(t, ca, gatewayTestSPIFFE, "# callers\n"+callerTestSPIFFE+"\n")
	config, err := gateway.serverTLSConfig()
	require.NoError(t, err)
	listener := workloadMemoryServer(t, config, gateway.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := workloadPeer(r.Context())
		require.True(t, ok)
		require.Equal(t, callerTestSPIFFE, id)
		w.WriteHeader(http.StatusNoContent)
	})))
	for _, test := range []struct {
		name    string
		ca      *workloadTestCA
		ids     []string
		noCert  bool
		allowed bool
	}{
		{name: "no certificate", noCert: true},
		{name: "untrusted chain", ca: newWorkloadTestCA(t), ids: []string{callerTestSPIFFE}},
		{name: "unlisted identity", ca: ca, ids: []string{"spiffe://acme.test/ns/acme-prod/sa/intruder"}},
		{name: "two URI SANs", ca: ca, ids: []string{callerTestSPIFFE, callerTestSPIFFE}},
		{name: "non SPIFFE URI", ca: ca, ids: []string{"https://example.com/worker"}},
		{name: "listed identity", ca: ca, ids: []string{callerTestSPIFFE}, allowed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := workloadMemoryClient(t, gateway, listener, gatewayTestSPIFFE)
			cfg := client.Transport.(*http.Transport).TLSClientConfig
			cfg.GetClientCertificate = nil
			if !test.noCert {
				leaf, _, _ := test.ca.leaf(t, test.ids...)
				cfg.Certificates = []tls.Certificate{leaf}
			}
			response, err := client.Get("https://workload/health")
			if !test.allowed {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			_ = response.Body.Close()
			require.Equal(t, http.StatusNoContent, response.StatusCode)
		})
	}
	// Keep this connection alive across the revocation; the middleware must
	// re-read the allowlist even when there is no new TLS handshake.
	caller, _ := workloadTestIdentity(t, ca, callerTestSPIFFE, gatewayTestSPIFFE+"\n")
	client := workloadMemoryClient(t, caller, listener, gatewayTestSPIFFE)
	wire, err := listener.DialContext(context.Background())
	require.NoError(t, err)
	connection := tls.Client(wire, client.Transport.(*http.Transport).TLSClientConfig)
	t.Cleanup(func() { _ = connection.Close() })
	require.NoError(t, connection.SetDeadline(time.Now().Add(3*time.Second)))
	reader := bufio.NewReader(connection)
	request, err := http.NewRequest(http.MethodGet, "https://workload/health", nil)
	require.NoError(t, err)
	require.NoError(t, request.Write(connection))
	response, err := http.ReadResponse(reader, request)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.NoError(t, os.WriteFile(paths["ALLOWED_CALLERS_FILE"], []byte("# revoked\n"), 0600))
	require.NoError(t, request.Write(connection))
	response, err = http.ReadResponse(reader, request)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	_, err = client.Get("https://workload/health")
	require.Error(t, err)
}

func TestWorkloadSolutionTLSIdentity(t *testing.T) {
	ca := newWorkloadTestCA(t)
	gateway, _ := workloadTestIdentity(t, ca, gatewayTestSPIFFE, callerTestSPIFFE+"\n")
	solution, _ := workloadTestIdentity(t, ca, callerTestSPIFFE, gatewayTestSPIFFE+"\n")
	config, err := solution.serverTLSConfig()
	require.NoError(t, err)
	listener := workloadMemoryServer(t, config, solution.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := workloadPeer(r.Context())
		require.True(t, ok)
		require.Equal(t, gatewayTestSPIFFE, id)
		_, _ = io.WriteString(w, "solution")
	})))
	for _, expected := range []string{callerTestSPIFFE, "spiffe://acme.test/ns/acme-prod/sa/different"} {
		client := workloadMemoryClient(t, gateway, listener, expected)
		response, err := client.Get("https://solution/assets/entry.js")
		if expected != callerTestSPIFFE {
			require.ErrorContains(t, err, "SPIFFE ID mismatch")
			continue
		}
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		_ = response.Body.Close()
		require.Equal(t, "solution", string(body))
	}
}

func TestWorkloadFilesRequiredAndRotation(t *testing.T) {
	ca := newWorkloadTestCA(t)
	identity, paths := workloadTestIdentity(t, ca, gatewayTestSPIFFE, callerTestSPIFFE+"\n")
	for key := range paths {
		t.Run(key, func(t *testing.T) {
			_, err := loadWorkloadIdentity(func(k string) (string, error) {
				if k == key {
					return "", nil
				}
				return paths[k], nil
			})
			require.ErrorContains(t, err, key)
			_, err = loadWorkloadIdentity(func(k string) (string, error) {
				if k == key {
					return filepath.Join(t.TempDir(), "absent"), nil
				}
				return paths[k], nil
			})
			require.ErrorContains(t, err, key)
		})
	}
	first := identity.certificate.Certificate()
	replacement, cert, key := ca.leaf(t, gatewayTestSPIFFE)
	require.NoError(t, os.WriteFile(paths["CERT_FILE"], cert, 0600))
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(paths["CERT_FILE"], future, future))
	require.Equal(t, first, identity.certificate.Certificate(), "half-written rotation keeps the last good pair")
	require.NoError(t, os.WriteFile(paths["KEY_FILE"], key, 0600))
	require.NoError(t, os.Chtimes(paths["KEY_FILE"], future, future))
	served, err := identity.certificate.GetCertificate(nil)
	require.NoError(t, err)
	presented, err := identity.certificate.GetClientCertificate(nil)
	require.NoError(t, err)
	require.Equal(t, replacement.Certificate, served.Certificate)
	require.Equal(t, served, presented)
}

// Existing solution routing tests use the same mTLS contract as production.
func newSolutionTestServer(t *testing.T, gateway *Gateway, id string, handler http.Handler) *httptest.Server {
	t.Helper()
	ca := newWorkloadTestCA(t)
	if gateway.workload == nil {
		gateway.workload, _ = workloadTestIdentity(t, ca, gatewayTestSPIFFE, "")
	}
	bundle, err := os.ReadFile(gateway.workload.trustFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(gateway.workload.trustFile, append(bundle, ca.pem...), 0600))
	solution, _ := workloadTestIdentity(t, ca, "spiffe://acme.test/ns/acme-prod/sa/"+id, gatewayTestSPIFFE+"\n")
	solution.trustFile = gateway.workload.trustFile
	config, err := solution.serverTLSConfig()
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(solution.handler(handler))
	server.TLS = config
	server.TLS.Certificates = []tls.Certificate{*solution.certificate.Certificate()}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func TestWorkloadProxyRequiresDeclaredIdentity(t *testing.T) {
	gateway := &Gateway{}
	recorder := httptest.NewRecorder()
	gateway.proxyTo(recorder, httptest.NewRequest(http.MethodGet, "/assets/entry.js", nil), MustURL("https://solution"), &RouteEntry{Service: "solution:example"})
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.NotContains(t, strings.ToLower(recorder.Body.String()), "credential")
}
