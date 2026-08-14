package infra_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests rely on TestMain in postgres_webhooks_test.go to set up
// testStore + testCtx. NEVER mock per saas-starter rule.

func TestProviderRegistered_SeededProviders(t *testing.T) {
	// Seeded by migrations: the built-in providers plus the generic OIDC set
	// that user_identities.provider foreign-keys against.
	for _, provider := range []string{"workos", "google", "oidc", "auth0", "okta", "ping"} {
		registered, err := testStore.ProviderRegistered(testCtx, provider)
		require.NoError(t, err)
		require.True(t, registered, "provider %q must be registered so login can insert an identity", provider)
	}
}

func TestProviderRegistered_UnknownProvider(t *testing.T) {
	registered, err := testStore.ProviderRegistered(testCtx, "not-a-real-provider")
	require.NoError(t, err)
	require.False(t, registered)
}
