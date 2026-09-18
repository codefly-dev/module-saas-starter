package adapters

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustodyIdentityIsStaticOrReloading(t *testing.T) {
	reloading := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &tls.Certificate{}, nil }
	require.False(t, hasCustodyIdentity(nil))
	require.False(t, hasCustodyIdentity(&tls.Config{ClientCAs: nil}))
	require.True(t, hasCustodyIdentity(&tls.Config{Certificates: []tls.Certificate{{}}}))
	require.True(t, hasCustodyIdentity(&tls.Config{GetCertificate: reloading}))
}

// A static leaf handed over beside a reloading GetCertificate would be served
// to every peer that sends no SNI, so the listener drops it; a static-only
// identity is kept as is.
func TestCustodyListenerNeverShadowsReloaderWithStaticLeaf(t *testing.T) {
	static := &tls.Config{Certificates: []tls.Certificate{{}}, MinVersion: tls.VersionTLS12, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return nil, nil }}
	listener := custodyListenerTLS(static)
	require.Len(t, listener.Certificates, 1)
	require.Nil(t, listener.GetCertificate)
	require.Equal(t, uint16(tls.VersionTLS13), listener.MinVersion)
	require.Nil(t, listener.GetConfigForClient)
	require.Len(t, static.Certificates, 1, "the host's config must not be mutated")

	shadowed := static.Clone()
	shadowed.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &tls.Certificate{}, nil }
	listener = custodyListenerTLS(shadowed)
	require.Empty(t, listener.Certificates)
	require.NotNil(t, listener.GetCertificate)
	require.Equal(t, uint16(tls.VersionTLS13), listener.MinVersion)
	require.Nil(t, listener.GetConfigForClient)
	require.Len(t, shadowed.Certificates, 1, "the host's config must not be mutated")
}
