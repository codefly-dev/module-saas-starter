package infra_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Migration 116 is the audit-event namespace cutover. It rewrites stored values
// (audit_event_types, audit_events, webhook_subscriptions) and only then
// constrains them, and it has to suspend the append-only triggers to touch
// history at all — a step whose omission is invisible afterwards. These tests
// replay the real migration files: down to the pre-116 shape, seed the legacy
// values a deployed database carries, then up.

func migrationSQL(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		candidate := filepath.Join(dir, "services", "store", "migrations", name)
		if body, err := os.ReadFile(candidate); err == nil {
			return string(body)
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, dir, parent, "could not locate services/store/migrations/%s", name)
		dir = parent
	}
}

// asMigrationOwner runs fn on one connection held under the migration-owner
// capability — the authority a migration runs under, and the only one that may
// ALTER the audit tables. The service's own pools deliberately hold no DDL
// privilege, so they cannot replay a migration.
func asMigrationOwner(t *testing.T, fn func(ctx context.Context, conn *pgxpool.Conn)) {
	t.Helper()
	ctx := testCtx
	connString, err := codefly.For(ctx).Service("store").Secret("postgres", "owner-connection")
	require.NoError(t, err)
	pool, err := pgxpool.New(ctx, connString)
	require.NoError(t, err)
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	fn(ctx, conn)
}

func mustExec(t *testing.T, ctx context.Context, conn *pgxpool.Conn, sql string, args ...any) {
	t.Helper()
	_, err := conn.Exec(ctx, sql, args...)
	require.NoError(t, err)
}

// replayMigration116 runs down, lets seed write the legacy-shaped rows the
// pre-116 schema still accepts, then runs up.
func replayMigration116(t *testing.T, seed func(ctx context.Context, conn *pgxpool.Conn)) {
	t.Helper()
	down := migrationSQL(t, "116_namespace_audit_events.down.sql")
	up := migrationSQL(t, "116_namespace_audit_events.up.sql")
	restored := false
	t.Cleanup(func() {
		if restored {
			return
		}
		// A failure between down and up would leave the shared schema without the
		// namespace constraints for every later test in the package.
		asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
			mustExec(t, ctx, conn, up)
		})
	})
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		mustExec(t, ctx, conn, down)
		seed(ctx, conn)
		mustExec(t, ctx, conn, up)
	})
	restored = true
}

// controlPlaneTx hands the caller the transaction WithControlPlane put on ctx,
// the same seam the other infra tests read raw SQL through. audit_events forces
// RLS, so this is how a test reads across tenants.
func controlPlaneTx(t *testing.T, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return fn(ctx, ctx.Value("tx").(pgx.Tx)) //nolint:staticcheck // shared key with WithControlPlane
	}))
}

// auditWriteError performs one audit_events write under the control plane and
// returns the database's refusal. A failed statement poisons the transaction, so
// the outer transaction is always rolled back.
func auditWriteError(t *testing.T, sql string, args ...any) error {
	t.Helper()
	rollback := errors.New("rollback probe")
	var writeErr error
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		_, writeErr = tx.Exec(ctx, sql, args...)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	return writeErr
}

// seedMigrationOrg writes the minimum user + organization a webhook subscription needs.
func seedMigrationOrg(t *testing.T, ctx context.Context, conn *pgxpool.Conn, slug string) string {
	t.Helper()
	userID := uuid.NewString()
	orgID := uuid.NewString()
	mustExec(t, ctx, conn,
		`INSERT INTO users (uuid, primary_email) VALUES ($1, $2)`,
		userID, fmt.Sprintf("%s@migration-116.test", slug))
	mustExec(t, ctx, conn,
		`INSERT INTO organizations (id, name, slug, owner_id) VALUES ($1, $2, $3, $4)`,
		orgID, "Migration 116 Co", slug, userID)
	t.Cleanup(func() {
		asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
			mustExec(t, ctx, conn, `DELETE FROM organizations WHERE id = $1`, orgID)
			mustExec(t, ctx, conn, `DELETE FROM users WHERE uuid = $1`, userID)
		})
	})
	return orgID
}

