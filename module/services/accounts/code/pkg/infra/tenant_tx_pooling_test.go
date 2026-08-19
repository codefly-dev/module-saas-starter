package infra_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/infra"
)

// TestWithOrgTx_OrgIDIsTransactionLocal_NoPoolLeak pins the single
// highest-consequence multi-tenant RLS footgun: app.current_org_id MUST
// be transaction-scoped (set via set_config(..., is_local => true) /
// SET LOCAL), never session-scoped. A session-scoped GUC survives on the
// physical connection after the transaction ends, so the next checkout —
// under an external transaction-mode pooler (PgBouncer), a *different*
// client — would inherit the previous tenant's org id and read its rows.
//
// The regression is made observable by pinning a dedicated pool to ONE
// physical connection (pool_max_conns=1): every checkout below is the
// same server connection WithOrgTx just used, and AfterRelease only runs
// RESET ROLE (not RESET ALL), so nothing else scrubs the GUC. If someone
// changed set_config's is_local arg to false, or used SET instead of
// SET LOCAL, the post-commit read would return the org id and this test
// would fail.
func TestWithOrgTx_OrgIDIsTransactionLocal_NoPoolLeak(t *testing.T) {
	const (
		orgA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		orgB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)

	single := singleConnStore(t)

	// Inside the transaction the GUC is set — the RLS policies read it.
	require.NoError(t, single.WithOrgTx(testCtx, orgA, func(ctx context.Context) error {
		require.Equal(t, orgA, currentOrgID(t, txFromCtx(t, ctx)),
			"app.current_org_id must be visible to the policy inside WithOrgTx")
		return nil
	}))

	// The tx has committed and its connection is back in the single-slot
	// pool. The next borrower on that exact connection MUST see no tenant
	// context — a bare (un-wrapped) query is fail-closed.
	require.Empty(t, currentOrgID(t, single.Pool()),
		"app.current_org_id leaked past its transaction onto the pooled connection")

	// A second tenant's transaction on the same connection sees only its
	// own org id, with no trace of the first — proving no cross-transaction
	// (hence no cross-client) carryover.
	require.NoError(t, single.WithOrgTx(testCtx, orgB, func(ctx context.Context) error {
		require.Equal(t, orgB, currentOrgID(t, txFromCtx(t, ctx)))
		return nil
	}))
	require.Empty(t, currentOrgID(t, single.Pool()))
}

// singleConnStore opens a dedicated store over the same database whose
// pool holds exactly one physical connection, so "the next checkout" is
// deterministically the connection the prior transaction just released.
func singleConnStore(t *testing.T) *infra.PostgresStore {
	t.Helper()
	url := testStore.Pool().Config().ConnString()
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	store, err := infra.NewPostgresStoreFromURL(testCtx, url+sep+"pool_max_conns=1")
	require.NoError(t, err)
	t.Cleanup(store.Close)
	return store
}

type orgIDQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func currentOrgID(t *testing.T, q orgIDQuerier) string {
	t.Helper()
	var got string
	require.NoError(t, q.QueryRow(testCtx,
		"SELECT current_setting('app.current_org_id', true)").Scan(&got))
	return got
}

func txFromCtx(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	tx, ok := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared context key with WithOrgTx
	require.True(t, ok, "WithOrgTx must place its tx on the context")
	return tx
}
