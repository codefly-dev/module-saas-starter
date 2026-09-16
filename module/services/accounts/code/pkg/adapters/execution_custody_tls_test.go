package adapters

import (
	"context"
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

	"accounts/pkg/business"
	"accounts/pkg/certreload"

	minter "accounts/pkg/auth/ed25519"

	"crypto/ed25519"

	"github.com/stretchr/testify/require"
)

type stubCustodyStore struct{}

func (stubCustodyStore) RegisterExecutionCustody(context.Context, business.ExecutionCustodyRecord) (business.ExecutionCustodyRecord, error) {
	return business.ExecutionCustodyRecord{}, nil
}
func (stubCustodyStore) FindExecutionCustody(context.Context, string, string, string) (business.ExecutionCustodyRecord, error) {
	return business.ExecutionCustodyRecord{}, nil
}
func (stubCustodyStore) GetExecutionCustody(context.Context, string) (business.ExecutionCustodyRecord, error) {
	return business.ExecutionCustodyRecord{}, nil
}
func (stubCustodyStore) PurgeExecutionCustody(context.Context, time.Time) error { return nil }

type stubAuthorityStore struct{}

func (stubAuthorityStore) ResolveWorkContextAuthority(context.Context, string, string, string, []business.WorkContextPermission) (*business.WorkContextAuthorityFacts, error) {
	return nil, nil
}
func (stubAuthorityStore) ResolveInstallationAuthority(context.Context, string, string, []business.WorkContextPermission) (*business.InstallationAuthorityFacts, error) {
	return nil, nil
}

type stubCipher struct{}

func (stubCipher) EncryptSecret(context.Context, string, string) (string, error) { return "", nil }
func (stubCipher) DecryptSecret(context.Context, string, string) (string, error) { return "", nil }

func custodyConsumerPolicy() map[string]ExecutionConsumerPolicy {
	return map[string]ExecutionConsumerPolicy{"example": {
		TaskResourceKind: "task", TaskActions: []string{"run"}, WorkerURI: "spiffe://example.test/worker",
		ParentAudience: "parent", TaskAudience: "task", Audience: "child", Profile: "profile",
		ResourceKind: "kind", ResourceID: "id", InvokeAction: "invoke", ReadAction: "read",
	}}
}

func configuredCustodyAuthority(t *testing.T) *WorkContextAuthorityServer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	authority := &WorkContextAuthorityServer{}
	authority.Configure(WorkContextAuthorityConfiguration{Issuer: "example.work", KeyID: "k1", PrivateKey: key, Authority: stubAuthorityStore{}})
	require.NoError(t, authority.configureErr)
	return authority
}

func custodyMinter(t *testing.T) *minter.Minter {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return minter.New(minter.Config{Issuer: "example.accounts", Audience: "example.accounts"}, key, nil)
}

// rotatingMount writes a projected-style mount and returns its reloader plus a
// rotate function that replaces the leaf in place.
func rotatingMount(t *testing.T) (*certreload.Reloader, func(commonName string)) {
	t.Helper()
	dir := t.TempDir()
	certFile, keyFile, caFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "ca.crt")
	issue := func(commonName string, serial int64) (certPEM, keyPEM []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: commonName},
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		require.NoError(t, err)
		pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
		require.NoError(t, err)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	}
	serial := int64(1)
	rotate := func(commonName string) {
		serial++
		certPEM, keyPEM := issue(commonName, serial)
		require.NoError(t, os.WriteFile(certFile, certPEM, 0600))
		require.NoError(t, os.WriteFile(keyFile, keyPEM, 0600))
	}
	certPEM, keyPEM := issue("leaf-before-rotation", serial)
	require.NoError(t, os.WriteFile(certFile, certPEM, 0600))
	require.NoError(t, os.WriteFile(keyFile, keyPEM, 0600))
	caPEM, _ := issue("client-ca", 900)
	require.NoError(t, os.WriteFile(caFile, caPEM, 0600))
	r, err := certreload.New(certFile, keyFile, caFile, os.ReadFile)
	require.NoError(t, err)
	return r, rotate
}

func rotatingTLSConfig(r *certreload.Reloader) *tls.Config {
	return &tls.Config{GetCertificate: r.GetCertificate, GetConfigForClient: r.GetConfigForClient, ClientCAs: r.ClientCAs(), MinVersion: tls.VersionTLS13}
}

