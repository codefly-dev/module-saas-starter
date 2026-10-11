//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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

// retentionEnsureMonth provisions the monthly partition holding day, the way the
// store does, from a session in zone: its bounds are UTC midnights whatever the
// zone, met to any neighbour an earlier release cut at another zone's midnight.
func retentionEnsureMonth(t *testing.T, ctx context.Context, tx pgx.Tx, zone, day string) {
	t.Helper()
	retentionSetZone(t, ctx, tx, zone)
	_, err := tx.Exec(ctx, fmt.Sprintf(`SELECT audit_events_ensure_partition('%s'::date)`, day))
	require.NoError(t, err)
}

// retentionLegacyMonth creates the monthly partition holding day the way
// releases before migration 33 did, from a session in zone: its bounds are that
// zone's midnights. Deployments still hold partitions cut this way, and
// retention and provisioning are held to them too.
func retentionLegacyMonth(t *testing.T, ctx context.Context, tx pgx.Tx, zone, day string) {
	t.Helper()
	retentionSetZone(t, ctx, tx, zone)
	inMonth, err := time.Parse(time.DateOnly, day)
	require.NoError(t, err)
	first := time.Date(inMonth.Year(), inMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
	_, err = tx.Exec(ctx, fmt.Sprintf(`CREATE TABLE public.%s PARTITION OF public.audit_events FOR VALUES FROM ('%s') TO ('%s')`,
		pgx.Identifier{"audit_events_" + first.Format("2006_01")}.Sanitize(),
		first.Format(time.DateOnly), first.AddDate(0, 1, 0).Format(time.DateOnly)))
	require.NoError(t, err)
}

// retentionBounds reads the range PostgreSQL records for a partition, as
// instants. It reads it in ISO and UTC, which the rest of the transaction keeps.
func retentionBounds(t *testing.T, ctx context.Context, tx pgx.Tx, partition string) (lower, upper time.Time) {
	t.Helper()
	_, err := tx.Exec(ctx, `SELECT set_config('DateStyle', 'ISO, MDY', true), set_config('TimeZone', 'UTC', true)`)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT r[1]::timestamptz, r[2]::timestamptz
		  FROM pg_class c,
		       regexp_match(pg_get_expr(c.relpartbound, 0), '^FOR VALUES FROM \(''([^'']+)''\) TO \(''([^'']+)''\)$') AS r
		 WHERE c.oid = to_regclass('public.' || $1)`, partition).Scan(&lower, &upper))
	return lower.UTC(), upper.UTC()
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

// A partition an earlier release created from a session whose zone is not UTC
// has bounds on that zone's midnights, so audit_events_1996_08 created from New
// York ends at 04:00 UTC on 1 September. Retention run from UTC with a cutoff of 1 September 00:00
// UTC must leave it alone: its last four hours hold September's events, which
// are newer than the cutoff. Choosing it by its name, read in the UTC session,
// dropped it and them.
func TestRetentionKeepsAPartitionThatEndsAfterTheCutoffWhateverZoneCreatedIt(t *testing.T) {
	asRetention(t, func(ctx context.Context, tx pgx.Tx) {
		retentionLegacyMonth(t, ctx, tx, "America/New_York", "1996-07-15")
		retentionLegacyMonth(t, ctx, tx, "America/New_York", "1996-08-15")
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
				retentionLegacyMonth(t, ctx, tx, "America/New_York", "1996-07-15")
				retentionLegacyMonth(t, ctx, tx, "America/New_York", "1996-08-15")

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
			// is, and a cutoff is never later than the present: only its name
			// can make it look older than the cutoff.
			name:      "MAXVALUE",
			ddl:       `CREATE TABLE public.audit_events_1997_04 PARTITION OF public.audit_events FOR VALUES FROM ('2200-03-01 00:00:00+00') TO (MAXVALUE)`,
			partition: "audit_events_1997_04",
			eventAt:   time.Date(2200, 3, 10, 12, 0, 0, 0, time.UTC),
			cutoff:    time.Date(1997, 5, 1, 0, 0, 0, 0, time.UTC),
			older:     "audit_events_1997_01",
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
// the writer holds up, so it neither waits for the writer nor fails because of
// it: retention runs on a schedule and a partition comes due about once a month.
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

// When nothing is due the function takes no lock at all. It reads bounds without
// opening a partition, so a session holding one ACCESS EXCLUSIVE -- another run
// dropping it, or DDL on it -- does not hold up a run that drops nothing.
func TestRetentionTakesNoLockWhenNothingIsDue(t *testing.T) {
	const held = "audit_events_1991_03"
	retentionRetire(t, "1992-01-01")
	retentionProvisionMonth(t, "1991-03-15", time.Date(1991, 3, 10, 12, 0, 0, 0, time.UTC), uuid.NewString())
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, `LOCK TABLE public.`+held+` IN ACCESS EXCLUSIVE MODE`)
		require.NoError(t, err)

		// Nothing is due: the partition ends after the cutoff.
		started := time.Now()
		dropped, err := retentionCommitted(time.Date(1991, 3, 20, 0, 0, 0, 0, time.UTC))
		require.NoError(t, err, "a run with nothing due does not wait for the partition")
		require.Zero(t, dropped)
		require.Less(t, time.Since(started), 5*time.Second)
	})
}

// The migration replaced the function's body, and CREATE OR REPLACE keeps its
// owner and grants; what it must have changed is the wait and the settings it
// reads bounds under. Execute stays with the control plane alone: retention is
// DDL on the audit table, and no tenant transaction may run it.
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
	require.Contains(t, config, "DateStyle=ISO, MDY", "a bound is rendered with its UTC offset, whatever the caller's DateStyle")
	require.Contains(t, config, "TimeZone=UTC", "a bound is rendered in one zone, whatever the caller's")
	require.False(t, publicMayExecute)
	require.True(t, controlPlaneMay)
	require.False(t, tenantMay)
}

// A partition's upper bound is read back as the instant PostgreSQL recorded,
// whatever DateStyle the session running retention uses. Any DateStyle but ISO
// renders a timestamp with time zone with its zone's abbreviation instead of
// its UTC offset, and an abbreviation does not name one offset, so the function
// renders and reads every bound under settings it pins itself.
func TestRetentionReadsEveryBoundAsTheInstantPostgreSQLRecorded(t *testing.T) {
	for _, style := range []string{"SQL, DMY", "Postgres", "German"} {
		t.Run(style, func(t *testing.T) {
			asRetention(t, func(ctx context.Context, tx pgx.Tx) {
				retentionEnsureMonth(t, ctx, tx, "UTC", "1996-07-15")
				retentionEnsureMonth(t, ctx, tx, "UTC", "1996-08-15")
				_, err := tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
				require.NoError(t, err)

				// audit_events_1996_08 ends at 1996-09-01 00:00 UTC. An event a
				// quarter of an hour before that is newer than a cutoff half an
				// hour before it, so the partition holding it is not due.
				end := time.Date(1996, 9, 1, 0, 0, 0, 0, time.UTC)
				require.Equal(t, "audit_events_1996_08", retentionInsertEvent(t, ctx, tx, end.Add(-15*time.Minute)))

				retentionSetZone(t, ctx, tx, "Europe/Dublin")
				_, err = tx.Exec(ctx, `SELECT set_config('DateStyle', $1, true)`, style)
				require.NoError(t, err)
				dropped, err := testStore.DropAuditPartitionsBefore(infra.BindControlPlaneTx(ctx, tx), end.Add(-30*time.Minute))
				require.NoError(t, err)

				after := retentionPartitions(t, ctx, tx)
				require.Contains(t, after, "audit_events_1996_08", "a partition that ends after the cutoff is not dropped")
				require.Equal(t, int64(1), retentionRowsIn(t, ctx, tx, "audit_events_1996_08"), "the event newer than the cutoff survives")
				require.NotContains(t, after, "audit_events_1996_07", "a partition that ends before the cutoff is dropped")
				require.Equal(t, int64(1), dropped)
			})
		})
	}
}

// The cutoff is one the audit_events policy allows by the database's clock: no
// later than the start of the transaction less the policy's window. A later one
// reaches partitions still inside the window -- the one holding the present and
// those provisioned ahead of it, or one a caller whose clock runs ahead counts
// as old -- so the function refuses it, and a missing one, and any at all when
// there is no policy, and drops nothing.
func TestRetentionRefusesACutoffThePolicyDoesNotAllow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		noPolicy bool
		cutoff   func(allowed time.Time) any
	}{
		{name: "after the present partition ends", cutoff: func(time.Time) any { return time.Now().AddDate(0, 2, 0) }},
		{name: "infinity", cutoff: func(time.Time) any { return pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true} }},
		{name: "NULL", cutoff: func(time.Time) any { return nil }},
		{name: "a second later than the policy allows", cutoff: func(allowed time.Time) any { return allowed.Add(time.Second) }},
		{name: "from a clock eleven hours ahead", cutoff: func(allowed time.Time) any { return allowed.Add(11 * time.Hour) }},
		{name: "with no policy", noPolicy: true, cutoff: func(allowed time.Time) any { return allowed.AddDate(-10, 0, 0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asRetention(t, func(ctx context.Context, tx pgx.Tx) {
				retentionSetZone(t, ctx, tx, "UTC")
				var allowed time.Time
				require.NoError(t, tx.QueryRow(ctx, `
					SELECT now() - make_interval(days => retention_days)
					  FROM data_retention_policies WHERE resource_type = 'audit_events'`).Scan(&allowed))
				_, err := tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
				require.NoError(t, err)
				// Other tests may have written to the present partition too, so
				// the check is that it keeps every row it had.
				present := retentionInsertEvent(t, ctx, tx, time.Now())
				before, rows := retentionPartitions(t, ctx, tx), retentionRowsIn(t, ctx, tx, present)

				attempt, err := tx.Begin(ctx)
				require.NoError(t, err)
				if tc.noPolicy {
					_, err = attempt.Exec(ctx, `DELETE FROM data_retention_policies WHERE resource_type = 'audit_events'`)
					require.NoError(t, err)
				}
				var dropped int64
				err = attempt.QueryRow(ctx, `SELECT audit_events_drop_partitions_before($1)`, tc.cutoff(allowed)).Scan(&dropped)
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, "a cutoff the policy does not allow is refused")
				require.Equal(t, "22023", pgErr.Code, "invalid_parameter_value")
				require.NoError(t, attempt.Rollback(ctx))

				require.Equal(t, before, retentionPartitions(t, ctx, tx), "a refused run drops nothing")
				require.Equal(t, rows, retentionRowsIn(t, ctx, tx, present), "the present's events survive")
			})
		})
	}
}

// A caller whose clock runs eleven hours ahead counts a partition still inside
// the window as old. The function holds the cutoff to the policy by the
// database's clock, so it refuses that one, and the partition and its event
// survive; at the cutoff the policy allows the partition is not due, since it
// ends after it. The policy's window is moved, inside the transaction, to end in
// 1970, where no other partition lies.
func TestRetentionKeepsAPartitionInsideTheWindowWhateverTheCallersClock(t *testing.T) {
	asRetention(t, func(ctx context.Context, tx pgx.Tx) {
		retentionSetZone(t, ctx, tx, "UTC")
		var allowed time.Time
		require.NoError(t, tx.QueryRow(ctx, `
			UPDATE data_retention_policies SET retention_days = now()::date - date '1970-06-15'
			 WHERE resource_type = 'audit_events'
			RETURNING now() - make_interval(days => retention_days)`).Scan(&allowed))
		const probe = "audit_events_1970_06"
		_, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TABLE public.%s PARTITION OF public.audit_events FOR VALUES FROM ('%s') TO ('%s')`,
			probe, allowed.Add(-time.Hour).Format(time.RFC3339Nano), allowed.Add(time.Hour).Format(time.RFC3339Nano)))
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
		require.NoError(t, err)
		require.Equal(t, probe, retentionInsertEvent(t, ctx, tx, allowed.Add(30*time.Minute)),
			"the premise: the partition holds an event inside the window")

		attempt, err := tx.Begin(ctx)
		require.NoError(t, err)
		_, err = testStore.DropAuditPartitionsBefore(infra.BindControlPlaneTx(ctx, attempt), allowed.Add(11*time.Hour))
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, "the cutoff of a clock eleven hours ahead is refused")
		require.Equal(t, "22023", pgErr.Code)
		require.NoError(t, attempt.Rollback(ctx))
		require.Contains(t, retentionPartitions(t, ctx, tx), probe)
		require.Equal(t, int64(1), retentionRowsIn(t, ctx, tx, probe), "the event inside the window survives")

		require.Zero(t, runRetentionIn(t, ctx, tx, allowed), "at the cutoff the policy allows, the partition is not due")
		require.Equal(t, int64(1), retentionRowsIn(t, ctx, tx, probe))
	})
}

