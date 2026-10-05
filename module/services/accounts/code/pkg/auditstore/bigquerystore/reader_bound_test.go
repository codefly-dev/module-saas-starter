package bigquerystore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auditstore/bigqueryfake"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// What a read holds in memory is a number the deployment chose, not a function
// of the history it reads. These tests read a month of content-class events —
// megabytes of details — through readers whose window budget is a few
// kilobytes, and hold two things to account: the answer is the one a reader
// with no practical bound gives, and the most any window counted never passed
// the budget (plus the one row that overflowed it).

const (
	boundProject    = "test-project"
	boundDataset    = "audit"
	boundDeployment = "deployment-1"
	boundOrg        = "aaaaaaaa-0000-4000-8000-000000000001"
	boundActor      = "cccccccc-0000-4000-8000-000000000003"
)

var boundNow = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

type boundWarehouse struct {
	fake *bigqueryfake.Server
}

func newBoundWarehouse(t *testing.T) *boundWarehouse {
	t.Helper()
	fake := bigqueryfake.New()
	fake.CreateTable(bigqueryfake.TablePath(boundProject, boundDataset, EventsTable), EventsSchema())
	fake.CreateTable(bigqueryfake.TablePath(boundProject, boundDataset, DetailsTable), DetailsSchema())
	return &boundWarehouse{fake: fake}
}

func (w *boundWarehouse) reader(t *testing.T, windowBytes, exportMaxBytes int64) *Reader {
	t.Helper()
	reader, err := NewReader(ReadConfig{
		Client: w.fake, Project: boundProject, Dataset: boundDataset, DeploymentID: boundDeployment,
		WindowBytes: windowBytes, ExportMaxBytes: exportMaxBytes, Now: func() time.Time { return boundNow },
	})
	require.NoError(t, err)
	return reader
}

func (w *boundWarehouse) append(t *testing.T, records ...business.AuditRecord) {
	t.Helper()
	events, details := BatchRows(business.AuditBatch{ID: "batch", DeploymentID: boundDeployment, Records: records})
	for _, row := range events {
		require.NoError(t, w.fake.Insert(bigqueryfake.TablePath(boundProject, boundDataset, EventsTable), row.Values))
	}
	for _, row := range details {
		require.NoError(t, w.fake.Insert(bigqueryfake.TablePath(boundProject, boundDataset, DetailsTable), row.Values))
	}
}

func boundEvent(t *testing.T, n int, at time.Time, payload map[string]any) business.AuditRecord {
	t.Helper()
	record, err := business.NewAuditRecord(business.AuditEntry{
		ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", n), OrgID: boundOrg, ActorID: boundActor, ActorType: business.ActorTypeUser,
		EventType: business.EventDocumentRead, SchemaVersion: 1, Resource: "document", ResourceID: fmt.Sprintf("doc-%d", n%5),
		Payload: payload, CreatedAt: at,
	}, business.RetentionContent)
	require.NoError(t, err)
	return record
}

// month fills the warehouse with a month of content-class events, forty a day,
// each with about 900 bytes of details; every hundredth is "rare" and each has a
// "group" of seven. Every event is delivered twice, as a redelivered relay
// batch would leave it. It returns the events, newest first.
func (w *boundWarehouse) month(t *testing.T) []business.AuditRecord {
	t.Helper()
	pad := strings.Repeat("p", 900)
	var records []business.AuditRecord
	for i := 0; i < 1200; i++ {
		at := boundNow.Add(-time.Hour - time.Duration(i)*36*time.Minute)
		payload := map[string]any{"group": i % 7, "pad": pad}
		if i%100 == 99 {
			payload["rare"] = true
		}
		records = append(records, boundEvent(t, i+1, at, payload))
	}
	w.append(t, records...)
	w.append(t, records...)
	return records
}

func entryIDs(entries []business.AuditEntry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.ID
	}
	return out
}

func orgScoped(q business.AuditQuery) business.AuditRead {
	q.OrgID = boundOrg
	return business.AuditRead{Scope: business.OrganizationAuditScope(boundOrg), Query: q}
}

// oneRow is the most one row can add to a window past the budget: the row that
// overflows it is counted before the window is abandoned.
const oneRow = 4 << 10

// everyPage reads a list to its last page.
func everyPage(t *testing.T, reader *Reader, q business.AuditQuery) (ids []string, pages int) {
	t.Helper()
	for {
		entries, next, err := reader.ListAuditEvents(context.Background(), orgScoped(q))
		require.NoError(t, err)
		ids = append(ids, entryIDs(entries)...)
		pages++
		if next == "" {
			return ids, pages
		}
		q.PageToken = next
	}
}

