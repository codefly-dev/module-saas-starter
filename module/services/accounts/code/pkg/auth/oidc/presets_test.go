package oidc_test

import (
	"accounts/pkg/auth/oidc"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkOSConfigMatchesCurrentSessionTokenContract(t *testing.T) {
	cfg := oidc.WorkOSConfig("client_123")
	// AuthKit scopes `iss` to the application. WorkOS publishes this at
	// https://api.workos.com/user_management/<client id>/.well-known/openid-configuration
	// — the bare host is never issued, so expecting it rejects every token
	// after a successful code exchange.
	require.Equal(t,
		"https://api.workos.com/user_management/client_123", cfg.Issuer)
	require.Equal(t, "https://api.workos.com/sso/jwks/client_123", cfg.JWKSURL)
	require.Equal(t, "org_id", cfg.OrgClaim)
	require.Equal(t, "client_id", cfg.ClientIDClaim)
	require.Equal(t, "client_123", cfg.ClientID)
	require.True(t, cfg.AllowMissingEmail)
}
