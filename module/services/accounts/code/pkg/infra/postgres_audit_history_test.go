//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auditstore/bigqueryfake"
	"accounts/pkg/auditstore/bigquerystore"
	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The history copy against the real audit_events (ADR 0009, item 7): its
// partitions and their bounds, its rows read back as the queue reads them, and
// a whole run — copy into the warehouse (the fake Storage Read API), verify by
// reading back, then drop — over a month no install provisions, so the run's
// drop reaches nothing else. A run interrupted before its drop leaves that
// month behind with its rows (they cannot be deleted); the next run's copy
// takes them along and retires the month, so assertions name this run's rows.

const historyMonth = "2002-03-01"

func historyPartitionExists(t *testing.T) bool {
	t.Helper()
	var exists bool
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return txFromCtx(t, ctx).QueryRow(ctx, `SELECT to_regclass('public.audit_events_2002_03') IS NOT NULL`).Scan(&exists)
	}))
	return exists
}

// seedHistoryMonth provisions the month and writes events into it through the
// emitter, as a postgres deployment wrote them.
func seedHistoryMonth(t *testing.T) []business.AuditEntry {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := txFromCtx(t, ctx).Exec(ctx, `SELECT audit_events_ensure_partition($1::date)`, historyMonth)
		return err
	}))
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	orgs := []string{uuid.NewString(), uuid.NewString(), ""}
	base := time.Date(2002, 3, 30, 22, 0, 0, 0, time.UTC)
	var entries []business.AuditEntry
	for i := 0; i < 9; i++ {
		org := orgs[i%3]
		eventType := business.EventDatasourceSourceSynced
		if i%2 == 0 {
			eventType = business.EventAuthLogin
		}
		entry := business.AuditEntry{
			ID: uuid.NewString(), OrgID: org, ActorID: uuid.NewString(), ActorType: business.ActorTypeUser,
			EventType: eventType, SchemaVersion: 1, Resource: "datasource", ResourceID: fmt.Sprintf("source-%d", i),
			// Two events share an instant, so the read pages on (created_at, id).
			CreatedAt: base.Add(time.Duration(i/2) * 90 * time.Minute),
			Payload:   map[string]any{"n": i, "ratio": 1.50, "label": "<b>"},
		}
		write := func(ctx context.Context) error { return emitter.EmitTx(ctx, entry) }
		if org == "" {
			require.NoError(t, testStore.WithControlPlane(testCtx, write))
		} else {
			require.NoError(t, testStore.WithOrgTx(testCtx, org, write))
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestAuditHistorySourceReadsPartitionsAndRowsAsTheQueueDoes(t *testing.T) {
	entries := seedHistoryMonth(t)
	from := time.Date(2002, 3, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		partitions, err := testStore.ListAuditPartitions(ctx)
		require.NoError(t, err)
		found := false
		for i, partition := range partitions {
			if i > 0 {
				require.False(t, partition.From.Before(partitions[i-1].From), "oldest first")
			}
			if partition.Name == "audit_events_2002_03" {
				found = true
				require.Equal(t, from, partition.From)
				require.Equal(t, to, partition.To)
			}
		}
		require.True(t, found)

		// Paged two at a time on (created_at, id), every row once, in order.
		var read []business.AuditEntry
		var after *business.AuditHistoryCursor
		for {
			page, err := testStore.ReadAuditHistory(ctx, from, to, after, 2)
			require.NoError(t, err)
			read = append(read, page...)
			if len(page) < 2 {
				break
			}
			last := page[len(page)-1]
			after = &business.AuditHistoryCursor{CreatedAt: last.CreatedAt, ID: last.ID}
		}
		byID := map[string]business.AuditEntry{}
		for i, entry := range read {
			if i > 0 {
				previous := read[i-1]
				require.True(t, previous.CreatedAt.Before(entry.CreatedAt) || previous.CreatedAt.Equal(entry.CreatedAt) && previous.ID < entry.ID)
			}
			byID[entry.ID] = entry
		}
		for _, want := range entries {
			got, ok := byID[want.ID]
			require.True(t, ok, "every row is read once")
			require.Equal(t, want.OrgID, got.OrgID)
			require.Equal(t, want.CreatedAt, got.CreatedAt)
			record, err := business.NewAuditRecord(got, business.RetentionContent)
			require.NoError(t, err)
			require.Equal(t, `{"label":"<b>","n":`+fmt.Sprint(want.Payload["n"])+`,"ratio":1.5}`, record.Details,
				"the canonical details of the row, as the queue's are")
		}

		counts, err := testStore.CountAuditHistory(ctx, from, to)
		require.NoError(t, err)
		require.Equal(t, int64(3), counts[entries[0].OrgID])
		require.Equal(t, int64(3), counts[entries[1].OrgID])
		require.GreaterOrEqual(t, counts[""], int64(3), "platform events count under no organization")
		return nil
	}))
	t.Run("the copy", func(t *testing.T) { runHistoryCopy(t, entries) })
}

