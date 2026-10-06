package auditeval

import (
	"fmt"
	"math"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// An aggregation keeps state across every window a store reads: a bucket per
// distinct key, a float per percentile input, a string per distinct value. The
// window budget of a store does not count it, so the aggregator does, and gives
// up rather than return an answer it could not finish.

// offerWindow offers the aggregator one window of a store's events: n events
// numbered from first, all of one type (so one bucket unless a spec groups by
// a payload key), each with its own number and its own text.
func offerWindow(aggregator *Aggregator, first, n int) error {
	at := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	for i := first; i < first+n; i++ {
		event := &Event{
			Entry:      business.AuditEntry{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), EventType: business.EventDocumentRead, CreatedAt: at.Add(time.Duration(i) * time.Second)},
			Details:    fmt.Sprintf(`{"n":%d,"uniq":"value-%06d","group":"g-%06d"}`, i, i, i),
			HasDetails: true,
		}
		if err := aggregator.Add(event); err != nil {
			return err
		}
	}
	return nil
}

// walk offers windows of 40 events until the aggregator gives up or it has
// offered windows of them, and reports how many it completed.
func walk(aggregator *Aggregator, windows int) (completed int, err error) {
	for ; completed < windows; completed++ {
		if err := offerWindow(aggregator, completed*40, 40); err != nil {
			return completed, err
		}
	}
	return completed, nil
}

func TestAnAggregationOverManyWindowsGivesUpPastItsStateBound(t *testing.T) {
	const budget = 16 << 10
	for name, spec := range map[string]business.AuditAggregationSpec{
		"a percentile":     {Metrics: []business.AuditMetric{{Op: "percentile", Field: "payload:n", Percentile: 0.5}}},
		"a distinct count": {Metrics: []business.AuditMetric{{Op: "count_distinct", Field: "payload:uniq"}}},
		"both, in one bucket": {Metrics: []business.AuditMetric{
			{Op: "percentile", Field: "payload:n", Percentile: 0.9},
			{Op: "count_distinct", Field: "payload:uniq"},
		}},
		"a bucket per key": {GroupBy: []string{"payload:group"}},
	} {
		aggregator, err := NewAggregator(spec, nil, budget)
		require.NoError(t, err, name)
		completed, err := walk(aggregator, 500)
		require.ErrorIs(t, err, business.ErrAuditAggregateTooLarge, "%s: 20000 events cannot fit %d bytes", name, budget)
		require.Positive(t, completed, "%s: the first window fits; it is the windows together that do not", name)
		require.Less(t, completed, 500, "%s: it gave up as soon as the state passed the bound", name)
		require.LessOrEqual(t, aggregator.state.Held(), int64(budget)+BucketBytes([]string{"g-000000"}, 2)+SampleBytes(1)+distinctEntryBytes+16,
			"%s: and kept no more than the one entry that passed it", name)
	}
}

func TestTheStateBoundCountsWhatIsKeptNotWhatIsOffered(t *testing.T) {
	// One bucket and one remembered value, whatever the windows: the same 20000
	// events keep a counter.
	spec := business.AuditAggregationSpec{Metrics: []business.AuditMetric{{Op: "count"}, {Op: "count_distinct", Field: "event_type", Alias: "types"}}}
	aggregator, err := NewAggregator(spec, nil, 4<<10)
	require.NoError(t, err)
	completed, err := walk(aggregator, 500)
	require.NoError(t, err)
	require.Equal(t, 500, completed)
	held := aggregator.state.Held()
	require.Less(t, held, int64(1<<10), "one bucket and one remembered value")
	buckets := aggregator.Buckets()
	require.Len(t, buckets, 1)
	require.Equal(t, int64(20000), buckets[0].Count)
	require.Equal(t, 1.0, buckets[0].Metrics["types"])

	// A value seen again costs nothing.
	require.NoError(t, offerWindow(aggregator, 0, 40))
	require.Equal(t, held, aggregator.state.Held(), "the same events again")
}

func TestAnAggregationUnderTheDefaultBoundMatchesOneWithoutAnyBound(t *testing.T) {
	spec := business.AuditAggregationSpec{
		GroupBy: []string{"event_type"},
		Metrics: []business.AuditMetric{
			{Op: "percentile", Field: "payload:n", Percentile: 0.5, Alias: "p50"},
			{Op: "percentile", Field: "payload:n", Percentile: 0.99, Alias: "p99"},
			{Op: "count_distinct", Field: "payload:uniq", Alias: "distinct"},
			{Op: "sum", Field: "payload:n", Alias: "total"},
		},
	}
	bounded, err := NewAggregator(spec, nil, 0)
	require.NoError(t, err)
	unbounded, err := NewAggregator(spec, nil, math.MaxInt64)
	require.NoError(t, err)
	for _, aggregator := range []*Aggregator{bounded, unbounded} {
		completed, err := walk(aggregator, 250)
		require.NoError(t, err)
		require.Equal(t, 250, completed)
	}
	got, want := bounded.Buckets(), unbounded.Buckets()
	require.Equal(t, want, got, "10000 events are well inside the default bound")
	require.Len(t, got, 1)
	require.Equal(t, 4999.5, got[0].Metrics["p50"])
	require.Equal(t, 9999*0.99, got[0].Metrics["p99"])
	require.Equal(t, 10000.0, got[0].Metrics["distinct"])
	require.Equal(t, 9999.0*10000/2, got[0].Metrics["total"])
}

func TestTheStateBoundDefaultsAndRefusesANegativeOne(t *testing.T) {
	_, err := NewAggregator(business.AuditAggregationSpec{}, nil, -1)
	require.ErrorContains(t, err, "cannot be negative")

	budget, err := NewStateBudget(0)
	require.NoError(t, err)
	require.NoError(t, budget.Take(business.AuditAggregateMaxBytes))
	require.ErrorIs(t, budget.Take(1), business.ErrAuditAggregateTooLarge, "zero is business.AuditAggregateMaxBytes")

	explicit, err := NewStateBudget(100)
	require.NoError(t, err)
	require.NoError(t, explicit.Take(100))
	err = explicit.Take(1)
	require.ErrorIs(t, err, business.ErrAuditAggregateTooLarge)
	require.ErrorContains(t, err, "narrow it", "the refusal says how to get past it")
}

func TestABucketsStateCountsItsKeysAndMetrics(t *testing.T) {
	require.Greater(t, BucketBytes([]string{"a", "b"}, 3), BucketBytes([]string{"a"}, 3))
	require.Greater(t, BucketBytes([]string{"a"}, 3), BucketBytes([]string{"a"}, 1))
	require.Equal(t, BucketBytes([]string{"x"}, 0)+2*100, BucketBytes([]string{string(make([]byte, 101))}, 0),
		"a key's text is held twice, in the bucket and in its identity")
	require.Equal(t, int64(8000), SampleBytes(1000))
}
