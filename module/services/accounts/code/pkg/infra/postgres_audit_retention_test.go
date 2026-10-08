//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"
)

// Audit retention runs on a schedule with nobody watching, through
// audit_events_drop_partitions_before. These tests hold it to what PostgreSQL
// records about a partition, not to what the partition is called, and to
// refusing rather than hanging when it cannot take its locks.
//
// Every probe month lies years before the install provisions anything, so a
// cutoff in it cannot reach a real partition. The tests that only need a
// transaction's worth of state run in one that rolls back, as the owner of the
// tables, so a partition they create, or retention drops, does not outlive them.

// retentionSetZone sets the session's time zone for the rest of the
// transaction, the way a client's zone setting reaches the server.
func retentionSetZone(t *testing.T, ctx context.Context, tx pgx.Tx, zone string) {
	t.Helper()
	_, err := tx.Exec(ctx, `SELECT set_config('TimeZone', $1, true)`, zone)
	require.NoError(t, err)
}

// retentionEnsureMonth creates the monthly partition holding day, from a session
// in zone: the partition's bounds are that zone's midnights, whatever zone
// retention later runs in.
func retentionEnsureMonth(t *testing.T, ctx context.Context, tx pgx.Tx, zone, day string) {
	t.Helper()
	retentionSetZone(t, ctx, tx, zone)
	_, err := tx.Exec(ctx, fmt.Sprintf(`SELECT audit_events_ensure_partition('%s'::date)`, day))
	require.NoError(t, err)
}

// retentionPartitions lists the partitions attached to audit_events, by name.
func retentionPartitions(t *testing.T, ctx context.Context, tx pgx.Tx) []string {
	t.Helper()
	rows, err := tx.Query(ctx, `
		SELECT c.relname::text
		  FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		 WHERE i.inhparent = 'public.audit_events'::regclass
		 ORDER BY 1`)
	require.NoError(t, err)
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return names
}

// retentionInsertEvent commits a platform event at an explicit instant and
// returns the partition PostgreSQL routed it to.
func retentionInsertEvent(t *testing.T, ctx context.Context, tx pgx.Tx, at time.Time) string {
	t.Helper()
	id := uuid.NewString()
	_, err := tx.Exec(ctx,
		`INSERT INTO audit_events (id, event_type, resource, created_at) VALUES ($1, $2, 'example', $3)`,
		id, string(business.EventAuthLogin), at)
	require.NoError(t, err)
	var partition string
	require.NoError(t, tx.QueryRow(ctx, `SELECT tableoid::regclass::text FROM audit_events WHERE id = $1`, id).Scan(&partition))
	return partition
}

