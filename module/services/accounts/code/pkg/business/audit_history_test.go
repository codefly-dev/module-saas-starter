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
	// hidden are tables the monthly listing does not return — the default
	// partition, a partition under another name — that still hold rows.
	hidden []AuditHistoryPartition
	rows   []AuditEntry
	// tableOf, when set, says which table a row sits in; the default is the
	// partition whose bounds hold it.
	tableOf func(AuditEntry) string
	// drops are the partitions each drop asked for, dropCutoffs its cutoffs.
	drops       [][]AuditHistoryDropTarget
	dropCutoffs []time.Time
	// dropNames, when set, is the set the database claims to have dropped (and
	// did drop), in place of the one asked for.
	dropNames func([]AuditHistoryDropTarget) []string
	// refusal, when set, is the database refusing the drop.
	refusal string
	// beforeCount runs before every count, so a test can change the table
	// between verification and the drop.
	beforeCount func(*historySource)
}

// WithControlPlane runs fn as one transaction: what fn drops stays dropped only
// if fn returns nil.
func (s *historySource) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	before := append([]AuditHistoryPartition(nil), s.partitions...)
	if err := fn(ctx); err != nil {
		s.partitions = before
		return err
	}
	return nil
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

func (s *historySource) table(row AuditEntry) string {
	if s.tableOf != nil {
		return s.tableOf(row)
	}
	for _, partition := range append(append([]AuditHistoryPartition(nil), s.partitions...), s.hidden...) {
		if !row.CreatedAt.Before(partition.From) && row.CreatedAt.Before(partition.To) {
			return partition.Name
		}
	}
	return "audit_events"
}

func (s *historySource) CountAuditHistoryByTable(_ context.Context, from, to time.Time) ([]AuditHistoryTableRows, error) {
	totals := map[string]int64{}
	for _, row := range s.rows {
		if row.CreatedAt.Before(to) && (from.IsZero() || !row.CreatedAt.Before(from)) {
			totals[s.table(row)]++
		}
	}
	var out []AuditHistoryTableRows
	for table, rows := range totals {
		out = append(out, AuditHistoryTableRows{Table: table, Rows: rows})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Table < out[j].Table })
	return out, nil
}