// historyWarehouse appends to the fake as the BigQuery writer streams.
type historyWarehouse struct{ fake *bigqueryfake.Server }

func (w historyWarehouse) AppendAuditBatch(_ context.Context, batch business.AuditBatch) error {
	events, details := bigquerystore.BatchRows(batch)
	for _, row := range events {
		if err := w.fake.Insert(bigqueryfake.TablePath("history-project", "audit", bigquerystore.EventsTable), row.Values); err != nil {
			return err
		}
	}
	for _, row := range details {
		if err := w.fake.Insert(bigqueryfake.TablePath("history-project", "audit", bigquerystore.DetailsTable), row.Values); err != nil {
			return err
		}
	}
	return nil
}

type historyArchive struct{ records int }

func (a *historyArchive) WriteAuditBatch(_ context.Context, batch business.AuditBatch) error {
	a.records += len(batch.Records)
	return nil
}

func runHistoryCopy(t *testing.T, entries []business.AuditEntry) {
	fake := bigqueryfake.New()
	fake.CreateTable(bigqueryfake.TablePath("history-project", "audit", bigquerystore.EventsTable), bigquerystore.EventsSchema())
	fake.CreateTable(bigqueryfake.TablePath("history-project", "audit", bigquerystore.DetailsTable), bigquerystore.DetailsSchema())
	reader, err := bigquerystore.NewReader(bigquerystore.ReadConfig{Client: fake, Project: "history-project", Dataset: "audit", DeploymentID: "deployment-history"})
	require.NoError(t, err)
	archive := &historyArchive{}
	through := time.Date(2002, 4, 1, 0, 0, 0, 0, time.UTC)
	copier, err := business.NewAuditHistoryCopy(business.AuditHistoryCopyConfig{
		Source: testStore, Store: historyWarehouse{fake: fake}, Archive: archive, ReadBack: reader, Types: testStore,
		DeploymentID: "deployment-history", ContentRetention: 365 * 24 * time.Hour, BatchSize: 2, Through: &through,
	})
	require.NoError(t, err)

	report, err := copier.Run(testCtx, business.AuditHistoryCopyOptions{})
	require.NoError(t, err)
	require.True(t, report.Verified)
	require.True(t, historyPartitionExists(t), "nothing is dropped without the confirmation")
	require.GreaterOrEqual(t, archive.records, len(entries))

	// The warehouse answers what audit_events did, through the same reads.
	for _, org := range []string{entries[0].OrgID, entries[1].OrgID} {
		got, _, err := reader.ListAuditEvents(testCtx, business.AuditRead{
			Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{OrgID: org, PageSize: 100},
		})
		require.NoError(t, err)
		require.Len(t, got, 3)
		var want []business.AuditEntry
		require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
			var err error
			want, _, _, err = testStore.QueryAuditLog(ctx, business.AuditQuery{OrgID: org, PageSize: 100})
			return err
		}))
		require.Equal(t, want, got)
	}

	report, err = copier.Run(testCtx, business.AuditHistoryCopyOptions{ConfirmDrop: true, VerifyOnly: true, ExpectedPartitionsSHA256: report.PartitionsSHA256})
	require.NoError(t, err)
	require.Positive(t, report.Dropped)
	require.Equal(t, through, report.Cutoff)
	require.False(t, historyPartitionExists(t), "the copied month is dropped, the way retention drops one")
	var copied int
	for _, partition := range report.Partitions {
		copied += partition.Copied
	}
	require.Zero(t, copied, "the confirming run found everything copied")
}

// historyRig is the copy's other two sides: the fake Storage Read API the
// store of record reads back through, and an archive that counts.
type historyRig struct {
	fake    *bigqueryfake.Server
	reader  *bigquerystore.Reader
	archive *historyArchive
}

