package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func clearAuthProviderEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"IDENTITY_PROVIDER",
		"CODEFLY__FIXTURE",
		"CODEFLY__ENVIRONMENT",
		"IDENTITY_CLIENT_ID",
		"IDENTITY_CLIENT_SECRET",
		"IDENTITY_ALLOWED_REDIRECT_URIS",
		"CODEFLY__WORKSPACE_CONFIGURATION__IDENTITY__IDENTITY_PROVIDER",
		"CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_PROVIDER",
		"CODEFLY__WORKSPACE_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_ID",
		"CODEFLY__WORKSPACE_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET",
		"CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_ID",
		"CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET",
		"CODEFLY__WORKSPACE_CONFIGURATION__IDENTITY__IDENTITY_ALLOWED_REDIRECT_URIS",
		"CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_ALLOWED_REDIRECT_URIS",
	} {
		t.Setenv(key, "")
	}
}

func setIdentityConfiguration(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__IDENTITY__"+key, value)
}

func TestWorkspaceEnvPreservesExactCodeflyConfigurationName(t *testing.T) {
	const key = "CODEFLY_INTERNAL_TOKEN"
	t.Setenv(key, "")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__INTERNAL_AUTH__"+key, "legacy-normalized")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__INTERNAL-AUTH__"+key, "runtime-exact")
	require.Equal(t, "runtime-exact", workspaceEnv("internal-auth", key))

	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__INTERNAL-AUTH__"+key, "")
	require.Equal(t, "legacy-normalized", workspaceEnv("internal-auth", key))
}

func TestConfiguredMFAStepUpMaxAge(t *testing.T) {
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__SECURITY__MFA_STEP_UP_MAX_AGE", "")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__SECURITY__MFA_STEP_UP_MAX_AGE", "")
	t.Setenv("MFA_STEP_UP_MAX_AGE", "")
	got, err := configuredMFAStepUpMaxAge()
	require.NoError(t, err)
	require.Equal(t, 15*time.Minute, got)

	t.Setenv("MFA_STEP_UP_MAX_AGE", "20m")
	got, err = configuredMFAStepUpMaxAge()
	require.NoError(t, err)
	require.Equal(t, 20*time.Minute, got)

	for _, invalid := range []string{"0", "-1m", "25h", "not-a-duration"} {
		t.Setenv("MFA_STEP_UP_MAX_AGE", invalid)
		_, err = configuredMFAStepUpMaxAge()
		require.Error(t, err, invalid)
	}

	t.Setenv("MFA_STEP_UP_MAX_AGE", "")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__SECURITY__MFA_STEP_UP_MAX_AGE", "25m")
	got, err = configuredMFAStepUpMaxAge()
	require.NoError(t, err)
	require.Equal(t, 25*time.Minute, got)
}

func TestConfiguredSessionPolicy(t *testing.T) {
	for _, key := range []string{
		"SESSION_ABSOLUTE_LIFETIME",
		"SESSION_IDLE_TIMEOUT",
		"SESSION_MAX_ACTIVE_DEVICES",
	} {
		t.Setenv(key, "")
		t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__SECURITY__"+key, "")
		t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__SECURITY__"+key, "")
	}

	policy, err := configuredSessionPolicy()
	require.NoError(t, err)
	require.Equal(t, 7*24*time.Hour, policy.AbsoluteLifetime)
	require.Equal(t, 24*time.Hour, policy.IdleTimeout)
	require.Equal(t, 10, policy.MaxActiveDevices)

	t.Setenv("SESSION_ABSOLUTE_LIFETIME", "720h")
	t.Setenv("SESSION_IDLE_TIMEOUT", "12h")
	t.Setenv("SESSION_MAX_ACTIVE_DEVICES", "4")
	policy, err = configuredSessionPolicy()
	require.NoError(t, err)
	require.Equal(t, 720*time.Hour, policy.AbsoluteLifetime)
	require.Equal(t, 12*time.Hour, policy.IdleTimeout)
	require.Equal(t, 4, policy.MaxActiveDevices)

	for name, value := range map[string]string{
		"SESSION_ABSOLUTE_LIFETIME":  "30m",
		"SESSION_IDLE_TIMEOUT":       "1m",
		"SESSION_MAX_ACTIVE_DEVICES": "101",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SESSION_ABSOLUTE_LIFETIME", "168h")
			t.Setenv("SESSION_IDLE_TIMEOUT", "24h")
			t.Setenv("SESSION_MAX_ACTIVE_DEVICES", "10")
			t.Setenv(name, value)
			_, err := configuredSessionPolicy()
			require.Error(t, err)
		})
	}

	t.Setenv("SESSION_ABSOLUTE_LIFETIME", "12h")
	t.Setenv("SESSION_IDLE_TIMEOUT", "24h")
	t.Setenv("SESSION_MAX_ACTIVE_DEVICES", "10")
	_, err = configuredSessionPolicy()
	require.Error(t, err, "idle timeout cannot exceed absolute lifetime")
}

