package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"accounts/pkg/email"

	"github.com/stretchr/testify/require"
)

func TestBuildDiscoveredOIDCStackSkipsDiscoveryWhenEndpointsPinned(t *testing.T) {
	clearAuthProviderEnvironment(t)
	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID", "client_01TEST")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET", "sk_test")
	// An unreachable issuer: when both the key set and the token endpoint are
	// pinned, an air-gapped deploy must start without ever contacting the
	// well-known endpoint. Before discovery was made conditional, this issuer was
	// fetched anyway and startup failed closed.
	setIdentityConfiguration(t, "IDENTITY_ISSUER", "https://identity.invalid/tenant")
	setIdentityConfiguration(t, "IDENTITY_JWKS_URL", "https://identity.invalid/tenant/jwks")
	setIdentityConfiguration(t, "IDENTITY_TOKEN_URL", "https://identity.invalid/tenant/token")
	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID_CLAIM", "client_id")

	validator, exchanger, err := buildProviderStack("workos", "")
	require.NoError(t, err)
	require.NotNil(t, validator)
	require.NotNil(t, exchanger)
}

func TestBuildDiscoveredOIDCStackDiscoversMissingEndpoints(t *testing.T) {
	clearAuthProviderEnvironment(t)

	var issuer string
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/openid-configuration", r.URL.Path)
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issuer": "` + issuer + `",
			"authorization_endpoint": "` + issuer + `/authorize",
			"token_endpoint": "` + issuer + `/token",
			"jwks_uri": "` + issuer + `/jwks"
		}`))
	}))
	defer server.Close()
	issuer = server.URL

	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID", "client_01TEST")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET", "sk_test")
	setIdentityConfiguration(t, "IDENTITY_ISSUER", issuer)
	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID_CLAIM", "client_id")
	// No IDENTITY_JWKS_URL / IDENTITY_TOKEN_URL: both must be filled from the
	// provider's published metadata.

	validator, exchanger, err := buildProviderStack("workos", "")
	require.NoError(t, err)
	require.NotNil(t, validator)
	require.NotNil(t, exchanger)
	require.Equal(t, 1, hits, "provider metadata must be discovered exactly once")
}

func TestConfiguredEmailSenderDefaultsToLog(t *testing.T) {
	t.Setenv("EMAIL_PROVIDER", "")
	sender, err := configuredEmailSender(t.Context())
	require.NoError(t, err)
	require.IsType(t, &email.LogSender{}, sender)
}

func TestConfiguredEmailSenderFailsClosed(t *testing.T) {
	t.Setenv("EMAIL_PROVIDER", "log")
	t.Setenv("RESEND_API_KEY", "re_accidental")
	_, err := configuredEmailSender(t.Context())
	require.ErrorContains(t, err, "credentials are present")

	t.Setenv("EMAIL_PROVIDER", "resend")
	t.Setenv("RESEND_API_KEY", "")
	t.Setenv("RESEND_WEBHOOK_SECRET", "")
	_, err = configuredEmailSender(t.Context())
	require.ErrorContains(t, err, "RESEND_API_KEY")

	t.Setenv("RESEND_API_KEY", "re_test")
	t.Setenv("RESEND_WEBHOOK_SECRET", "whsec_test")
	t.Setenv("RESEND_API_BASE", "http://localhost:9999")
	sender, err := configuredEmailSender(t.Context())
	require.NoError(t, err)
	require.IsType(t, &email.ResendSender{}, sender)

	t.Setenv("EMAIL_PROVIDER", "typo")
	_, err = configuredEmailSender(t.Context())
	require.ErrorContains(t, err, "EMAIL_PROVIDER must be one of")
}

func TestConfiguredAbuseVerifierFailsClosed(t *testing.T) {
	t.Setenv("ABUSE_PROTECTION_MODE", "disabled")
	t.Setenv("TURNSTILE_SECRET_KEY", "accidental")
	_, err := configuredAbuseVerifier()
	require.ErrorContains(t, err, "while ABUSE_PROTECTION_MODE is disabled")

	t.Setenv("ABUSE_PROTECTION_MODE", "turnstile")
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	_, err = configuredAbuseVerifier()
	require.ErrorContains(t, err, "secret key")

	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	t.Setenv("TURNSTILE_ALLOWED_HOSTNAMES", "localhost,app.example.com")
	t.Setenv("TURNSTILE_VERIFY_URL", "http://localhost:9999/siteverify")
	verifier, err := configuredAbuseVerifier()
	require.NoError(t, err)
	require.NotNil(t, verifier)
}