func newHistoryRig(t *testing.T) *historyRig {
	t.Helper()
	fake := bigqueryfake.New()
	fake.CreateTable(bigqueryfake.TablePath("history-project", "audit", bigquerystore.EventsTable), bigquerystore.EventsSchema())
	fake.CreateTable(bigqueryfake.TablePath("history-project", "audit", bigquerystore.DetailsTable), bigquerystore.DetailsSchema())
	reader, err := bigquerystore.NewReader(bigquerystore.ReadConfig{Client: fake, Project: "history-project", Dataset: "audit", DeploymentID: "deployment-history"})
	require.NoError(t, err)
	return &historyRig{fake: fake, reader: reader, archive: &historyArchive{}}
}

func (r *historyRig) copier(t *testing.T, source business.AuditHistorySource, through time.Time) *business.AuditHistoryCopy {
	t.Helper()
	copier, err := business.NewAuditHistoryCopy(business.AuditHistoryCopyConfig{
		Source: source, Store: historyWarehouse{fake: r.fake}, Archive: r.archive, ReadBack: r.reader, Types: testStore,
		DeploymentID: "deployment-history", ContentRetention: 365 * 24 * time.Hour, BatchSize: 2, Through: &through,
	})
	require.NoError(t, err)
	return copier
}

// ensurePartitionUnderZone creates the month's partition from a session in the
// given time zone: PostgreSQL renders the month's first instant in the session's
// zone, so a zone other than UTC gives a partition whose bounds are not UTC
// midnights.
func ensurePartitionUnderZone(t *testing.T, zone, day string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := txFromCtx(t, ctx)
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL TimeZone = '%s'`, zone)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT audit_events_ensure_partition($1::date)`, day)
		return err
	}))
}

// insertHistoryRow commits a platform event at an explicit instant, the way a
// writer backdating a row would.
func insertHistoryRow(t *testing.T, at time.Time) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := txFromCtx(t, ctx).Exec(ctx,
			`INSERT INTO audit_events (id, event_type, resource, created_at) VALUES ($1, $2, 'example', $3)`,
			uuid.NewString(), string(business.EventAuthLogin), at)
		return err
	}))
}

func auditPartitionExists(t *testing.T, name string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return txFromCtx(t, ctx).QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, name).Scan(&exists)
	}))
	return exists
}

// auditPartitionRows counts the rows a partition holds, through the parent.
func auditPartitionRows(t *testing.T, name string) int64 {
	t.Helper()
	var rows int64
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return txFromCtx(t, ctx).QueryRow(ctx,
			`SELECT count(*) FROM audit_events WHERE tableoid = to_regclass('public.' || $1)`, name).Scan(&rows)
	}))
	return rows
}

// retireTestMonths drops what a test left behind, by the retention function, so
// no month outlives the test that provisioned it.
func retireTestMonths(t *testing.T, before string) {
	t.Helper()
	t.Cleanup(func() {
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			_, err := txFromCtx(t, ctx).Exec(ctx, `SELECT audit_events_drop_partitions_before($1::timestamptz)`, before)
			return err
		}))
	})
}