func (s *historySource) DropVerifiedAuditPartitions(_ context.Context, cutoff time.Time, targets []AuditHistoryDropTarget) ([]string, error) {
	s.drops = append(s.drops, append([]AuditHistoryDropTarget(nil), targets...))
	s.dropCutoffs = append(s.dropCutoffs, cutoff)
	if s.refusal != "" {
		return nil, &AuditHistoryDropRefusal{Reason: s.refusal}
	}
	names := targetNames(targets)
	if s.dropNames != nil {
		names = s.dropNames(targets)
	}
	gone := map[string]bool{}
	for _, name := range names {
		gone[name] = true
	}
	var kept []AuditHistoryPartition
	for _, partition := range s.partitions {
		if !gone[partition.Name] {
			kept = append(kept, partition)
		}
	}
	s.partitions = kept
	return names, nil
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

// historyDetailsRow is one row of a split store's details table.
type historyDetailsRow struct {
	eventID, sha256, text string
}

// splitHistoryStore is a store of record as both adapters keep one: an events
// row for every record — carrying its details when the event is security-class —
// and, written after the events rows, a details row for each content-class
// record. A read attaches the details to every events row of the event by its
// id, and when the details rows disagree reports one whose hash is not the
// event's, as the adapters' readers do. failDetails makes the details write of
// that (1-based) append fail after its events rows landed: the partial write
// either adapter can leave.
type splitHistoryStore struct {
	events      []StoredAuditEvent // a content-class row carries no details
	details     []historyDetailsRow
	appends     int
	failDetails int
}

func (s *splitHistoryStore) AppendAuditBatch(_ context.Context, batch AuditBatch) error {
	s.appends++
	for _, record := range batch.Records {
		entry := record.Entry
		entry.Payload = nil
		event := StoredAuditEvent{
			DeploymentID: batch.DeploymentID, Entry: entry, Retention: record.Retention, DetailsSHA256: record.DetailsSHA256,
		}
		if record.Retention == RetentionSecurity {
			event.Details, event.HasDetails = record.Details, true
		}
		s.events = append(s.events, event)
	}
	if s.appends == s.failDetails {
		return errors.New("details write failed")
	}
	for _, record := range batch.Records {
		if record.Retention != RetentionSecurity {
			s.details = append(s.details, historyDetailsRow{record.Entry.ID, record.DetailsSHA256, record.Details})
		}
	}
	return nil
}

func (s *splitHistoryStore) ReadStoredAuditEvents(_ context.Context, from, to time.Time, visit func(StoredAuditEvent) error) error {
	for _, event := range s.events {
		if event.Entry.CreatedAt.Before(from) || !event.Entry.CreatedAt.Before(to) {
			continue
		}
		if event.Retention != RetentionSecurity {
			for _, row := range s.details {
				if row.eventID != event.Entry.ID {
					continue
				}
				event.Details, event.HasDetails = row.text, true
				if row.sha256 != event.DetailsSHA256 {
					break
				}
			}
		}
		if err := visit(event); err != nil {
			return err
		}
	}
	return nil
}

func (s *splitHistoryStore) copies() map[string]int {
	out := map[string]int{}
	for _, event := range s.events {
		out[event.Entry.ID]++
	}
	return out
}

type historyArchive struct{ batches []AuditBatch }

func (a *historyArchive) WriteAuditBatch(_ context.Context, batch AuditBatch) error {
	a.batches = append(a.batches, batch)
	return nil
}

// historyNow is after the last of the fixture's months: a drop needs a cutoff
// that has passed.
var historyNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

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
	// split, when set, is the store of record in place of store: one that keeps
	// a content-class event's details in a table of their own, as the adapters do.
	split *splitHistoryStore
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
	if f.split != nil {
		cfg.Store, cfg.ReadBack = f.split, f.split
	}
	if cfg.Through == nil && len(f.source.partitions) > 0 {
		through := f.source.partitions[len(f.source.partitions)-1].To
		cfg.Through = &through
	}
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
	report, err = f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), f.dropOptions(t, AuditHistoryCopyConfig{}))
	require.NoError(t, err)
	require.Equal(t, appends, f.store.appends, "a resumed run copies nothing already there")
	require.Equal(t, []time.Time{report.Cutoff}, f.source.dropCutoffs)
	require.Equal(t, []AuditHistoryDropTarget{
		{"audit_events_2026_08", 15}, {"audit_events_2026_09", 12}, {"audit_events_2026_10", 0},
	}, f.source.drops[0], "the database is asked for each verified partition by name, with the rows it was verified with")
	require.Equal(t, int64(3), report.Dropped)
	require.Equal(t, []string{"audit_events_2026_08", "audit_events_2026_09", "audit_events_2026_10"}, report.DroppedNames,
		"the receipt names what the database says it dropped")
	require.Empty(t, f.source.partitions)
}

func TestAuditHistoryCopyResumesAfterAnInterruption(t *testing.T) {
	f := newHistoryFixture()
	f.store.failAppend = 3
	_, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), f.dropOptions(t, AuditHistoryCopyConfig{}))
	require.ErrorContains(t, err, "warehouse unavailable")
	require.Empty(t, f.source.drops, "an interrupted run drops nothing")
	partial := len(f.store.copies())
	require.Positive(t, partial)
	require.Less(t, partial, len(f.source.rows))

	f.store.failAppend = 0
	report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), f.dropOptions(t, AuditHistoryCopyConfig{}))
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
	report, err := f.copier(t, AuditHistoryCopyConfig{ContentRetention: 7 * 24 * time.Hour}).Run(context.Background(), f.dropOptions(t, AuditHistoryCopyConfig{ContentRetention: 7 * 24 * time.Hour}))
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
	report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), f.dropOptions(t, AuditHistoryCopyConfig{}))
	require.ErrorContains(t, err, "changed since it was verified")
	require.True(t, report.Verified)
	require.Contains(t, report.DropRefused, "audit_events_2026_09")
	require.Empty(t, f.source.drops)
}