func TestConfiguredBillingBaseURLRequiresExactSafeOrigin(t *testing.T) {
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__APPLICATION__APP_BASE_URL", "")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__APPLICATION__APP_BASE_URL", "")
	t.Setenv("APP_BASE_URL", "")
	for _, tc := range []struct {
		name        string
		baseURL     string
		environment string
		want        string
		wantError   bool
	}{
		{name: "production https", baseURL: "https://app.example.com", want: "https://app.example.com"},
		{name: "trailing slash normalized", baseURL: "https://app.example.com/", want: "https://app.example.com"},
		{name: "local http", baseURL: "http://localhost:21931", want: "http://localhost:21931"},
		{name: "codefly local uses trusted request origin", environment: "local", want: ""},
		{name: "missing uses trusted request origin", want: ""},
		{name: "remote http rejected", baseURL: "http://app.example.com", wantError: true},
		{name: "path rejected", baseURL: "https://app.example.com/base", wantError: true},
		{name: "query rejected", baseURL: "https://app.example.com?tenant=x", wantError: true},
		{name: "credentials rejected", baseURL: "https://user@app.example.com", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__APPLICATION__APP_BASE_URL", tc.baseURL)
			t.Setenv("CODEFLY__ENVIRONMENT", tc.environment)
			got, err := configuredBillingBaseURL()
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestConfiguredApplicationBaseURLPrefersCodeflyConfiguration(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://raw-env.example.com")
	t.Setenv(
		"CODEFLY__WORKSPACE_CONFIGURATION__APPLICATION__APP_BASE_URL",
		"http://localhost:54321",
	)

	got, err := configuredApplicationBaseURL()
	require.NoError(t, err)
	require.Equal(t, "http://localhost:54321", got)
}

func TestConfiguredApplicationBaseURLDoesNotTrustAmbientShell(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://ambient-shell.example.com")
	t.Setenv("CODEFLY__ENVIRONMENT", "production")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__APPLICATION__APP_BASE_URL", "")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__APPLICATION__APP_BASE_URL", "")

	got, err := configuredApplicationBaseURL()
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestConfiguredWebAuthnRequiresExactRelyingPartyPolicy(t *testing.T) {
	for _, key := range []string{"WEBAUTHN_RP_ID", "WEBAUTHN_RP_DISPLAY_NAME", "WEBAUTHN_RP_ORIGINS"} {
		t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__SECURITY__"+key, "")
		t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__SECURITY__"+key, "")
	}
	t.Setenv("WEBAUTHN_RP_ID", "")
	t.Setenv("WEBAUTHN_RP_ORIGINS", "")
	_, _, _, err := configuredWebAuthn()
	require.Error(t, err)

	t.Setenv("WEBAUTHN_RP_ID", "app.example.com")
	t.Setenv("WEBAUTHN_RP_DISPLAY_NAME", "Acme")
	t.Setenv("WEBAUTHN_RP_ORIGINS", "https://app.example.com, http://localhost:21931/")
	rpID, displayName, origins, err := configuredWebAuthn()
	require.NoError(t, err)
	require.Equal(t, "app.example.com", rpID)
	require.Equal(t, "Acme", displayName)
	require.Equal(t, []string{"https://app.example.com", "http://localhost:21931"}, origins)

	for _, invalidRPID := range []string{"https://app.example.com", "app.example.com:443", "app.example.com/path"} {
		t.Setenv("WEBAUTHN_RP_ID", invalidRPID)
		_, _, _, err = configuredWebAuthn()
		require.Error(t, err, invalidRPID)
	}

	t.Setenv("WEBAUTHN_RP_ID", "app.example.com")
	for _, invalidOrigin := range []string{"app.example.com", "https://app.example.com/path", "ftp://app.example.com"} {
		t.Setenv("WEBAUTHN_RP_ORIGINS", invalidOrigin)
		_, _, _, err = configuredWebAuthn()
		require.Error(t, err, invalidOrigin)
	}
}

func TestConfiguredWebAuthnReadsCodeflySecurityConfiguration(t *testing.T) {
	for _, key := range []string{"WEBAUTHN_RP_ID", "WEBAUTHN_RP_DISPLAY_NAME", "WEBAUTHN_RP_ORIGINS"} {
		t.Setenv(key, "")
	}
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__SECURITY__WEBAUTHN_RP_ID", "localhost")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__SECURITY__WEBAUTHN_RP_DISPLAY_NAME", "Fixture Stack")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__SECURITY__WEBAUTHN_RP_ORIGINS", "http://localhost:21931")

	rpID, displayName, origins, err := configuredWebAuthn()
	require.NoError(t, err)
	require.Equal(t, "localhost", rpID)
	require.Equal(t, "Fixture Stack", displayName)
	require.Equal(t, []string{"http://localhost:21931"}, origins)
}

func TestBuildOAuthRequestPolicySupportsCodeflyOriginAndExactFallbackRedirects(t *testing.T) {
	clearAuthProviderEnvironment(t)

	policy, err := buildOAuthRequestPolicy("workos")
	require.NoError(t, err)
	require.NoError(t, policy.ValidateForPublicOrigin(
		"workos",
		"http://localhost:54321/auth/callback",
		"http://localhost:54321",
	))
	require.Error(t, policy.Validate("workos", "http://localhost:54321/auth/callback"))

	setIdentityConfiguration(t, "IDENTITY_ALLOWED_REDIRECT_URIS", "REPLACE_ME")
	policy, err = buildOAuthRequestPolicy("workos")
	require.NoError(t, err)
	require.Error(t, policy.Validate("workos", "REPLACE_ME"))

	setIdentityConfiguration(t, "IDENTITY_ALLOWED_REDIRECT_URIS", "http://localhost:21931/auth/callback, https://app.acme.com/auth/callback")
	policy, err = buildOAuthRequestPolicy("workos")
	require.NoError(t, err)
	require.NoError(t, policy.Validate("workos", "https://app.acme.com/auth/callback"))
	require.Error(t, policy.Validate("workos", "https://app.acme.com/auth/callback/"))
}

func TestConfiguredAuthProviderDoesNotImplicitlyEnableDevelopment(t *testing.T) {
	clearAuthProviderEnvironment(t)
	require.Empty(t, configuredAuthProvider(""))

	_, _, err := buildProviderStack(configuredAuthProvider(""), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "IDENTITY_PROVIDER is required")
}

func TestConfiguredAuthProviderAllowsExplicitFixtureMode(t *testing.T) {
	clearAuthProviderEnvironment(t)
	setIdentityConfiguration(t, "IDENTITY_PROVIDER", "fixture")
	require.Equal(t, "dev", configuredAuthProvider("dev-admin"))

	validator, exchanger, err := buildProviderStack(configuredAuthProvider("dev-admin"), "dev-admin")
	require.NoError(t, err)
	require.NotNil(t, validator)
	require.Nil(t, exchanger)
}

func TestConfiguredAuthProviderNormalizesExplicitProvider(t *testing.T) {
	clearAuthProviderEnvironment(t)
	setIdentityConfiguration(t, "IDENTITY_PROVIDER", "  GoOgLe  ")
	require.Equal(t, "google", configuredAuthProvider(""))
}

func TestConfiguredAuthProviderKeepsExternalIdentityWhenDataFixtureIsSelected(t *testing.T) {
	clearAuthProviderEnvironment(t)
	setIdentityConfiguration(t, "IDENTITY_PROVIDER", "workos")
	require.Equal(t, "workos", configuredAuthProvider("dev-admin"), "data fixtures must not replace the Codefly-configured identity provider")
}

func TestFixtureAuthenticationRequiresSDKSelectedFixture(t *testing.T) {
	clearAuthProviderEnvironment(t)
	setIdentityConfiguration(t, "IDENTITY_PROVIDER", "fixture")
	require.Equal(t, "fixture", configuredAuthProvider(""))
	_, _, err := buildProviderStack(configuredAuthProvider(""), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "explicit Codefly fixture")
}

func TestFixtureVariableCannotOverrideProductionProvider(t *testing.T) {
	clearAuthProviderEnvironment(t)
	setIdentityConfiguration(t, "IDENTITY_PROVIDER", "workos")
	require.Equal(t, "workos", configuredAuthProvider(""))
}

func TestBuildProviderStackRejectsUnknownAndIncompleteProviders(t *testing.T) {
	clearAuthProviderEnvironment(t)

	// A non-preset provider name is a generic OIDC provider; without
	// credentials it must still fail startup closed, not at first login.
	_, _, err := buildProviderStack("unknown", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity provider unknown requires IDENTITY_CLIENT_ID")

	_, _, err = buildProviderStack("workos", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "IDENTITY_CLIENT_ID")

	setIdentityConfiguration(t, "IDENTITY_CLIENT_ID", "REPLACE_ME")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET", "REPLACE_ME")
	_, _, err = buildProviderStack("workos", "")
	require.Error(t, err, "checked-in placeholder credentials must fail startup")
}
