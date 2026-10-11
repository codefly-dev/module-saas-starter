//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"accounts/pkg/infra/storetx"
)

// Audit partition provisioning creates the months audit writes land in, at
// startup and on each retention tick, through the store's SECURITY DEFINER
// functions. These tests hold it to bounds that do not depend on the session
// that provisions, to neighbours an earlier release cut at another zone's
// midnight, and to giving up rather than holding audit writes behind a lock it
// cannot have.

// A new partition is bounded by UTC midnights whatever the zone of the session
// that provisions it, so its range does not depend on who ran the call.
func TestANewPartitionIsBoundedByUTCMidnightsWhateverTheSessionsZone(t *testing.T) {
	for _, zone := range []string{"America/New_York", "Asia/Kolkata", "Pacific/Auckland", "UTC"} {
		t.Run(zone, func(t *testing.T) {
			asRetention(t, func(ctx context.Context, tx pgx.Tx) {
				retentionEnsureMonth(t, ctx, tx, zone, "2002-05-15")
				lower, upper := retentionBounds(t, ctx, tx, "audit_events_2002_05")
				require.Equal(t, time.Date(2002, 5, 1, 0, 0, 0, 0, time.UTC), lower)
				require.Equal(t, time.Date(2002, 6, 1, 0, 0, 0, 0, time.UTC), upper)
			})
		})
	}
}

// A partition an earlier release cut at another zone's midnight still borders
// the months provisioned after it. A new month starts where its predecessor ends
// and ends where its successor starts: a range from UTC midnight would overlap a
// neighbour cut west of UTC, which PostgreSQL refuses, or leave a gap beside one
// cut east of it, where an insert finds no partition. Every instant at the seam
// lands in one of the two.
func TestANewPartitionMeetsANeighbourCutAtAnotherZonesMidnight(t *testing.T) {
	const month = "audit_events_2002_08"
	for _, tc := range []struct {
		name, zone, neighbourDay, neighbour string
		neighbourIsNext                     bool
	}{
		{"after a month cut west of UTC", "America/New_York", "2002-07-15", "audit_events_2002_07", false},
		{"after a month cut east of UTC", "Asia/Tokyo", "2002-07-15", "audit_events_2002_07", false},
		{"before a month cut west of UTC", "America/New_York", "2002-09-15", "audit_events_2002_09", true},
		{"before a month cut east of UTC", "Asia/Tokyo", "2002-09-15", "audit_events_2002_09", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asRetention(t, func(ctx context.Context, tx pgx.Tx) {
				retentionLegacyMonth(t, ctx, tx, tc.zone, tc.neighbourDay)
				retentionEnsureMonth(t, ctx, tx, "UTC", "2002-08-15")

				neighbourLower, neighbourUpper := retentionBounds(t, ctx, tx, tc.neighbour)
				lower, upper := retentionBounds(t, ctx, tx, month)
				seam, earlier, later := lower, tc.neighbour, month
				if tc.neighbourIsNext {
					seam, earlier, later = upper, month, tc.neighbour
					require.Equal(t, time.Date(2002, 8, 1, 0, 0, 0, 0, time.UTC), lower, "the month starts at UTC midnight")
					require.Equal(t, neighbourLower, upper, "and ends where its successor starts")
				} else {
					require.Equal(t, neighbourUpper, lower, "the month starts where its predecessor ends")
					require.Equal(t, time.Date(2002, 9, 1, 0, 0, 0, 0, time.UTC), upper, "and ends at UTC midnight")
				}
				require.NotZero(t, seam.Hour(), "the premise: the neighbour's bound is not a UTC midnight")

				_, err := tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
				require.NoError(t, err)
				require.Equal(t, earlier, retentionInsertEvent(t, ctx, tx, seam.Add(-time.Second)), "the last second before the seam")
				require.Equal(t, later, retentionInsertEvent(t, ctx, tx, seam), "the seam itself")
			})
		})
	}
}

