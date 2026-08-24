package ed25519minter_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	ed25519minter "accounts/pkg/auth/ed25519"
)

// A non-loopback http:// address must be rejected before any request is made —
// over cleartext http both the Vault token and the returned signing key leak.
func TestLoadKeyFromVault_RejectsCleartextNonLoopback(t *testing.T) {
	for _, addr := range []string{
		"http://vault.internal:8200",
		"http://10.0.0.5:8200",
		"http://vault.example.com",
	} {
		_, err := ed25519minter.LoadKeyFromVault(context.Background(), ed25519minter.VaultKeyLoaderConfig{
			Address: addr,
			Token:   "s.token",
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
