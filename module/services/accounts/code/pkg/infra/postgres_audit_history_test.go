//go:build !pure

package infra_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"accounts/pkg/auditstore/bigqueryfake"
	"accounts/pkg/auditstore/bigquerystore"
	"accounts/pkg/business"

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

	report, err = copier.Run(testCtx, business.AuditHistoryCopyOptions{ConfirmDrop: true})
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
