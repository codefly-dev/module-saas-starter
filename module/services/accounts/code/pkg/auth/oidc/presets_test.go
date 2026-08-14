package oidc_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth/oidc"
)

func TestOktaConfig(t *testing.T) {
	cfg := oidc.OktaConfig("  https://acme.okta.com/oauth2/aus1a2b3c/  ", "api://default")
	require.Equal(t, "okta", cfg.ProviderName)
	require.Equal(t, "https://acme.okta.com/oauth2/aus1a2b3c", cfg.Issuer)
	require.Equal(t, "https://acme.okta.com/oauth2/aus1a2b3c/v1/keys", cfg.JWKSURL)
	require.Equal(t, "api://default", cfg.Audience)

	// The preset is a concrete instance of the generic path, so it must build a
	// validator without further configuration.
	_, err := oidc.New(cfg)
	require.NoError(t, err)
}
