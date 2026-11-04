//go:build !pure

package infra_test

import (
	"testing"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/pkg/infra"
)

// Startup refuses a database whose policies judge by a scope setting this
// process cannot bind. The decision is covered by its own table of catalogs;
// what this adds is the half that table cannot state — that the query behind it
// reads a real catalog correctly, both when the answer is yes and when it is no.
//
// The no case is staged rather than installed. A policy reading the retired
// names alone IS version 64's shape, one relation's worth of it, and the rule
// is per policy: the pre-rename catalog is seventy-five of these. Staging it in
// a transaction that rolls back proves the query sees it, over the same catalog
// the live one is in, without a second cluster to install 64 into — and without
// leaving anything behind for the next test in the package.
func TestCatalogScopeSettingCheckReadsTheLiveCatalog(t *testing.T) {
	dsn, err := codefly.For(testCtx).Service("store").Secret("postgres", "owner-connection")
	require.NoError(t, err)
	pool, err := pgxpool.New(testCtx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	// This database is at the ledger's frontier, inside the compatibility
	// window, and must serve.
	require.NoError(t, infra.RequireCatalogReadsScopeSettings(testCtx, pool))

	tx, err := pool.Begin(testCtx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(testCtx) }()

	_, err = tx.Exec(testCtx, `
CREATE TABLE public.scope_setting_version_probe (id uuid PRIMARY KEY, org_id uuid NOT NULL);
ALTER TABLE public.scope_setting_version_probe ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.scope_setting_version_probe FORCE ROW LEVEL SECURITY;
CREATE POLICY scope_setting_version_probe_tenant ON public.scope_setting_version_probe
    FOR ALL TO PUBLIC
    USING ((org_id)::text = current_setting('`+infra.LegacyTenantScopeSetting+`', true))
    WITH CHECK ((org_id)::text = current_setting('`+infra.LegacyTenantScopeSetting+`', true));`)
	require.NoError(t, err)

	err = infra.RequireCatalogReadsScopeSettings(testCtx, tx)
	require.Error(t, err, "a policy judging by the retired tenant setting alone must refuse startup, not read as an empty table")
	require.Contains(t, err.Error(), "scope_setting_version_probe_tenant")
	require.Contains(t, err.Error(), "66_scope_settings_transition")

	// And the compatibility window over the same policy is admitted, so what
	// the check refuses is the retired name read ALONE rather than the retired
	// name at all — which is the state every policy in this database is in.
	_, err = tx.Exec(testCtx, `
ALTER POLICY scope_setting_version_probe_tenant ON public.scope_setting_version_probe
    USING ((org_id)::text = COALESCE(NULLIF(current_setting('`+infra.TenantScopeSetting+`', true), ''), current_setting('`+infra.LegacyTenantScopeSetting+`', true)))
    WITH CHECK ((org_id)::text = COALESCE(NULLIF(current_setting('`+infra.TenantScopeSetting+`', true), ''), current_setting('`+infra.LegacyTenantScopeSetting+`', true)));`)
	require.NoError(t, err)
	require.NoError(t, infra.RequireCatalogReadsScopeSettings(testCtx, tx))
}