// retentionRowsIn counts the rows a partition holds, read through the parent.
func retentionRowsIn(t *testing.T, ctx context.Context, tx pgx.Tx, partition string) int64 {
	t.Helper()
	var rows int64
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE tableoid = to_regclass('public.' || $1)`, partition).Scan(&rows))
	return rows
}

// asRetention runs fn in a transaction that rolls back, as the owner of the
// audit tables, and hands it the context a store method needs to run in that
// transaction. A test that wants the role retention runs as switches to it
// itself, after the DDL only the owner may run.
func asRetention(t *testing.T, fn func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		fn(ctx, tx)
	})
}

// runRetentionIn runs the scheduled retention call as the control-plane role,
// from a UTC session, inside tx.
func runRetentionIn(t *testing.T, ctx context.Context, tx pgx.Tx, cutoff time.Time) int64 {
	t.Helper()
	_, err := tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
	require.NoError(t, err)
	retentionSetZone(t, ctx, tx, "UTC")
	dropped, err := testStore.DropAuditPartitionsBefore(infra.BindControlPlaneTx(ctx, tx), cutoff)
	require.NoError(t, err)
	return dropped
}

// A partition created from a session whose zone is not UTC has bounds on that
// zone's midnights, so audit_events_1996_08 created from New York ends at 04:00
// UTC on 1 September. Retention run from UTC with a cutoff of 1 September 00:00
// UTC must leave it alone: its last four hours hold September's events, which
// are newer than the cutoff. Choosing it by its name, read in the UTC session,
// dropped it and them.
func TestRetentionKeepsAPartitionThatEndsAfterTheCutoffWhateverZoneCreatedIt(t *testing.T) {
	asRetention(t, func(ctx context.Context, tx pgx.Tx) {
		retentionEnsureMonth(t, ctx, tx, "America/New_York", "1996-07-15")
		retentionEnsureMonth(t, ctx, tx, "America/New_York", "1996-08-15")
		retentionEnsureMonth(t, ctx, tx, "UTC", "1996-06-15")

		_, err := tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
		require.NoError(t, err)
		cutoff := time.Date(1996, 9, 1, 0, 0, 0, 0, time.UTC)

		// The premise: a September event, newer than the cutoff, is held by the
		// partition named for August; an August one is held by July's.
		require.Equal(t, "audit_events_1996_08",
			retentionInsertEvent(t, ctx, tx, cutoff.Add(2*time.Hour)), "the premise: the partition named for August ends after the cutoff")
		require.Equal(t, "audit_events_1996_07",
			retentionInsertEvent(t, ctx, tx, time.Date(1996, 8, 1, 2, 0, 0, 0, time.UTC)), "the premise: the partition named for July ends in August")

		before := retentionPartitions(t, ctx, tx)
		dropped := runRetentionIn(t, ctx, tx, cutoff)
		after := retentionPartitions(t, ctx, tx)

		require.Contains(t, after, "audit_events_1996_08", "a partition that ends after the cutoff is not dropped")
		require.Equal(t, int64(1), retentionRowsIn(t, ctx, tx, "audit_events_1996_08"), "the events newer than the cutoff survive")
		require.NotContains(t, after, "audit_events_1996_07", "a partition that ends before the cutoff is dropped")
		require.NotContains(t, after, "audit_events_1996_06", "a partition that ends before the cutoff is dropped")
		require.Equal(t, int64(len(before)-len(after)), dropped, "the count is the partitions that went")
		require.Equal(t, int64(2), dropped)
	})
}

// The same partitions, retention run from a session in the creator's zone: the
// answer does not depend on the zone retention runs in.
func TestRetentionDecidesAlikeFromAnyZone(t *testing.T) {
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Kolkata", "Pacific/Auckland"} {
		t.Run(zone, func(t *testing.T) {
			asRetention(t, func(ctx context.Context, tx pgx.Tx) {
				retentionEnsureMonth(t, ctx, tx, "America/New_York", "1996-07-15")
				retentionEnsureMonth(t, ctx, tx, "America/New_York", "1996-08-15")

				_, err := tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
				require.NoError(t, err)
				retentionSetZone(t, ctx, tx, zone)
				// 1996-09-01 04:00 UTC, the instant audit_events_1996_08 ends.
				end := time.Date(1996, 9, 1, 4, 0, 0, 0, time.UTC)

				dropped, err := testStore.DropAuditPartitionsBefore(infra.BindControlPlaneTx(ctx, tx), end.Add(-time.Second))
				require.NoError(t, err)
				require.Equal(t, int64(1), dropped, "one second before the end, only July is due")
				require.True(t, slices.Contains(retentionPartitions(t, ctx, tx), "audit_events_1996_08"))

				dropped, err = testStore.DropAuditPartitionsBefore(infra.BindControlPlaneTx(ctx, tx), end)
				require.NoError(t, err)
				require.Equal(t, int64(1), dropped, "at the end, August is due")
				require.False(t, slices.Contains(retentionPartitions(t, ctx, tx), "audit_events_1996_08"))
			})
		})
	}
}

// A partition whose bounds are not a quoted FROM/TO pair is never dropped,
// whatever it is called: a cutoff rule cannot say what a DEFAULT partition holds,
// and a range open at either end has no month. Each probe carries a monthly name
// that a name-based rule would take for one older than the cutoff.
func TestRetentionNeverDropsAPartitionWhoseBoundsItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ddl       string
		partition string
		eventAt   time.Time
		cutoff    time.Time
		// older is a monthly partition created beside the probe that retention
		// must still drop: refusing the probe is not the end of the run.
		older string
	}{
		{
			name:      "DEFAULT",
			ddl:       `CREATE TABLE public.audit_events_1997_03 PARTITION OF public.audit_events DEFAULT`,
			partition: "audit_events_1997_03",
			eventAt:   time.Date(1997, 3, 10, 12, 0, 0, 0, time.UTC),
			cutoff:    time.Date(1997, 5, 1, 0, 0, 0, 0, time.UTC),
			older:     "audit_events_1997_01",
		},
		{
			name:      "MINVALUE",
			ddl:       `CREATE TABLE public.audit_events_1997_03 PARTITION OF public.audit_events FOR VALUES FROM (MINVALUE) TO ('1997-04-01 00:00:00+00')`,
			partition: "audit_events_1997_03",
			eventAt:   time.Date(1997, 3, 10, 12, 0, 0, 0, time.UTC),
			cutoff:    time.Date(1997, 5, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			// Open at the top, so it can only start after every partition there
			// is: a cutoff in 2200 makes every real partition due, and the
			// transaction rolls them back with the probe.
			name:      "MAXVALUE",
			ddl:       `CREATE TABLE public.audit_events_2200_03 PARTITION OF public.audit_events FOR VALUES FROM ('2200-03-01 00:00:00+00') TO (MAXVALUE)`,
			partition: "audit_events_2200_03",
			eventAt:   time.Date(2200, 3, 10, 12, 0, 0, 0, time.UTC),
			cutoff:    time.Date(2200, 5, 1, 0, 0, 0, 0, time.UTC),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asRetention(t, func(ctx context.Context, tx pgx.Tx) {
				if tc.older != "" {
					retentionEnsureMonth(t, ctx, tx, "UTC", "1997-01-15")
				}
				_, err := tx.Exec(ctx, tc.ddl)
				require.NoError(t, err)
				_, err = tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
				require.NoError(t, err)
				require.Equal(t, tc.partition, retentionInsertEvent(t, ctx, tx, tc.eventAt), "the premise: the probe holds an event")

				before := retentionPartitions(t, ctx, tx)
				dropped := runRetentionIn(t, ctx, tx, tc.cutoff)
				after := retentionPartitions(t, ctx, tx)

				require.Contains(t, after, tc.partition, "a partition whose bounds are not a month's is not dropped")
				require.Equal(t, int64(1), retentionRowsIn(t, ctx, tx, tc.partition), "its event survives")
				require.Equal(t, int64(len(before)-len(after)), dropped)
				if tc.older != "" {
					require.NotContains(t, after, tc.older, "the run goes on past a partition it will not drop")
				}
			})
		})
	}
}

// retentionRetire drops what a test left behind, by the retention function, so
// no probe month outlives the test that provisioned it.
func retentionRetire(t *testing.T, before string) {
	t.Helper()
	t.Cleanup(func() {
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			_, err := storetx.Tx(ctx).Exec(ctx, `SELECT audit_events_drop_partitions_before($1::timestamptz)`, before)
			return err
		}))
	})
}

// retentionCommitted runs the scheduled retention call in its own committed
// control-plane transaction, from a UTC session.
func retentionCommitted(cutoff time.Time) (int64, error) {
	var dropped int64
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		if _, err := storetx.Tx(ctx).Exec(ctx, `SELECT set_config('TimeZone', 'UTC', true)`); err != nil {
			return err
		}
		var err error
		dropped, err = testStore.DropAuditPartitionsBefore(ctx, cutoff)
		return err
	})
	return dropped, err
}

func retentionPartitionRows(t *testing.T, name string) (exists bool, rows int64) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		if err := tx.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, name).Scan(&exists); err != nil || !exists {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tableoid = to_regclass('public.' || $1)`, name).Scan(&rows)
	}))
	return exists, rows
}

