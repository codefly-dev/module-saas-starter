//go:build !pure

package infra_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Migration 148 drops the retired custody relation. A consumer still brokering
// through it holds a live, unerased grant, and dropping the table under that
// grant would end the consumer's work with nothing to say so; the migration
// refuses while such a grant exists and applies once none does. The relation
// forces row-level security with role-keyed policies, so a naive count run by
// the migration owner sees nothing — which is exactly the case the test seeds.
func TestMigration148RefusesToDropLiveCustodyGrants(t *testing.T) {
	down := migrationSQL(t, "148_drop_execution_custody.down.sql")
	up := migrationSQL(t, "148_drop_execution_custody.up.sql")

	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		mustExec(t, ctx, conn, down)
		// Whatever the outcome below, leave the shared schema at 148: dropped.
		// Deferred rather than t.Cleanup because the connection belongs to this
		// session and is released when it ends; a require failure exits through
		// these defers too.
		defer func() {
			_, _ = conn.Exec(context.Background(), `DROP TABLE IF EXISTS public.execution_custody`)
		}()

		owner, org := seedCustodyOwner(t, ctx, conn)
		defer func() {
			bg := context.Background()
			_, _ = conn.Exec(bg, `DELETE FROM public.organizations WHERE id = $1`, org)
			_, _ = conn.Exec(bg, `DELETE FROM public.users WHERE uuid = $1`, owner)
		}()
		// The owner is filtered by the forced policies like anyone else; the
		// seed has to land under the shape a deployed table carries, so RLS is
		// lifted only for the insert and forced again before the migration runs.
		mustExec(t, ctx, conn, `ALTER TABLE public.execution_custody NO FORCE ROW LEVEL SECURITY`)
		mustExec(t, ctx, conn, `
			INSERT INTO public.execution_custody
				(reference, org_id, owner_id, admission_id, fingerprint, envelope, expires_at)
			VALUES ($1, $2, $3, 'live', repeat('a', 64), 'cfs1:vault-transit:fixture', $4),
			       ($5, $2, $3, 'expired', repeat('b', 64), 'cfs1:vault-transit:fixture', $6),
			       ($7, $2, $3, 'erased', repeat('c', 64), '', $4)`,
			uuid.NewString(), org, owner, time.Now().Add(time.Hour),
			uuid.NewString(), time.Now().Add(-time.Hour),
			uuid.NewString())
		mustExec(t, ctx, conn, `ALTER TABLE public.execution_custody FORCE ROW LEVEL SECURITY`)

		t.Run("a live grant refuses the drop and leaves the table", func(t *testing.T) {
			_, err := conn.Exec(ctx, up)
			var pgErr *pgconn.PgError
			require.ErrorAs(t, err, &pgErr)
			require.Equal(t, "23514", pgErr.Code, "refused as a check violation, not a generic error")
			require.Contains(t, pgErr.Message, "1 live grant")

			var exists bool
			require.NoError(t, conn.QueryRow(ctx,
				`SELECT to_regclass('public.execution_custody') IS NOT NULL`).Scan(&exists))
			require.True(t, exists, "a refused migration must not have dropped the table")
		})

		t.Run("expired and erased grants do not hold the drop", func(t *testing.T) {
			mustExec(t, ctx, conn, `ALTER TABLE public.execution_custody NO FORCE ROW LEVEL SECURITY`)
			mustExec(t, ctx, conn, `UPDATE public.execution_custody SET envelope = '' WHERE admission_id = 'live'`)
			mustExec(t, ctx, conn, `ALTER TABLE public.execution_custody FORCE ROW LEVEL SECURITY`)

			mustExec(t, ctx, conn, up)

			var exists bool
			require.NoError(t, conn.QueryRow(ctx,
				`SELECT to_regclass('public.execution_custody') IS NOT NULL`).Scan(&exists))
			require.False(t, exists)
		})
	})
}

// Each Exec above is its own implicit transaction, so the refused DO block rolls
// back on its own and the session stays usable for the second subtest.
func seedCustodyOwner(t *testing.T, ctx context.Context, conn *pgxpool.Conn) (owner, org string) {
	t.Helper()
	owner, org = uuid.NewString(), uuid.NewString()
	mustExec(t, ctx, conn,
		`INSERT INTO public.users (uuid, primary_email, status) VALUES ($1, $2, 'active')`,
		owner, "custody-"+owner[:8]+"@example.com")
	mustExec(t, ctx, conn,
		`INSERT INTO public.organizations (id, name, slug, owner_id) VALUES ($1, 'Example', $2, $3)`,
		org, "custody-"+org[:8], owner)
	return owner, org
}
