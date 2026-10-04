package business

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The history copy against in-memory stand-ins for audit_events, the store of
// record and the archive: what it copies, what it verifies, when it drops, and
// how a run after an interrupted one finishes the job.

type historySource struct {
	partitions []AuditHistoryPartition
	rows       []AuditEntry
	drops      []time.Time
	// beforeCount runs before every count, so a test can change the table
	// between verification and the drop.
	beforeCount func(*historySource)
}

func (s *historySource) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *historySource) ListAuditPartitions(context.Context) ([]AuditHistoryPartition, error) {
	return append([]AuditHistoryPartition(nil), s.partitions...), nil
}

func (s *historySource) ReadAuditHistory(_ context.Context, from, to time.Time, after *AuditHistoryCursor, limit int) ([]AuditEntry, error) {
	sorted := append([]AuditEntry(nil), s.rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})
	var out []AuditEntry
	for _, row := range sorted {
		if row.CreatedAt.Before(from) || !row.CreatedAt.Before(to) {
			continue
		}
		if after != nil && (row.CreatedAt.Before(after.CreatedAt) || row.CreatedAt.Equal(after.CreatedAt) && row.ID <= after.ID) {
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *historySource) CountAuditHistory(_ context.Context, from, to time.Time) (map[string]int64, error) {
	if s.beforeCount != nil {
		s.beforeCount(s)
	}
	counts := map[string]int64{}
	for _, row := range s.rows {
		if !row.CreatedAt.Before(from) && row.CreatedAt.Before(to) {
			counts[row.OrgID]++
		}
	}
	return counts, nil
}

func (s *historySource) DropAuditPartitionsBefore(_ context.Context, before time.Time) (int64, error) {
	s.drops = append(s.drops, before)
	var kept []AuditHistoryPartition
	var dropped int64
	for _, partition := range s.partitions {
		if partition.To.After(before) {
			kept = append(kept, partition)
		} else {
			dropped++
		}
	}
	s.partitions = kept
	return dropped, nil
}

// historyStore is a store of record that keeps what it is given, as the
// BigQuery tables do: a content-class event's details beside its events row.
type historyStore struct {
	stored     []StoredAuditEvent
	appends    int
	failAppend int // the 1-based append that fails; 0 never
	tamper     func(*StoredAuditEvent)
}

func (s *historyStore) AppendAuditBatch(_ context.Context, batch AuditBatch) error {
	s.appends++
	if s.appends == s.failAppend {
		return errors.New("warehouse unavailable")
	}
	for _, record := range batch.Records {
		entry := record.Entry
		entry.Payload = nil
		event := StoredAuditEvent{
			DeploymentID: batch.DeploymentID, Entry: entry, Retention: record.Retention,
			DetailsSHA256: record.DetailsSHA256, Details: record.Details, HasDetails: true,
		}
		if s.tamper != nil {
			s.tamper(&event)
		}
		s.stored = append(s.stored, event)
	}
	return nil
}

func (s *historyStore) ReadStoredAuditEvents(_ context.Context, from, to time.Time, visit func(StoredAuditEvent) error) error {
	for _, event := range s.stored {
		if !event.Entry.CreatedAt.Before(from) && event.Entry.CreatedAt.Before(to) {
			if err := visit(event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *historyStore) copies() map[string]int {
	out := map[string]int{}
	for _, event := range s.stored {
		out[event.Entry.ID]++
	}
	return out
}

type historyArchive struct{ batches []AuditBatch }

func (a *historyArchive) WriteAuditBatch(_ context.Context, batch AuditBatch) error {
	a.batches = append(a.batches, batch)
	return nil
}

var historyNow = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func month(year int, m time.Month) AuditHistoryPartition {
	from := time.Date(year, m, 1, 0, 0, 0, 0, time.UTC)
	return AuditHistoryPartition{Name: fmt.Sprintf("audit_events_%04d_%02d", year, m), From: from, To: from.AddDate(0, 1, 0)}
}

func historyRow(n int, org string, eventType EventType, at time.Time) AuditEntry {
	return AuditEntry{
		ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", n), OrgID: org, ActorID: "11111111-1111-4111-8111-111111111111",
		ActorType: ActorTypeUser, EventType: eventType, SchemaVersion: 1, Resource: "datasource",
		ResourceID: fmt.Sprintf("source-%d", n), CreatedAt: at,
		Payload: map[string]any{"n": n, "repo": "acme/handbook"},
	}
}

type historyFixture struct {
	source  *historySource
	store   *historyStore
	archive *historyArchive
}

func newHistoryFixture() *historyFixture {
	orgA, orgB := "aaaaaaaa-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"
	source := &historySource{partitions: []AuditHistoryPartition{month(2026, time.August), month(2026, time.September), month(2026, time.October)}}
	n := 0
	for day := time.Date(2026, 8, 30, 6, 0, 0, 0, time.UTC); day.Before(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)); day = day.Add(10 * time.Hour) {
		for _, org := range []string{orgA, orgB, ""} {
			n++
			eventType := EventDatasourceSourceSynced // content
			if n%3 == 0 {
				eventType = EventAuthLogin // security
			}
			source.rows = append(source.rows, historyRow(n, org, eventType, day))
		}
	}
	return &historyFixture{source: source, store: &historyStore{}, archive: &historyArchive{}}
}

func (f *historyFixture) copier(t *testing.T, cfg AuditHistoryCopyConfig) *AuditHistoryCopy {
	t.Helper()
	cfg.Source, cfg.Store, cfg.Archive, cfg.ReadBack = f.source, f.store, f.archive, f.store
	cfg.DeploymentID = "deployment-1"
	if cfg.ContentRetention == 0 {
		cfg.ContentRetention = 400 * 24 * time.Hour
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 4
	}
	cfg.Now = func() time.Time { return historyNow }
	copier, err := NewAuditHistoryCopy(cfg)
	require.NoError(t, err)
	return copier
}

func TestAuditHistoryCopyCopiesVerifiesAndDropsOnlyWhenConfirmed(t *testing.T) {
	f := newHistoryFixture()
	report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	require.True(t, report.Verified)
	require.Empty(t, f.source.drops, "nothing is dropped without the confirmation")
	require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), report.Cutoff)

	copies := f.store.copies()
	require.Len(t, copies, len(f.source.rows))
	for _, row := range f.source.rows {
		require.Equal(t, 1, copies[row.ID], "copied once: %s", row.ID)
	}
	var orgs = map[string]int64{}
	for _, partition := range report.Partitions {
		for org, count := range partition.Orgs {
			require.Equal(t, count.Postgres, count.Verified)
			orgs[org] += count.Postgres
		}
	}
	require.Equal(t, int64(len(f.source.rows)/3), orgs[""], "platform events are copied too")

	// Each copy carries the envelope, class and hash a live event of the row gets.
	resolver := NewAuditEventResolver(nil)
	for _, batch := range f.archive.batches {
		require.Equal(t, "deployment-1", batch.DeploymentID)
		require.LessOrEqual(t, len(batch.Records), 4)
		for _, record := range batch.Records {
			resolved, err := resolver.Resolve(context.Background(), record.Entry.EventType)
			require.NoError(t, err)
			want, err := NewAuditRecord(record.Entry, resolved.RetentionClass())
			require.NoError(t, err)
			require.Equal(t, want.DetailsSHA256, record.DetailsSHA256)
			require.Equal(t, want.Retention, record.Retention)
		}
	}

	// A second run finds everything there: it copies nothing, verifies, and with
	// the confirmation drops every partition up to the cutoff.
	appends := f.store.appends
	report, err = f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true})
	require.NoError(t, err)
	require.Equal(t, appends, f.store.appends, "a resumed run copies nothing already there")
	require.Equal(t, []time.Time{report.Cutoff}, f.source.drops)
	require.Equal(t, int64(3), report.Dropped)
	require.Empty(t, f.source.partitions)
}

