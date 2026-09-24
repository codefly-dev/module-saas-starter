//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Tenant-isolation attack battery, live database half. Each test runs one
// attack from the tenant-isolation audit as SQL inside an ordinary tenant
// transaction, on the same runtime login production uses, and asserts the
// secure outcome; each was red before the fix it names. The SQL stands in for an injected expression: anything a query can
// evaluate, an injection can.

// inTenantTx runs attack inside WithOrgTx for a fresh org, after checking the
// transaction really is the restricted tenant role, so a pass can never come
// from a test harness that connects with more authority than production.
func inTenantTx(t *testing.T, attack func(ctx context.Context) error) error {
	t.Helper()
	return testStore.WithOrgTx(testCtx, uuid.NewString(), func(ctx context.Context) error {
		var role string
		require.NoError(t, txFromCtx(t, ctx).QueryRow(ctx, "SELECT current_user").Scan(&role))
		require.Equal(t, "app_tenant", role, "a tenant transaction must run as app_tenant")
		return attack(ctx)
	})
}

// TestAttack_TenantRoleWritesOnlyRowSecuredTables: a table without row-level
// security that app_tenant may write is a table any tenant transaction can
// write for every tenant and for the platform.
func TestAttack_TenantRoleWritesOnlyRowSecuredTables(t *testing.T) {
	var exposed []string
	require.NoError(t, inTenantTx(t, func(ctx context.Context) error {
		rows, err := txFromCtx(t, ctx).Query(ctx, `
			SELECT c.relname
			  FROM pg_catalog.pg_class c
			  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relrowsecurity
			   AND (has_table_privilege(c.oid, 'INSERT') OR has_table_privilege(c.oid, 'UPDATE') OR has_table_privilege(c.oid, 'DELETE'))
			 ORDER BY 1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			exposed = append(exposed, name)
		}
		return rows.Err()
	}))
	require.Empty(t, exposed, "app_tenant can write tables that carry no row-level security")
}

// TestAttack_TenantRoleCannotWritePlatformAdmins: platform_admins carries no
// row-level security, and app_tenant may insert into it, so a write reachable
// from any tenant transaction can mint a platform administrator.
func TestAttack_TenantRoleCannotWritePlatformAdmins(t *testing.T) {
	for _, privilege := range []string{"INSERT", "UPDATE", "DELETE"} {
		t.Run(privilege, func(t *testing.T) {
			var held bool
			require.NoError(t, inTenantTx(t, func(ctx context.Context) error {
				return txFromCtx(t, ctx).QueryRow(ctx,
					"SELECT has_table_privilege('public.platform_admins', $1)", privilege).Scan(&held)
			}))
			require.Falsef(t, held, "app_tenant holds %s on platform_admins", privilege)
		})
	}
}