// A writer that does not finish makes retention refuse, not hang: the function
// bounds its wait for the table's lock, drops nothing, and a later run, when the
// writer is gone, drops the partition. And a run with nothing due takes no lock
// at all, so it neither waits for the writer nor fails because of it: retention
// runs on a schedule and a partition comes due about once a month.
func TestRetentionRefusesInsteadOfHangingBehindAWriterThatDoesNotFinish(t *testing.T) {
	retentionRetire(t, "1999-01-01")
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		retentionSetZone(t, ctx, tx, "UTC")
		_, err := tx.Exec(ctx, `SELECT audit_events_ensure_partition('1998-03-15'::date)`)
		return err
	}))

	inserted, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var writerErr error
	go func() {
		defer close(finished)
		writerErr = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			if _, err := storetx.Tx(ctx).Exec(ctx,
				`INSERT INTO audit_events (id, event_type, resource, created_at) VALUES ($1, $2, 'example', $3)`,
				uuid.NewString(), string(business.EventAuthLogin), time.Date(1998, 3, 10, 12, 0, 0, 0, time.UTC)); err != nil {
				return err
			}
			close(inserted)
			<-release
			return nil
		})
	}()
	select {
	case <-inserted:
	case <-finished:
		t.Fatalf("the writer failed before it held the partition: %v", writerErr)
	}
	var releaseOnce sync.Once
	releaseWriter := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() { releaseWriter(); <-finished })

	// Nothing is due: the cutoff precedes the end of the only old partition.
	started := time.Now()
	dropped, err := retentionCommitted(time.Date(1998, 3, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err, "a run with nothing due does not wait for a writer")
	require.Zero(t, dropped)
	require.Less(t, time.Since(started), 5*time.Second)

	// Something is due, behind a writer that is not finishing.
	type outcome struct {
		dropped int64
		err     error
	}
	refused := make(chan outcome, 1)
	go func() {
		dropped, err := retentionCommitted(time.Date(1998, 4, 1, 0, 0, 0, 0, time.UTC))
		refused <- outcome{dropped, err}
	}()
	select {
	case got := <-refused:
		var pgErr *pgconn.PgError
		require.ErrorAs(t, got.err, &pgErr, "retention refused for want of the lock")
		require.Equal(t, "55P03", pgErr.Code, "lock_not_available: the wait is bounded")
		require.Zero(t, got.dropped)
	case <-time.After(45 * time.Second):
		t.Fatal("retention hung behind a writer that had not finished")
	}
	exists, _ := retentionPartitionRows(t, "audit_events_1998_03")
	require.True(t, exists, "a refused run drops nothing")

	releaseWriter()
	<-finished
	require.NoError(t, writerErr)
	exists, rows := retentionPartitionRows(t, "audit_events_1998_03")
	require.True(t, exists)
	require.Equal(t, int64(1), rows, "the writer's row was never at risk")
	dropped, err = retentionCommitted(time.Date(1998, 4, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, int64(1), dropped, "the next run, with the writer gone, drops the partition")
	exists, _ = retentionPartitionRows(t, "audit_events_1998_03")
	require.False(t, exists)
}

// The migration replaced the function's body, and CREATE OR REPLACE keeps its
// owner and grants; what it must have changed is the wait. Execute stays with the
// control plane alone: retention is DDL on the audit table, and no tenant
// transaction may run it.
func TestRetentionFunctionBoundsItsLockWaitAndKeepsItsGrants(t *testing.T) {
	var config []string
	var publicMayExecute, controlPlaneMay, tenantMay bool
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `
			SELECT COALESCE(proconfig, ARRAY[]::text[]),
			       proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(proacl) a WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE'),
			       has_function_privilege('app_control_plane', oid, 'EXECUTE'),
			       has_function_privilege('app_tenant', oid, 'EXECUTE')
			  FROM pg_proc
			 WHERE oid = 'public.audit_events_drop_partitions_before(timestamptz)'::regprocedure`,
		).Scan(&config, &publicMayExecute, &controlPlaneMay, &tenantMay)
	}))
	require.Contains(t, config, "lock_timeout=10s", "a stuck writer is a refusal, not a hang")
	require.False(t, publicMayExecute)
	require.True(t, controlPlaneMay)
	require.False(t, tenantMay)
}