// The function decides under its lock from the catalog as it stands once the
// lock is held, and a statement reads the catalog as it stands only at READ
// COMMITTED: at REPEATABLE READ or SERIALIZABLE every statement reads the
// snapshot the transaction took first, before the lock. It refuses to run at
// either, and drops nothing.
func TestRetentionRunsOnlyAtReadCommitted(t *testing.T) {
	for _, level := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(level), func(t *testing.T) {
			asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
				tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: level})
				require.NoError(t, err)
				defer func() { _ = tx.Rollback(ctx) }()
				retentionEnsureMonth(t, ctx, tx, "UTC", "1996-03-15")
				_, err = tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
				require.NoError(t, err)

				attempt, err := tx.Begin(ctx)
				require.NoError(t, err)
				_, err = testStore.DropAuditPartitionsBefore(infra.BindControlPlaneTx(ctx, attempt), time.Date(1996, 5, 1, 0, 0, 0, 0, time.UTC))
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, "retention refuses a transaction that reads one snapshot throughout")
				require.Equal(t, "25000", pgErr.Code, "invalid_transaction_state")
				require.NoError(t, attempt.Rollback(ctx))
				require.Contains(t, retentionPartitions(t, ctx, tx), "audit_events_1996_03", "a refused run drops nothing")
			})
		})
	}
}

