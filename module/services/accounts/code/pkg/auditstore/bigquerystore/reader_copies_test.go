package bigquerystore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auditstore/auditeval"
	"accounts/pkg/auditstore/bigqueryfake"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// The relay delivers at least once, so the events and details tables may hold
// an event, and its details, many times over. Every copy of an event shares its
// occurrence, so all of them land in one window and no narrower window can shed
// them. What a window counts against its budget is what it keeps, once per
// event, or a window whose unique data fits would be unreadable for ever.

// deliver appends records times times, as a relay redelivering a batch would.
func (w *boundWarehouse) deliver(t *testing.T, times int, records ...business.AuditRecord) {
	t.Helper()
	for i := 0; i < times; i++ {
		w.append(t, records...)
	}
}

// insertDetails writes one more details-table copy of a record's event, with
// text as its details and the hash of text.
func (w *boundWarehouse) insertDetails(t *testing.T, record business.AuditRecord, text string) {
	t.Helper()
	row := DetailRow(boundDeployment, record)
	row.Values["details"] = text
	row.Values["details_sha256"] = business.AuditDetailsSHA256(text)
	require.NoError(t, w.fake.Insert(bigqueryfake.TablePath(boundProject, boundDataset, DetailsTable), row.Values))
}

// instant is n content-class events at one instant, each with about 900 bytes
// of details; the odd ones are "rare", and each is in a "group" of two.
func instant(t *testing.T, n int, at time.Time) []business.AuditRecord {
	t.Helper()
	pad := strings.Repeat("p", 900)
	var records []business.AuditRecord
	for i := 1; i <= n; i++ {
		payload := map[string]any{"group": i % 2, "pad": pad}
		if i%2 == 1 {
			payload["rare"] = true
		}
		records = append(records, boundEvent(t, i, at, payload))
	}
	return records
}

// detailsCost is what a window counts for the details of records, once each.
func detailsCost(records ...business.AuditRecord) int64 {
	var cost int64
	for _, record := range records {
		cost += int64(detailRowOverheadBytes + len(record.Entry.ID) + len(record.DetailsSHA256) + len(record.Details))
	}
	return cost
}

// readKinds are the reads that hold a window in memory, each returning its
// answer in a form two readers can be compared by.
var readKinds = map[string]func(*testing.T, *Reader) (any, error){
	"a payload-filtered list": func(t *testing.T, reader *Reader) (any, error) {
		var ids []string
		q := business.AuditQuery{PageSize: 2, PayloadContains: map[string]any{"rare": true}}
		for {
			entries, next, err := reader.ListAuditEvents(context.Background(), orgScoped(q))
			if err != nil {
				return nil, err
			}
			ids = append(ids, entryIDs(entries)...)
			if next == "" {
				return ids, nil
			}
			q.PageToken = next
		}
	},
	"a payload aggregation": func(t *testing.T, reader *Reader) (any, error) {
		return reader.AggregateAuditEvents(context.Background(), orgScoped(business.AuditQuery{}),
			business.AuditAggregationSpec{GroupBy: []string{"payload:group"}})
	},
	"an aggregation of envelopes": func(t *testing.T, reader *Reader) (any, error) {
		return reader.AggregateAuditEvents(context.Background(), orgScoped(business.AuditQuery{}), business.AuditAggregationSpec{})
	},
	"an export": func(t *testing.T, reader *Reader) (any, error) {
		return reader.ExportAuditEvents(context.Background(), orgScoped(business.AuditQuery{}))
	},
}

