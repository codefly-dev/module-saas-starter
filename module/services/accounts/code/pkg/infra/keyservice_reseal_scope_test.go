//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"
)

// The crypto-shredding guard keys off orgColumn: a row whose organization holds
// its own key, or revoked it, is left alone. A column that HAS an organization
// but whose inventory entry forgets orgColumn is therefore swept for a protected
// organization anyway — re-sealing a customer's credentials under a key we hold,
// which is the exact failure the guard exists to prevent.
//
// A hand-written list of which columns are organization-scoped cannot catch
// that: the new column would simply be absent from it and read as user-scoped.
// So the question is asked of the SCHEMA — does this table have an org_id? — and
// the inventory must agree.
func TestEveryEnvelopedColumnOnAnOrgTableNamesItsOrgColumn(t *testing.T) {
	for _, column := range infra.EnvelopedColumns() {
		hasOrgID := tableHasColumn(t, column.Table, "org_id")
		if hasOrgID {
			require.Equal(t, "org_id", column.OrgColumn,
				"%s.%s sits on a table with org_id, so the inventory must name it: without orgColumn the "+
					"re-seal sweep cannot tell whether the row belongs to an organization that holds its own "+
					"key, and would re-seal a customer's credential under a key this deployment controls",
				column.Table, column.Column)
			continue
		}
		require.Empty(t, column.OrgColumn,
			"%s.%s names an organization column its table does not have", column.Table, column.Column)
	}
}

// And the inverse of the user-scoped decision: a column with no organization
// must be one we deliberately decided is not an organization's. Listing them
// here means adding a third kind of value fails until someone decides which it
// is.
func TestColumnsWithNoOrganizationAreTheOnesWeDecidedAreNot(t *testing.T) {
	notAnOrganizations := map[string]string{
		"mfa_devices.secret_encrypted":               "a TOTP seed belongs to a person, who may be in many organizations",
		"webauthn_credentials.credential_encrypted":  "a WebAuthn credential belongs to a person, not an organization",
		"webauthn_ceremonies.session_data_encrypted": "a ceremony belongs to a person mid-login",
	}
	for _, column := range infra.EnvelopedColumns() {
		key := column.Table + "." + column.Column
		if column.OrgColumn != "" {
			require.NotContains(t, notAnOrganizations, key,
				"%s is organization-scoped in the inventory but listed as not an organization's", key)
			continue
		}
		require.Contains(t, notAnOrganizations, key,
			"%s has no organization column: decide whether it is an organization's data (give it one, so "+
				"crypto-shredding covers it) or a person's (list it here with the reason)", key)
	}
}

// tableHasColumn asks the live schema. information_schema is privilege-filtered,
// so this runs under the control plane, which reads every relation.
func tableHasColumn(t *testing.T, table, column string) bool {
	t.Helper()
	var present bool
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
			)`, table, column).Scan(&present)
	}))
	return present
}
