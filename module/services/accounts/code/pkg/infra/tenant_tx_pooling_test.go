//go:build !pure

package infra_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"
)

// TestWithOrgTx_OrgIDIsTransactionLocal_NoPoolLeak pins the single
// highest-consequence multi-tenant RLS footgun: codefly.current_tenant_id MUST
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
// the same server connection WithOrgTx just used. The store installs no
// release hook (see singleConnStore), so the request pool's reset on release
// cannot scrub the GUC and hide the leak. The single-connection cap is a
// backstop that forecloses even a concurrent second connection. If someone
// changed set_config's is_local arg to false, or used SET instead of SET
// LOCAL, the post-commit read would return the org id and this test would
// fail.
func TestWithOrgTx_OrgIDIsTransactionLocal_NoPoolLeak(t *testing.T) {
	const (
		orgA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		orgB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)

	single := singleConnStore(t)

	// Inside the transaction the GUC is set — the RLS policies read it.
	require.NoError(t, single.WithOrgTx(testCtx, orgA, func(ctx context.Context) error {
		require.Equal(t, orgA, currentOrgID(t, txFromCtx(t, ctx)),
			"codefly.current_tenant_id must be visible to the policy inside WithOrgTx")
		return nil
	}))

	// The tx has committed and its connection is back in the single-slot
	// pool. The next borrower on that exact connection MUST see no tenant
	// context — a bare (un-wrapped) query is fail-closed.
	require.Empty(t, currentOrgID(t, single.Pool()),
		"codefly.current_tenant_id leaked past its transaction onto the pooled connection")

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
		"orgB's codefly.current_tenant_id leaked past its transaction onto the pooled connection")
}

// singleConnStore opens a dedicated store over the request login whose pool
// is capped at one physical connection and installs no connection hook, so
// what one transaction leaves on the connection is exactly what the next
// borrower finds there. The login's session default makes the connection
// app_tenant without a hook. The cap is a backstop to the tests' sequential
// access; asserting it applied keeps the pool_max_conns URL parameter from
// silently becoming a no-op — e.g. against a keyword-form DSN — which would let
// the pool open a second, clean connection and make the tests vacuous.
func singleConnStore(t *testing.T) *infra.PostgresStore {
	t.Helper()
	url := testStore.Pool().Config().ConnString()
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	config, err := pgxpool.ParseConfig(url + sep + "pool_max_conns=1")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(testCtx, config)
	require.NoError(t, err)
	store := infra.StoreOnPool(pool)
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
		"SELECT coalesce(current_setting('codefly.current_tenant_id', true), '')").Scan(&got))
	return got
}

func txFromCtx(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	tx := storetx.Tx(ctx)
	require.NotNil(t, tx, "WithOrgTx must place its tx on the context")
	return tx
}

func currentUserID(t *testing.T, q orgIDQuerier) string {
	t.Helper()
	var got string
	require.NoError(t, q.QueryRow(testCtx,
		"SELECT coalesce(current_setting('codefly.current_user_id', true), '')").Scan(&got))
	return got
}

// poisonSession leaves a session-level org and user binding on the single
// connection of store's pool — the state a borrower that used
// set_config(..., false) or SET, rather than a transaction-local binding,
// would hand the next one.
func poisonSession(t *testing.T, store *infra.PostgresStore, orgID, userID string) {
	t.Helper()
	_, err := store.Pool().Exec(testCtx,
		"SELECT set_config('codefly.current_tenant_id', $1, false), set_config('codefly.current_user_id', $2, false)", orgID, userID)
	require.NoError(t, err)
	require.Equal(t, orgID, currentOrgID(t, store.Pool()), "the session binding really did survive its statement")
	require.Equal(t, userID, currentUserID(t, store.Pool()))
}