func TestAWindowOfRedeliveredCopiesIsCountedOncePerEventNotOncePerCopy(t *testing.T) {
	at := boundNow.Add(-time.Hour)
	records := instant(t, 6, at)
	once, many := newBoundWarehouse(t), newBoundWarehouse(t)
	once.deliver(t, 1, records...)
	many.deliver(t, 60, records...)
	require.Len(t, many.fake.Rows(bigqueryfake.TablePath(boundProject, boundDataset, EventsTable)), 360, "sixty copies of each of six events")
	require.Len(t, many.fake.Rows(bigqueryfake.TablePath(boundProject, boundDataset, DetailsTable)), 360)

	// Six events and their details are about 17 KB; 360 copies would be a megabyte.
	const budget = 32 << 10
	for name, read := range readKinds {
		reference, copies := once.reader(t, budget, 0), many.reader(t, budget, 0)
		want, err := read(t, reference)
		require.NoError(t, err, name)
		got, err := read(t, copies)
		require.NoError(t, err, "%s: the unique data fits the window, however many copies the tables hold", name)
		require.Equal(t, want, got, "%s: the same answer as a single delivery", name)
		require.Equal(t, reference.windowPeak.Load(), copies.windowPeak.Load(), "%s: counted for what is kept, not for what was delivered", name)
		require.Greater(t, copies.windowPeak.Load(), int64(0), name)
		require.LessOrEqual(t, copies.windowPeak.Load(), int64(budget), name)
	}

	// And the answers are the events, once each.
	exported, err := many.reader(t, budget, 0).ExportAuditEvents(context.Background(), orgScoped(business.AuditQuery{}))
	require.NoError(t, err)
	require.Len(t, exported, 6)
	require.NotNil(t, exported[0].Payload)
	buckets, err := many.reader(t, budget, 0).AggregateAuditEvents(context.Background(), orgScoped(business.AuditQuery{}),
		business.AuditAggregationSpec{GroupBy: []string{"payload:group"}})
	require.NoError(t, err)
	var total int64
	for _, bucket := range buckets {
		total += bucket.Count
	}
	require.Equal(t, int64(6), total)
}

// The window counts exactly what it keeps: the budget that holds the unique
// details holds them whatever the copies, and one byte less does not.
func TestAWindowBudgetThatHoldsTheUniqueDetailsHoldsThemWhateverTheCopies(t *testing.T) {
	at := boundNow.Add(-time.Hour)
	records := instant(t, 6, at)
	w := newBoundWarehouse(t)
	w.deliver(t, 40, records...)
	cost := detailsCost(records...)
	q := business.AuditQuery{PayloadContains: map[string]any{"rare": true}}

	fits := w.reader(t, cost, 0)
	entries, _, err := fits.ListAuditEvents(context.Background(), orgScoped(q))
	require.NoError(t, err)
	require.Len(t, entries, 3)
	require.Equal(t, cost, fits.windowPeak.Load())

	short := w.reader(t, cost-1, 0)
	_, _, err = short.ListAuditEvents(context.Background(), orgScoped(q))
	require.ErrorIs(t, err, ErrReadTooDense, "one byte short of the unique details, and the instant cannot be split")
}

func TestUniqueDataOverTheBudgetStillFailsHoweverItIsDelivered(t *testing.T) {
	at := boundNow.Add(-time.Hour)
	records := instant(t, 80, at)
	const budget = 32 << 10
	for _, deliveries := range []int{1, 3} {
		w := newBoundWarehouse(t)
		w.deliver(t, deliveries, records...)
		for name, read := range readKinds {
			reader := w.reader(t, budget, 0)
			_, err := read(t, reader)
			require.ErrorIs(t, err, ErrReadTooDense, "%s, delivered %d times: eighty events at one instant are more than a window holds", name, deliveries)
			require.LessOrEqual(t, reader.windowPeak.Load(), int64(budget+oneRow), name)
		}
	}
}