func TestAuditHistoryCopyResumesAfterAnInterruption(t *testing.T) {
	f := newHistoryFixture()
	f.store.failAppend = 3
	_, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true})
	require.ErrorContains(t, err, "warehouse unavailable")
	require.Empty(t, f.source.drops, "an interrupted run drops nothing")
	partial := len(f.store.copies())
	require.Positive(t, partial)
	require.Less(t, partial, len(f.source.rows))

	f.store.failAppend = 0
	report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true})
	require.NoError(t, err)
	require.True(t, report.Verified)
	copied := 0
	for _, partition := range report.Partitions {
		copied += partition.Copied
	}
	require.Equal(t, len(f.source.rows)-partial, copied, "the second run copies only what the first did not")
	for id, n := range f.store.copies() {
		require.Equal(t, 1, n, "event %s is in the store once", id)
	}
	archived := map[string]int{}
	for _, batch := range f.archive.batches {
		for _, record := range batch.Records {
			archived[record.Entry.ID]++
		}
	}
	require.Len(t, archived, len(f.source.rows))
	twice := 0
	for _, n := range archived {
		if n == 2 {
			twice++
		}
	}
	require.Equal(t, 4, twice, "the batch archived before its append failed is archived again, as a relay retry would")
	require.Equal(t, int64(3), report.Dropped)
}