// Creating a partition takes audit_events ACCESS EXCLUSIVE, which every audit
// write queues behind for as long as provisioning waits for it. Behind a reader
// that does not finish, provisioning gives up after ten seconds and creates
// nothing; with nothing to create it takes no such lock and does not wait at
// all; once the reader is gone, the partition is created.
func TestProvisioningRefusesInsteadOfHangingBehindAReader(t *testing.T) {
	const probe = "audit_events_1989_01"
	retentionDropTableAfter(t, probe)
	// A provisioning call that does not bound its own wait fails here on this
	// deadline, rather than hang the suite.
	ensure := func() error {
		ctx, cancel := context.WithTimeout(testCtx, 30*time.Second)
		defer cancel()
		return testStore.WithControlPlane(ctx, func(ctx context.Context) error {
			_, err := storetx.Tx(ctx).Exec(ctx, `SELECT audit_events_ensure_partition('1989-01-15'::date)`)
			return err
		})
	}
	provision := func() error {
		return testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return testStore.EnsureAuditPartitions(ctx, 3)
		})
	}
	require.NoError(t, provision(), "the premise: every month the store provisions exists")

	release := retentionHoldReader(t)
	started := time.Now()
	err := ensure()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "provisioning gave up for want of its lock")
	require.Equal(t, "55P03", pgErr.Code, "lock_not_available")
	require.Less(t, time.Since(started), 11*time.Second)
	exists, _ := retentionPartitionRows(t, probe)
	require.False(t, exists, "provisioning that gave up created nothing")

	started = time.Now()
	require.NoError(t, provision(), "with nothing to create, provisioning does not wait for the reader")
	require.Less(t, time.Since(started), 5*time.Second)

	release()
	require.NoError(t, ensure())
	exists, _ = retentionPartitionRows(t, probe)
	require.True(t, exists, "with the reader gone, the partition is created")
}

