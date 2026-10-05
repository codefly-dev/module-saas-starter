//go:build !pure

package infra_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// The queue's and the quarantine's down migrations (18 and 21) drop the only
// copy of whatever rows they hold, so they refuse while any row is there.

// migrationOwner runs fn on a connection with the authority migrations run
// under, inside a transaction it rolls back, so a down migration can be run
// against the real schema and leave it as it was.
func migrationOwner(t *testing.T, fn func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	run := func(ctx context.Context, conn *pgx.Conn) {
		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		fn(ctx, tx)
	}
	if raw := os.Getenv("ACCOUNTS_INSTALLER_TEST_DATABASE_URL"); raw != "" {
		conn, err := pgx.Connect(testCtx, raw)
		require.NoError(t, err)
		defer conn.Close(testCtx)
		run(testCtx, conn)
		return
	}
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) { run(ctx, conn.Conn()) })
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile("../../../../store/migrations/" + name)
	require.NoError(t, err)
	return string(body)
}

// asRole runs sql as role inside the transaction and returns to the session's own.
func asRole(t *testing.T, ctx context.Context, tx pgx.Tx, role, sql string, args ...any) {
	t.Helper()
	_, err := tx.Exec(ctx, "SET LOCAL ROLE "+role)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, sql, args...)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "RESET ROLE")
	require.NoError(t, err)
}

// Rolling the queue's migration back drops the only copy of every event the
// relay has not delivered, so it must refuse while any row is queued.
func TestRollingBackTheQueueRefusesWhileEventsAreQueued(t *testing.T) {
	down := readMigration(t, "18_audit_event_queue.down.sql")
	orgID := seedOrg(t, seedUser(t))

	migrationOwner(t, func(ctx context.Context, tx pgx.Tx) {
		asRole(t, ctx, tx, "app_job_worker", `DELETE FROM public.audit_event_queue`)

		_, err := tx.Exec(ctx, "SAVEPOINT empty")
		require.NoError(t, err)
		_, err = tx.Exec(ctx, down)
		require.NoError(t, err, "an empty queue rolls back")
		_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT empty")
		require.NoError(t, err)

		asRole(t, ctx, tx, "app_control_plane", `
			INSERT INTO public.audit_event_queue (id, event_type, schema_version, actor_type, resource, org_id, created_at)
			VALUES ($1, 'saas.session.revoked', 1, 'system', 'session', $2, now())`, business.NewIDString(), orgID)
		_, err = tx.Exec(ctx, "SAVEPOINT queued")
		require.NoError(t, err)
		_, err = tx.Exec(ctx, down)
		require.Error(t, err, "a queue with a row in it does not")
		require.True(t, strings.Contains(err.Error(), "still holds 1 queued audit events"), err.Error())
		_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT queued")
		require.NoError(t, err)
	})
}

func TestRollingBackTheQuarantineRefusesWhileRowsAreSetAside(t *testing.T) {
	down := readMigration(t, "21_audit_event_quarantine.down.sql")

	migrationOwner(t, func(ctx context.Context, tx pgx.Tx) {
		countQuarantine := func() int {
			_, err := tx.Exec(ctx, "SET LOCAL ROLE app_job_worker")
			require.NoError(t, err)
			var n int
			require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM public.audit_event_quarantine`).Scan(&n))
			_, err = tx.Exec(ctx, "RESET ROLE")
			require.NoError(t, err)
			return n
		}
		held := countQuarantine()

		asRole(t, ctx, tx, "app_job_worker", `
			INSERT INTO public.audit_event_quarantine (seq, xact_id, id, event_type, schema_version, actor_type, resource, created_at, enqueued_at, error)
			VALUES (9000000000, pg_current_xact_id(), $1, 'saas.session.revoked', 1, 'user', 'session', now(), now(), 'refused')`, business.NewIDString())
		_, err := tx.Exec(ctx, "SAVEPOINT held")
		require.NoError(t, err)
		_, err = tx.Exec(ctx, down)
		require.Error(t, err, "the quarantine is never dropped with rows in it")
		require.Contains(t, err.Error(), "audit_event_quarantine still holds")
		_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT held")
		require.NoError(t, err)
		require.Equal(t, held+1, countQuarantine())
	})
}