func TestAuditHistoryVerificationFailureBlocksTheDrop(t *testing.T) {
	for name, tc := range map[string]struct {
		prepare func(*historyFixture)
		opts    AuditHistoryCopyOptions
		want    string
	}{
		"an event the store does not hold": {
			opts: AuditHistoryCopyOptions{VerifyOnly: true, ConfirmDrop: true},
			want: "missing from the store",
		},
		"a hash that differs": {
			prepare: func(f *historyFixture) {
				f.store.tamper = func(e *StoredAuditEvent) {
					if e.Entry.ID == f.source.rows[4].ID {
						e.DetailsSHA256 = AuditDetailsSHA256("{}")
					}
				}
			},
			opts: AuditHistoryCopyOptions{ConfirmDrop: true},
			want: "details hash differs",
		},
		"details that do not hash to it": {
			prepare: func(f *historyFixture) {
				f.store.tamper = func(e *StoredAuditEvent) {
					if e.Entry.ID == f.source.rows[2].ID {
						e.Details = `{"n":0}`
					}
				}
			},
			opts: AuditHistoryCopyOptions{ConfirmDrop: true},
			want: "do not hash",
		},
		"an envelope that differs": {
			prepare: func(f *historyFixture) {
				f.store.tamper = func(e *StoredAuditEvent) {
					if e.Entry.ID == f.source.rows[1].ID {
						e.Entry.OrgID = "cccccccc-0000-4000-8000-000000000003"
					}
				}
			},
			opts: AuditHistoryCopyOptions{ConfirmDrop: true},
			want: "envelope differs",
		},
		"content details missing inside the window": {
			prepare: func(f *historyFixture) {
				f.store.tamper = func(e *StoredAuditEvent) {
					if e.Retention == RetentionContent {
						e.Details, e.HasDetails = "", false
					}
				}
			},
			opts: AuditHistoryCopyOptions{ConfirmDrop: true},
			want: "content details are missing",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHistoryFixture()
			if tc.prepare != nil {
				tc.prepare(f)
			}
			report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), tc.opts)
			require.ErrorIs(t, err, ErrAuditHistoryUnverified)
			require.False(t, report.Verified)
			require.Empty(t, f.source.drops)
			var problems []string
			for _, partition := range report.Partitions {
				problems = append(problems, partition.Problems...)
			}
			require.NotEmpty(t, problems)
			require.Contains(t, problems[0], tc.want)
		})
	}
}

func TestAuditHistoryContentDetailsPastTheWindowAreNotRequired(t *testing.T) {
	f := newHistoryFixture()
	f.store.tamper = func(e *StoredAuditEvent) {
		if e.Retention == RetentionContent {
			e.Details, e.HasDetails = "", false // expired with their day partition
		}
	}
	report, err := f.copier(t, AuditHistoryCopyConfig{ContentRetention: 7 * 24 * time.Hour}).Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true})
	require.NoError(t, err)
	require.True(t, report.Verified)
	require.Equal(t, int64(3), report.Dropped)
}

func TestAuditHistoryDropIsRefusedWhenAPartitionChangedSinceVerification(t *testing.T) {
	f := newHistoryFixture()
	f.source.beforeCount = func(s *historySource) {
		s.beforeCount = nil
		s.rows = append(s.rows, historyRow(999, "", EventAuthLogin, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)))
	}
	report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true})
	require.ErrorContains(t, err, "changed since it was verified")
	require.True(t, report.Verified)
	require.Contains(t, report.DropRefused, "audit_events_2026_09")
	require.Empty(t, f.source.drops)
}

func TestAuditHistoryThroughLimitsThePartitionsAndTheCutoff(t *testing.T) {
	f := newHistoryFixture()
	through := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	report, err := f.copier(t, AuditHistoryCopyConfig{Through: &through}).Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true})
	require.NoError(t, err)
	require.Len(t, report.Partitions, 1)
	require.Equal(t, "audit_events_2026_08", report.Partitions[0].Partition.Name)
	require.Equal(t, []time.Time{through}, f.source.drops)
	for _, event := range f.store.stored {
		require.True(t, event.Entry.CreatedAt.Before(through), "only August is copied")
	}

	// A partition the run did not verify that the cutoff would drop refuses it.
	f = newHistoryFixture()
	f.source.partitions = append([]AuditHistoryPartition{{Name: "audit_events_2026_07", From: month(2026, time.July).From, To: month(2026, time.July).To}}, f.source.partitions...)
	copier := f.copier(t, AuditHistoryCopyConfig{Through: &through})
	copier.cfg.Source = &listingAfterVerification{historySource: f.source, extra: AuditHistoryPartition{Name: "audit_events_2026_06", From: month(2026, time.June).From, To: month(2026, time.June).To}}
	_, err = copier.Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true})
	require.ErrorContains(t, err, "audit_events_2026_06 ends before the cutoff and was not verified")
	require.Empty(t, f.source.drops)
}

// listingAfterVerification lists one more partition on every listing after the
// first, as a partition created while the copy ran would appear.
type listingAfterVerification struct {
	*historySource
	extra  AuditHistoryPartition
	listed int
}

func (l *listingAfterVerification) ListAuditPartitions(ctx context.Context) ([]AuditHistoryPartition, error) {
	l.listed++
	partitions, err := l.historySource.ListAuditPartitions(ctx)
	if l.listed > 1 {
		partitions = append(partitions, l.extra)
	}
	return partitions, err
}

func TestAuditHistoryVerifyOnlyWritesNothing(t *testing.T) {
	f := newHistoryFixture()
	_, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	appends, archived := f.store.appends, len(f.archive.batches)
	report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{VerifyOnly: true})
	require.NoError(t, err)
	require.True(t, report.Verified)
	require.Equal(t, appends, f.store.appends)
	require.Len(t, f.archive.batches, archived)
}
