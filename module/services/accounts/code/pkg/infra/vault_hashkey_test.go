package infra

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashKeyReturnsVaultHMAC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/transit/hmac/api-keys", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"hmac":"vault:v1:abc123"}}`))
	}))
	defer server.Close()

	client := NewVaultClientDirect(server.URL, "test-token")
	hash, err := client.HashKey(t.Context(), "cfly_sk_live_secret")
	require.NoError(t, err)
	require.Equal(t, "vault:v1:abc123", hash)
}

// TestHashKeyFailsClosedOnVaultError proves HashKey no longer downgrades to a
// local SHA-256 when Vault errors: a silent downgrade would produce a hash that
// never matches the HMAC written for the same key once Vault recovers.
func TestHashKeyFailsClosedOnVaultError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sealed", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewVaultClientDirect(server.URL, "test-token")
	_, err := client.HashKey(t.Context(), "cfly_sk_live_secret")
	require.Error(t, err)
}

func TestHashKeyFailsClosedOnMissingHMAC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()

	client := NewVaultClientDirect(server.URL, "test-token")
	_, err := client.HashKey(t.Context(), "cfly_sk_live_secret")
	require.ErrorContains(t, err, "missing hmac")
}
