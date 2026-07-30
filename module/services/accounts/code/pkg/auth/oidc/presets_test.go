package oidc_test

import (
	"accounts/pkg/auth/oidc"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkOSConfigMatchesCurrentSessionTokenContract(t *testing.T) {
	cfg := oidc.WorkOSConfig("client_123")
	require.Equal(t, "https://api.workos.com", cfg.Issuer)
	require.Equal(t, "https://api.workos.com/sso/jwks/client_123", cfg.JWKSURL)
	require.Equal(t, "org_id", cfg.OrgClaim)
	require.Equal(t, "client_id", cfg.ClientIDClaim)
	require.Equal(t, "client_123", cfg.ClientID)
	require.True(t, cfg.AllowMissingEmail)
}