// retentionProvisionMonth commits the monthly partition holding day, created
// from a UTC session, and one event at eventAt in the organization org.
func retentionProvisionMonth(t *testing.T, day string, eventAt time.Time, org string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('TimeZone', 'UTC', true)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT audit_events_ensure_partition($1::date)`, day); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO audit_events (id, event_type, resource, org_id, created_at) VALUES ($1, $2, 'example', $3, $4)`,
			uuid.NewString(), string(business.EventAuthLogin), org, eventAt)
		return err
	}))
}

// retentionDropTableAfter drops table, as its owner, once the test is over,
// whether retention dropped it, a test detached it, or it is still attached.
func retentionDropTableAfter(t *testing.T, table string) {
	t.Helper()
	t.Cleanup(func() {
		asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
			_, err := conn.Exec(ctx, `DROP TABLE IF EXISTS public.`+pgx.Identifier{table}.Sanitize())
			require.NoError(t, err)
		})
	})
}

// retentionHoldReader opens a control-plane transaction that has read
// audit_events, so holds ACCESS SHARE on it and on every partition, and keeps
// it open until the returned release is called.
func retentionHoldReader(t *testing.T) (release func()) {
	t.Helper()
	reading, done, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var readerErr error
	go func() {
		defer close(finished)
		readerErr = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			var rows int64
			if err := storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&rows); err != nil {
				return err
			}
			close(reading)
			<-done
			return nil
		})
	}()
	select {
	case <-reading:
	case <-finished:
		t.Fatalf("the reader failed before it held audit_events: %v", readerErr)
	}
	var once sync.Once
	release = func() { once.Do(func() { close(done); <-finished }) }
	t.Cleanup(release)
	return release
}

// retentionAwaitQueuedLocks waits until n sessions wait for an ACCESS EXCLUSIVE
// lock on audit_events.
func retentionAwaitQueuedLocks(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var queued int
		err := testPool.QueryRow(testCtx, `
			SELECT count(*) FROM pg_locks
			 WHERE locktype = 'relation' AND relation = 'public.audit_events'::regclass
			   AND mode = 'AccessExclusiveLock' AND NOT granted`).Scan(&queued)
		return err == nil && queued >= n
	}, 30*time.Second, 20*time.Millisecond, "%d sessions never queued for audit_events", n)
}

