package keyservice

// The Vault backend's own behaviour: the Transit wire shape, and the AppRole
// re-login that a revoked token triggers. The properties it shares with every
// backend — the round trip, the purpose binding, an outage that is not an
// invalid credential — are asserted once, in the conformance suite.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/vaultconnection"
)

func TestVaultSealerUsesTheTransitEndpoints(t *testing.T) {
	var encryptedPayload string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "test-token", r.Header.Get("X-Vault-Token"))
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/transit/encrypt/api-keys":
			encryptedPayload = body["plaintext"]
			_, _ = fmt.Fprint(w, `{"data":{"ciphertext":"vault:v7:test-ciphertext"}}`)
		case "/v1/transit/decrypt/api-keys":
			require.Equal(t, "vault:v7:test-ciphertext", body["ciphertext"])
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"plaintext": encryptedPayload}})
		case "/v1/transit/hmac/api-keys":
			_, _ = fmt.Fprint(w, `{"data":{"hmac":"vault:v1:abc123"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cipher, err := NewCipher(NewVaultSealerDirect(server.URL, "test-token"), nil)
	require.NoError(t, err)

	stored, err := cipher.EncryptSecret(t.Context(), "mfa-totp", "TOP-SECRET-SEED")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(stored, EnvelopePrefix(TagVaultTransit)))
	require.NotContains(t, stored, "TOP-SECRET-SEED")

	plaintext, err := cipher.DecryptSecret(t.Context(), "mfa-totp", stored)
	require.NoError(t, err)
	require.Equal(t, "TOP-SECRET-SEED", plaintext)

	hash, err := cipher.HashKey(t.Context(), "cfly_sk_live_secret")
	require.NoError(t, err)
	require.Equal(t, "vault:v1:abc123", hash)
}

// The stored form is unchanged from before the key service existed: a value
// sealed by the pre-seam code must still open, because nothing re-seals it.
func TestVaultSealerOpensThePreSeamStoredForm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "vault:v1:legacy", body["ciphertext"])
		payload, err := json.Marshal(purposeBoundPayload{Purpose: "mfa-totp", Value: "LEGACYSEED"})
		require.NoError(t, err)
		writeVaultData(w, map[string]any{"plaintext": base64.StdEncoding.EncodeToString(payload)})
	}))
	defer server.Close()

	cipher, err := NewCipher(NewVaultSealerDirect(server.URL, "token"), nil)
	require.NoError(t, err)
	// Exactly what rows written before this change hold.
	stored := "cfs1:vault-transit:" + base64.RawURLEncoding.EncodeToString([]byte("vault:v1:legacy"))
	plaintext, err := cipher.DecryptSecret(t.Context(), "mfa-totp", stored)
	require.NoError(t, err)
	require.Equal(t, "LEGACYSEED", plaintext)
}

// There is intentionally no local fallback for the keyed hash: a silent
// downgrade to an unkeyed digest would produce a hash that never matches the one
// written for the same key once the key service recovers.
func TestVaultSealerFailsClosedOnTheKeyedHash(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"vault errors": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "sealed", http.StatusInternalServerError)
		},
		"vault answers without an hmac": func(w http.ResponseWriter, _ *http.Request) {
			writeVaultData(w, map[string]any{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			_, err := NewVaultSealerDirect(server.URL, "token").MAC(t.Context(), "cfly_sk_live_secret")
			require.Error(t, err)
		})
	}
}

func TestVaultSealerRefusesAnUndecodablePayload(t *testing.T) {
	sealer := NewVaultSealerDirect("http://unused.invalid", "token")
	_, err := sealer.Open(t.Context(), "mfa-totp", "not-base64-url!!")
	require.ErrorIs(t, err, business.ErrInvalidSecretEnvelope)
}

// A Transit call whose token Vault has revoked logs in again and is retried
// once, instead of failing every call until the cached token's lease ends.
func TestVaultSealerLogsInAgainWhenVaultRefusesItsToken(t *testing.T) {
	var mu sync.Mutex
	logins, transit := 0, []string{}
	// Plain http on loopback: the binding has no CA to pin anywhere any more,
	// so a TLS fixture would exercise a transport the contract does not have.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/auth/approle/login":
			logins++
			_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{
				"client_token": fmt.Sprintf("token-%d", logins), "lease_duration": 3600, "renewable": true,
			}})
		case strings.HasPrefix(r.URL.Path, "/v1/transit/encrypt/"):
			token := r.Header.Get("X-Vault-Token")
			transit = append(transit, token)
			if token == "token-1" { // revoked out from under the client
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = fmt.Fprint(w, `{"data":{"ciphertext":"vault:v1:sealed"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	connection, err := vaultconnection.New(vaultconnection.Config{
		Address: server.URL,
		AppRole: &vaultconnection.AppRoleAuth{RoleID: "role-fixture", SecretID: "secret-fixture"},
		Runtime: vaultconnection.RuntimeLocal,
	})
	require.NoError(t, err)
	sealer := &VaultSealer{address: connection.Address, connection: connection, client: connection.Client}

	_, err = sealer.request(context.Background(), "/v1/transit/encrypt/api-keys", `{"plaintext":"eA=="}`)
	require.NoError(t, err)
	require.Equal(t, []string{"token-1", "token-2"}, transit)
	require.Equal(t, 2, logins)
}

func TestVaultSealerHealthAcceptsTheUnsealedCodes(t *testing.T) {
	for status, healthy := range map[int]bool{200: true, 429: true, 472: true, 473: true, 500: false, 501: false, 503: false} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/v1/sys/health", r.URL.Path)
			w.WriteHeader(status)
		}))
		err := NewVaultSealerDirect(server.URL, "token").Health(t.Context())
		if healthy {
			require.NoError(t, err, "HTTP %d signals a reachable, un-sealed Vault", status)
		} else {
			require.Error(t, err, "HTTP %d is not healthy", status)
		}
		server.Close()
	}
}