func TestAuditHistoryThroughLimitsThePartitionsAndTheCutoff(t *testing.T) {
	f := newHistoryFixture()
	through := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	report, err := f.copier(t, AuditHistoryCopyConfig{Through: &through}).Run(context.Background(), f.dropOptions(t, AuditHistoryCopyConfig{Through: &through}))
	require.NoError(t, err)
	require.Len(t, report.Partitions, 1)
	require.Equal(t, "audit_events_2026_08", report.Partitions[0].Partition.Name)
	require.Equal(t, []time.Time{through}, f.source.dropCutoffs)
	for _, event := range f.store.stored {
		require.True(t, event.Entry.CreatedAt.Before(through), "only August is copied")
	}

	// A partition the run did not verify that the cutoff would drop refuses it.
	f = newHistoryFixture()
	f.source.partitions = append([]AuditHistoryPartition{{Name: "audit_events_2026_07", From: month(2026, time.July).From, To: month(2026, time.July).To}}, f.source.partitions...)
	copier := f.copier(t, AuditHistoryCopyConfig{Through: &through})
	digest := f.dropOptions(t, AuditHistoryCopyConfig{Through: &through})
	copier.cfg.Source = &listingAfterVerification{historySource: f.source, extra: AuditHistoryPartition{Name: "audit_events_2026_06", From: month(2026, time.June).From, To: month(2026, time.June).To}}
	_, err = copier.Run(context.Background(), digest)
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

// Construct the explicit prior verification receipt against a separate copy of
// the source, so interruption/tamper tests retain their original effects.
func (f *historyFixture) dropOptions(t *testing.T, cfg AuditHistoryCopyConfig) AuditHistoryCopyOptions {
	t.Helper()
	clone := &historyFixture{source: &historySource{partitions: append([]AuditHistoryPartition(nil), f.source.partitions...), rows: append([]AuditEntry(nil), f.source.rows...)}, store: &historyStore{}, archive: &historyArchive{}}
	report, err := clone.copier(t, cfg).Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	return AuditHistoryCopyOptions{ConfirmDrop: true, ExpectedPartitionsSHA256: report.PartitionsSHA256}
}

func TestAuditHistoryDropDigestBindsContentAndNeedsAPriorVerification(t *testing.T) {
	f := newHistoryFixture()
	copier := f.copier(t, AuditHistoryCopyConfig{})
	plan, err := copier.Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	verify, err := copier.Run(context.Background(), AuditHistoryCopyOptions{VerifyOnly: true})
	require.NoError(t, err)
	require.Equal(t, plan.PartitionsSHA256, verify.PartitionsSHA256, "copy counts and run timing do not alter a verification authorization")
	_, err = copier.Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true, VerifyOnly: true})
	require.ErrorContains(t, err, "matching verification digest")
	require.Empty(t, f.source.drops)
	// Equal counts are insufficient: model a source and destination rewritten
	// together after the receipt, preserving IDs and counts but changing content.
	f.source.rows[0].Payload = map[string]any{"n": 999, "repo": "example/changed"}
	resolver := NewAuditEventResolver(nil)
	resolved, err := resolver.Resolve(context.Background(), f.source.rows[0].EventType)
	require.NoError(t, err)
	record, err := NewAuditRecord(f.source.rows[0], resolved.RetentionClass())
	require.NoError(t, err)
	for i := range f.store.stored {
		if f.store.stored[i].Entry.ID == record.Entry.ID {
			f.store.stored[i].Details = record.Details
			f.store.stored[i].DetailsSHA256 = record.DetailsSHA256
		}
	}
	current, err := copier.Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true, VerifyOnly: true, ExpectedPartitionsSHA256: plan.PartitionsSHA256})
	require.ErrorContains(t, err, "matching verification digest")
	require.True(t, current.Verified, "current copy is valid but the operator approved different content")
	require.NotEqual(t, plan.PartitionsSHA256, current.PartitionsSHA256)
	require.Empty(t, f.source.drops)
	copier.cfg.Through = nil
	_, err = copier.Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true, VerifyOnly: true, ExpectedPartitionsSHA256: current.PartitionsSHA256})
	require.ErrorContains(t, err, "explicit cutoff")
	require.Empty(t, f.source.drops)
}