// detachPending reports whether table is a partition of audit_events whose
// detach has begun and not finished.
func detachPending(table string) (bool, error) {
	var pending bool
	err := testPool.QueryRow(testCtx, `
		SELECT inhdetachpending FROM pg_inherits
		 WHERE inhrelid = to_regclass('public.' || $1) AND inhparent = 'public.audit_events'::regclass`, table).Scan(&pending)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return pending, err
}

func retentionDetachPending(t *testing.T, table string) bool {
	t.Helper()
	pending, err := detachPending(table)
	require.NoError(t, err)
	return pending
}

// retentionOwnerRows counts table's rows in the organization org, read by name
// as the table's owner: the partitions force row-level security, so the owner
// sees an organization's rows only with that organization bound.
func retentionOwnerRows(t *testing.T, ctx context.Context, conn *pgxpool.Conn, table, org string) int64 {
	t.Helper()
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT set_config('app.current_org_id', $1, true)`, org)
	require.NoError(t, err)
	var rows int64
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM public.`+pgx.Identifier{table}.Sanitize()).Scan(&rows))
	return rows
}

type retentionOutcome struct {
	dropped int64
	err     error
}

// The decision is taken again once the lock is held. A partition detached
// while retention waited for its lock -- an operator keeping that month out of
// retention -- is no longer audit_events', and the run leaves it, and its
// events, alone.
func TestRetentionLeavesAPartitionDetachedWhileItWaitedForItsLock(t *testing.T) {
	const kept = "audit_events_1995_02"
	org := uuid.NewString()
	retentionRetire(t, "1996-01-01")
	retentionDropTableAfter(t, kept)
	retentionProvisionMonth(t, "1995-02-15", time.Date(1995, 2, 10, 12, 0, 0, 0, time.UTC), org)

	release := retentionHoldReader(t)
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		detached := make(chan error, 1)
		go func() {
			_, err := conn.Exec(ctx, `ALTER TABLE public.audit_events DETACH PARTITION public.`+kept)
			detached <- err
		}()
		retentionAwaitQueuedLocks(t, 1)

		// Retention finds the partition due before it locks audit_events, and
		// then waits for that lock behind the detach.
		ran := make(chan retentionOutcome, 1)
		go func() {
			dropped, err := retentionCommitted(time.Date(1995, 4, 1, 0, 0, 0, 0, time.UTC))
			ran <- retentionOutcome{dropped, err}
		}()
		retentionAwaitQueuedLocks(t, 2)
		release()

		require.NoError(t, <-detached, "the premise: the detach committed while retention waited")
		got := <-ran
		require.NoError(t, got.err)
		require.Zero(t, got.dropped, "a partition that left audit_events is not retention's")
		exists, _ := retentionPartitionRows(t, kept)
		require.True(t, exists, "the detached table survives")
		require.Equal(t, int64(1), retentionOwnerRows(t, ctx, conn, kept, org), "and its events")
	})
}

// A partition whose DETACH ... CONCURRENTLY has begun and not finished is
// leaving audit_events, and is never dropped. The detach commits the partition
// as pending, then waits for every transaction that may still see it;
// interrupted there, it leaves the partition pending until someone finalizes it.
func TestRetentionNeverDropsAPartitionBeingDetached(t *testing.T) {
	const pending = "audit_events_1995_05"
	org := uuid.NewString()
	retentionRetire(t, "1996-01-01")
	retentionDropTableAfter(t, pending)
	retentionProvisionMonth(t, "1995-05-15", time.Date(1995, 5, 10, 12, 0, 0, 0, time.UTC), org)

	release := retentionHoldReader(t)
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		detached := make(chan error, 1)
		go func() {
			_, err := conn.Exec(ctx, `ALTER TABLE public.audit_events DETACH PARTITION public.`+pending+` CONCURRENTLY`)
			detached <- err
		}()
		require.Eventually(t, func() bool {
			isPending, err := detachPending(pending)
			return err == nil && isPending
		}, 30*time.Second, 20*time.Millisecond, "the detach never committed its first phase")
		require.NoError(t, conn.Conn().PgConn().CancelRequest(ctx))
		var pgErr *pgconn.PgError
		require.ErrorAs(t, <-detached, &pgErr)
		require.Equal(t, "57014", pgErr.Code, "the premise: the detach was interrupted")
		release()
		require.True(t, retentionDetachPending(t, pending), "the premise: the partition is pending detach")

		dropped, err := retentionCommitted(time.Date(1995, 7, 1, 0, 0, 0, 0, time.UTC))
		require.NoError(t, err)
		require.Zero(t, dropped, "a partition being detached is not dropped")
		require.True(t, retentionDetachPending(t, pending))
		require.Equal(t, int64(1), retentionOwnerRows(t, ctx, conn, pending, org), "its events survive")
	})
}

