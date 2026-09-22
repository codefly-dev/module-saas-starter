//go:build !pure

package infra_test

import (
	"context"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/internal/testdb"
)

// A store instance applying migrations holds the runner's lock for its whole
// run, which on a cold database is the entire chain.
const (
	migrationLockWait           = 2 * time.Minute
	migrationLockDeadlineMargin = 10 * time.Second
)

// The store's ledger is one generated baseline; nothing here replays a
// migration file any more. What remains is the authority a test needs to act
// as the migration owner — the only session that may ALTER a table or assume a
// runtime role to observe it — and the lock discipline that keeps such a
// session exclusive with a service that is still migrating.

// asMigrationOwner runs fn on one connection held under the migration-owner
// capability — the authority a migration runs under, and the only one that may
// ALTER the audit tables. The service's own pools deliberately hold no DDL
// privilege, so they cannot replay a migration.
//
// The store database is shared, and a service start applies migrations over the
// same catalog rows a replay rewrites — two sessions altering one table's
// catalog entry is `tuple concurrently updated`, which aborts whichever loses.
// The package lock serializes this package against the other suites, not against
// a service that is still migrating. The runner takes one advisory lock for the
// length of its run, so fn holds that same lock: the two are then exclusive
// however the phase happens to be scheduled.
//
// fn must not call asMigrationOwner again: a nested call opens a second session
// and waits on the lock this one already holds, so the slot below refuses it
// outright rather than letting it block out the whole wait budget and then
// report a timeout that accuses the store.
func asMigrationOwner(t *testing.T, fn func(ctx context.Context, conn *pgxpool.Conn)) {
	t.Helper()
	require.NoError(t, testdb.EnterMigrationLock())
	defer testdb.ExitMigrationLock()

	ctx := testCtx
	connString, err := codefly.For(ctx).Service("store").Secret("postgres", "owner-connection")
	require.NoError(t, err)
	pool, err := pgxpool.New(ctx, connString)
	require.NoError(t, err)
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()

	lockID := migrationRunnerLockID(t, ctx, conn, connString)
	lockCtx, cancel := migrationLockContext(t, ctx)
	defer cancel()
	_, err = conn.Exec(lockCtx, `SELECT pg_advisory_lock($1)`, lockID)
	require.NoError(t, err, "acquire the store migration lock")
	defer func() {
		var released bool
		if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, lockID).Scan(&released); err != nil {
			// A failed callback can leave the session unusable, and closing the
			// pool ends the session, which releases the lock anyway. Failing here
			// would bury the failure that caused it.
			t.Logf("release the store migration lock: %v", err)
			return
		}
		require.True(t, released,
			"released a lock this session never held: the derived id is not the one the runner takes")
	}()

	fn(ctx, conn)
}

// migrationLockContext bounds the wait for the runner's lock without outliving
// the test binary's own deadline. A wait that outlives it is killed as a panic
// dump naming no cause, which is what bounding the wait exists to prevent.
func migrationLockContext(t *testing.T, ctx context.Context) (context.Context, context.CancelFunc) {
	t.Helper()
	wait := migrationLockWait
	if deadline, ok := t.Deadline(); ok {
		if budget := time.Until(deadline) - migrationLockDeadlineMargin; budget < wait {
			wait = budget
		}
	}
	return context.WithTimeout(ctx, wait)
}

// migrationRunnerLockID resolves the lock on conn's own session, so the schema
// and database hashed into it are the ones this connection resolves — the same
// way the runner reads them for itself.
func migrationRunnerLockID(t *testing.T, ctx context.Context, conn *pgxpool.Conn, connString string) int64 {
	t.Helper()
	var schema, database string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT CURRENT_SCHEMA(), CURRENT_DATABASE()`).Scan(&schema, &database))
	lockID, err := testdb.MigrationRunnerLockID(connString, schema, database)
	require.NoError(t, err)
	return lockID
}

// Holding the lock is what makes the replay exclusive with a migrating service,
// so a second session must not be able to take it while the replay runs. Nothing
// in the replay's own result would reveal a lock it failed to hold.
func TestMigrationReplayExcludesTheMigrationRunner(t *testing.T) {
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		connString, err := codefly.For(ctx).Service("store").Secret("postgres", "owner-connection")
		require.NoError(t, err)
		contender, err := pgxpool.New(ctx, connString)
		require.NoError(t, err)
		defer contender.Close()

		var acquired bool
		require.NoError(t, contender.QueryRow(ctx,
			`SELECT pg_try_advisory_lock($1)`,
			migrationRunnerLockID(t, ctx, conn, connString)).Scan(&acquired))
		require.False(t, acquired,
			"a store instance applying migrations must wait for the replay to finish")
	})
}

func mustExec(t *testing.T, ctx context.Context, conn *pgxpool.Conn, sql string, args ...any) {
	t.Helper()
	_, err := conn.Exec(ctx, sql, args...)
	require.NoError(t, err)
}