// The drop is by name, the names are the verified ones, and the receipt carries
// what the database reports it dropped. A database that drops anything else —
// the way a drop by partition name in another time zone would — is rolled back
// before it commits, whatever it claims.
func TestAuditHistoryDropRefusesASetThatIsNotTheVerifiedOne(t *testing.T) {
	for name, tc := range map[string]struct {
		dropNames func([]AuditHistoryDropTarget) []string
		want      string
	}{
		"one more than was verified": {
			dropNames: func(targets []AuditHistoryDropTarget) []string {
				return append(targetNames(targets), "audit_events_2026_11")
			},
			want: "not the verified",
		},
		"one fewer": {
			dropNames: func(targets []AuditHistoryDropTarget) []string { return targetNames(targets)[:2] },
			want:      "not the verified",
		},
		"another in place of one": {
			dropNames: func(targets []AuditHistoryDropTarget) []string {
				return []string{"audit_events_2026_08", "audit_events_2026_09", "audit_events_2026_12"}
			},
			want: "not the verified",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHistoryFixture()
			f.source.dropNames = tc.dropNames
			options := f.dropOptions(t, AuditHistoryCopyConfig{})
			report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), options)
			require.ErrorIs(t, err, ErrAuditHistoryDropRefused)
			require.Contains(t, report.DropRefused, tc.want)
			require.Empty(t, report.DroppedNames)
			require.Zero(t, report.Dropped)
			require.Len(t, f.source.partitions, 3, "the transaction was rolled back")
		})
	}
}

func TestAuditHistoryDropReportsTheDatabasesRefusal(t *testing.T) {
	f := newHistoryFixture()
	f.source.refusal = "partition audit_events_2026_09 holds 13 rows, 12 were verified"
	report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), f.dropOptions(t, AuditHistoryCopyConfig{}))
	require.ErrorIs(t, err, ErrAuditHistoryDropRefused)
	require.ErrorContains(t, err, "holds 13 rows, 12 were verified")
	require.Equal(t, f.source.refusal, report.DropRefused)
	require.True(t, report.Verified, "the copy was verified; the drop was refused")
	require.Empty(t, report.DroppedNames)
	require.Len(t, f.source.partitions, 3)
}

func TestAuditHistoryDropNeedsACutoffThatHasPassed(t *testing.T) {
	f := newHistoryFixture()
	copier := f.copier(t, AuditHistoryCopyConfig{})
	copier.cfg.Now = func() time.Time { return time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC) }
	plan, err := copier.Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err, "copying and verifying through a month that has not ended is allowed")
	report, err := copier.Run(context.Background(), AuditHistoryCopyOptions{ConfirmDrop: true, VerifyOnly: true, ExpectedPartitionsSHA256: plan.PartitionsSHA256})
	require.ErrorIs(t, err, ErrAuditHistoryDropRefused)
	require.Contains(t, report.DropRefused, "completed month")
	require.Empty(t, f.source.drops)
}

// A run that finds no partition to verify has not verified anything, and says
// so as an outcome of its own rather than as success.
func TestAuditHistoryWithNoPartitionToVerifyClaimsNothing(t *testing.T) {
	f := newHistoryFixture()
	f.source.partitions, f.source.rows = nil, nil
	through := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, opts := range []AuditHistoryCopyOptions{{}, {VerifyOnly: true, ConfirmDrop: true, ExpectedPartitionsSHA256: "ignored"}} {
		report, err := f.copier(t, AuditHistoryCopyConfig{Through: &through}).Run(context.Background(), opts)
		require.ErrorIs(t, err, ErrAuditHistoryNothingToVerify)
		require.False(t, report.Verified)
		require.Empty(t, report.Uncovered)
		require.Empty(t, f.source.drops)
	}
}

