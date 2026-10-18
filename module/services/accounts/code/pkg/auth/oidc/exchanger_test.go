package oidc_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/auth/oidc"
	"accounts/pkg/business"
)

// Fake OAuth token endpoint that mirrors the standard response shape.
func newFakeTokenEndpoint(t *testing.T, expected map[string]string, resp map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		require.NoError(t, r.ParseForm())
		for k, v := range expected {
			require.Equal(t, v, r.PostForm.Get(k), "form param %q", k)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestExchanger_Happy(t *testing.T) {
	ctx := context.Background()
	server := newFakeTokenEndpoint(t,
		map[string]string{
			"grant_type":    "authorization_code",
			"code":          "test-code",
			"redirect_uri":  "https://app.acme.com/auth/callback",
			"client_id":     "client_01ABC",
			"client_secret": "secret_xyz",
		},
		map[string]any{
			"access_token": "opaque-access-token",
			"id_token":     "eyJidToken",
			"token_type":   "Bearer",
			"expires_in":   900,
		},
	)
	defer server.Close()

	ex, err := oidc.NewExchanger(oidc.ExchangerConfig{
		TokenURL:     server.URL,
		ClientID:     "client_01ABC",
		ClientSecret: "secret_xyz",
	})
	require.NoError(t, err)

	resp, err := ex.Exchange(ctx, "test-code", "https://app.acme.com/auth/callback", "")
	require.NoError(t, err)
	require.Equal(t, "opaque-access-token", resp.AccessToken)
	require.Equal(t, "eyJidToken", resp.IDToken)
	require.Equal(t, "Bearer", resp.TokenType)
	require.Equal(t, int64(900), resp.ExpiresIn)
}

func TestExchanger_NonOKResponse(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer server.Close()

	ex, _ := oidc.NewExchanger(oidc.ExchangerConfig{
		TokenURL:     server.URL,
		ClientID:     "id",
		ClientSecret: "secret",
	})
	_, err := ex.Exchange(ctx, "bad-code", "https://x", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_grant")
}

func TestExchanger_MissingAccessToken(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": "only-id-token"})
	}))
	defer server.Close()

	ex, _ := oidc.NewExchanger(oidc.ExchangerConfig{
		TokenURL:     server.URL,
		ClientID:     "id",
		ClientSecret: "secret",
	})
	_, err := ex.Exchange(ctx, "code", "https://x", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing access_token")
}

func TestExchanger_NewExchanger_Validation(t *testing.T) {
	_, err := oidc.NewExchanger(oidc.ExchangerConfig{})
	require.Error(t, err)

	_, err = oidc.NewExchanger(oidc.ExchangerConfig{TokenURL: "https://x"})
	require.Error(t, err)

	_, err = oidc.NewExchanger(oidc.ExchangerConfig{
		TokenURL: "https://x",
		ClientID: "id",
	})
	require.Error(t, err, "secret required")
}

func TestExchanger_RefusalCarriesStatusAndCodeNeverTheBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"client secret provider-detail-xyz is wrong"}`))
	}))
	defer server.Close()

	ex, _ := oidc.NewExchanger(oidc.ExchangerConfig{TokenURL: server.URL, ClientID: "id", ClientSecret: "secret"})
	_, err := ex.Exchange(context.Background(), "bad-code", "https://x", "")
	var refused *auth.TokenEndpointError
	require.ErrorAs(t, err, &refused)
	require.Equal(t, auth.TokenEndpointError{Status: http.StatusUnauthorized, Code: "invalid_client"}, *refused)
	require.NotContains(t, err.Error(), "provider-detail-xyz")
}

func TestExchanger_WithClientSecretChangesOnlyTheSecret(t *testing.T) {
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		got = append(got, r.PostForm.Get("client_id")+"/"+r.PostForm.Get("client_secret")+"/"+r.PostForm.Get("code"))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer server.Close()

	ex, err := oidc.NewExchanger(oidc.ExchangerConfig{TokenURL: server.URL, ClientID: "id", ClientSecret: "real"})
	require.NoError(t, err)
	exchanger := oidc.AsBusinessExchanger(ex)
	control, err := exchanger.(interface {
		WithClientSecretFrom(func(string) (string, error)) (business.CodeExchanger, error)
	}).WithClientSecretFrom(func(configured string) (string, error) { return configured + "-control", nil })
	require.NoError(t, err)
	_, _ = control.Exchange(context.Background(), "c", "https://x", "")
	_, _ = exchanger.Exchange(context.Background(), "c", "https://x", "")
	require.Equal(t, []string{"id/real-control/c", "id/real/c"}, got, "the control leaves the original untouched")
}
