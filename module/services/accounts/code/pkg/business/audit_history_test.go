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
//
// It can also refuse rows for good, as both adapters do: refuseDetails names
// events whose details row it refuses (its events row has landed), refuseEvents
// those whose events row it refuses (and so their details row is never sent), each
// reported as a PermanentRowRejection beside the other rows' result. By default
// the rest of the batch is written, as ClickHouse does around a refused row;
// stopRest leaves the rest of the refused table's rows unwritten, as BigQuery
// does for the rows it reports as stopped. unreadable names events whose details
// the store accepts and never reads back, as when their partition is already gone.
type splitHistoryStore struct {
	events      []StoredAuditEvent // a content-class row carries no details
	details     []historyDetailsRow
	appends     int
	failDetails int

	refuseDetails, refuseEvents, unreadable map[string]bool
	stopRest                                bool
	// attempts counts the records of every append by event id.
	attempts map[string]int
}

func refusalOf(id, what string) error {
	return &PermanentRowRejection{EventID: id, Cause: errors.New(what + " refused")}
}

func (s *splitHistoryStore) AppendAuditBatch(_ context.Context, batch AuditBatch) error {
	s.appends++
	if s.attempts == nil {
		s.attempts = map[string]int{}
	}
	var refusals, eventRefusals []error
	for _, record := range batch.Records {
		s.attempts[record.Entry.ID]++
		if s.refuseEvents[record.Entry.ID] {
			eventRefusals = append(eventRefusals, refusalOf(record.Entry.ID, "events row"))
		}
	}
	refusals = append(refusals, eventRefusals...)
	if s.stopRest && len(eventRefusals) > 0 {
		return errors.Join(append(refusals, errors.New("events rows stopped"))...)
	}
	for _, record := range batch.Records {
		if s.refuseEvents[record.Entry.ID] {
			continue
		}
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
	var detailRefusals []error
	for _, record := range batch.Records {
		if record.Retention != RetentionSecurity && !s.refuseEvents[record.Entry.ID] && s.refuseDetails[record.Entry.ID] {
			detailRefusals = append(detailRefusals, refusalOf(record.Entry.ID, "details row"))
		}
	}
	refusals = append(refusals, detailRefusals...)
	if s.stopRest && len(detailRefusals) > 0 {
		return errors.Join(append(refusals, errors.New("details rows stopped"))...)
	}
	for _, record := range batch.Records {
		if record.Retention != RetentionSecurity && !s.refuseEvents[record.Entry.ID] && !s.refuseDetails[record.Entry.ID] {
			s.details = append(s.details, historyDetailsRow{record.Entry.ID, record.DetailsSHA256, record.Details})
		}
	}
	return errors.Join(refusals...)
}

func (s *splitHistoryStore) ReadStoredAuditEvents(_ context.Context, from, to time.Time, visit func(StoredAuditEvent) error) error {
	for _, event := range s.events {
		if event.Entry.CreatedAt.Before(from) || !event.Entry.CreatedAt.Before(to) {
			continue
		}
		if event.Retention != RetentionSecurity && !s.unreadable[event.Entry.ID] {
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
// historyRecordOf is the record the copy builds for a row of the fixture's kind.
func historyRecordOf(t *testing.T, n int, eventType EventType, at time.Time) AuditRecord {
	t.Helper()
	resolved, err := NewAuditEventResolver(nil).Resolve(context.Background(), eventType)
	require.NoError(t, err)
	record, err := NewAuditRecord(historyRow(n, "", eventType, at), resolved.RetentionClass())
	require.NoError(t, err)
	return record
}

// historyStoredOf is a copy of record as a store holds it right after the events
// row landed: a security-class one with its details, a content-class one without.
func historyStoredOf(record AuditRecord, change func(*StoredAuditEvent)) StoredAuditEvent {
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

// historyInspector is a copy with nothing behind it, for the rule alone.
func historyInspector(t *testing.T, retention time.Duration, now time.Time) *AuditHistoryCopy {
	t.Helper()
	copier, err := NewAuditHistoryCopy(AuditHistoryCopyConfig{
		Source: &historySource{}, Store: &historyStore{}, Archive: &historyArchive{}, ReadBack: &historyStore{},
		DeploymentID: "deployment-1", ContentRetention: retention,
		Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	return copier
}

func TestAuditHistoryInspectSaysWhatAWriteRemoves(t *testing.T) {
	copier := historyInspector(t, 7*24*time.Hour, historyNow)
	// recent is held; margin is a day the store may have expired a few hours ago
	// and may still hold; old is long gone.
	recent, margin, old := historyNow.Add(-24*time.Hour), historyNow.Add(-7*24*time.Hour), historyNow.Add(-30*24*time.Hour)
	recordOf := func(n int, eventType EventType, at time.Time) AuditRecord {
		return historyRecordOf(t, n, eventType, at)
	}
	storedOf := historyStoredOf
	hasDetails := func(record AuditRecord) func(*StoredAuditEvent) {
		return func(e *StoredAuditEvent) { e.Details, e.HasDetails = record.Details, true }
	}
	otherOrg := func(e *StoredAuditEvent) { e.Entry.OrgID = "cccccccc-0000-4000-8000-000000000003" }
	noDetails := func(e *StoredAuditEvent) { e.Details, e.HasDetails = "", false }

	inside, outside := recordOf(1, EventDatasourceSourceSynced, recent), recordOf(2, EventDatasourceSourceSynced, old)
	inMargin := recordOf(4, EventDatasourceSourceSynced, margin)
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
		"content details missing in the margin: written for, not a failure": {
			record: inMargin, copies: []StoredAuditEvent{storedOf(inMargin, nil)}, writable: true,
		},
		"no copy in the margin":                 {record: inMargin, problem: "missing from the store", writable: true},
		"content details present in the margin": {record: inMargin, copies: []StoredAuditEvent{storedOf(inMargin, hasDetails(inMargin))}},
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
		// A write for missing details is made once: a second copy of the event
		// beside the first is the proof that one was made.
		"every copy missing details, so a write was made": {
			record: inside, copies: []StoredAuditEvent{storedOf(inside, nil), storedOf(inside, nil)},
			problem: "content details are missing inside the content window; the store holds 2 copies, so an earlier write did not supply them, and the event is not written again",
		},
		"every copy missing details in the margin, so a write was made": {
			record: inMargin, copies: []StoredAuditEvent{storedOf(inMargin, nil), storedOf(inMargin, nil)},
		},
		"two copies, the details attached": {
			record: inside, copies: []StoredAuditEvent{storedOf(inside, hasDetails(inside)), storedOf(inside, hasDetails(inside))},
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

// historyTotals is what a run reported across its partitions.
type historyTotals struct {
	copied, rewritten, failures int
	verified, postgres          int64
	problems                    []string
}

func totalsOf(report AuditHistoryReport) historyTotals {
	var totals historyTotals
	for _, partition := range report.Partitions {
		totals.copied += partition.Copied
		totals.rewritten += partition.Rewritten
		totals.failures += partition.Failures
		totals.problems = append(totals.problems, partition.Problems...)
		for _, count := range partition.Orgs {
			totals.verified += count.Verified
			totals.postgres += count.Postgres
		}
	}
	return totals
}

// The store refuses one event's details row for good — its events row has
// landed — as ClickHouse does when its isolation finds the row, and as BigQuery
// does when it reports the row invalid (and the rest of that request stopped).
// That event is a verification failure of its own. It does not stop the copy of
// the others, and it is not written again on every run: a write for missing
// details is made once, and the run that finds one already made says so.
func TestAuditHistoryAPermanentlyRefusedDetailsRowStopsNothingAndIsNotRepeated(t *testing.T) {
	for name, stopRest := range map[string]bool{"the rest written around the refused row": false, "the rest of the request stopped": true} {
		t.Run(name, func(t *testing.T) {
			f := newHistoryFixture()
			bad := f.source.rows[0].ID // the first content-class event, in the first batch
			require.Equal(t, RetentionContent, historyRecordOf(t, 1, f.source.rows[0].EventType, f.source.rows[0].CreatedAt).Retention)
			f.split = &splitHistoryStore{refuseDetails: map[string]bool{bad: true}, stopRest: stopRest}
			run := func() (AuditHistoryReport, historyTotals) {
				report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
				require.ErrorIs(t, err, ErrAuditHistoryUnverified, "a refused event is a verification failure, not a failed run")
				require.False(t, report.Verified)
				totals := totalsOf(report)
				require.Equal(t, 1, totals.failures)
				require.Len(t, totals.problems, 1)
				require.Contains(t, totals.problems[0], bad)
				require.Equal(t, int64(len(f.source.rows)), totals.postgres)
				require.Equal(t, int64(len(f.source.rows)-1), totals.verified, "every other event is copied and verified")
				return report, totals
			}

			// The run reaches the end: every later window and the later partition.
			report, totals := run()
			require.Len(t, report.Partitions, 3)
			require.Equal(t, len(f.source.rows)-1, totals.copied, "the refused event is not counted as copied")
			require.Zero(t, totals.rewritten)
			require.Contains(t, totals.problems[0], "the store refused the event's row for good")
			require.Equal(t, 1, f.split.copies()[bad])
			require.Equal(t, 1, f.split.attempts[bad], "its batch is sent again without it, not with it")
			september := report.Partitions[1]
			require.NotEmpty(t, september.Orgs)
			require.Zero(t, september.Failures)
			events, archived := len(f.split.events), len(f.archive.batches)

			// The next run makes the one write for the details it is missing and is
			// refused again; nothing else is written.
			_, totals = run()
			require.Zero(t, totals.copied+totals.rewritten)
			require.Equal(t, 2, f.split.attempts[bad])
			require.Equal(t, 2, f.split.copies()[bad])
			require.Equal(t, events+1, len(f.split.events))
			require.Equal(t, archived+1, len(f.archive.batches))
			require.Equal(t, []string{bad}, historyRecordIDs(f.archive.batches[len(f.archive.batches)-1].Records))
			require.Contains(t, totals.problems[0], "the store refused the event's row for good")

			// Neither this run nor the ones after it write or archive anything.
			appends := f.split.appends
			for range 3 {
				_, totals = run()
				require.Zero(t, totals.copied+totals.rewritten)
				require.Equal(t, appends, f.split.appends, "nothing is appended")
				require.Equal(t, events+1, len(f.split.events))
				require.Len(t, f.archive.batches, archived+1, "nothing is archived")
				require.Equal(t, 2, f.split.attempts[bad])
				require.Contains(t, totals.problems[0], "the store holds 2 copies")
				require.Contains(t, totals.problems[0], "not written again")
			}
		})
	}
}

func historyRecordIDs(records []AuditRecord) []string {
	var ids []string
	for _, record := range records {
		ids = append(ids, record.Entry.ID)
	}
	return ids
}

// An event whose events row the store refuses for good is stored nowhere, so
// nothing on the next run says a write was made: it is written again each run, and
// reported each time. The cost is one archive object holding that event alone, not
// the window's, and no row in the store.
func TestAuditHistoryAnEventWhoseEventsRowIsRefusedStopsNothing(t *testing.T) {
	for name, stopRest := range map[string]bool{"the rest written around the refused row": false, "the rest of the request stopped": true} {
		t.Run(name, func(t *testing.T) {
			f := newHistoryFixture()
			bad := f.source.rows[0].ID
			f.split = &splitHistoryStore{refuseEvents: map[string]bool{bad: true}, stopRest: stopRest}
			for run := range 3 {
				before := len(f.archive.batches)
				report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
				require.ErrorIs(t, err, ErrAuditHistoryUnverified)
				totals := totalsOf(report)
				require.Equal(t, 1, totals.failures)
				require.Equal(t, int64(len(f.source.rows)-1), totals.verified)
				require.Len(t, totals.problems, 1)
				require.Contains(t, totals.problems[0], bad)
				require.Contains(t, totals.problems[0], "the store refused the event's row for good; missing from the store")
				require.Zero(t, f.split.copies()[bad], "nothing of it is stored")
				if run == 0 {
					require.Equal(t, len(f.source.rows)-1, totals.copied)
				} else {
					require.Zero(t, totals.copied+totals.rewritten)
					require.Len(t, f.archive.batches, before+1)
					require.Equal(t, []string{bad}, historyRecordIDs(f.archive.batches[before].Records))
				}
			}
		})
	}
}

// A refusal that names no event of the batch is no evidence about any of them:
// the run fails, as it does for any other failure of the store.
func TestAuditHistoryARefusalOfAnEventNotInTheBatchFailsTheRun(t *testing.T) {
	f := newHistoryFixture()
	store := &refusingHistoryStore{historyStore: f.store, err: &PermanentRowRejection{EventID: "someone-else", Cause: errors.New("refused")}}
	copier := f.copier(t, AuditHistoryCopyConfig{})
	copier.cfg.Store = store
	_, err := copier.Run(context.Background(), AuditHistoryCopyOptions{})
	require.ErrorContains(t, err, "append batch")
	require.NotErrorIs(t, err, ErrAuditHistoryUnverified)
	require.Equal(t, 1, store.calls, "the batch is not sent again")
}

type refusingHistoryStore struct {
	*historyStore
	err   error
	calls int
}

func (s *refusingHistoryStore) AppendAuditBatch(context.Context, AuditBatch) error {
	s.calls++
	return s.err
}

// A write whose details the store accepts and then never shows is made once in a
// run, once more by the next, and not again: the run that finds the second copy
// says that a write was made and did not supply them.
func TestAuditHistoryDetailsWrittenButNeverReadableAreNotWrittenAgainAndAgain(t *testing.T) {
	f := newHistoryFixture()
	bad := f.source.rows[0].ID
	f.split = &splitHistoryStore{unreadable: map[string]bool{bad: true}}
	run := func() historyTotals {
		report, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
		require.ErrorIs(t, err, ErrAuditHistoryUnverified)
		totals := totalsOf(report)
		require.Equal(t, 1, totals.failures)
		require.Equal(t, int64(len(f.source.rows)-1), totals.verified)
		require.Contains(t, totals.problems[0], bad)
		return totals
	}

	totals := run()
	require.Equal(t, len(f.source.rows), totals.copied)
	require.Equal(t, 1, f.split.attempts[bad], "written once in the run, not again after the read-back")
	require.Contains(t, totals.problems[0], "written by this run, but the read-back finds it incomplete: content details are missing")

	totals = run()
	require.Equal(t, 1, totals.rewritten)
	require.Zero(t, totals.copied)
	require.Equal(t, 2, f.split.attempts[bad])
	require.Contains(t, totals.problems[0], "written by this run, but the read-back finds it incomplete")

	appends, archived := f.split.appends, len(f.archive.batches)
	for range 2 {
		totals = run()
		require.Zero(t, totals.copied+totals.rewritten)
		require.Equal(t, appends, f.split.appends)
		require.Len(t, f.archive.batches, archived)
		require.Contains(t, totals.problems[0], "an earlier write did not supply them")
	}
}

// The rule for the details of a content-class event, at the instants where it
// changes. The store holds the details until the start of the event's UTC day
// plus the retention, because it expires by day, so an event's details are
// required while now is before that instant — and only then. For the margin
// after it they may still be held: the copy writes them, and never fails for them.
// A short retention keeps its requirement: one day still requires the events of
// today.
func TestAuditHistoryDetailsRuleAtTheBoundaries(t *testing.T) {
	const day = 24 * time.Hour
	today := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	nows := map[string]time.Time{
		"at midnight":          today,
		"at noon":              today.Add(12 * time.Hour),
		"just before midnight": today.Add(day - time.Nanosecond),
	}
	type zone int
	const (
		required zone = iota // the store holds the details: a failure without them
		optional             // it may: written for, never a failure
		neither              // it does not: no failure, no write
	)
	for _, retention := range []time.Duration{day, 2 * day, 7 * day} {
		for nowName, now := range nows {
			copier := historyInspector(t, retention, now)
			first := today.Add(-retention) // the day whose details the store may expire as of today
			for offset, want := range map[int]zone{3: required, 2: required, 1: required, 0: optional, -1: optional, -2: neither, -3: neither} {
				for _, at := range []time.Duration{0, day - time.Nanosecond} {
					created := first.Add(time.Duration(offset)*day + at)
					if created.After(now) {
						continue
					}
					name := fmt.Sprintf("retention %s %s, event %d days from the first day, %s into its day", retention, nowName, offset, at)
					t.Run(name, func(t *testing.T) {
						record := historyRecordOf(t, 1, EventDatasourceSourceSynced, created)
						copies := []StoredAuditEvent{historyStoredOf(record, nil)}
						problem, writable := copier.inspect(record, copies)
						switch want {
						case required:
							require.Equal(t, "content details are missing inside the content window", problem)
							require.True(t, writable)
						case optional:
							require.Empty(t, problem, "the store may have expired them")
							require.True(t, writable, "and may still hold them")
						default:
							require.Empty(t, problem)
							require.False(t, writable)
						}
						require.Equal(t, problem, copier.verify(record, copies))
						// The same rule for an event that is not in the store yet: it
						// is written, whichever the zone.
						problem, writable = copier.inspect(record, nil)
						require.Equal(t, "missing from the store", problem)
						require.True(t, writable)
					})
				}
			}
		}
	}
}

// The same rule through a run, with the events of the fixture in each zone
// (retention 62 days, now 2 November at noon): 30 August is past the margin,
// 31 August and 1 September are in it, 2 September is held. The store accepts
// each event's details and never shows those of one event of each day.
func TestAuditHistoryContentDetailsInTheMarginAreWrittenForAndNeverFailed(t *testing.T) {
	f := newHistoryFixture()
	config := AuditHistoryCopyConfig{ContentRetention: 62 * 24 * time.Hour}
	f.split = &splitHistoryStore{unreadable: map[string]bool{}}
	byDay := map[int]string{}
	for _, row := range f.source.rows {
		if row.EventType == EventDatasourceSourceSynced && byDay[row.CreatedAt.Day()] == "" {
			byDay[row.CreatedAt.Day()] = row.ID
			f.split.unreadable[row.ID] = true
		}
	}
	past, margin1, margin2, held := byDay[30], byDay[31], byDay[1], byDay[2]
	require.Len(t, byDay, 4)
	run := func() historyTotals {
		report, err := f.copier(t, config).Run(context.Background(), AuditHistoryCopyOptions{})
		require.ErrorIs(t, err, ErrAuditHistoryUnverified)
		totals := totalsOf(report)
		require.Equal(t, 1, totals.failures, "only the event the store holds details of fails")
		require.Contains(t, totals.problems[0], held)
		return totals
	}

	run()
	for _, id := range []string{past, margin1, margin2, held} {
		require.Equal(t, 1, f.split.attempts[id], id)
	}
	totals := run()
	require.Equal(t, 3, totals.rewritten, "the held event and the two in the margin are written once more; the one past it is not")
	for id, attempts := range map[string]int{past: 1, margin1: 2, margin2: 2, held: 2} {
		require.Equal(t, attempts, f.split.attempts[id], id)
	}
	appends := f.split.appends
	for range 2 {
		totals = run()
		require.Zero(t, totals.copied+totals.rewritten)
		require.Equal(t, appends, f.split.appends)
	}
}

// With a retention of one day the details of today's events are still required,
// and those of yesterday's are written for without being required: the store
// held them until midnight.
func TestAuditHistoryAShortRetentionStillRequiresTheDetailsOfTodaysEvents(t *testing.T) {
	today, yesterday := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	november := month(2026, time.November)
	source := &historySource{partitions: []AuditHistoryPartition{november}, rows: []AuditEntry{
		historyRow(1, "", EventDatasourceSourceSynced, yesterday), historyRow(2, "", EventDatasourceSourceSynced, today),
	}}
	newFixture := func(split *splitHistoryStore) *historyFixture {
		return &historyFixture{source: source, store: &historyStore{}, archive: &historyArchive{}, split: split}
	}
	config := AuditHistoryCopyConfig{ContentRetention: 24 * time.Hour}

	// A details write that failed is made again for today's event.
	f := newFixture(&splitHistoryStore{failDetails: 1})
	f.source = &historySource{partitions: []AuditHistoryPartition{november}, rows: source.rows[1:]}
	_, err := f.copier(t, config).Run(context.Background(), AuditHistoryCopyOptions{})
	require.ErrorContains(t, err, "details write failed")
	require.Empty(t, f.split.details)
	report, err := f.copier(t, config).Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	require.True(t, report.Verified)
	require.Equal(t, 1, totalsOf(report).rewritten)
	require.Len(t, f.split.details, 1)

	// Details the store never shows fail verification for today's event alone.
	f = newFixture(&splitHistoryStore{unreadable: map[string]bool{source.rows[0].ID: true, source.rows[1].ID: true}})
	report, err = f.copier(t, config).Run(context.Background(), AuditHistoryCopyOptions{})
	require.ErrorIs(t, err, ErrAuditHistoryUnverified)
	totals := totalsOf(report)
	require.Equal(t, 1, totals.failures)
	require.Contains(t, totals.problems[0], source.rows[1].ID)
	require.Contains(t, totals.problems[0], "content details are missing inside the content window")
}