// Two copies of one event that carry different hashes disagree — the history
// verification reports it. A read joins the event to the copy under the hash
// its own row carries, as the ClickHouse store does, so the answer does not
// depend on the order the copies arrive in, a copy no event names is never
// shown, and each distinct copy kept is counted once.
func TestConflictingDetailsAreJoinedByHashAndEachDistinctCopyCountedOnce(t *testing.T) {
	at := boundNow.Add(-time.Hour)
	good := boundEvent(t, 1, at, map[string]any{"mark": "good"})
	other := boundEvent(t, 2, at, map[string]any{"mark": "other"})
	const forged = `{"mark": "forged"}`
	const budget = 32 << 10
	clean := newBoundWarehouse(t)
	clean.deliver(t, 1, good, other)
	cleanReader := clean.reader(t, budget, 0)
	_, _, err := cleanReader.ListAuditEvents(context.Background(), orgScoped(business.AuditQuery{PayloadContains: map[string]any{"mark": "good"}}))
	require.NoError(t, err)
	forgedCost := int64(detailRowOverheadBytes + len(good.Entry.ID) + len(business.AuditDetailsSHA256(forged)) + len(forged))

	for name, forgedFirst := range map[string]bool{"the conflicting copy first": true, "the conflicting copy last": false} {
		for _, deliveries := range []int{1, 25} {
			w := newBoundWarehouse(t)
			if forgedFirst {
				for i := 0; i < deliveries; i++ {
					w.insertDetails(t, good, forged)
				}
			}
			w.deliver(t, deliveries, good, other)
			if !forgedFirst {
				for i := 0; i < deliveries; i++ {
					w.insertDetails(t, good, forged)
				}
			}
			label := fmt.Sprintf("%s, delivered %d times", name, deliveries)

			// A filter on the details the event's own row names finds it, with those details.
			reader := w.reader(t, budget, 0)
			entries, _, err := reader.ListAuditEvents(context.Background(), orgScoped(business.AuditQuery{PayloadContains: map[string]any{"mark": "good"}}))
			require.NoError(t, err, label)
			require.Equal(t, []string{good.Entry.ID}, entryIDs(entries), label)
			require.Equal(t, map[string]any{"mark": "good"}, entries[0].Payload, label)
			require.Equal(t, cleanReader.windowPeak.Load()+forgedCost, reader.windowPeak.Load(),
				"%s: the conflicting copy is kept, so it is counted, once however often it was delivered", label)

			// The copy no event row names is never shown.
			entries, _, err = w.reader(t, budget, 0).ListAuditEvents(context.Background(), orgScoped(business.AuditQuery{PayloadContains: map[string]any{"mark": "forged"}}))
			require.NoError(t, err, label)
			require.Empty(t, entries, label)

			// Without a filter the page's details are joined afterwards, by the same rule.
			entries, _, err = w.reader(t, budget, 0).ListAuditEvents(context.Background(), orgScoped(business.AuditQuery{}))
			require.NoError(t, err, label)
			require.Len(t, entries, 2, label)
			marks := map[string]any{}
			for _, entry := range entries {
				marks[entry.ID] = entry.Payload["mark"]
			}
			require.Equal(t, map[string]any{good.Entry.ID: "good", other.Entry.ID: "other"}, marks, label)

			// The windows an aggregation and an export hold agree.
			buckets, err := w.reader(t, budget, 0).AggregateAuditEvents(context.Background(), orgScoped(business.AuditQuery{}),
				business.AuditAggregationSpec{GroupBy: []string{"payload:mark"}})
			require.NoError(t, err, label)
			counts := map[string]int64{}
			for _, bucket := range buckets {
				counts[bucket.Key] = bucket.Count
			}
			require.Equal(t, map[string]int64{"good": 1, "other": 1}, counts, label)
			exported, err := w.reader(t, budget, 0).ExportAuditEvents(context.Background(), orgScoped(business.AuditQuery{}))
			require.NoError(t, err, label)
			require.Len(t, exported, 2, label)
		}
	}
}

// A list page joins the details of its own events, which are a page's. What
// that read keeps is bounded by the page, not by the copies the table holds:
// of a flood of copies under other hashes it keeps none.
func TestAPagesDetailsAreBoundedByThePageNotByTheCopiesOfItsEvents(t *testing.T) {
	at := boundNow.Add(-time.Hour)
	good := boundEvent(t, 1, at, map[string]any{"mark": "good"})
	w := newBoundWarehouse(t)
	w.deliver(t, 5, good)
	for i := 0; i < 200; i++ {
		w.insertDetails(t, good, fmt.Sprintf(`{"mark": "forged-%d"}`, i))
	}
	reader := w.reader(t, 0, 0)

	// The event as a list page holds it: its envelope and the hash of its details.
	page := []*auditeval.Event{{Entry: good.Entry, DetailsSHA256: good.DetailsSHA256}}
	details, err := reader.pageDetails(context.Background(), business.OrganizationAuditScope(boundOrg), page)
	require.NoError(t, err)
	require.Equal(t, map[detailKey]string{{eventID: good.Entry.ID, sha256: good.DetailsSHA256}: good.Details}, details,
		"the one copy the event names; the 204 others under the same id are skipped")

	require.NoError(t, reader.joinDetails(context.Background(), business.OrganizationAuditScope(boundOrg), page))
	require.True(t, page[0].HasDetails)
	require.Equal(t, good.Details, page[0].Details)
}
