package ed25519minter_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	ed25519minter "accounts/pkg/auth/ed25519"
)

// Without the operator opt-in, every non-loopback http:// address is rejected
// before any request is made — over cleartext http both the Vault token and the
// returned signing key leak. A ".svc" name is not special: the suffix says
// nothing about whether the peer is actually mesh-protected, and
// vault.svc.example.com is externally routable despite the ".svc." label, so
// neither may be admitted on the strength of its name alone.
func TestLoadKeyFromVault_RejectsCleartextNonLoopback(t *testing.T) {
	for _, addr := range []string{
		"http://vault.internal:8200",
		"http://10.0.0.5:8200",
		"http://vault.example.com",
		"http://vault.svc.example.com:8200",
		"http://vault.example.svc:8200",
		"http://vault.example.svc.cluster.local:8200",
	} {
		_, err := ed25519minter.LoadKeyFromVault(context.Background(), ed25519minter.VaultKeyLoaderConfig{
			Address: addr,
			Token:   "s.token",
		})
		require.Error(t, err, addr)
		require.Contains(t, err.Error(), "cleartext http", addr)
	}
}

type errRoundTripper struct{}

func (errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dialed")
}

// With the composition's mesh assertion, a cleartext in-cluster Service address
// is admitted — the cell's Vault listens in-mesh without TLS of its own, so
// there is no https URL to give. Validation runs before the request, so an
// admitted host reaches the HTTP transport (here a stub that always errors) and
// fails at the fetch step, never with the cleartext refusal.
func TestLoadKeyFromVault_AdmitsAnInClusterServiceWhenTheMeshIsAsserted(t *testing.T) {
	for _, addr := range []string{
		"http://vault.vault.svc:8200",
		"http://vault.vault.svc.cluster.local:8200",
		"http://vault.example.svc:8200",
	} {
		_, err := ed25519minter.LoadKeyFromVault(context.Background(), ed25519minter.VaultKeyLoaderConfig{
			Address:       addr,
			Token:         "s.token",
			MeshProtected: true,
			HTTPClient:    &http.Client{Transport: errRoundTripper{}},
		})
		require.Error(t, err, addr)
		require.NotContains(t, err.Error(), "cleartext http", addr)
		require.Contains(t, err.Error(), "fetch vault key", addr)
	}
}

// The assertion covers only what a mesh can cover. It is not a blanket opt-in:
// an external name, a bare IP or a short name stays refused with it in place,
// because nothing the composition said covers the wire to those — and
// "vault.svc.example.com" is externally routable despite its label.
func TestLoadKeyFromVault_MeshAssertionIsNotABlanketOptIn(t *testing.T) {
	for _, addr := range []string{
		"http://vault.internal:8200",
		"http://10.0.0.5:8200",
		"http://vault.example.com:8200",
		"http://vault.svc.example.com:8200",
		"http://vault:8200",
	} {
		_, err := ed25519minter.LoadKeyFromVault(context.Background(), ed25519minter.VaultKeyLoaderConfig{
			Address:       addr,
			Token:         "s.token",
			MeshProtected: true,
			HTTPClient:    &http.Client{Transport: errRoundTripper{}},
		})
		require.Error(t, err, addr)
		require.Contains(t, err.Error(), "cleartext http", addr)
	}
}

func TestLoadKeyFromVault_RejectsNonHTTPScheme(t *testing.T) {
	_, err := ed25519minter.LoadKeyFromVault(context.Background(), ed25519minter.VaultKeyLoaderConfig{
		Address: "ftp://vault.internal:8200",
		Token:   "s.token",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "scheme")
}

// Loopback http stays allowed so the dev fixture keeps working: an httptest
// server binds 127.0.0.1, so a successful fetch proves validation admitted it.
func TestLoadKeyFromVault_AllowsLoopbackHTTP(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var envelope struct {
		Data struct {
			Data struct {
				PrivateKey string `json:"private_key"`
				PublicKey  string `json:"public_key"`
			} `json:"data"`
		} `json:"data"`
	}
	envelope.Data.Data.PrivateKey = base64.StdEncoding.EncodeToString(priv.Seed())
	envelope.Data.Data.PublicKey = base64.StdEncoding.EncodeToString(pub)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(envelope)
	}))
	defer srv.Close()

	got, err := ed25519minter.LoadKeyFromVault(context.Background(), ed25519minter.VaultKeyLoaderConfig{
		Address: srv.URL, // http://127.0.0.1:<port>
		Token:   "s.token",
	})
	require.NoError(t, err)
	require.Equal(t, ed25519.PrivateKey(priv), got)
}