func TestAPayloadFilteredListHoldsOneBoundedWindowOfDetailsNotTheHistory(t *testing.T) {
	w := newBoundWarehouse(t)
	records := w.month(t)
	var want []string
	for _, record := range records {
		if record.Entry.Payload["rare"] == true {
			want = append(want, record.Entry.ID)
		}
	}
	require.Len(t, want, 12)

	const budget = 64 << 10
	small, large := w.reader(t, budget, 0), w.reader(t, 1<<40, 0)
	q := business.AuditQuery{PageSize: 5, PayloadContains: map[string]any{"rare": true}}

	got, pages := everyPage(t, small, q)
	require.Equal(t, want, got, "every rare event, once each, newest first, across the pages")
	require.Equal(t, 3, pages)
	reference, _ := everyPage(t, large, q)
	require.Equal(t, reference, got, "the answer does not depend on the budget")

	require.LessOrEqual(t, small.windowPeak.Load(), int64(budget+oneRow), "no window counted more than its budget")
	require.Greater(t, large.windowPeak.Load(), int64(4*budget), "unbounded, the same read holds a month of details at once")
}

func TestAPayloadAggregationHoldsOneBoundedWindowAndCountsEachEventOnce(t *testing.T) {
	w := newBoundWarehouse(t)
	w.month(t)
	spec := business.AuditAggregationSpec{GroupBy: []string{"payload:group"}}
	read := orgScoped(business.AuditQuery{})

	const budget = 96 << 10
	small, large := w.reader(t, budget, 0), w.reader(t, 1<<40, 0)
	before := len(w.fake.Sessions())
	got, err := small.AggregateAuditEvents(context.Background(), read, spec)
	require.NoError(t, err)
	halved := len(w.fake.Sessions()) - before
	before = len(w.fake.Sessions())
	reference, err := large.AggregateAuditEvents(context.Background(), read, spec)
	require.NoError(t, err)
	require.Equal(t, reference, got, "the answer does not depend on the budget")
	require.Greater(t, halved, len(w.fake.Sessions())-before, "the bound costs reads, in windows halved to fit, never memory")

	counts := map[string]int64{}
	var total int64
	for _, bucket := range got {
		counts[bucket.Key] = bucket.Count
		total += bucket.Count
	}
	require.Equal(t, int64(1200), total, "1200 events, each delivered twice, counted once each")
	require.Equal(t, int64(172), counts["0"], "1200 events in seven groups: 172 or 171")
	require.Equal(t, int64(171), counts["6"])

	require.LessOrEqual(t, small.windowPeak.Load(), int64(budget+oneRow))
	require.Greater(t, large.windowPeak.Load(), int64(4*budget))
}

func TestAnExportHoldsOneBoundedWindowAndTheEventsItReturns(t *testing.T) {
	w := newBoundWarehouse(t)
	records := w.month(t)
	var want []string
	for _, record := range records {
		want = append(want, record.Entry.ID)
	}
	read := orgScoped(business.AuditQuery{})

	const budget = 96 << 10
	small, large := w.reader(t, budget, 0), w.reader(t, 1<<40, 0)
	got, err := small.ExportAuditEvents(context.Background(), read)
	require.NoError(t, err)
	require.Equal(t, want, entryIDs(got), "every event once, newest first, across every window")
	reference, err := large.ExportAuditEvents(context.Background(), read)
	require.NoError(t, err)
	require.Equal(t, reference, got, "the answer does not depend on the budget")
	require.NotNil(t, got[0].Payload, "with its payload")

	require.LessOrEqual(t, small.windowPeak.Load(), int64(budget+oneRow))
	require.Greater(t, large.windowPeak.Load(), int64(4*budget))
}

func TestAnExportGivesUpPastItsBoundInsteadOfHoldingTheHistory(t *testing.T) {
	w := newBoundWarehouse(t)
	w.month(t)
	read := orgScoped(business.AuditQuery{})

	// The month is about 1.2 MB of events; the bound is a tenth of it.
	bounded := w.reader(t, 96<<10, 128<<10)
	before := len(w.fake.Sessions())
	_, err := bounded.ExportAuditEvents(context.Background(), read)
	require.ErrorIs(t, err, business.ErrAuditExportTooLarge)
	stopped := len(w.fake.Sessions()) - before

	before = len(w.fake.Sessions())
	_, err = w.reader(t, 96<<10, 0).ExportAuditEvents(context.Background(), read)
	require.NoError(t, err)
	require.Less(t, stopped, len(w.fake.Sessions())-before, "it stopped reading as soon as the bound was passed")

	// A narrower export fits the same bound.
	recent := boundNow.Add(-24 * time.Hour)
	events, err := bounded.ExportAuditEvents(context.Background(), orgScoped(business.AuditQuery{From: &recent}))
	require.NoError(t, err)
	require.NotEmpty(t, events)
}

