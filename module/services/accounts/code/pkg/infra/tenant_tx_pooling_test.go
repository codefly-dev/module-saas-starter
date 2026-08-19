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
// What makes the leak observable is that this test drives the pool
// sequentially from one goroutine: once WithOrgTx commits and releases,
// the pool holds exactly one idle connection, and pgxpool hands an idle
// connection back before opening a new one — so every read below lands on
// the same server connection WithOrgTx just used. AfterRelease runs only
// RESET ROLE (not RESET ALL), so nothing else scrubs the GUC. The
// single-connection cap (asserted in singleConnStore) is a backstop that
// forecloses even a concurrent second connection. If someone changed
// set_config's is_local arg to false, or used SET instead of SET LOCAL,
// the post-commit read would return the org id and this test would fail.
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

	// A second tenant reuses the same connection. The in-tx read only
	// confirms the mechanism re-engages for orgB — a leaked session GUC
	// would be masked here anyway, since the fresh SET LOCAL overrides it.
	// The leak check is the post-commit require.Empty below: it proves
	// orgB's value, like orgA's, does not survive onto the pooled
	// connection, so there is no cross-transaction (hence no cross-client)
	// carryover.
	require.NoError(t, single.WithOrgTx(testCtx, orgB, func(ctx context.Context) error {
		require.Equal(t, orgB, currentOrgID(t, txFromCtx(t, ctx)))
		return nil
	}))
	require.Empty(t, currentOrgID(t, single.Pool()),
		"orgB's app.current_org_id leaked past its transaction onto the pooled connection")
}

// singleConnStore opens a dedicated store over the same database whose
// pool is capped at one physical connection. The cap is a backstop to the
// test's sequential access (see the test doc); asserting it applied keeps
// the pool_max_conns URL parameter from silently becoming a no-op — e.g.
// against a keyword-form DSN — which would let the pool open a second,
// clean connection and make the test vacuous.
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
	require.Equal(t, int32(1), store.Pool().Config().MaxConns,
		"pool_max_conns=1 did not apply — the single-connection backstop is a no-op")
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
