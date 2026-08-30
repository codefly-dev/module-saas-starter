package infra_test

import (
	"context"
	"sync"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// orgTxSettings reads the raw JSONB settings for an org inside the current
// tenant transaction. The typed getter would discard composed content the
// binary has no schema for, so raw reads are how these tests observe the
// stored ProtoJSON directly.
func orgTxSettings(ctx context.Context, t *testing.T, orgID string) string {
	t.Helper()
	tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithOrgTx
	var raw string
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT settings::text FROM org_generic_settings WHERE org_id = $1`, orgID).Scan(&raw))
	return raw
}

func TestOrgGenericSettingsStoreUpsertAndRead(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.UpdateOrgGenericSettings(ctx, orgID, &gen.OrganizationSettings{}, nil); err != nil {
			return err
		}
		got, err := testStore.GetOrgGenericSettings(ctx, orgID)
		if err != nil {
			return err
		}
		require.NotNil(t, got)
		require.JSONEq(t, `{}`, orgTxSettings(ctx, t, orgID))
		return nil
	}))

	// A read with no row still resolves to an empty document, never an error.
	other := seedOrg(t, owner)
	require.NoError(t, testStore.WithOrgTx(testCtx, other, func(ctx context.Context) error {
		got, err := testStore.GetOrgGenericSettings(ctx, other)
		if err != nil {
			return err
		}
		require.NotNil(t, got)
		return nil
	}))
}

func TestOrgGenericSettingsResetPrunesEmptyParentsAndPreservesSiblings(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithOrgTx
		if _, err := tx.Exec(ctx,
			`INSERT INTO org_generic_settings (org_id, settings) VALUES ($1, $2::jsonb)`,
			orgID,
			`{"appearance":{"theme":"THEME_PREFERENCE_DARK"},"email":{"marketing":true,"product":false}}`,
		); err != nil {
			return err
		}

		// The store's reset path runs settings_jsonb_delete_paths on the org
		// column before merging the (empty) patch, pruning parents that become
		// empty while nested siblings survive.
		if err := testStore.UpdateOrgGenericSettings(ctx, orgID, &gen.OrganizationSettings{},
			[]string{"appearance.theme", "email.product"}); err != nil {
			return err
		}
		require.JSONEq(t, `{"email":{"marketing":true}}`, orgTxSettings(ctx, t, orgID))
		return nil
	}))
}

func TestOrgGenericSettingsDeepMergePreservesSiblingsAndExplicitFalse(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithOrgTx
		if _, err := tx.Exec(ctx,
			`INSERT INTO org_generic_settings (org_id, settings) VALUES ($1, $2::jsonb)`,
			orgID, `{"future":{"new_field":true},"email":{"marketing":true}}`,
		); err != nil {
			return err
		}
		// The shared, schema-agnostic deep-merge (migration 80) operates on the
		// org column: unknown branches survive, explicit false overwrites.
		if _, err := tx.Exec(ctx, `
			UPDATE org_generic_settings
			   SET settings = public.settings_jsonb_deep_merge(settings, '{"email":{"product":false}}'::jsonb)
			 WHERE org_id = $1`, orgID); err != nil {
			return err
		}
		require.JSONEq(t,
			`{"future":{"new_field":true},"email":{"marketing":true,"product":false}}`,
			orgTxSettings(ctx, t, orgID))
		return nil
	}))
}

func TestOrgGenericSettingsRLSIsolatesTenants(t *testing.T) {
	ownerA := seedUser(t)
	orgA := seedOrg(t, ownerA)
	ownerB := seedUser(t)
	orgB := seedOrg(t, ownerB)

	require.NoError(t, testStore.WithOrgTx(testCtx, orgA, func(ctx context.Context) error {
		return testStore.UpdateOrgGenericSettings(ctx, orgA, &gen.OrganizationSettings{}, nil)
	}))

	// Org B sees no row for A: its own read is empty and a direct count of A's
	// row returns zero under B's tenant transaction.
	require.NoError(t, testStore.WithOrgTx(testCtx, orgB, func(ctx context.Context) error {
		got, err := testStore.GetOrgGenericSettings(ctx, orgB)
		if err != nil {
			return err
		}
		require.NotNil(t, got)

		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithOrgTx
		var visible int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM org_generic_settings WHERE org_id = $1`, orgA).Scan(&visible); err != nil {
			return err
		}
		require.Zero(t, visible, "tenant B must not see tenant A's settings row")
		return nil
	}))

	// Org B cannot write a row scoped to A: the RLS WITH CHECK rejects it.
	err := testStore.WithOrgTx(testCtx, orgB, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithOrgTx
		_, execErr := tx.Exec(ctx,
			`INSERT INTO org_generic_settings (org_id, settings) VALUES ($1, '{}'::jsonb)`, orgA)
		return execErr
	})
	require.Error(t, err, "cross-tenant write must be blocked by RLS")

	// Org A still sees its own row.
	require.NoError(t, testStore.WithOrgTx(testCtx, orgA, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithOrgTx
		var visible int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM org_generic_settings WHERE org_id = $1`, orgA).Scan(&visible); err != nil {
			return err
		}
		require.Equal(t, 1, visible)
		return nil
	}))
}

func TestOrgGenericSettingsColumnRejectsNonObjectAndOversize(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	nonObject := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithOrgTx
		_, err := tx.Exec(ctx,
			`INSERT INTO org_generic_settings (org_id, settings) VALUES ($1, '"scalar"'::jsonb)`, orgID)
		return err
	})
	require.Error(t, nonObject, "non-object settings must fail the typed-object CHECK")

	oversize := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithOrgTx
		_, err := tx.Exec(ctx,
			`INSERT INTO org_generic_settings (org_id, settings)
			 VALUES ($1, jsonb_build_object('k', repeat('x', 200000)))`, orgID)
		return err
	})
	require.Error(t, oversize, "settings larger than 128 KiB must fail the CHECK")
}

// TestOrgGenericSettingsConcurrentResetsSerializeWithoutLostUpdates drives the
// store's INSERT … ON CONFLICT DO UPDATE path from parallel transactions, each
// pruning a distinct key. The row lock must serialize them so no writer's delete
// clobbers another's, and untouched siblings must survive — the org analogue of
// the concurrent-sparse-patch guarantee the user surface has.
func TestOrgGenericSettingsConcurrentResetsSerializeWithoutLostUpdates(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key with WithOrgTx
		_, err := tx.Exec(ctx,
			`INSERT INTO org_generic_settings (org_id, settings) VALUES ($1, $2::jsonb)`,
			orgID, `{"keya":1,"keyb":1,"keyc":1,"keyd":1}`)
		return err
	}))

	resetKeys := []string{"keya", "keyb"}
	start := make(chan struct{})
	errs := make(chan error, len(resetKeys))
	var wait sync.WaitGroup
	for _, key := range resetKeys {
		wait.Add(1)
		go func(key string) {
			defer wait.Done()
			<-start
			errs <- testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
				return testStore.UpdateOrgGenericSettings(ctx, orgID, &gen.OrganizationSettings{}, []string{key})
			})
		}(key)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.JSONEq(t, `{"keyc":1,"keyd":1}`, orgTxSettings(ctx, t, orgID),
			"concurrent resets must each land without clobbering untouched siblings")
		return nil
	}))
}