// Events older than the cutoff that no verified partition holds fail the run:
// the partitions were chosen by their bounds, and the table can hold events
// elsewhere — in a default partition, a partition under another name, one that
// straddles the cutoff, or when it is not partitioned at all.
func TestAuditHistoryEventsOutsideTheVerifiedPartitionsFailVerification(t *testing.T) {
	newYork := func(m time.Month) AuditHistoryPartition {
		p := month(2026, m)
		p.From, p.To = p.From.Add(4*time.Hour), p.To.Add(4*time.Hour)
		return p
	}
	through := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		prepare func(*historyFixture)
		table   string
		rows    int64
	}{
		"a default partition": {
			prepare: func(f *historyFixture) {
				// An event from before the first partition, which only a default
				// partition could hold.
				f.source.rows = append(f.source.rows, historyRow(900, "", EventAuthLogin, time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)))
				f.source.tableOf = func(e AuditEntry) string {
					if e.CreatedAt.Before(month(2026, time.August).From) {
						return "audit_events_default"
					}
					return "audit_events_2026_08"
				}
			},
			table: "audit_events_default", rows: 1,
		},
		"a partition under another name": {
			prepare: func(f *historyFixture) {
				// The listing returns monthly names only; this one holds August 30th.
				odd := AuditHistoryPartition{Name: "audit_events_aug_late", From: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)}
				f.source.hidden = []AuditHistoryPartition{odd}
				f.source.partitions = []AuditHistoryPartition{{Name: "audit_events_2026_07", From: month(2026, time.July).From, To: odd.From}, {Name: "audit_events_2026_08", From: odd.To, To: month(2026, time.August).To}}
			},
			table: "audit_events_aug_late", rows: 6,
		},
		"a partition that straddles the cutoff": {
			prepare: func(f *historyFixture) {
				f.source.partitions = []AuditHistoryPartition{newYork(time.July), newYork(time.August)}
			},
			table: "audit_events_2026_08", rows: 15,
		},
		"a table that is not partitioned": {
			prepare: func(f *historyFixture) { f.source.partitions = nil },
			table:   "audit_events", rows: 15,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHistoryFixture()
			tc.prepare(f)
			report, err := f.copier(t, AuditHistoryCopyConfig{Through: &through}).Run(context.Background(), AuditHistoryCopyOptions{})
			require.ErrorIs(t, err, ErrAuditHistoryUnverified)
			require.False(t, report.Verified)
			require.Contains(t, report.Uncovered, AuditHistoryTableRows{Table: tc.table, Rows: tc.rows})

			// The digest of the unverified run authorizes nothing.
			_, err = f.copier(t, AuditHistoryCopyConfig{Through: &through}).Run(context.Background(),
				AuditHistoryCopyOptions{VerifyOnly: true, ConfirmDrop: true, ExpectedPartitionsSHA256: report.PartitionsSHA256})
			require.ErrorIs(t, err, ErrAuditHistoryUnverified)
			require.Empty(t, f.source.drops)
		})
	}
}

// Contiguous verified partitions leave no gap to look in but the two ends, and
// nothing outside them passes unnoticed on either.
func TestAuditHistoryLooksForUncoveredEventsOnlyWhereThePartitionsAreNot(t *testing.T) {
	f := newHistoryFixture()
	queried := &countingTables{historySource: f.source}
	through := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	copier := f.copier(t, AuditHistoryCopyConfig{Through: &through})
	copier.cfg.Source = queried
	_, err := copier.Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	require.Len(t, queried.intervals, 1, "before the first partition; the cutoff is the last partition's end")
	require.True(t, queried.intervals[0][0].IsZero())
	require.Equal(t, month(2026, time.August).From, queried.intervals[0][1])
}

type countingTables struct {
	*historySource
	intervals [][2]time.Time
}

