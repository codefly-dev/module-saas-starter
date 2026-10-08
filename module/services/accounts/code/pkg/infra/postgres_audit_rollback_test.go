//go:build !pure

package infra_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// The queue's and the quarantine's down migrations (30 and 33) drop the only
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
	down := readMigration(t, "31_audit_event_queue.down.sql")
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
	down := readMigration(t, "34_audit_event_quarantine.down.sql")

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

// migrationSessions hands fn two independent sessions with the authority
// migrations run under: the first stands in for a request or the relay that is
// writing to the table, the second for the operator running a down migration.
// Neither is wrapped in a transaction: fn begins and ends its own.
func migrationSessions(t *testing.T, fn func(ctx context.Context, writer, roller *pgx.Conn)) {
	t.Helper()
	run := func(ctx context.Context, writer *pgx.Conn) {
		roller, err := pgx.ConnectConfig(ctx, writer.Config())
		require.NoError(t, err)
		defer roller.Close(ctx)
		fn(ctx, writer, roller)
	}
	if raw := os.Getenv("ACCOUNTS_INSTALLER_TEST_DATABASE_URL"); raw != "" {
		writer, err := pgx.Connect(testCtx, raw)
		require.NoError(t, err)
		defer writer.Close(testCtx)
		run(testCtx, writer)
		return
	}
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) { run(ctx, conn.Conn()) })
}

// A down migration that counts the rows and then drops the table must keep
// writers out between the two. A row inserted by a transaction that has not
// committed is invisible to the count, and DROP TABLE then waits for that
// transaction and drops the table beneath the row it just committed: the only
// copy of an event, gone, by a rollback that was written to refuse exactly that.
func TestRollingBackWaitsForAnInFlightWriterBeforeItCounts(t *testing.T) {
	cases := []struct {
		name       string
		down       string
		table      string
		writerRole string
		insert     string
		refusal    string
	}{
		{
			name:       "queue",
			down:       "31_audit_event_queue.down.sql",
			table:      "public.audit_event_queue",
			writerRole: "app_control_plane",
			insert: `INSERT INTO public.audit_event_queue (id, event_type, schema_version, actor_type, resource, created_at)
				VALUES ($1, 'saas.session.revoked', 1, 'system', 'session', now())`,
			refusal: "audit_event_queue still holds 1 queued audit events",
		},
		{
			name:       "quarantine",
			down:       "34_audit_event_quarantine.down.sql",
			table:      "public.audit_event_quarantine",
			writerRole: "app_job_worker",
			insert: `INSERT INTO public.audit_event_quarantine (seq, xact_id, id, event_type, schema_version, actor_type, resource, created_at, enqueued_at, error)
				VALUES (9000000001, pg_current_xact_id(), $1, 'saas.session.revoked', 1, 'system', 'session', now(), now(), 'refused')`,
			refusal: "audit_event_quarantine still holds 1 audit events",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			down := readMigration(t, tc.down)
			eventID := business.NewIDString()

			migrationSessions(t, func(ctx context.Context, writer, roller *pgx.Conn) {
				ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
				defer cancel()

				// The race needs an empty table, or the count refuses for the
				// rows already there and proves nothing. The migration owner
				// may truncate; no role the application runs as can.
				empty := func() {
					_, err := roller.Exec(context.WithoutCancel(ctx), "TRUNCATE "+tc.table)
					require.NoError(t, err)
				}
				empty()
				defer empty()

				// The roller opens first and is left waiting on the writer.
				rolling, err := roller.Begin(ctx)
				require.NoError(t, err)
				var rollerPID int
				require.NoError(t, rolling.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&rollerPID))
				defer func() { _ = rolling.Rollback(context.WithoutCancel(ctx)) }()

				// An insert that has not committed.
				writing, err := writer.Begin(ctx)
				require.NoError(t, err)
				defer func() { _ = writing.Rollback(context.WithoutCancel(ctx)) }()
				_, err = writing.Exec(ctx, "SET LOCAL ROLE "+tc.writerRole)
				require.NoError(t, err)
				_, err = writing.Exec(ctx, tc.insert, eventID)
				require.NoError(t, err)

				finished := make(chan error, 1)
				go func() {
					_, err := rolling.Exec(ctx, down)
					finished <- err
				}()
				// Whatever the down migration does, it is stuck behind the writer.
				deadline := time.Now().Add(15 * time.Second)
				for {
					var waiting bool
					require.NoError(t, writing.QueryRow(ctx,
						`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)`, rollerPID).Scan(&waiting))
					if waiting {
						break
					}
					select {
					case err := <-finished:
						t.Fatalf("the down migration finished while an insert was uncommitted: %v", err)
					default:
					}
					require.True(t, time.Now().Before(deadline), "the down migration never waited on the uncommitted insert")
					time.Sleep(20 * time.Millisecond)
				}
				select {
				case err := <-finished:
					t.Fatalf("the down migration finished while an insert was uncommitted: %v", err)
				case <-time.After(200 * time.Millisecond):
				}

				require.NoError(t, writing.Commit(ctx))

				var verdict error
				select {
				case verdict = <-finished:
				case <-time.After(30 * time.Second):
					t.Fatal("the down migration never finished after the insert committed")
				}
				require.Error(t, verdict, "the down migration dropped %s beneath an event that committed while it ran", tc.table)
				require.Contains(t, verdict.Error(), tc.refusal)
				require.NoError(t, rolling.Rollback(ctx))

				var kept int
				kept, err = countWhere(ctx, roller, tc.table, eventID)
				require.NoError(t, err)
				require.Equal(t, 1, kept, "the committed event is still in %s", tc.table)
			})
		})
	}
}

// countWhere counts the rows of table with the given id, as the relay's role,
// the only one both audit tables admit a read from.
func countWhere(ctx context.Context, conn *pgx.Conn, table, id string) (int, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE app_job_worker"); err != nil {
		return 0, err
	}
	var n int
	err = tx.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE id = $1", id).Scan(&n)
	return n, err
}
