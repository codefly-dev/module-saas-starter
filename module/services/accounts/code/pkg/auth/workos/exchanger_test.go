package workos_test

import (
	"accounts/pkg/auth"
	workosauth "accounts/pkg/auth/workos"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type validator struct {
	claims *auth.Claims
	token  string
	err    error
}

func (v *validator) Validate(_ context.Context, token string) (*auth.Claims, error) {
	v.token = token
	if v.err != nil {
		return nil, v.err
	}
	copy := *v.claims
	return &copy, nil
}

func TestNewExchangerRequiresCompleteConfiguration(t *testing.T) {
	for _, cfg := range []workosauth.Config{
		{},
		{TokenURL: "https://workos.test", ClientID: "client", ClientSecret: "secret"},
		{TokenURL: "https://workos.test", ClientID: "client", Validator: &validator{}},
	} {
		_, err := workosauth.NewExchanger(cfg)
		require.Error(t, err)
	}
}

func TestExchangeJoinsVerifiedTokenAndWorkOSUser(t *testing.T) {
	var received map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":    "signed-workos-token",
			"organization_id": "org_123",
			"user": map[string]any{
				"id":             "user_123",
				"email":          "OWNER@Example.com",
				"email_verified": true,
			},
		})
	}))
	t.Cleanup(server.Close)

	tokenValidator := &validator{claims: &auth.Claims{
		Provider:      "workos",
		Subject:       "user_123",
		ProviderOrgID: "org_123",
	}}
	exchanger, err := workosauth.NewExchanger(workosauth.Config{
		TokenURL:     server.URL,
		ClientID:     "client_123",
		ClientSecret: "secret_123",
		Validator:    tokenValidator,
	})
	require.NoError(t, err)

	tokens, err := exchanger.Exchange(
		context.Background(),
		"authorization-code",
		"http://localhost:21931/auth/callback",
		"pkce-verifier",
	)
	require.NoError(t, err)
	require.Equal(t, "authorization_code", received["grant_type"])
	require.Equal(t, "authorization-code", received["code"])
	require.Equal(t, "pkce-verifier", received["code_verifier"])
	require.NotContains(t, received, "redirect_uri")
	require.Equal(t, "signed-workos-token", tokenValidator.token)
	require.Equal(t, "owner@example.com", tokens.Claims.Email)
	require.Equal(t, "user_123", tokens.Claims.Subject)
	require.True(t, tokens.Claims.EmailVerified, "the provider's verification fact is propagated to the claims")
}

func TestExchangeRejectsSubjectMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "signed-workos-token",
			"user": map[string]any{
				"id":             "different-user",
				"email":          "owner@example.com",
				"email_verified": true,
			},
		})
	}))
	t.Cleanup(server.Close)

	exchanger, err := workosauth.NewExchanger(workosauth.Config{
		TokenURL:     server.URL,
		ClientID:     "client_123",
		ClientSecret: "secret_123",
		Validator: &validator{claims: &auth.Claims{
			Provider: "workos",
			Subject:  "user_123",
		}},
	})
	require.NoError(t, err)

	_, err = exchanger.Exchange(context.Background(), "code", "https://app/callback", "")
	require.ErrorContains(t, err, "subject")
}

func TestExchangeRejectsUnverifiedEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "signed-workos-token",
			"user": map[string]any{
				"id":             "user_123",
				"email":          "owner@example.com",
				"email_verified": false,
			},
		})
	}))
	t.Cleanup(server.Close)

	exchanger, err := workosauth.NewExchanger(workosauth.Config{
		TokenURL:     server.URL,
		ClientID:     "client_123",
		ClientSecret: "secret_123",
		Validator:    &validator{claims: &auth.Claims{}},
	})
	require.NoError(t, err)

	_, err = exchanger.Exchange(context.Background(), "code", "https://app/callback", "")
	require.ErrorContains(t, err, "not verified")
}

func TestExchangeRejectsOrganizationMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":    "signed-workos-token",
			"organization_id": "response-org",
			"user": map[string]any{
				"id":             "user_123",
				"email":          "owner@example.com",
				"email_verified": true,
			},
		})
	}))
	t.Cleanup(server.Close)

	exchanger, err := workosauth.NewExchanger(workosauth.Config{
		TokenURL:     server.URL,
		ClientID:     "client_123",
		ClientSecret: "secret_123",
		Validator: &validator{claims: &auth.Claims{
			Provider:      "workos",
			Subject:       "user_123",
			ProviderOrgID: "token-org",
		}},
	})
	require.NoError(t, err)

	_, err = exchanger.Exchange(context.Background(), "code", "https://app/callback", "")
	require.ErrorContains(t, err, "organization")
}

func TestExchangeRejectsInvalidSignedToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "forged-token",
			"user": map[string]any{
				"id":             "user_123",
				"email":          "owner@example.com",
				"email_verified": true,
			},
		})
	}))
	t.Cleanup(server.Close)

	exchanger, err := workosauth.NewExchanger(workosauth.Config{
		TokenURL:     server.URL,
		ClientID:     "client_123",
		ClientSecret: "secret_123",
		Validator:    &validator{err: errors.New("signature rejected")},
	})
	require.NoError(t, err)

	_, err = exchanger.Exchange(context.Background(), "code", "https://app/callback", "")
	require.ErrorContains(t, err, "validate access token")
}

func TestExchangeDoesNotLeakProviderErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"do-not-leak-this-provider-detail"}`))
	}))
	t.Cleanup(server.Close)

	exchanger, err := workosauth.NewExchanger(workosauth.Config{
		TokenURL:     server.URL,
		ClientID:     "client_123",
		ClientSecret: "secret_123",
		Validator:    &validator{claims: &auth.Claims{}},
	})
	require.NoError(t, err)

	_, err = exchanger.Exchange(context.Background(), "code", "https://app/callback", "")
	require.ErrorContains(t, err, "http 401")
	require.NotContains(t, err.Error(), "do-not-leak")
}