func (c *countingTables) CountAuditHistoryByTable(ctx context.Context, from, to time.Time) ([]AuditHistoryTableRows, error) {
	c.intervals = append(c.intervals, [2]time.Time{from, to})
	return c.historySource.CountAuditHistoryByTable(ctx, from, to)
}

// The copy matches events by id within a read window while audit_events is keyed
// by (id, created_at). Two rows with one id in the same window cannot both match
// the copies found, so verification fails closed; in different windows each copy
// matches its own row. module/AUDIT_OPERATIONS.md says exactly this.
func TestAuditHistoryDuplicateIDsFailVerificationWithinAWindow(t *testing.T) {
	for name, tc := range map[string]struct {
		offset   time.Duration
		verified bool
	}{
		"in the same UTC-day window":    {offset: time.Hour, verified: false},
		"in a different window (a day)": {offset: 24 * time.Hour, verified: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHistoryFixture()
			twin := f.source.rows[0]
			twin.CreatedAt = twin.CreatedAt.Add(tc.offset)
			f.source.rows = append(f.source.rows, twin)
			report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
			if tc.verified {
				require.NoError(t, err)
				require.True(t, report.Verified)
				require.Equal(t, 2, f.store.copies()[twin.ID], "one stored row per source row")
				return
			}
			require.ErrorIs(t, err, ErrAuditHistoryUnverified)
			require.False(t, report.Verified)
			var problems []string
			for _, partition := range report.Partitions {
				problems = append(problems, partition.Problems...)
			}
			require.Contains(t, problems[0], "stored envelope differs from the row")
		})
	}
}

// Both adapters write an event's events row before its details row, so a
// details write that fails leaves the events row behind. The copy decides what
// to write from what verification would conclude, not from the id being in the
// store: the next run writes the event again, its details land, and they attach
// to the events row already there.
func TestAuditHistoryRewritesEventsWhoseDetailsWriteFailed(t *testing.T) {
	f := newHistoryFixture()
	f.split = &splitHistoryStore{failDetails: 1}
	_, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
	require.ErrorContains(t, err, "details write failed")
	require.Empty(t, f.split.details, "the failed append left events rows and no details")
	require.Len(t, f.split.events, 4, "the first batch's events rows landed")
	var landed []string // the ids of the content-class events whose details are missing
	for _, event := range f.split.events {
		if event.Retention == RetentionContent {
			landed = append(landed, event.Entry.ID)
		}
	}
	require.Len(t, landed, 3)

	f.split.failDetails = 0
	report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	require.True(t, report.Verified)
	var copied, rewritten int
	for _, partition := range report.Partitions {
		copied += partition.Copied
		rewritten += partition.Rewritten
		require.Zero(t, partition.Failures)
	}
	require.Equal(t, 3, rewritten, "the content events whose details were lost are written again")
	require.Equal(t, len(f.source.rows)-4, copied, "the events the first run never reached are copied")

	copies := f.split.copies()
	for _, id := range landed {
		require.Equal(t, 2, copies[id], "the events row of the failed write and the one written again: %s", id)
	}
	// Every copy a read-back returns carries its details, so every copy passes
	// the verification the run just made.
	require.NoError(t, f.split.ReadStoredAuditEvents(context.Background(), time.Time{}, historyNow, func(event StoredAuditEvent) error {
		require.True(t, event.HasDetails, "event %s", event.Entry.ID)
		require.Equal(t, event.DetailsSHA256, AuditDetailsSHA256(event.Details))
		return nil
	}))

	// A run after that one finds nothing to write, and drops what was verified.
	appends := f.split.appends
	report, err = f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), f.dropOptions(t, AuditHistoryCopyConfig{}))
	require.NoError(t, err)
	require.Equal(t, appends, f.split.appends)
	require.Equal(t, int64(3), report.Dropped)
	for _, partition := range report.Partitions {
		require.Zero(t, partition.Copied+partition.Rewritten)
	}
}