// Two runs at once drop each due partition once, and neither fails: the second
// waits for the first's lock, then decides from the catalog as the first left
// it.
func TestTwoRetentionRunsAtOnceDropEachPartitionOnce(t *testing.T) {
	retentionRetire(t, "1996-01-01")
	org := uuid.NewString()
	retentionProvisionMonth(t, "1995-08-15", time.Date(1995, 8, 10, 12, 0, 0, 0, time.UTC), org)
	retentionProvisionMonth(t, "1995-09-15", time.Date(1995, 9, 10, 12, 0, 0, 0, time.UTC), org)

	release := retentionHoldReader(t)
	ran := make(chan retentionOutcome, 2)
	for queued := 1; queued <= 2; queued++ {
		go func() {
			dropped, err := retentionCommitted(time.Date(1995, 11, 1, 0, 0, 0, 0, time.UTC))
			ran <- retentionOutcome{dropped, err}
		}()
		retentionAwaitQueuedLocks(t, queued)
	}
	release()

	var total int64
	for range 2 {
		got := <-ran
		require.NoError(t, got.err)
		total += got.dropped
	}
	require.Equal(t, int64(2), total, "each due partition is dropped once")
	for _, name := range []string{"audit_events_1995_08", "audit_events_1995_09"} {
		exists, _ := retentionPartitionRows(t, name)
		require.False(t, exists)
	}
}

// retentionSession opens a session of the tables' owner outside every pool, to
// hold a lock in, or change the catalog from, while retention runs in another.
func retentionSession(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn, err := codefly.For(testCtx).Service("store").Secret("postgres", "owner-connection")
	require.NoError(t, err)
	conn, err := pgx.Connect(testCtx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(testCtx) })
	return conn
}

// retentionAwaitWaiters waits until n sessions wait for a lock on relation.
func retentionAwaitWaiters(t *testing.T, relation string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting int
		err := testPool.QueryRow(testCtx, `
			SELECT count(*) FROM pg_locks WHERE relation = to_regclass($1) AND NOT granted`, relation).Scan(&waiting)
		return err == nil && waiting >= n
	}, 10*time.Second, 20*time.Millisecond, "%d sessions never waited for %s", n, relation)
}

