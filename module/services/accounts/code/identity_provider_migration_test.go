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

// The shipped local defaults for the datasource keys carry placeholder markers, and
// this module's repository is public — so a deployed runtime holding one would sign
// with a published value. It is treated as ABSENT, which leaves the purpose
// unavailable: the state minting and redemption already define, and the reason this
// does not refuse at boot the way a perimeter credential does.
func TestR1019DeployedDatasourceDefaultsRefused(t *testing.T) {
	keys := []string{"DATASOURCE_CONTENT_TICKET_KEY", "DATASOURCE_ACCOUNT_LINK_KEY"}
	for _, key := range keys {
		// Two guards run here, and only the last case isolates the marker one: the
		// two shipped values are shorter than the length floor, so removing the
		// marker check still refuses them. A long marker-carrying value is what
		// proves the marker check itself fires.
		for _, shipped := range []string{
			"local-dev-only-replace-me",
			"LOCAL-DEV-ONLY-REPLACE-ME",
			"some-change-me-value-long-enough-to-pass-a-length-floor",
		} {
			t.Run(key+"/"+shipped[:12], func(t *testing.T) {
				blankWorkspaceKey(t, "datasource-keys", key)
				t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__DATASOURCE_KEYS__"+key, shipped)

				require.Nil(t, usableDatasourceKey(key, false),
					"a deployed runtime must not sign with a published value")
				require.Equal(t, []byte(shipped), usableDatasourceKey(key, true),
					"local development uses its shipped default")
			})
		}
	}
}

// A short value is refused for the same reason the perimeter floor exists, and a
// provisioned one is used — so the refusal is the value and not a key that is never
// usable.
func TestR1019DatasourceKeyFloorAndProvisionedValue(t *testing.T) {
	const key = "DATASOURCE_CONTENT_TICKET_KEY"
	blankWorkspaceKey(t, "datasource-keys", key)

	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__DATASOURCE_KEYS__"+key, "7Kq2Xp9Vb4Nf")
	require.Nil(t, usableDatasourceKey(key, false))

	provisioned := strings.Repeat("7Kq2Xp9Vb4Nf", 4)
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__DATASOURCE_KEYS__"+key, provisioned)
	require.Equal(t, []byte(provisioned), usableDatasourceKey(key, false))
}