// A content-class event past the content window may have no details — the
// warehouse expired them — so verification does not ask for them and the copy
// does not write the event again to supply them.
func TestAuditHistoryDoesNotRewriteContentEventsPastTheWindow(t *testing.T) {
	f := newHistoryFixture()
	f.split = &splitHistoryStore{failDetails: 1}
	config := AuditHistoryCopyConfig{ContentRetention: 7 * 24 * time.Hour}
	_, err := f.copier(t, config).Run(context.Background(), AuditHistoryCopyOptions{})
	require.ErrorContains(t, err, "details write failed")

	f.split.failDetails = 0
	report, err := f.copier(t, config).Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	require.True(t, report.Verified)
	for _, partition := range report.Partitions {
		require.Zero(t, partition.Rewritten)
	}
	for id, n := range f.split.copies() {
		require.Equal(t, 1, n, "event %s is in the store once", id)
	}
}

// A write appends a copy; it removes nothing. So an event whose stored copy is
// wrong in itself is left alone — written again, the wrong copy would still be
// there, and still fail verification — and stays a verification failure.
func TestAuditHistoryDoesNotRewriteWhatAWriteCannotFix(t *testing.T) {
	const content, security = 0, 2 // indexes of rows of the fixture
	for name, tc := range map[string]struct {
		row    int
		tamper func(*StoredAuditEvent)
		want   string
	}{
		"a copy under another deployment": {
			row:    content,
			tamper: func(e *StoredAuditEvent) { e.DeploymentID = "deployment-2" },
			want:   "stored under deployment",
		},
		"an envelope that differs": {
			row:    content,
			tamper: func(e *StoredAuditEvent) { e.Entry.OrgID = "cccccccc-0000-4000-8000-000000000003" },
			want:   "envelope differs",
		},
		"a retention class that differs": {
			row:    content,
			tamper: func(e *StoredAuditEvent) { e.Retention = RetentionSecurity },
			want:   "stored as security, classified content",
		},
		"a details hash that differs": {
			row:    content,
			tamper: func(e *StoredAuditEvent) { e.DetailsSHA256 = AuditDetailsSHA256("{}") },
			want:   "details hash differs",
		},
		"details that do not hash to the row's hash": {
			row:    content,
			tamper: func(e *StoredAuditEvent) { e.Details = `{"n":0}` },
			want:   "do not hash",
		},
		"details missing under a hash that differs": {
			row: content,
			tamper: func(e *StoredAuditEvent) {
				e.DetailsSHA256, e.Details, e.HasDetails = AuditDetailsSHA256("{}"), "", false
			},
			want: "details hash differs",
		},
		"security-class details missing": {
			row:    security,
			tamper: func(e *StoredAuditEvent) { e.Details, e.HasDetails = "", false },
			want:   "security-class details are missing",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHistoryFixture()
			bad := f.source.rows[tc.row].ID
			f.store.tamper = func(e *StoredAuditEvent) {
				if e.Entry.ID == bad {
					tc.tamper(e)
				}
			}
			_, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
			require.ErrorIs(t, err, ErrAuditHistoryUnverified)
			appends, archived := f.store.appends, len(f.archive.batches)

			// The run that follows would append a good copy beside the bad one
			// if it wrote at all: the tamper is gone, and nothing is appended.
			f.store.tamper = nil
			report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
			require.ErrorIs(t, err, ErrAuditHistoryUnverified)
			require.False(t, report.Verified)
			require.Equal(t, appends, f.store.appends, "nothing is appended")
			require.Len(t, f.archive.batches, archived, "nothing is archived")
			require.Equal(t, 1, f.store.copies()[bad])
			var problems []string
			for _, partition := range report.Partitions {
				require.Zero(t, partition.Copied+partition.Rewritten)
				problems = append(problems, partition.Problems...)
			}
			require.Len(t, problems, 1)
			require.Contains(t, problems[0], bad)
			require.Contains(t, problems[0], tc.want)
		})
	}
}