// retentionKeepPolicy puts the audit_events retention policy back as it is now
// once the test is over, whatever the test changes it to, or if it deletes it.
func retentionKeepPolicy(t *testing.T) {
	t.Helper()
	var id string
	var days int
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT id::text, retention_days FROM data_retention_policies WHERE resource_type = 'audit_events'`).Scan(&id, &days))
	})
	t.Cleanup(func() {
		asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
			_, err := conn.Exec(ctx, `
				INSERT INTO data_retention_policies (id, resource_type, retention_days) VALUES ($1, 'audit_events', $2)
				ON CONFLICT (resource_type) DO UPDATE SET retention_days = EXCLUDED.retention_days`, id, days)
			require.NoError(t, err)
		})
	})
}

// Holding audit_events does not stop a partition being renamed, and LOCK TABLE
// finds the table it locks by name. Retention chooses March and waits for it
// behind a reader, and meanwhile an operator renames March away and April to
// March's name. Locking by that name would reach April, which ends after the
// cutoff, and drop it and its event. Retention locks March by identity, so it
// refuses, drops nothing, and April keeps its event. That holds whether March's
// new name takes it out of retention's names or is another month's, under which
// March is still due; the next run decides from the catalog as the renames left
// it.
func TestRetentionDropsNothingWhenAPartitionItChoseIsRenamedWhileItWaits(t *testing.T) {
	const march, april = "audit_events_1994_03", "audit_events_1994_04"
	for _, tc := range []struct {
		name, renamed string
		nextDrops     int64
	}{
		{"out of retention's names", "audit_events_1994_03_renamed", 0},
		{"to another month's name", "audit_events_1993_12", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org := uuid.NewString()
			for _, table := range []string{march, april, tc.renamed} {
				retentionDropTableAfter(t, table)
			}
			retentionProvisionMonth(t, "1994-03-15", time.Date(1994, 3, 10, 12, 0, 0, 0, time.UTC), org)
			retentionProvisionMonth(t, "1994-04-15", time.Date(1994, 4, 2, 12, 0, 0, 0, time.UTC), org)
			cutoff := time.Date(1994, 4, 1, 0, 0, 0, 0, time.UTC)

			reader := retentionSession(t)
			_, err := reader.Exec(testCtx, `BEGIN; LOCK TABLE ONLY public.`+march+` IN ACCESS SHARE MODE`)
			require.NoError(t, err)
			operator := retentionSession(t)
			renames := make(chan error, 1)
			go func() {
				_, err := operator.Exec(testCtx, `BEGIN;
					ALTER TABLE public.`+march+` RENAME TO `+tc.renamed+`;
					ALTER TABLE public.`+april+` RENAME TO `+march+`;
					COMMIT`)
				renames <- err
			}()
			retentionAwaitWaiters(t, "public."+march, 1)

			ran := make(chan retentionOutcome, 1)
			go func() {
				dropped, err := retentionCommitted(cutoff)
				ran <- retentionOutcome{dropped, err}
			}()
			retentionAwaitWaiters(t, "public."+march, 2)
			_, err = reader.Exec(testCtx, `COMMIT`)
			require.NoError(t, err)
			require.NoError(t, <-renames, "the premise: the renames committed while retention waited")

			got := <-ran
			var pgErr *pgconn.PgError
			require.ErrorAs(t, got.err, &pgErr, "retention refused")
			require.Equal(t, "55000", pgErr.Code, "object_not_in_prerequisite_state")
			require.Zero(t, got.dropped)
			asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
				require.Equal(t, int64(1), retentionOwnerRows(t, ctx, conn, march, org), "April, under March's name, keeps its event")
				require.Equal(t, int64(1), retentionOwnerRows(t, ctx, conn, tc.renamed, org), "and March, renamed, its own")
			})

			dropped, err := retentionCommitted(cutoff)
			require.NoError(t, err)
			require.Equal(t, tc.nextDrops, dropped, "the next run decides from the catalog as the renames left it")
			exists, _ := retentionPartitionRows(t, march)
			require.True(t, exists, "and April is never due")
		})
	}
}

// Renaming a partition out of retention's names keeps it from retention, and
// does so even while a run that chose it waits for a lock: the run, holding
// every partition it chose, reads which are due once more, and drops none when
// one is no longer. Retention chooses February and March and waits behind a
// reader for February; an operator renames March out of retention's names
// meanwhile. The run drops nothing, and the next one drops February alone.
func TestRetentionKeepsAPartitionRenamedOutOfItsNamesWhileItWaits(t *testing.T) {
	const february, march, kept = "audit_events_1994_02", "audit_events_1994_03", "audit_events_1994_03_kept"
	org := uuid.NewString()
	for _, table := range []string{february, march, kept} {
		retentionDropTableAfter(t, table)
	}
	retentionProvisionMonth(t, "1994-02-15", time.Date(1994, 2, 10, 12, 0, 0, 0, time.UTC), org)
	retentionProvisionMonth(t, "1994-03-15", time.Date(1994, 3, 10, 12, 0, 0, 0, time.UTC), org)
	cutoff := time.Date(1994, 4, 1, 0, 0, 0, 0, time.UTC)

	reader := retentionSession(t)
	_, err := reader.Exec(testCtx, `BEGIN; LOCK TABLE ONLY public.`+february+` IN ACCESS SHARE MODE`)
	require.NoError(t, err)
	ran := make(chan retentionOutcome, 1)
	go func() {
		dropped, err := retentionCommitted(cutoff)
		ran <- retentionOutcome{dropped, err}
	}()
	retentionAwaitWaiters(t, "public."+february, 1)
	_, err = retentionSession(t).Exec(testCtx, `ALTER TABLE public.`+march+` RENAME TO `+kept)
	require.NoError(t, err, "the premise: March is renamed while retention waits for February")
	_, err = reader.Exec(testCtx, `COMMIT`)
	require.NoError(t, err)

	got := <-ran
	var pgErr *pgconn.PgError
	require.ErrorAs(t, got.err, &pgErr, "retention refused")
	require.Equal(t, "55000", pgErr.Code, "object_not_in_prerequisite_state")
	require.Zero(t, got.dropped)

	dropped, err := retentionCommitted(cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(1), dropped, "the next run drops February alone")
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		require.Equal(t, int64(1), retentionOwnerRows(t, ctx, conn, kept, org), "March, renamed out, keeps its event")
	})
}

// A run holds the audit_events policy it keeps to until it commits. A change
// to the policy made while the run waits for a lock -- on audit_events, or on
// the partition it drops -- waits in turn for the run: whether it extends the
// window or deletes the policy to keep every record, it cannot commit under a
// run that decided by the policy it replaces. It goes through once the run
// has committed.
func TestAPolicyChangeWaitsForARetentionRunUnderWay(t *testing.T) {
	const due = "audit_events_1994_01"
	for _, change := range []struct{ name, statement string }{
		{"extending the window", `UPDATE data_retention_policies SET retention_days = 20000 WHERE resource_type = 'audit_events'`},
		{"deleting the policy", `DELETE FROM data_retention_policies WHERE resource_type = 'audit_events'`},
	} {
		for _, held := range []string{"audit_events", due} {
			t.Run(change.name+" while it waits for "+held, func(t *testing.T) {
				retentionDropTableAfter(t, due)
				retentionProvisionMonth(t, "1994-01-15", time.Date(1994, 1, 10, 12, 0, 0, 0, time.UTC), uuid.NewString())
				retentionKeepPolicy(t)

				holder := retentionSession(t)
				_, err := holder.Exec(testCtx, `BEGIN; LOCK TABLE ONLY public.`+held+` IN ACCESS SHARE MODE`)
				require.NoError(t, err)
				ran := make(chan retentionOutcome, 1)
				go func() {
					dropped, err := retentionCommitted(time.Date(1994, 2, 1, 0, 0, 0, 0, time.UTC))
					ran <- retentionOutcome{dropped, err}
				}()
				retentionAwaitWaiters(t, "public."+held, 1)

				operator := retentionSession(t)
				_, err = operator.Exec(testCtx, `BEGIN; SET LOCAL lock_timeout = '1s'; `+change.statement)
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, "the change waits for the run")
				require.Equal(t, "55P03", pgErr.Code, "lock_not_available")
				_, err = operator.Exec(testCtx, `ROLLBACK`)
				require.NoError(t, err)

				_, err = holder.Exec(testCtx, `COMMIT`)
				require.NoError(t, err)
				got := <-ran
				require.NoError(t, got.err)
				require.Equal(t, int64(1), got.dropped, "the run kept to the policy it held")
				_, err = operator.Exec(testCtx, change.statement)
				require.NoError(t, err, "once the run has committed, the change goes through")
			})
		}
	}
}

// The cutoff is checked against the policy as the run reads it under its row
// lock, not as it read it before. A run finds a partition due, then waits to
// lock the policy behind an operator who has the table to themselves and
// extends the window; once the change commits, the run reads the new window,
// refuses the cutoff, and drops nothing.
func TestRetentionKeepsToAPolicyChangedWhileItWaitsToLockIt(t *testing.T) {
	const due = "audit_events_1994_09"
	retentionDropTableAfter(t, due)
	retentionProvisionMonth(t, "1994-09-15", time.Date(1994, 9, 10, 12, 0, 0, 0, time.UTC), uuid.NewString())
	retentionKeepPolicy(t)

	operator := retentionSession(t)
	_, err := operator.Exec(testCtx, `BEGIN; LOCK TABLE data_retention_policies IN EXCLUSIVE MODE`)
	require.NoError(t, err)
	ran := make(chan retentionOutcome, 1)
	go func() {
		dropped, err := retentionCommitted(time.Date(1994, 10, 1, 0, 0, 0, 0, time.UTC))
		ran <- retentionOutcome{dropped, err}
	}()
	retentionAwaitWaiters(t, "public.data_retention_policies", 1)
	_, err = operator.Exec(testCtx, `
		UPDATE data_retention_policies SET retention_days = 20000 WHERE resource_type = 'audit_events';
		COMMIT`)
	require.NoError(t, err)

	got := <-ran
	var pgErr *pgconn.PgError
	require.ErrorAs(t, got.err, &pgErr, "the run keeps to the window the change left")
	require.Equal(t, "22023", pgErr.Code, "invalid_parameter_value")
	require.Zero(t, got.dropped)
	exists, _ := retentionPartitionRows(t, due)
	require.True(t, exists)
}

// A change to the policy already under way when a run, having found something
// due, reads the policy again makes the run refuse rather than wait: which
// window the change leaves is unknown until it commits. The run drops nothing,
// at once, and the next run keeps to what the change left -- here a window
// that does not allow the cutoff.
func TestRetentionRefusesWhileItsPolicyIsBeingChanged(t *testing.T) {
	const due = "audit_events_1994_06"
	retentionDropTableAfter(t, due)
	retentionProvisionMonth(t, "1994-06-15", time.Date(1994, 6, 10, 12, 0, 0, 0, time.UTC), uuid.NewString())
	retentionKeepPolicy(t)
	cutoff := time.Date(1994, 7, 1, 0, 0, 0, 0, time.UTC)

	operator := retentionSession(t)
	_, err := operator.Exec(testCtx, `BEGIN; UPDATE data_retention_policies SET retention_days = 20000 WHERE resource_type = 'audit_events'`)
	require.NoError(t, err)
	started := time.Now()
	dropped, err := retentionCommitted(cutoff)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "the run refused")
	require.Equal(t, "55P03", pgErr.Code, "lock_not_available")
	require.Zero(t, dropped)
	require.Less(t, time.Since(started), 5*time.Second, "without waiting for the change")
	exists, _ := retentionPartitionRows(t, due)
	require.True(t, exists)

	_, err = operator.Exec(testCtx, `COMMIT`)
	require.NoError(t, err)
	_, err = retentionCommitted(cutoff)
	require.ErrorAs(t, err, &pgErr, "the next run keeps to the window the change left")
	require.Equal(t, "22023", pgErr.Code, "invalid_parameter_value")
	exists, _ = retentionPartitionRows(t, due)
	require.True(t, exists)
}

// withSessionDefault returns connection with a server setting every session it
// opens starts with.
func withSessionDefault(t *testing.T, connection, setting, value string) string {
	t.Helper()
	if parsed, err := url.Parse(connection); err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		query := parsed.Query()
		query.Set(setting, value)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return connection + " " + setting + "=" + value
}

// The store's own retention call runs at READ COMMITTED whatever isolation the
// database gives a transaction by default, so the function's refusal never
// stops scheduled retention. A caller's own transaction at another level is
// refused and drops nothing.
func TestRetentionRunsAtReadCommittedWhateverTheDatabaseDefault(t *testing.T) {
	const due = "audit_events_1995_10"
	retentionRetire(t, "1996-01-01")
	retentionProvisionMonth(t, "1995-10-15", time.Date(1995, 10, 10, 12, 0, 0, 0, time.UTC), uuid.NewString())

	secret := func(key string) string {
		value, err := codefly.For(testCtx).Service("store").Secret("postgres", key)
		require.NoError(t, err)
		return value
	}
	store, err := infra.NewPostgresStoreWithCapabilities(testCtx,
		secret("read-only-connection"), secret("read-write-connection"),
		withSessionDefault(t, secret("control-plane-connection"), "default_transaction_isolation", "serializable"))
	require.NoError(t, err)
	t.Cleanup(store.Close)

	var level string
	require.NoError(t, store.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT current_setting('transaction_isolation')`).Scan(&level)
	}))
	require.Equal(t, "serializable", level, "the premise: this store's transactions default to SERIALIZABLE")

	cutoff := time.Date(1995, 12, 1, 0, 0, 0, 0, time.UTC)
	err = store.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := store.DropAuditPartitionsBefore(ctx, cutoff)
		return err
	})
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "a caller's SERIALIZABLE transaction is refused")
	require.Equal(t, "25000", pgErr.Code, "invalid_transaction_state")
	exists, _ := retentionPartitionRows(t, due)
	require.True(t, exists, "a refused run drops nothing")

	dropped, err := store.DropAuditPartitionsBefore(testCtx, cutoff)
	require.NoError(t, err, "with no transaction of its caller's, retention opens its own at READ COMMITTED")
	require.Equal(t, int64(1), dropped)
	exists, _ = retentionPartitionRows(t, due)
	require.False(t, exists)
}