// Every request transaction binds the whole request scope, not only the half it
// carries: WithOrgTx clears the user, WithUserTx clears the org, and
// As(identity) clears whichever the identity lacks. Otherwise a session-level
// user binding left on the connection would serve the previous user's sessions
// and mfa_devices rows inside the next caller's org transaction.
func TestRequestTransactionsClearTheBindingTheyDoNotCarry(t *testing.T) {
	const (
		staleOrg  = "cccccccc-cccc-cccc-cccc-cccccccccccc"
		staleUser = "dddddddd-dddd-dddd-dddd-dddddddddddd"
		orgID     = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
		userID    = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	)
	for name, tc := range map[string]struct {
		run      func(store *infra.PostgresStore, fn func(ctx context.Context) error) error
		wantOrg  string
		wantUser string
	}{
		"WithOrgTx": {
			run: func(store *infra.PostgresStore, fn func(ctx context.Context) error) error {
				return store.WithOrgTx(testCtx, orgID, fn)
			},
			wantOrg: orgID,
		},
		"WithUserTx": {
			run: func(store *infra.PostgresStore, fn func(ctx context.Context) error) error {
				return store.WithUserTx(testCtx, userID, fn)
			},
			wantUser: userID,
		},
		"As a user without an org": {
			run: func(store *infra.PostgresStore, fn func(ctx context.Context) error) error {
				return store.As(business.Identity{UserID: userID}).Within(testCtx, fn)
			},
			wantUser: userID,
		},
		"As an org without a user": {
			run: func(store *infra.PostgresStore, fn func(ctx context.Context) error) error {
				return store.As(business.Identity{OrgID: orgID}).Within(testCtx, fn)
			},
			wantOrg: orgID,
		},
	} {
		t.Run(name, func(t *testing.T) {
			single := singleConnStore(t)
			poisonSession(t, single, staleOrg, staleUser)
			require.NoError(t, tc.run(single, func(ctx context.Context) error {
				tx := txFromCtx(t, ctx)
				require.Equal(t, tc.wantOrg, currentOrgID(t, tx), "org binding inside the transaction")
				require.Equal(t, tc.wantUser, currentUserID(t, tx), "user binding inside the transaction")
				return nil
			}))
		})
	}
}

// A request connection comes back to the pool carrying no request binding,
// whatever its borrower left at session level, and comes back rather than being
// thrown away. RESET ROLE alone would return the role and keep the scope
// settings, so the next borrower's un-wrapped reads — and any transaction that
// binds only half the scope — would run as the previous caller.
//
// Both halves are asserted on the connection the reset actually ran on, by
// driving the pool's own AfterRelease judgement. The earlier shape released the
// connection to the shared pool and then waited up to five seconds for that
// backend's PID to reappear among the idle ones, which is two conditions at
// once: it needed the reset to have cleaned the session AND nothing else in the
// package to be holding every request connection for the length of the wait.
// The second is not a property of this code — a background dispatcher is enough
// to fail it — and a failure could not say which half it was. The judgement
// returns the verdict the pool acts on: true reuses the connection, false
// destroys it. Asking it directly is the same property with the timing removed,
// and names the failure when it breaks.
func TestRequestConnectionCarriesNoBindingAfterRelease(t *testing.T) {
	const (
		staleOrg  = "12121212-1212-1212-1212-121212121212"
		staleUser = "34343434-3434-3434-3434-343434343434"
	)
	conn, err := testPool.Acquire(testCtx)
	require.NoError(t, err)
	defer conn.Release()

	// What a borrower that used set_config(..., false) or SET, rather than a
	// transaction-local binding, hands back — under both generations of both
	// names, because inside migration 66's compatibility window a predicate
	// falls through to the retired name when the live one is empty, so a
	// session-level binding on either is the previous caller's scope.
	_, err = conn.Exec(testCtx,
		`SELECT set_config($1, $3, false), set_config($2, $4, false),
		        set_config($5, $3, false), set_config($6, $4, false)`,
		infra.TenantScopeSetting, infra.UserScopeSetting, staleOrg, staleUser,
		infra.LegacyTenantScopeSetting, infra.LegacyUserScopeSetting)
	require.NoError(t, err)
	require.Equal(t, staleOrg, currentOrgID(t, conn), "the session binding really did survive its statement")
	require.Equal(t, staleUser, currentUserID(t, conn))

	require.True(t, infra.ReturnsToTenantRole(conn.Conn()),
		"the release judgement refused the connection it had just reset, so every request connection would be destroyed on release instead of reused")

	// And this is the session the next borrower would be handed.
	for _, setting := range []string{
		infra.TenantScopeSetting, infra.UserScopeSetting,
		infra.LegacyTenantScopeSetting, infra.LegacyUserScopeSetting,
	} {
		require.Empty(t, sessionSetting(t, conn, setting),
			"a request connection returned to the pool still carries %s", setting)
	}
	var role string
	require.NoError(t, conn.QueryRow(testCtx, "SELECT current_user::text").Scan(&role))
	require.Equal(t, "app_tenant", role, "a released request connection must come back as app_tenant")
}

// sessionSetting is one scope setting's value on q, the empty string when
// nothing has bound it.
func sessionSetting(t *testing.T, q orgIDQuerier, setting string) string {
	t.Helper()
	var got string
	require.NoError(t, q.QueryRow(testCtx,
		"SELECT coalesce(current_setting($1, true), '')", setting).Scan(&got))
	return got
}