// What the copy writes and what verification reports are one rule, applied to
// every copy of the event: a write is for the failure it removes, and when a
// copy is wrong in a way no write removes, that is what is reported.
func TestAuditHistoryInspectSaysWhatAWriteRemoves(t *testing.T) {
	copier, err := NewAuditHistoryCopy(AuditHistoryCopyConfig{
		Source: &historySource{}, Store: &historyStore{}, Archive: &historyArchive{}, ReadBack: &historyStore{},
		DeploymentID: "deployment-1", ContentRetention: 7 * 24 * time.Hour,
		Now: func() time.Time { return historyNow },
	})
	require.NoError(t, err)
	recent, old := historyNow.Add(-24*time.Hour), historyNow.Add(-30*24*time.Hour)
	recordOf := func(n int, eventType EventType, at time.Time) AuditRecord {
		resolved, err := NewAuditEventResolver(nil).Resolve(context.Background(), eventType)
		require.NoError(t, err)
		record, err := NewAuditRecord(historyRow(n, "", eventType, at), resolved.RetentionClass())
		require.NoError(t, err)
		return record
	}
	storedOf := func(record AuditRecord, change func(*StoredAuditEvent)) StoredAuditEvent {
		entry := record.Entry
		entry.Payload = nil
		stored := StoredAuditEvent{DeploymentID: "deployment-1", Entry: entry, Retention: record.Retention, DetailsSHA256: record.DetailsSHA256}
		if record.Retention == RetentionSecurity {
			stored.Details, stored.HasDetails = record.Details, true
		}
		if change != nil {
			change(&stored)
		}
		return stored
	}
	hasDetails := func(record AuditRecord) func(*StoredAuditEvent) {
		return func(e *StoredAuditEvent) { e.Details, e.HasDetails = record.Details, true }
	}
	otherOrg := func(e *StoredAuditEvent) { e.Entry.OrgID = "cccccccc-0000-4000-8000-000000000003" }
	noDetails := func(e *StoredAuditEvent) { e.Details, e.HasDetails = "", false }

	inside, outside := recordOf(1, EventDatasourceSourceSynced, recent), recordOf(2, EventDatasourceSourceSynced, old)
	login := recordOf(3, EventAuthLogin, recent)
	for name, tc := range map[string]struct {
		record   AuditRecord
		copies   []StoredAuditEvent
		problem  string
		writable bool
	}{
		"no copy":         {record: inside, problem: "missing from the store", writable: true},
		"a complete copy": {record: inside, copies: []StoredAuditEvent{storedOf(inside, hasDetails(inside))}},
		"content details missing inside the window": {
			record: inside, copies: []StoredAuditEvent{storedOf(inside, nil)},
			problem: "content details are missing inside the content window", writable: true,
		},
		"content details missing past the window": {record: outside, copies: []StoredAuditEvent{storedOf(outside, nil)}},
		"security details missing": {
			record: login, copies: []StoredAuditEvent{storedOf(login, noDetails)},
			problem: "security-class details are missing",
		},
		"one copy missing details, the other differing, in either order": {
			record: inside, copies: []StoredAuditEvent{storedOf(inside, nil), storedOf(inside, func(e *StoredAuditEvent) { otherOrg(e); hasDetails(inside)(e) })},
			problem: "stored envelope differs from the row",
		},
		"one copy differing, the other missing details": {
			record: inside, copies: []StoredAuditEvent{storedOf(inside, func(e *StoredAuditEvent) { otherOrg(e); hasDetails(inside)(e) }), storedOf(inside, nil)},
			problem: "stored envelope differs from the row",
		},
		"every copy missing details": {
			record: inside, copies: []StoredAuditEvent{storedOf(inside, nil), storedOf(inside, nil)},
			problem: "content details are missing inside the content window", writable: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			problem, writable := copier.inspect(tc.record, tc.copies)
			require.Equal(t, tc.problem, problem)
			require.Equal(t, tc.writable, writable)
			require.Equal(t, tc.problem, copier.verify(tc.record, tc.copies), "verification reports what inspect found")
		})
	}
}