// The function waits ten seconds in all for its locks, not ten for each:
// lock_timeout bounds one wait, and a run that drops two partitions waits up to
// three times, each time holding audit_events ACCESS EXCLUSIVE, which audit
// writes queue behind. Two sessions each hold a due partition; the first lets go
// after six seconds, the second not until the run is over. The run gives up
// when its ten seconds are spent, drops nothing, and a later run drops both.
func TestRetentionWaitsTenSecondsInAllForItsLocks(t *testing.T) {
	retentionRetire(t, "1993-01-01")
	for _, day := range []string{"1992-01-15", "1992-02-15"} {
		retentionProvisionMonth(t, day, time.Date(1992, 1, 10, 12, 0, 0, 0, time.UTC), uuid.NewString())
	}
	dsn, err := codefly.For(testCtx).Service("store").Secret("postgres", "owner-connection")
	require.NoError(t, err)
	hold := func(partition string) (release func()) {
		conn, err := pgx.Connect(testCtx, dsn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close(testCtx) })
		_, err = conn.Exec(testCtx, `BEGIN; LOCK TABLE ONLY public.`+pgx.Identifier{partition}.Sanitize()+` IN ACCESS SHARE MODE`)
		require.NoError(t, err)
		var once sync.Once
		release = func() { once.Do(func() { _, err := conn.Exec(testCtx, `COMMIT`); require.NoError(t, err) }) }
		t.Cleanup(release)
		return release
	}
	awaitWaitingFor := func(partition string) {
		require.Eventually(t, func() bool {
			var waiting int
			err := testPool.QueryRow(testCtx, `
				SELECT count(*) FROM pg_locks
				 WHERE relation = to_regclass('public.' || $1) AND mode = 'AccessExclusiveLock' AND NOT granted`,
				partition).Scan(&waiting)
			return err == nil && waiting > 0
		}, 10*time.Second, 20*time.Millisecond, "retention never waited for %s", partition)
	}
	releaseFirst := hold("audit_events_1992_01")
	releaseSecond := hold("audit_events_1992_02")

	type outcome struct {
		dropped int64
		err     error
		elapsed time.Duration
	}
	ran := make(chan outcome, 1)
	go func() {
		started := time.Now()
		dropped, err := retentionCommitted(time.Date(1992, 3, 1, 0, 0, 0, 0, time.UTC))
		ran <- outcome{dropped, err, time.Since(started)}
	}()
	awaitWaitingFor("audit_events_1992_01")
	time.Sleep(6 * time.Second)
	releaseFirst()
	awaitWaitingFor("audit_events_1992_02")

	select {
	case got := <-ran:
		var pgErr *pgconn.PgError
		require.ErrorAs(t, got.err, &pgErr, "retention gave up for want of its locks")
		require.Equal(t, "55P03", pgErr.Code, "lock_not_available")
		require.Zero(t, got.dropped)
		require.Less(t, got.elapsed, 11*time.Second, "ten seconds of waiting in all, not ten for each lock")
	case <-time.After(30 * time.Second):
		t.Fatal("retention went on waiting past its ten seconds")
	}
	for _, partition := range []string{"audit_events_1992_01", "audit_events_1992_02"} {
		exists, _ := retentionPartitionRows(t, partition)
		require.True(t, exists, "a run that gave up dropped nothing")
	}

	releaseSecond()
	dropped, err := retentionCommitted(time.Date(1992, 3, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, int64(2), dropped, "with its locks free, the next run drops both")
}

