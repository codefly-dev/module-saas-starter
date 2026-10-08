//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/infra/storetx"
)

// Migration 20 has to be present and shaped as the resolver expects before any
// of it is wired, because a missing grant or policy here fails at delivery time
// — when a webhook worker opens a sealed secret outside any tenant transaction —
// rather than at boot.
func TestOrgKeyBindingsSchema(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)

		var forced bool
		require.NoError(t, tx.QueryRow(ctx,
			`SELECT relforcerowsecurity FROM pg_class WHERE oid = 'public.org_key_bindings'::regclass`).Scan(&forced))
		require.True(t, forced, "a table holding which key seals a tenant's data must force row security")

		// Every role that opens a sealed credential must be able to resolve the
		// key that opens it. webhook_subscriptions grants SELECT to the two
		// worker roles for the same reason.
		for _, role := range []string{"app_tenant", "app_control_plane", "app_job_worker", "app_webhook_worker"} {
			var granted bool
			require.NoError(t, tx.QueryRow(ctx,
				`SELECT has_table_privilege($1, 'public.org_key_bindings', 'SELECT')`, role).Scan(&granted))
			require.True(t, granted, "%s opens sealed credentials, so it must read the key binding", role)
		}

		// A tenant may read its own binding and write none: a tenant that could
		// write one could point its credentials at another organization's key.
		for _, privilege := range []string{"INSERT", "UPDATE", "DELETE"} {
			var granted bool
			require.NoError(t, tx.QueryRow(ctx,
				`SELECT has_table_privilege('app_tenant', 'public.org_key_bindings', $1)`, privilege).Scan(&granted))
			require.False(t, granted, "a tenant must not %s its own key binding", privilege)
		}
		return nil
	}))
}

// A revocation is terminal and the key a binding names is immutable. Both are
// enforced below every writer rather than remembered by one.
func TestOrgKeyBindingRevocationIsTerminalAndKeyRefImmutable(t *testing.T) {
	orgID := seedOrg(t, seedUser(t))
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		_, err := tx.Exec(ctx,
			`INSERT INTO org_key_bindings (org_id, key_ref, customer_held) VALUES ($1, $2, true)`,
			orgID, "platform-provisioned-key-ref")
		return err
	}))

	// Repointing the binding would leave it describing a key that sealed none of
	// the data, because every stored envelope records the key that sealed it.
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx,
			`UPDATE org_key_bindings SET key_ref = 'a-different-key' WHERE org_id = $1`, orgID)
		return err
	})
	require.ErrorContains(t, err, "key_ref is immutable")

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx,
			`UPDATE org_key_bindings SET revoked_at = NOW(), revoked_reason = 'customer instruction' WHERE org_id = $1`, orgID)
		return err
	}))

	// Un-revoking would assert that credentials sealed under a destroyed key are
	// readable again, which no edit to this row can make true.
	err = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx,
			`UPDATE org_key_bindings SET revoked_at = NULL, revoked_reason = NULL WHERE org_id = $1`, orgID)
		return err
	})
	require.ErrorContains(t, err, "revocation is terminal")
}