// Provisioning waits ten seconds in all for its locks, not ten for each. One
// session holds audit_events, which creating a new month needs, for six seconds;
// another holds an existing partition, whose policies bringing it in step reads,
// until provisioning is over. Provisioning gives up when its ten seconds are
// spent, and creates nothing.
func TestProvisioningWaitsTenSecondsInAllForItsLocks(t *testing.T) {
	const held = "audit_events_1989_06"
	retentionDropTableAfter(t, held)
	retentionProvisionMonth(t, "1989-06-15", time.Date(1989, 6, 10, 12, 0, 0, 0, time.UTC), uuid.NewString())

	dsn, err := codefly.For(testCtx).Service("store").Secret("postgres", "owner-connection")
	require.NoError(t, err)
	hold := func(statement string) (release func()) {
		conn, err := pgx.Connect(testCtx, dsn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close(testCtx) })
		_, err = conn.Exec(testCtx, `BEGIN; `+statement)
		require.NoError(t, err)
		var once sync.Once
		release = func() { once.Do(func() { _, err := conn.Exec(testCtx, `COMMIT`); require.NoError(t, err) }) }
		t.Cleanup(release)
		return release
	}
	awaitWaiting := func(relation, mode string) {
		require.Eventually(t, func() bool {
			var waiting int
			err := testPool.QueryRow(testCtx, `
				SELECT count(*) FROM pg_locks WHERE relation = to_regclass($1) AND mode = $2 AND NOT granted`,
				relation, mode).Scan(&waiting)
			return err == nil && waiting > 0
		}, 10*time.Second, 20*time.Millisecond, "provisioning never waited for %s", relation)
	}

	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		// A month ahead that no call has provisioned, so this call must create it.
		var ahead int
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT min(k) FROM generate_series(4, 24) AS k
			 WHERE to_regclass('public.audit_events_' ||
			       to_char(date_trunc('month', now() AT TIME ZONE 'UTC') + make_interval(months => k), 'YYYY_MM')) IS NULL`,
		).Scan(&ahead))

		releaseParent := hold(`LOCK TABLE ONLY public.audit_events IN ACCESS SHARE MODE`)
		releasePartition := hold(`LOCK TABLE public.` + held + ` IN ACCESS EXCLUSIVE MODE`)

		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
		require.NoError(t, err)
		type outcome struct {
			err     error
			elapsed time.Duration
		}
		ran := make(chan outcome, 1)
		go func() {
			started := time.Now()
			_, err := tx.Exec(ctx, `SELECT audit_events_provision_partitions($1)`, ahead)
			ran <- outcome{err, time.Since(started)}
		}()
		awaitWaiting("public.audit_events", "AccessExclusiveLock")
		time.Sleep(6 * time.Second)
		releaseParent()
		awaitWaiting("public."+held, "AccessShareLock")

		select {
		case got := <-ran:
			var pgErr *pgconn.PgError
			require.ErrorAs(t, got.err, &pgErr, "provisioning gave up for want of its locks")
			require.Equal(t, "55P03", pgErr.Code, "lock_not_available")
			require.Less(t, got.elapsed, 11*time.Second, "ten seconds of waiting in all, not ten for each lock")
		case <-time.After(30 * time.Second):
			t.Fatal("provisioning went on waiting past its ten seconds")
		}
		releasePartition()
	})
}

// Bringing a partition in step locks the partition, not its name. While
// provisioning tries for a partition out of step, an operator renames it away
// and another partition to its name, and a reader that queued for the old name
// ends up holding that other partition to the end. Provisioning brings the
// partition it listed in step, under its new name, without waiting for that
// reader, and leaves the other partition as it was. The partition is out of
// step for want of policies, so provisioning reads it without opening it, and
// its first wait is for the partition's lock.
func TestBringingAPartitionInStepLocksThePartitionNotItsName(t *testing.T) {
	const target, other, renamed, tmp = "audit_events_1993_06", "audit_events_1993_07", "audit_events_1993_06_renamed", "audit_events_1993_07_tmp"
	for _, table := range []string{target, other, renamed, tmp} {
		retentionDropTableAfter(t, table)
	}
	retentionProvisionMonth(t, "1993-06-15", time.Date(1993, 6, 10, 12, 0, 0, 0, time.UTC), uuid.NewString())
	retentionProvisionMonth(t, "1993-07-15", time.Date(1993, 7, 10, 12, 0, 0, 0, time.UTC), uuid.NewString())
	policies := func(table string) (count int) {
		require.NoError(t, testPool.QueryRow(testCtx,
			`SELECT count(*) FROM pg_policy WHERE polrelid = to_regclass('public.' || $1)`, table).Scan(&count))
		return count
	}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.EnsureAuditPartitions(ctx, 3)
	}), "the premise: every month provisioning makes exists")
	operator := retentionSession(t)
	_, err := operator.Exec(testCtx, `DO $$
		DECLARE
			pol record;
		BEGIN
			FOR pol IN SELECT polname FROM pg_policy WHERE polrelid = 'public.`+target+`'::regclass LOOP
				EXECUTE format('DROP POLICY %I ON public.`+target+`', pol.polname);
			END LOOP;
		END $$`)
	require.NoError(t, err)
	inStep := policies(other)
	require.Zero(t, policies(target), "the premise: a partition out of step")
	require.Positive(t, inStep)

	reader := retentionSession(t)
	_, err = reader.Exec(testCtx, `BEGIN; LOCK TABLE ONLY public.`+target+` IN ACCESS SHARE MODE`)
	require.NoError(t, err)
	// The operator holds the other partition first, then waits for the target
	// behind the reader.
	renames := make(chan error, 1)
	go func() {
		_, err := operator.Exec(testCtx, `BEGIN;
			ALTER TABLE public.`+other+` RENAME TO `+tmp+`;
			ALTER TABLE public.`+target+` RENAME TO `+renamed+`;
			ALTER TABLE public.`+tmp+` RENAME TO `+target+`;
			COMMIT`)
		renames <- err
	}()
	retentionAwaitWaiters(t, "public."+target, 1)
	follower := retentionSession(t)
	followed := make(chan error, 1)
	go func() {
		_, err := follower.Exec(testCtx, `BEGIN; LOCK TABLE public.`+target+` IN ACCESS SHARE MODE`)
		followed <- err
	}()
	retentionAwaitWaiters(t, "public."+target, 2)

	type outcome struct {
		err     error
		elapsed time.Duration
	}
	provisioned := make(chan outcome, 1)
	go func() {
		started := time.Now()
		err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return testStore.EnsureAuditPartitions(ctx, 3)
		})
		provisioned <- outcome{err, time.Since(started)}
	}()
	// Provisioning is at the target: trying for it, or queued for it.
	require.Eventually(t, func() bool {
		var waiting int
		err := reader.QueryRow(testCtx, `
			SELECT count(*) FROM pg_stat_activity
			 WHERE query LIKE '%audit_events_provision_partitions%' AND pid <> pg_backend_pid()
			   AND (wait_event = 'PgSleep' OR wait_event_type = 'Lock')`).Scan(&waiting)
		return err == nil && waiting > 0
	}, 10*time.Second, 20*time.Millisecond, "provisioning never came to the target")
	_, err = reader.Exec(testCtx, `COMMIT`)
	require.NoError(t, err)
	require.NoError(t, <-renames, "the premise: the renames committed while provisioning tried for the target")
	require.NoError(t, <-followed, "the premise: the follower holds the partition that took the target's name")

	select {
	case got := <-provisioned:
		require.NoError(t, got.err, "provisioning did not wait for the follower")
		require.Less(t, got.elapsed, 11*time.Second)
	case <-time.After(30 * time.Second):
		t.Fatal("provisioning went on waiting past its ten seconds")
	}
	require.Equal(t, inStep, policies(renamed), "the partition it listed is in step, under its new name")
	require.Equal(t, inStep, policies(target), "and the one that took its name is as it was")
	_, err = follower.Exec(testCtx, `ROLLBACK`)
	require.NoError(t, err)
}

// Bringing every partition in step reads the list of partitions holding
// audit_events ACCESS SHARE, so a retention run cannot drop one it has listed
// before it reaches it. The call starts while retention waits for audit_events
// behind a reader, and queues behind retention; retention drops a partition,
// and the call, listing what is left, brings every partition in step. That
// holds for the store's provisioning call and for the one operators run.
func TestBringingEveryPartitionInStepSurvivesARetentionRunThatDropsOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"provisioning", func(ctx context.Context) error { return testStore.EnsureAuditPartitions(ctx, 3) }},
		{"securing every partition", func(ctx context.Context) error {
			_, err := storetx.Tx(ctx).Exec(ctx, `SELECT audit_events_secure_all_partitions()`)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const march = "audit_events_1989_03"
			retentionDropTableAfter(t, march)
			retentionProvisionMonth(t, "1989-03-15", time.Date(1989, 3, 10, 12, 0, 0, 0, time.UTC), uuid.NewString())
			call := func() error { return testStore.WithControlPlane(testCtx, tc.call) }
			require.NoError(t, call(), "the premise: the call succeeds with nothing else running")

			release := retentionHoldReader(t)
			ran := make(chan retentionOutcome, 1)
			go func() {
				dropped, err := retentionCommitted(time.Date(1989, 4, 1, 0, 0, 0, 0, time.UTC))
				ran <- retentionOutcome{dropped, err}
			}()
			retentionAwaitQueuedLocks(t, 1)
			called := make(chan error, 1)
			go func() { called <- call() }()
			require.Eventually(t, func() bool {
				var waiting int
				err := testPool.QueryRow(testCtx, `
					SELECT count(*) FROM pg_locks
					 WHERE relation = 'public.audit_events'::regclass AND mode = 'AccessShareLock' AND NOT granted`).Scan(&waiting)
				return err == nil && waiting > 0
			}, 10*time.Second, 20*time.Millisecond, "the call never queued behind retention")
			release()

			got := <-ran
			require.NoError(t, got.err)
			require.Equal(t, int64(1), got.dropped, "the premise: retention dropped a partition while the call waited")
			require.NoError(t, <-called, "every partition left is brought in step")
		})
	}
}

// Provisioning and retention are DDL on the audit table, so execute on their
// entry points stays with the control plane, and on the functions they share
// with nobody but their owner.
func TestOnlyTheControlPlaneProvisionsOrRetiresAuditPartitions(t *testing.T) {
	for _, tc := range []struct {
		function     string
		controlPlane bool
	}{
		{"audit_events_provision_partitions(integer)", true},
		{"audit_events_ensure_partition(date)", true},
		{"audit_events_secure_all_partitions()", true},
		{"audit_events_drop_partitions_before(timestamptz)", true},
		{"audit_events_create_partitions(date[], timestamptz)", false},
		{"audit_events_secure_partition_within(regclass, timestamptz)", false},
		{"audit_events_secure_partition(regclass)", false},
		{"audit_partition_lock_budget(timestamptz)", false},
		{"audit_partition_lock(regclass, timestamptz)", false},
		{"audit_partitions_due(timestamptz)", false},
	} {
		t.Run(strings.SplitN(tc.function, "(", 2)[0], func(t *testing.T) {
			var publicMay, controlPlaneMay, tenantMay bool
			var config []string
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				return storetx.Tx(ctx).QueryRow(ctx, fmt.Sprintf(`
					SELECT proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(proacl) a WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE'),
					       has_function_privilege('app_control_plane', oid, 'EXECUTE'),
					       has_function_privilege('app_tenant', oid, 'EXECUTE'),
					       COALESCE(proconfig, ARRAY[]::text[])
					  FROM pg_proc WHERE oid = 'public.%s'::regprocedure`, tc.function),
				).Scan(&publicMay, &controlPlaneMay, &tenantMay, &config)
			}))
			require.False(t, publicMay)
			require.Equal(t, tc.controlPlane, controlPlaneMay)
			require.False(t, tenantMay)
			if tc.controlPlane {
				require.Contains(t, config, "lock_timeout=10s", "every entry point bounds its waits")
			}
		})
	}
}
