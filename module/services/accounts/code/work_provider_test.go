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

// setEmailConfiguration sets one key of the `email` workspace group the way the
// Codefly runtime delivers it; secret selects the group's secret namespace.
func setEmailConfiguration(t *testing.T, key, value string, secret bool) {
	t.Helper()
	namespace := "CODEFLY__WORKSPACE_CONFIGURATION__EMAIL__"
	if secret {
		namespace = "CODEFLY__WORKSPACE_SECRET_CONFIGURATION__EMAIL__"
	}
	t.Setenv(namespace+key, value)
}

func clearEmailConfiguration(t *testing.T) {
	t.Helper()
	for _, key := range []string{"EMAIL_PROVIDER", "EMAIL_FROM", "RESEND_API_BASE", "RESEND_API_KEY", "RESEND_WEBHOOK_SECRET"} {
		setEmailConfiguration(t, key, "", false)
		setEmailConfiguration(t, key, "", true)
		// A raw process variable is not an authority; keep one set so a
		// regression to os.Getenv shows up as a wrong selection.
		t.Setenv(key, "")
	}
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__APPLICATION__EMAIL_FROM", "")
}

func TestConfiguredEmailDefaultsToLogOnlyLocally(t *testing.T) {
	clearEmailConfiguration(t)
	config, err := configuredEmail(t.Context(), true)
	require.NoError(t, err)
	require.IsType(t, &email.LogSender{}, config.sender)
	require.Equal(t, "no-reply@localhost", config.from)

	_, err = configuredEmail(t.Context(), false)
	require.ErrorContains(t, err, "EMAIL_PROVIDER is required outside the local environment")

	setEmailConfiguration(t, "EMAIL_PROVIDER", "log", false)
	_, err = configuredEmail(t.Context(), false)
	require.ErrorContains(t, err, "refused outside the local environment")
}

func TestConfiguredEmailIgnoresRawProcessVariables(t *testing.T) {
	clearEmailConfiguration(t)
	// The variables a deployment that bypassed the workspace group would have
	// set. They select nothing: outside local, the group is still unset.
	t.Setenv("EMAIL_PROVIDER", "resend")
	t.Setenv("RESEND_API_KEY", "re_raw")
	t.Setenv("RESEND_WEBHOOK_SECRET", "whsec_raw")
	_, err := configuredEmail(t.Context(), false)
	require.ErrorContains(t, err, "EMAIL_PROVIDER is required outside the local environment")
}

func TestConfiguredEmailDisabledSelectsNoSender(t *testing.T) {
	clearEmailConfiguration(t)
	setEmailConfiguration(t, "EMAIL_PROVIDER", "disabled", false)
	config, err := configuredEmail(t.Context(), false)
	require.NoError(t, err)
	require.Nil(t, config.sender)

	setEmailConfiguration(t, "RESEND_API_KEY", "re_accidental", true)
	_, err = configuredEmail(t.Context(), false)
	require.ErrorContains(t, err, "credentials are present while EMAIL_PROVIDER is disabled")
}

func TestConfiguredEmailFailsClosed(t *testing.T) {
	clearEmailConfiguration(t)
	setEmailConfiguration(t, "EMAIL_PROVIDER", "log", false)
	setEmailConfiguration(t, "RESEND_API_KEY", "re_accidental", true)
	_, err := configuredEmail(t.Context(), true)
	require.ErrorContains(t, err, "credentials are present")

	setEmailConfiguration(t, "EMAIL_PROVIDER", "resend", false)
	setEmailConfiguration(t, "RESEND_API_KEY", "", true)
	_, err = configuredEmail(t.Context(), true)
	require.ErrorContains(t, err, "RESEND_API_KEY")

	setEmailConfiguration(t, "RESEND_API_KEY", "re_test", true)
	setEmailConfiguration(t, "RESEND_WEBHOOK_SECRET", "whsec_test", true)
	setEmailConfiguration(t, "RESEND_API_BASE", "http://localhost:9999", false)
	config, err := configuredEmail(t.Context(), true)
	require.NoError(t, err)
	require.IsType(t, &email.ResendSender{}, config.sender)

	// A deployed Resend sender must name its sender address: the localhost
	// default is one a provider rejects, which ends every job undelivered.
	_, err = configuredEmail(t.Context(), false)
	require.ErrorContains(t, err, "EMAIL_FROM is required")
	setEmailConfiguration(t, "EMAIL_FROM", "Example <no-reply@example.com>", false)
	config, err = configuredEmail(t.Context(), false)
	require.NoError(t, err)
	require.Equal(t, "Example <no-reply@example.com>", config.from)

	setEmailConfiguration(t, "EMAIL_PROVIDER", "typo", false)
	_, err = configuredEmail(t.Context(), true)
	require.ErrorContains(t, err, "EMAIL_PROVIDER must be one of: disabled, log, resend")
}

func TestConfiguredEmailRefusesSenderInApplicationGroup(t *testing.T) {
	clearEmailConfiguration(t)
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__APPLICATION__EMAIL_FROM", "no-reply@example.com")
	_, err := configuredEmail(t.Context(), true)
	require.ErrorContains(t, err, "it belongs to the email group")
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