func servedLeaf(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	tlsListener := tls.NewListener(listener, cfg)
	go func() {
		conn, err := tlsListener.Accept()
		if err == nil {
			_ = conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	require.NoError(t, err)
	defer conn.Close()
	state := conn.ConnectionState()
	require.Empty(t, state.ServerName, "client sent SNI; the no-SNI path is not under test")
	return state.PeerCertificates[0].Subject.CommonName
}

// Both custody listeners must accept a host that supplies its identity through
// GetCertificate. Demanding a populated Certificates slice forces a rotating
// host to leave a startup snapshot there, which crypto/tls then prefers for
// every client that sends no SNI — silently defeating the reload.
func TestCustodyListenersAcceptRotatingIdentity(t *testing.T) {
	authority, m := configuredCustodyAuthority(t), custodyMinter(t)
	r, _ := rotatingMount(t)
	config := ExecutionCustodyConfig{Authority: authority, Minter: m, Store: stubCustodyStore{}, Cipher: stubCipher{}, Consumers: custodyConsumerPolicy()}

	rotating := rotatingTLSConfig(r)
	require.Empty(t, rotating.Certificates, "the host must not pin a startup snapshot")
	server, err := NewExecutionCustodyServer(config, rotating)
	require.NoError(t, err)
	require.Empty(t, server.TLSConfig.Certificates, "hardening reintroduced a static identity")
	require.NotNil(t, server.TLSConfig.GetCertificate)
	_, err = NewCustodyWorkContextGRPC(authority, m, true, rotating)
	require.NoError(t, err)

	none := &tls.Config{ClientCAs: r.ClientCAs(), MinVersion: tls.VersionTLS13}
	_, err = NewExecutionCustodyServer(config, none)
	require.Error(t, err, "listener accepted a config with no server identity at all")
	_, err = NewCustodyWorkContextGRPC(authority, m, true, none)
	require.Error(t, err, "revision listener accepted a config with no server identity at all")
}

// The end-to-end shape: a client dialling the listener by IP sends no SNI, and
// must still be handed the leaf that is on disk now rather than the one that was
// there when the process started.
func TestCustodyServerServesRotatedLeafWithoutSNI(t *testing.T) {
	authority, m := configuredCustodyAuthority(t), custodyMinter(t)
	r, rotate := rotatingMount(t)
	server, err := NewExecutionCustodyServer(ExecutionCustodyConfig{Authority: authority, Minter: m, Store: stubCustodyStore{}, Cipher: stubCipher{}, Consumers: custodyConsumerPolicy()}, rotatingTLSConfig(r))
	require.NoError(t, err)

	require.Equal(t, "leaf-before-rotation", servedLeaf(t, server.TLSConfig))
	rotate("leaf-after-rotation")
	require.Eventually(t, func() bool { return servedLeaf(t, server.TLSConfig) == "leaf-after-rotation" }, 4*certreload.CheckInterval, certreload.CheckInterval/4,
		"listener kept serving the startup leaf after the mount rotated")
}

// The client CA hook exists so a re-issued bundle reaches a live listener. It
// must carry trust material only: a host hook is not permitted to relax client
// authentication, the minimum protocol version or certificate verification.
func TestCustodyTrustRotationCarriesOnlyTrustMaterial(t *testing.T) {
	replacement := x509.NewCertPool()
	certPEM, _ := func() ([]byte, []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		template := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "client-ca-after"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		require.NoError(t, err)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
	}()
	require.True(t, replacement.AppendCertsFromPEM(certPEM))

	hardened := &tls.Config{ClientAuth: tls.VerifyClientCertIfGiven, MinVersion: tls.VersionTLS13, ClientCAs: x509.NewCertPool()}
	custodyTrustRotation(hardened, func(*tls.ClientHelloInfo) (*tls.Config, error) {
		// A host hook that tries to downgrade every hardened field.
		return &tls.Config{ClientCAs: replacement, ClientAuth: tls.NoClientCert, MinVersion: tls.VersionTLS10, InsecureSkipVerify: true}, nil
	})
	got, err := hardened.GetConfigForClient(nil)
	require.NoError(t, err)
	require.True(t, got.ClientCAs.Equal(replacement), "re-issued client CA bundle not carried onto the listener")
	require.Equal(t, tls.VerifyClientCertIfGiven, got.ClientAuth, "host hook weakened client authentication")
	require.Equal(t, uint16(tls.VersionTLS13), got.MinVersion, "host hook weakened the minimum TLS version")
	require.False(t, got.InsecureSkipVerify, "host hook disabled verification")
	require.Nil(t, got.GetConfigForClient, "rotated config recurses into the host hook")

	// A hook that offers no trust material leaves the hardened config in force.
	custodyTrustRotation(hardened, func(*tls.ClientHelloInfo) (*tls.Config, error) { return nil, nil })
	got, err = hardened.GetConfigForClient(nil)
	require.NoError(t, err)
	require.Nil(t, got)

	// No hook at all keeps the previous refusal of per-connection configuration.
	custodyTrustRotation(hardened, nil)
	require.Nil(t, hardened.GetConfigForClient)
}

// The trust-rotation hook replaces the whole tls.Config for the handshake, so it
// must carry the listener's ALPN list too. net/http derives that list into a
// clone the hook cannot see, and grpc-go re-applies its own to whatever the hook
// returns; both listeners must still negotiate h2 while rotation is active.
func TestCustodyListenersNegotiateALPNWhileRotating(t *testing.T) {
	authority, m := configuredCustodyAuthority(t), custodyMinter(t)
	r, _ := rotatingMount(t)

	negotiated := func(addr string, offer []string) string {
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, NextProtos: offer})
		require.NoError(t, err)
		defer conn.Close()
		return conn.ConnectionState().NegotiatedProtocol
	}

	server, err := NewExecutionCustodyServer(ExecutionCustodyConfig{Authority: authority, Minter: m, Store: stubCustodyStore{}, Cipher: stubCipher{}, Consumers: custodyConsumerPolicy()}, rotatingTLSConfig(r))
	require.NoError(t, err)
	brokerListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = server.ServeTLS(brokerListener, "", "") }()
	defer server.Close()
	require.Equal(t, "h2", negotiated(brokerListener.Addr().String(), []string{"h2", "http/1.1"}),
		"broker dropped HTTP/2 on a rotating listener")
	require.Equal(t, "http/1.1", negotiated(brokerListener.Addr().String(), []string{"http/1.1"}),
		"broker refused an HTTP/1.1-only client")

	revision, err := NewCustodyWorkContextGRPC(authority, m, true, rotatingTLSConfig(r))
	require.NoError(t, err)
	revisionListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = revision.Serve(revisionListener) }()
	defer revision.Stop()
	require.Equal(t, "h2", negotiated(revisionListener.Addr().String(), []string{"h2"}),
		"revision listener dropped HTTP/2 on a rotating listener")
}