// A retention window is a positive number of days, and the table refuses any
// other, so no writer can leave a window retention would have to read as
// something it does not say.
func TestARetentionPolicyKeepsRecordsForAPositiveNumberOfDays(t *testing.T) {
	asRetention(t, func(ctx context.Context, tx pgx.Tx) {
		for _, days := range []int{0, -1} {
			for _, statement := range []string{
				`UPDATE data_retention_policies SET retention_days = $1 WHERE resource_type = 'sessions'`,
				`INSERT INTO data_retention_policies (resource_type, retention_days) VALUES ('example_records', $1)`,
			} {
				attempt, err := tx.Begin(ctx)
				require.NoError(t, err)
				_, err = attempt.Exec(ctx, statement, days)
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, "%d days is refused: %s", days, statement)
				require.Equal(t, "23514", pgErr.Code, "check_violation")
				require.NoError(t, attempt.Rollback(ctx))
			}
		}
	})
}

// The migration that made a window a positive number of days refuses to apply
// while a policy holds one that is not, and names it: which window was meant is
// the operator's to say, not retention's to guess.
func TestTheRetentionMigrationRefusesAWindowThatIsNotPositive(t *testing.T) {
	paths, err := filepath.Glob("../../../../store/migrations/*_audit_retention_partition_bounds.up.sql")
	require.NoError(t, err)
	require.Len(t, paths, 1, "the migration that holds the rule")
	migration, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	asRetention(t, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, `
			ALTER TABLE data_retention_policies DROP CONSTRAINT data_retention_policies_retention_days_positive;
			UPDATE data_retention_policies SET retention_days = 0 WHERE resource_type = 'sessions'`)
		require.NoError(t, err, "the premise: a policy from before the rule")
		_, err = tx.Exec(ctx, string(migration))
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, "the migration refuses")
		require.Equal(t, "23514", pgErr.Code, "check_violation")
		require.Contains(t, pgErr.Message, "sessions (0 days)", "and names the policy")
	})
}
