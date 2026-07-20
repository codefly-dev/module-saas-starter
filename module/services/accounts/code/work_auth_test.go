package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func clearAuthProviderEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"AUTH_PROVIDER",
		"CODEFLY__FIXTURE",
		"CODEFLY__ENVIRONMENT",
		"CODEFLY__WORKSPACE_CONFIGURATION__WORKOS__AUTH_PROVIDER",
		"CODEFLY__WORKSPACE_SECRET_CONFIGURATION__WORKOS__AUTH_PROVIDER",
		"WORKOS_CLIENT_ID",
		"WORKOS_CLIENT_SECRET",
		"OAUTH_ALLOWED_REDIRECT_URIS",
		"CODEFLY__WORKSPACE_CONFIGURATION__WORKOS__WORKOS_CLIENT_ID",
		"CODEFLY__WORKSPACE_CONFIGURATION__WORKOS__WORKOS_CLIENT_SECRET",
		"CODEFLY__WORKSPACE_SECRET_CONFIGURATION__WORKOS__WORKOS_CLIENT_ID",
		"CODEFLY__WORKSPACE_SECRET_CONFIGURATION__WORKOS__WORKOS_CLIENT_SECRET",
		"CODEFLY__WORKSPACE_CONFIGURATION__WORKOS__OAUTH_ALLOWED_REDIRECT_URIS",
		"CODEFLY__WORKSPACE_SECRET_CONFIGURATION__WORKOS__OAUTH_ALLOWED_REDIRECT_URIS",
	} {
		t.Setenv(key, "")
	}
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
		{name: "codefly local default", environment: "local", want: "http://localhost:21931"},
		{name: "missing outside local", wantError: true},
		{name: "remote http rejected", baseURL: "http://app.example.com", wantError: true},
		{name: "path rejected", baseURL: "https://app.example.com/base", wantError: true},
		{name: "query rejected", baseURL: "https://app.example.com?tenant=x", wantError: true},
		{name: "credentials rejected", baseURL: "https://user@app.example.com", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_BASE_URL", tc.baseURL)
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

func TestBuildOAuthRequestPolicyRequiresExactConfiguredRedirects(t *testing.T) {
	clearAuthProviderEnvironment(t)

	_, err := buildOAuthRequestPolicy("workos")
	require.Error(t, err)
	require.Contains(t, err.Error(), "OAUTH_ALLOWED_REDIRECT_URIS")

	t.Setenv("OAUTH_ALLOWED_REDIRECT_URIS", "REPLACE_ME")
	_, err = buildOAuthRequestPolicy("workos")
	require.Error(t, err, "checked-in placeholder redirect must fail startup")

	t.Setenv("OAUTH_ALLOWED_REDIRECT_URIS", "http://localhost:21931/auth/callback, https://app.acme.com/auth/callback")
	policy, err := buildOAuthRequestPolicy("workos")
	require.NoError(t, err)
	require.NoError(t, policy.Validate("workos", "https://app.acme.com/auth/callback"))
	require.Error(t, policy.Validate("workos", "https://app.acme.com/auth/callback/"))
}

func TestConfiguredAuthProviderDoesNotImplicitlyEnableDevelopment(t *testing.T) {
	clearAuthProviderEnvironment(t)
	require.Empty(t, configuredAuthProvider(""))

	_, _, err := buildProviderStack(configuredAuthProvider(""), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "AUTH_PROVIDER is required")
}

func TestConfiguredAuthProviderAllowsExplicitFixtureMode(t *testing.T) {
	clearAuthProviderEnvironment(t)
	require.Equal(t, "dev", configuredAuthProvider("dev-admin"))

	validator, exchanger, err := buildProviderStack(configuredAuthProvider("dev-admin"), "dev-admin")
	require.NoError(t, err)
	require.NotNil(t, validator)
	require.Nil(t, exchanger)
}

func TestConfiguredAuthProviderNormalizesExplicitProvider(t *testing.T) {
	clearAuthProviderEnvironment(t)
	t.Setenv("AUTH_PROVIDER", "  GoOgLe  ")
	require.Equal(t, "google", configuredAuthProvider(""))
}

func TestConfiguredAuthProviderExplicitFixtureOverridesLocalProviderConfig(t *testing.T) {
	clearAuthProviderEnvironment(t)
	t.Setenv("AUTH_PROVIDER", "workos")
	require.Equal(t, "dev", configuredAuthProvider("dev-admin"), "SDK-selected fixture must deterministically select fixture auth")
}

func TestFixtureAuthenticationRequiresSDKSelectedFixture(t *testing.T) {
	clearAuthProviderEnvironment(t)
	require.Empty(t, configuredAuthProvider(""))
	_, _, err := buildProviderStack("dev", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "SDK-selected fixture")
}

func TestFixtureVariableCannotOverrideProductionProvider(t *testing.T) {
	clearAuthProviderEnvironment(t)
	t.Setenv("AUTH_PROVIDER", "workos")
	require.Equal(t, "workos", configuredAuthProvider(""))
}

func TestBuildProviderStackRejectsUnknownAndIncompleteProviders(t *testing.T) {
	clearAuthProviderEnvironment(t)

	_, _, err := buildProviderStack("unknown", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported AUTH_PROVIDER")

	_, _, err = buildProviderStack("workos", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "WORKOS_CLIENT_ID")

	t.Setenv("WORKOS_CLIENT_ID", "REPLACE_ME")
	t.Setenv("WORKOS_CLIENT_SECRET", "REPLACE_ME")
	_, _, err = buildProviderStack("workos", "")
	require.Error(t, err, "checked-in placeholder credentials must fail startup")
}