func TestMigration116RewritesHistoryAndSubscriptions(t *testing.T) {
	eventID := uuid.NewString()
	subscriptionID := uuid.NewString()
	var orgID string

	var before int64
	controlPlaneTx(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&before)
	})

	replayMigration116(t, func(ctx context.Context, conn *pgxpool.Conn) {
		orgID = seedMigrationOrg(t, ctx, conn, "migration-116-rewrite")
		// A history row typed the way every producer typed it before the cutover,
		// and a subscription routed on the same legacy name.
		mustExec(t, ctx, conn,
			`INSERT INTO audit_events (id, event_type, actor_type, resource, org_id, created_at)
			 VALUES ($1, 'auth.login', 'user', 'session', $2, NOW())`, eventID, orgID)
		mustExec(t, ctx, conn,
			`INSERT INTO webhook_subscriptions (id, org_id, url, secret_encrypted, events)
			 VALUES ($1, $2, 'https://example.com/hook', 'shh', ARRAY['auth.login', 'not_an_event'])`,
			subscriptionID, orgID)
	})

	t.Cleanup(func() {
		asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
			mustExec(t, ctx, conn, `DELETE FROM webhook_subscriptions WHERE id = $1`, subscriptionID)
			mustExec(t, ctx, conn, `ALTER TABLE audit_events DISABLE TRIGGER audit_events_no_delete`)
			mustExec(t, ctx, conn, `DELETE FROM audit_events WHERE id = $1`, eventID)
			mustExec(t, ctx, conn, `ALTER TABLE audit_events ENABLE TRIGGER audit_events_no_delete`)
		})
	})

	controlPlaneTx(t, func(ctx context.Context, tx pgx.Tx) error {
		var eventType string
		var createdAt, after any
		if err := tx.QueryRow(ctx,
			`SELECT event_type, created_at FROM audit_events WHERE id = $1`,
			eventID).Scan(&eventType, &createdAt); err != nil {
			return err
		}
		require.Equal(t, "saas.auth.login", eventType, "history must be rewritten in place, not dropped")
		require.NotNil(t, createdAt, "the rewrite must not disturb the row's identity or timestamp")

		if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&after); err != nil {
			return err
		}
		require.EqualValues(t, before+1, after, "the rewrite must not lose or duplicate a row")

		var events []string
		if err := tx.QueryRow(ctx,
			`SELECT events FROM webhook_subscriptions WHERE id = $1`, subscriptionID).Scan(&events); err != nil {
			return err
		}
		// The subscriber keeps firing with no application-side aliasing; a name
		// that never resolved to an event is left exactly as the customer wrote it.
		require.Equal(t, []string{"saas.auth.login", "not_an_event"}, events)
		return nil
	})
}