func droppedIn(report business.AuditHistoryReport, prefix string) []string {
	var out []string
	for _, name := range report.DroppedNames {
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out
}

// Partitions created from a session whose zone is not UTC have bounds on that
// zone's midnights, so the month named audit_events_2003_08 ends at 04:00 UTC on
// the first of September. The copy verifies the partitions whose bounds end by
// the cutoff, and must drop exactly those: dropping by name would also take
// August, whose first four September hours are events no one verified.
func TestAuditHistoryDropsExactlyTheVerifiedPartitionsWhateverZoneCreatedThem(t *testing.T) {
	retireTestMonths(t, "2004-01-01")
	ensurePartitionUnderZone(t, "America/New_York", "2003-07-15")
	ensurePartitionUnderZone(t, "America/New_York", "2003-08-15")
	insertHistoryRow(t, time.Date(2003, 7, 10, 12, 0, 0, 0, time.UTC))
	insertHistoryRow(t, time.Date(2003, 7, 20, 12, 0, 0, 0, time.UTC))
	// September's first hours, in the partition named for August.
	insertHistoryRow(t, time.Date(2003, 9, 1, 2, 0, 0, 0, time.UTC))
	through := time.Date(2003, 9, 1, 0, 0, 0, 0, time.UTC)

	rig := newHistoryRig(t)
	copier := rig.copier(t, testStore, through)
	report, err := copier.Run(testCtx, business.AuditHistoryCopyOptions{})
	require.NoError(t, err)
	require.True(t, report.Verified)
	require.Empty(t, report.Uncovered, "the August partition holds nothing older than the cutoff")
	report, err = copier.Run(testCtx, business.AuditHistoryCopyOptions{ConfirmDrop: true, VerifyOnly: true, ExpectedPartitionsSHA256: report.PartitionsSHA256})
	require.NoError(t, err)

	require.Equal(t, []string{"audit_events_2003_07"}, droppedIn(report, "audit_events_2003_"))
	require.Equal(t, int64(len(report.DroppedNames)), report.Dropped)
	require.False(t, auditPartitionExists(t, "audit_events_2003_07"))
	require.True(t, auditPartitionExists(t, "audit_events_2003_08"), "a partition the run did not verify is not dropped")
	require.Equal(t, int64(1), auditPartitionRows(t, "audit_events_2003_08"))
}

// A partition that straddles the cutoff holds events before it that no verified
// partition covers. The copy cannot have verified them, so it does not pass,
// and nothing is dropped.
func TestAuditHistoryDoesNotPassAPartitionThatStraddlesTheCutoff(t *testing.T) {
	retireTestMonths(t, "2006-01-01")
	ensurePartitionUnderZone(t, "America/New_York", "2005-10-15")
	ensurePartitionUnderZone(t, "America/New_York", "2005-11-15")
	insertHistoryRow(t, time.Date(2005, 10, 10, 12, 0, 0, 0, time.UTC))
	insertHistoryRow(t, time.Date(2005, 11, 20, 12, 0, 0, 0, time.UTC))
	through := time.Date(2005, 12, 1, 0, 0, 0, 0, time.UTC)

	rig := newHistoryRig(t)
	copier := rig.copier(t, testStore, through)
	report, err := copier.Run(testCtx, business.AuditHistoryCopyOptions{})
	require.ErrorIs(t, err, business.ErrAuditHistoryUnverified)
	require.False(t, report.Verified)
	require.Len(t, report.Uncovered, 1)
	require.Contains(t, report.Uncovered[0].Table, "audit_events_2005_11")
	require.Equal(t, int64(1), report.Uncovered[0].Rows)

	// Even with the digest the run printed, the confirmation drops nothing.
	report, err = copier.Run(testCtx, business.AuditHistoryCopyOptions{ConfirmDrop: true, VerifyOnly: true, ExpectedPartitionsSHA256: report.PartitionsSHA256})
	require.ErrorIs(t, err, business.ErrAuditHistoryUnverified)
	require.Empty(t, report.DroppedNames)
	require.True(t, auditPartitionExists(t, "audit_events_2005_10"))
	require.True(t, auditPartitionExists(t, "audit_events_2005_11"), "its November events were never verified")
	require.Equal(t, int64(1), auditPartitionRows(t, "audit_events_2005_11"))
}

// backdatingSource commits a backdated row right after the recount the drop
// makes of a verified partition, the window a count-then-drop leaves open.
type backdatingSource struct {
	*infra.PostgresStore
	afterCount func()
}

func (s *backdatingSource) CountAuditHistory(ctx context.Context, from, to time.Time) (map[string]int64, error) {
	counts, err := s.PostgresStore.CountAuditHistory(ctx, from, to)
	if hook := s.afterCount; hook != nil {
		s.afterCount = nil
		hook()
	}
	return counts, err
}

func TestAuditHistoryDropDoesNotDestroyARowCommittedAfterTheRecount(t *testing.T) {
	retireTestMonths(t, "2004-06-01")
	ensurePartitionUnderZone(t, "UTC", "2004-03-15")
	for day := 3; day <= 5; day++ {
		insertHistoryRow(t, time.Date(2004, 3, day, 12, 0, 0, 0, time.UTC))
	}
	through := time.Date(2004, 4, 1, 0, 0, 0, 0, time.UTC)

	rig := newHistoryRig(t)
	report, err := rig.copier(t, testStore, through).Run(testCtx, business.AuditHistoryCopyOptions{})
	require.NoError(t, err)

	racing := &backdatingSource{PostgresStore: testStore, afterCount: func() {
		insertHistoryRow(t, time.Date(2004, 3, 6, 12, 0, 0, 0, time.UTC))
	}}
	refused, err := rig.copier(t, racing, through).Run(testCtx, business.AuditHistoryCopyOptions{
		ConfirmDrop: true, VerifyOnly: true, ExpectedPartitionsSHA256: report.PartitionsSHA256,
	})

	require.ErrorIs(t, err, business.ErrAuditHistoryDropRefused, "the drop was refused, not reported as done")
	require.Contains(t, refused.DropRefused, "audit_events_2004_03 holds 4 rows, 3 were verified")
	require.Empty(t, refused.DroppedNames)
	require.True(t, auditPartitionExists(t, "audit_events_2004_03"), "the backdated row's partition was dropped with it")
	require.Equal(t, int64(4), auditPartitionRows(t, "audit_events_2004_03"), "a row committed after the recount is not destroyed")
}

// dropVerified calls the database function as the history copy does, in its own
// control-plane transaction.
func dropVerified(cutoff time.Time, targets ...business.AuditHistoryDropTarget) ([]string, error) {
	var dropped []string
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		dropped, err = testStore.DropVerifiedAuditPartitions(ctx, cutoff, targets)
		return err
	})
	return dropped, err
}

