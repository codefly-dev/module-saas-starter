package infra

import (
	"accounts/pkg/vaultconnection"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// A Transit call whose token Vault has revoked logs in again and is retried
// once, instead of failing every call until the cached token's lease ends.
func TestVaultClientLogsInAgainWhenVaultRefusesItsToken(t *testing.T) {
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
	client := &VaultClient{address: connection.Address, transitKey: "api-keys", connection: connection}

	_, err = client.request(context.Background(), http.MethodPost, "/v1/transit/encrypt/api-keys", `{"plaintext":"eA=="}`)
	require.NoError(t, err)
	require.Equal(t, []string{"token-1", "token-2"}, transit)
	require.Equal(t, 2, logins)
}