// A new-code accounts instance reconciles the registry at startup, so it can
// insert `saas.*` rows before the migration runs. When it does, the legacy row is
// still pinned by history (the deprecated-and-unreferenced sweep spares it) and
// the naive rewrite collides on audit_event_types_pkey — which aborts the
// migration, fails the store's runtime-init, and takes the service graph down.
func TestMigration116SurvivesAPreSyncedRegistry(t *testing.T) {
	eventID := uuid.NewString()

	replayMigration116(t, func(ctx context.Context, conn *pgxpool.Conn) {
		// History pinning the legacy name so it cannot simply be swept away. The
		// down-migration has already restored the bare 'auth.login' registry row.
		mustExec(t, ctx, conn,
			`INSERT INTO audit_events (id, event_type, actor_type, resource, created_at)
			 VALUES ($1, 'auth.login', 'system', 'session', NOW())`, eventID)
		// What a newly-deployed instance's SyncAuditEventTypes would have written.
		mustExec(t, ctx, conn,
			`INSERT INTO audit_event_types (name, version, category, owner, deprecated)
			 VALUES ('saas.auth.login', 1, 'security', 'accounts', FALSE)`)
	})

	t.Cleanup(func() {
		asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
			mustExec(t, ctx, conn, `ALTER TABLE audit_events DISABLE TRIGGER audit_events_no_delete`)
			mustExec(t, ctx, conn, `DELETE FROM audit_events WHERE id = $1`, eventID)
			mustExec(t, ctx, conn, `ALTER TABLE audit_events ENABLE TRIGGER audit_events_no_delete`)
		})
	})

	controlPlaneTx(t, func(ctx context.Context, tx pgx.Tx) error {
		var eventType string
		if err := tx.QueryRow(ctx,
			`SELECT event_type FROM audit_events WHERE id = $1`, eventID).Scan(&eventType); err != nil {
			return err
		}
		require.Equal(t, "saas.auth.login", eventType,
			"history pinned to the legacy name must land on the pre-synced namespaced row")

		var legacy int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_event_types WHERE name = 'auth.login'`).Scan(&legacy); err != nil {
			return err
		}
		require.Zero(t, legacy, "the superseded legacy registry row must be gone, not duplicated")
		return nil
	})
}

// Both rewritten tables FORCE row-level security, so their policies apply to the
// table owner and the migration's rewrites would match zero rows — silently —
// unless FORCE is suspended. Suspending it is only safe if it is restored:
// leaving either table un-FORCEd is a tenant-isolation hole strictly worse than
// the bug it was suspended for.
func TestMigration116RestoresForcedRowLevelSecurity(t *testing.T) {
	controlPlaneTx(t, func(ctx context.Context, tx pgx.Tx) error {
		for _, table := range []string{"audit_events", "webhook_subscriptions"} {
			var forced bool
			if err := tx.QueryRow(ctx,
				`SELECT relforcerowsecurity FROM pg_class WHERE relname = $1`, table).Scan(&forced); err != nil {
				return err
			}
			require.Truef(t, forced, "%s must still FORCE row-level security after migration 116", table)
		}
		return nil
	})
}

// The migration disables the append-only triggers to rewrite history. Re-enabling
// them is the step a rushed migration forgets, and nothing else would notice.
func TestMigration116RestoresImmutability(t *testing.T) {
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		orgID := seedMigrationOrg(t, ctx, conn, "migration-116-immutable")
		eventID := uuid.NewString()
		mustExec(t, ctx, conn,
			`INSERT INTO audit_events (id, event_type, actor_type, resource, org_id, created_at)
			 VALUES ($1, 'saas.auth.login', 'user', 'session', $2, NOW())`, eventID, orgID)
		t.Cleanup(func() {
			asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
				mustExec(t, ctx, conn, `ALTER TABLE audit_events DISABLE TRIGGER audit_events_no_delete`)
				mustExec(t, ctx, conn, `DELETE FROM audit_events WHERE id = $1`, eventID)
				mustExec(t, ctx, conn, `ALTER TABLE audit_events ENABLE TRIGGER audit_events_no_delete`)
			})
		})

		// audit_events forces RLS even for the table owner, so the row has to be in
		// scope for the UPDATE to reach the row-level trigger at all.
		mustExec(t, ctx, conn, `SELECT set_config('app.current_org_id', $1, false)`, orgID)
		_, err := conn.Exec(ctx,
			`UPDATE audit_events SET event_type = 'saas.user.updated' WHERE id = $1`, eventID)
		require.ErrorContains(t, err, "audit_events table is append-only")
	})
}

// The foreign key ADR 0003 specified and migration 97 omitted: an unregistered
// event type is now a write error at the last layer that can still catch it.
func TestAuditEventTypeFKRejectsUnregistered(t *testing.T) {
	err := auditWriteError(t,
		`INSERT INTO audit_events (id, event_type, actor_type, resource, created_at)
		 VALUES ($1, 'saas.nope.not_real', 'system', 'test', NOW())`, uuid.NewString())
	require.ErrorContains(t, err, "audit_events_event_type_fkey")
}

// A namespace is structurally required: the CHECK demands at least three
// segments, so a bare <aggregate>.<event> cannot be written at all.
func TestAuditEventTypeFormatCheckRejectsBareName(t *testing.T) {
	for _, bare := range []string{"login", "auth.login"} {
		err := auditWriteError(t,
			`INSERT INTO audit_events (id, event_type, actor_type, resource, created_at)
			 VALUES ($1, $2, 'system', 'test', NOW())`, uuid.NewString(), bare)
		require.ErrorContainsf(t, err, "audit_events_event_type_format",
			"event_type %q must fail the namespace CHECK", bare)
	}
}