// A writer whose insert is in flight when the drop starts is waited for: the
// drop locks the partition before it counts, so the count includes the row the
// writer then commits, disagrees with the number verified, and drops nothing.
// Counting first and locking after would see the old count, wait out the
// writer at the DROP, and take the writer's row with the table.
func TestVerifiedPartitionDropWaitsForAnInFlightInsertAndThenRefusesToDestroyIt(t *testing.T) {
	retireTestMonths(t, "2008-01-01")
	ensurePartitionUnderZone(t, "UTC", "2007-05-15")
	insertHistoryRow(t, time.Date(2007, 5, 3, 12, 0, 0, 0, time.UTC))
	insertHistoryRow(t, time.Date(2007, 5, 4, 12, 0, 0, 0, time.UTC))
	cutoff := time.Date(2007, 6, 1, 0, 0, 0, 0, time.UTC)

	inserted, release := make(chan struct{}), make(chan struct{})
	writer := make(chan error, 1)
	go func() {
		writer <- testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			if _, err := txFromCtx(t, ctx).Exec(ctx,
				`INSERT INTO audit_events (id, event_type, resource, created_at) VALUES ($1, $2, 'example', $3)`,
				uuid.NewString(), string(business.EventAuthLogin), time.Date(2007, 5, 6, 12, 0, 0, 0, time.UTC)); err != nil {
				return err
			}
			close(inserted)
			<-release
			return nil
		})
	}()
	select {
	case <-inserted:
	case err := <-writer:
		t.Fatalf("the in-flight insert failed: %v", err)
	}

	type outcome struct {
		dropped []string
		err     error
	}
	dropper := make(chan outcome, 1)
	go func() {
		dropped, err := dropVerified(cutoff, business.AuditHistoryDropTarget{Name: "audit_events_2007_05", Rows: 2})
		dropper <- outcome{dropped, err}
	}()
	select {
	case got := <-dropper:
		t.Fatalf("the drop did not wait for the insert in flight: %v, %v", got.dropped, got.err)
	case <-time.After(750 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-writer)

	got := <-dropper
	var refusal *business.AuditHistoryDropRefusal
	require.ErrorAs(t, got.err, &refusal)
	require.Contains(t, refusal.Reason, "audit_events_2007_05 holds 3 rows, 2 were verified")
	require.Empty(t, got.dropped)
	require.Equal(t, int64(3), auditPartitionRows(t, "audit_events_2007_05"), "the writer's row survives")
}

