package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A credential moving between configuration groups must not treat the DESTINATION
// group's shipped placeholder as the destination being ready: that defeats the
// fallback exactly on the cells that still deliver the value under the old group,
// which are the only ones the fallback exists for.
func TestR1019IdentityProviderMigration(t *testing.T) {
	const provisioned = "an-operator-supplied-value-of-sufficient-length"

	for _, key := range []string{"IDENTITY_CLIENT_SECRET", "IDENTITY_MANAGEMENT_API_KEY"} {
		t.Run(key, func(t *testing.T) {
			blankWorkspaceKey(t, "identity-provider", key)
			blankWorkspaceKey(t, "identity", key)

			// The cell has not migrated: the destination holds only the shipped
			// placeholder, the legacy group holds the real value.
			t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY_PROVIDER__"+key,
				"local-dev-only-replace-me")
			t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__"+key, provisioned)
			require.Equal(t, provisioned, identityProviderSecret(key),
				"a shipped placeholder in the destination must not override a provisioned legacy value")

			// The cell has migrated: the destination wins.
			t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY_PROVIDER__"+key,
				"the-migrated-"+key)
			require.Equal(t, "the-migrated-"+key, identityProviderSecret(key))
		})
	}
}

// The selection rule itself, in both directions, so the test above cannot pass by
// always preferring the legacy group.
func TestR1019ProvisionedCredentialExcludesShippedPlaceholders(t *testing.T) {
	for _, shipped := range []string{
		"local-dev-only-replace-me",
		"LOCAL-DEV-ONLY-REPLACE-ME",
		"change-me",
		"a-placeholder",
		"REPLACE_ME",
		"",
		"   ",
	} {
		require.False(t, isProvisionedCredential(shipped),
			"%q is not an operator-supplied value", shipped)
	}
	require.True(t, isProvisionedCredential(strings.Repeat("7Kq2Xp9Vb4Nf", 3)))
}

// Local development runs on the shipped defaults, and must still resolve to one —
// from the group this service now declares.
func TestR1019LocalDevelopmentKeepsTheShippedPlaceholder(t *testing.T) {
	const key = "IDENTITY_CLIENT_SECRET"
	blankWorkspaceKey(t, "identity-provider", key)
	blankWorkspaceKey(t, "identity", key)
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY_PROVIDER__"+key,
		"local-dev-only-replace-me")

	require.Equal(t, "local-dev-only-replace-me", identityProviderSecret(key))
}