func TestAWindowThatCannotBeHalvedFailsInsteadOfOverflowing(t *testing.T) {
	w := newBoundWarehouse(t)
	at := boundNow.Add(-time.Hour)
	pad := strings.Repeat("p", 900)
	var records []business.AuditRecord
	for i := 1; i <= 80; i++ {
		records = append(records, boundEvent(t, i, at, map[string]any{"group": 1, "pad": pad}))
	}
	w.append(t, records...)

	reader := w.reader(t, 32<<10, 0)
	_, err := reader.AggregateAuditEvents(context.Background(), orgScoped(business.AuditQuery{}), business.AuditAggregationSpec{GroupBy: []string{"payload:group"}})
	require.ErrorIs(t, err, ErrReadTooDense, "eighty events at one microsecond are more than a window holds")
	require.LessOrEqual(t, reader.windowPeak.Load(), int64(32<<10+oneRow))
}

func TestHalvingAWindowCoversItExactly(t *testing.T) {
	lo := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	hi := lo.Add(48 * time.Hour)
	ceiling := lo.Add(1000 * time.Hour)
	for name, w := range map[string]window{
		"closed":    {lo: &lo, hi: &hi},
		"exclusive": {lo: &lo, hi: &hi, hiExclusive: true},
		"open":      {lo: &lo},
	} {
		newer, older, ok := w.split(ceiling)
		require.True(t, ok, name)
		require.Equal(t, w.hi, newer.hi, "%s: the newer half keeps the upper bound", name)
		require.Equal(t, w.hiExclusive, newer.hiExclusive, name)
		require.Equal(t, w.lo, older.lo, "%s: the older half keeps the lower bound", name)
		require.True(t, older.hiExclusive, "%s: the older half stops short of the middle", name)
		require.True(t, newer.lo.Equal(*older.hi), "%s: the halves meet at the middle, which only the newer holds", name)
		require.True(t, newer.lo.After(*w.lo) && newer.lo.Before(ceiling), name)
	}

	mid, _, _ := window{lo: &lo, hi: &hi}.split(ceiling)
	require.True(t, mid.lo.Equal(lo.Add(24*time.Hour)))
	_, _, ok := window{lo: &lo}.split(lo)
	require.False(t, ok, "no span below the ceiling")
	two := lo.Add(time.Microsecond)
	_, _, ok = window{lo: &lo, hi: &two}.split(ceiling)
	require.False(t, ok, "a microsecond is the least a window spans")
	_, _, ok = window{hi: &hi}.split(ceiling)
	require.False(t, ok, "an open lower bound has no middle")

	// A window that spans centuries is halved at its middle too, not at the
	// 146 years a saturated duration would give.
	earliest := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	newerHalf, olderHalf, ok := window{lo: &earliest, hi: &today, hiExclusive: true}.split(today)
	require.True(t, ok)
	require.True(t, newerHalf.lo.After(time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)) && newerHalf.lo.Before(time.Date(1100, 1, 1, 0, 0, 0, 0, time.UTC)),
		"the middle of the years 1 and 2026: %s", newerHalf.lo)
	require.True(t, olderHalf.hi.Equal(*newerHalf.lo))

	// And walking down it reaches every microsecond of the window exactly once.
	spans := []window{{lo: &lo, hi: &hi}}
	var leaves []window
	for len(spans) > 0 && len(leaves) < 64 {
		w := spans[0]
		spans = spans[1:]
		newer, older, ok := w.split(ceiling)
		if !ok || w.hi.Sub(*w.lo) < time.Hour {
			leaves = append(leaves, w)
			continue
		}
		spans = append(spans, newer, older)
	}
	slices.SortFunc(leaves, func(a, b window) int { return a.lo.Compare(*b.lo) })
	require.True(t, leaves[0].lo.Equal(lo))
	for i := 1; i < len(leaves); i++ {
		require.True(t, leaves[i-1].hi.Equal(*leaves[i].lo), "adjacent leaves meet")
		require.True(t, leaves[i-1].hiExclusive, "and the older leaf excludes the instant they meet at")
	}
	require.True(t, leaves[len(leaves)-1].hi.Equal(hi))
}