// Once the drop holds the partition nothing routes into it: a writer that
// arrives meanwhile waits, and when the drop commits it is told there is no
// partition for its row instead of having the row accepted and destroyed.
func TestVerifiedPartitionDropHoldsOffLaterInsertsUntilItCommits(t *testing.T) {
	retireTestMonths(t, "2010-01-01")
	ensurePartitionUnderZone(t, "UTC", "2009-05-15")
	insertHistoryRow(t, time.Date(2009, 5, 3, 12, 0, 0, 0, time.UTC))
	cutoff := time.Date(2009, 6, 1, 0, 0, 0, 0, time.UTC)

	dropped, release := make(chan struct{}), make(chan struct{})
	dropper := make(chan error, 1)
	go func() {
		dropper <- testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			names, err := testStore.DropVerifiedAuditPartitions(ctx, cutoff, []business.AuditHistoryDropTarget{{Name: "audit_events_2009_05", Rows: 1}})
			if err != nil {
				return err
			}
			if len(names) != 1 || names[0] != "audit_events_2009_05" {
				return fmt.Errorf("dropped %v", names)
			}
			close(dropped)
			<-release
			return nil
		})
	}()
	select {
	case <-dropped:
	case err := <-dropper:
		t.Fatalf("the drop failed: %v", err)
	}

	writer := make(chan error, 1)
	go func() {
		writer <- testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			_, err := txFromCtx(t, ctx).Exec(ctx,
				`INSERT INTO audit_events (id, event_type, resource, created_at) VALUES ($1, $2, 'example', $3)`,
				uuid.NewString(), string(business.EventAuthLogin), time.Date(2009, 5, 6, 12, 0, 0, 0, time.UTC))
			return err
		})
	}()
	select {
	case err := <-writer:
		t.Fatalf("a writer got past the drop's lock before it committed: %v", err)
	case <-time.After(750 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-dropper)
	require.ErrorContains(t, <-writer, "no partition")
	require.False(t, auditPartitionExists(t, "audit_events_2009_05"))
}

// The function names what it will drop and checks each name against the
// catalog; it never works out a set for itself, and a refusal changes nothing.
func TestVerifiedPartitionDropRefusesWhatItWasNotAskedToVerify(t *testing.T) {
	retireTestMonths(t, "2012-01-01")
	ensurePartitionUnderZone(t, "America/New_York", "2011-07-15")
	ensurePartitionUnderZone(t, "America/New_York", "2011-08-15")
	insertHistoryRow(t, time.Date(2011, 7, 10, 12, 0, 0, 0, time.UTC))
	cutoff := time.Date(2011, 9, 1, 0, 0, 0, 0, time.UTC)

	for name, tc := range map[string]struct {
		targets []business.AuditHistoryDropTarget
		want    string
	}{
		// Named explicitly, the August partition ends at 04:00 UTC on 1 September:
		// the cutoff, which the name alone would have admitted, does not.
		"a partition that ends after the cutoff": {
			targets: []business.AuditHistoryDropTarget{{Name: "audit_events_2011_07", Rows: 1}, {Name: "audit_events_2011_08", Rows: 0}},
			want:    "audit_events_2011_08 ends at 2011-09-01 04:00:00+00, after the cutoff",
		},
		"a partition that is not there":    {targets: []business.AuditHistoryDropTarget{{Name: "audit_events_2011_01", Rows: 0}}, want: "does not exist"},
		"a name that is not a month":       {targets: []business.AuditHistoryDropTarget{{Name: "audit_events", Rows: 0}}, want: "not a monthly audit_events partition name"},
		"a count that is not the verified": {targets: []business.AuditHistoryDropTarget{{Name: "audit_events_2011_07", Rows: 2}}, want: "holds 1 rows, 2 were verified"},
		"nothing at all":                   {targets: nil, want: "required"},
		"a name twice": {
			targets: []business.AuditHistoryDropTarget{{Name: "audit_events_2011_07", Rows: 1}, {Name: "audit_events_2011_07", Rows: 1}},
			want:    "distinct",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dropped, err := dropVerified(cutoff, tc.targets...)
			var refusal *business.AuditHistoryDropRefusal
			require.ErrorAs(t, err, &refusal)
			require.Contains(t, refusal.Reason, tc.want)
			require.Empty(t, dropped)
			require.True(t, auditPartitionExists(t, "audit_events_2011_07"))
			require.True(t, auditPartitionExists(t, "audit_events_2011_08"))
			require.Equal(t, int64(1), auditPartitionRows(t, "audit_events_2011_07"))
			// The recount lifts FORCE on a partition for the length of its own
			// transaction; a refusal rolls that back with everything else.
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				requirePartitionsSecured(t, ctx, txFromCtx(t, ctx))
				return nil
			}))
		})
	}

	dropped, err := dropVerified(cutoff, business.AuditHistoryDropTarget{Name: "audit_events_2011_07", Rows: 1})
	require.NoError(t, err)
	require.Equal(t, []string{"audit_events_2011_07"}, dropped)
	require.False(t, auditPartitionExists(t, "audit_events_2011_07"))
	require.True(t, auditPartitionExists(t, "audit_events_2011_08"))
}
