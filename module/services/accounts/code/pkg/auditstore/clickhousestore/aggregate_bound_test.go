package clickhousestore

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"testing"

	"accounts/pkg/auditstore/auditeval"
	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"
)

// An aggregation in ClickHouse returns, per group, the percentile's inputs, and
// the service holds them while it takes the percentile. What it holds must be
// bounded by the aggregation's state budget before the driver decodes them, not
// only counted after, so the statement is pinned here and the service's side of
// it is pinned against rows a server could return.

const boundOrg = "22222222-2222-2222-2222-222222222222"

func boundRead() business.AuditRead {
	return business.AuditRead{Scope: business.OrganizationAuditScope(boundOrg), Query: business.AuditQuery{OrgID: boundOrg}}
}

func boundPlan(t *testing.T) plan {
	t.Helper()
	read := boundRead()
	m, err := auditeval.NewMatcher(read)
	require.NoError(t, err)
	return newPlan(m, read.Query)
}

func boundStore(t *testing.T, conn Conn, limit int64) *Store {
	t.Helper()
	cfg := validConfig()
	cfg.AggregateMaxBytes = limit
	store, err := New(conn, cfg)
	require.NoError(t, err)
	return store
}

var percentileByType = business.AuditAggregationSpec{
	GroupBy: []string{"event_type"},
	Metrics: []business.AuditMetric{{Op: "percentile", Field: "payload:n", Percentile: 0.5, Alias: "p50"}},
}

// params maps each bound parameter's name to the text it is sent as.
func params(t *testing.T, args []any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, arg := range args {
		named, ok := arg.(driver.NamedValue)
		require.True(t, ok)
		if text, ok := named.Value.(string); ok {
			out[named.Name] = text
		}
	}
	return out
}

func TestAPercentileStatementCapsItsArraysAndGatesItsRows(t *testing.T) {
	const limit = 64 << 10
	store := boundStore(t, panicConn{}, limit)
	agg, ok, err := store.buildAggregation(boundPlan(t), percentileByType, nil)
	require.NoError(t, err)
	require.True(t, ok)
	bound := params(t, agg.args)

	// Each array is cut to one more than the budget could hold, at what each
	// input costs the service: past that a group is past the budget by its count
	// alone, and no array the server builds or ships is any longer.
	const maxSamples = limit / 16
	require.Equal(t, maxSamples, store.maxSamples())
	sized := regexp.MustCompile(`groupArray\(\{(p\d+):UInt64\}\)\(`).FindStringSubmatch(agg.statement)
	require.NotNil(t, sized, "the percentile's array has a size cap: %s", agg.statement)
	require.Equal(t, strconv.Itoa(maxSamples+1), bound[sized[1]])

	// The rows are gated on the read's whole state, as the service counts it:
	// each group's bucket, key bytes and inputs, summed over every group, against
	// the budget.
	require.Contains(t, agg.statement, "sum(")
	require.Contains(t, agg.statement, ") OVER ()")
	require.Contains(t, agg.statement, "emptyArrayFloat64()")
	require.Contains(t, agg.statement, "length(d0)")
	counted := map[string]bool{}
	for _, text := range bound {
		counted[text] = true
	}
	for name, want := range map[string]int64{
		"the budget":         limit,
		"a bucket":           auditeval.BucketBytes([]string{""}, 1),
		"a byte of a key":    auditeval.BucketBytes([]string{"x"}, 0) - auditeval.BucketBytes([]string{""}, 0),
		"a percentile input": scannedSampleBytes,
	} {
		require.True(t, counted[strconv.FormatInt(want, 10)], "%s (%d) is bound to the statement", name, want)
	}

	// An aggregation of keys alone has no array and is gated on its keys.
	agg, ok, err = store.buildAggregation(boundPlan(t), business.AuditAggregationSpec{GroupBy: []string{"payload:k"}}, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotContains(t, agg.statement, "groupArray")
	require.Contains(t, agg.statement, ") OVER ()")
	require.Contains(t, agg.statement, "length(d0)")
}

// aggregateConn answers the aggregation statement with rows a server could
// return, in the order the statement selects them: the keys, the count, each
// metric, each metric's sample count, the inexact total, and whether the
// statement withheld its rows for the budget.
type aggregateConn struct {
	Conn
	statements []string
	rows       [][]any
	served     int
}

func (c *aggregateConn) Query(_ context.Context, query string, _ ...any) (driver.Rows, error) {
	c.statements = append(c.statements, query)
	return &aggregateRows{conn: c}, nil
}

type aggregateRows struct {
	driver.Rows
	conn *aggregateConn
	next int
}

func (r *aggregateRows) Next() bool {
	r.next++
	return r.next <= len(r.conn.rows)
}

func (r *aggregateRows) Scan(dest ...any) error {
	r.conn.served++
	row := r.conn.rows[r.next-1]
	if len(dest) != len(row) {
		return fmt.Errorf("aggregateRows: %d destinations for %d columns", len(dest), len(row))
	}
	for i, d := range dest {
		switch d := d.(type) {
		case *string:
			*d = row[i].(string)
		case *uint64:
			*d = row[i].(uint64)
		case *uint8:
			*d = row[i].(uint8)
		case *float64:
			*d = row[i].(float64)
		case *[]float64:
			*d = append([]float64(nil), row[i].([]float64)...)
		default:
			return fmt.Errorf("aggregateRows: unsupported destination %T", d)
		}
	}
	return nil
}

func (r *aggregateRows) Err() error   { return nil }
func (r *aggregateRows) Close() error { return nil }

// inputs is n percentile inputs, 1 to n.
func inputs(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(i + 1)
	}
	return out
}

// group is the row of one group of percentileByType: a key, its count, its
// inputs and their number, and the two gate columns.
func group(key string, samples []float64, inexact uint64, over uint8) []any {
	return []any{key, uint64(len(samples)), samples, uint64(len(samples)), inexact, over}
}

func TestAReadTheStatementWithheldIsRefusedFromItsFirstRow(t *testing.T) {
	// What a server returns for a read past the budget: the keys and the arrays
	// empty, the counts exact, the gate set on every row. The service refuses
	// on the first, without reading arrays that are not there, and without
	// falling back to evaluating the events itself.
	conn := &aggregateConn{rows: [][]any{
		{"", uint64(9_000_000), []float64(nil), uint64(9_000_000), uint64(0), uint8(1)},
		{"", uint64(1), []float64(nil), uint64(1), uint64(0), uint8(1)},
	}}
	store := boundStore(t, conn, 64<<10)
	_, err := store.AggregateAuditEvents(context.Background(), boundRead(), percentileByType)
	require.ErrorIs(t, err, business.ErrAuditAggregateTooLarge)
	require.ErrorContains(t, err, "the bound is 65536 bytes")
	require.Equal(t, 1, conn.served, "it stopped at the first row")
	require.Len(t, conn.statements, 1, "and did not aggregate the events itself")
}

func TestAnInexactReadIsTheServicesToEvaluateWhateverTheBoundSays(t *testing.T) {
	// The statement withholds for an inexact row as for the budget; the service
	// then evaluates the aggregation over the events, under its own budget, and
	// does not refuse on the statement's account.
	conn := &aggregateConn{rows: [][]any{{"", uint64(3), []float64(nil), uint64(3), uint64(1), uint8(1)}}}
	state, err := auditeval.NewStateBudget(64 << 10)
	require.NoError(t, err)
	store := boundStore(t, conn, 64<<10)
	buckets, exact, err := store.aggregateInClickHouse(context.Background(), boundPlan(t), percentileByType, nil, state)
	require.NoError(t, err)
	require.False(t, exact)
	require.Nil(t, buckets)
	require.Zero(t, state.Held())
}

func TestAGroupPastTheBoundIsRefusedEvenIfTheServerReturnsIt(t *testing.T) {
	// The service counts what it receives with the arithmetic the statement used,
	// so a statement that returned more than it should have is refused too: by
	// the count when one metric alone passes the budget, by the budget when the
	// groups together do.
	const limit = 16 << 10
	store := boundStore(t, panicConn{}, limit)
	maxSamples := store.maxSamples()
	for name, rows := range map[string][][]any{
		"one group past the most inputs the budget holds": {group("a", inputs(maxSamples+1), 0, 0)},
		"a count that overflows what it is multiplied by": {{"a", uint64(1), []float64(nil), uint64(1 << 62), uint64(0), uint8(0)}},
	} {
		conn := &aggregateConn{rows: rows}
		store.conn = conn
		state, err := auditeval.NewStateBudget(limit)
		require.NoError(t, err)
		_, _, err = store.aggregateInClickHouse(context.Background(), boundPlan(t), percentileByType, nil, state)
		require.ErrorIs(t, err, business.ErrAuditAggregateTooLarge, name)
		require.Zero(t, state.Held(), "%s: refused by the count, before any input is counted or sorted", name)
	}

	// A row whose arrays are not the inputs its counts say it has is an error,
	// never a percentile of what is there.
	for name, row := range map[string][]any{
		"withheld arrays, no gate": {"a", uint64(5), []float64(nil), uint64(5), uint64(0), uint8(0)},
		"a truncated array":        {"a", uint64(5), inputs(4), uint64(5), uint64(0), uint8(0)},
	} {
		store.conn = &aggregateConn{rows: [][]any{row}}
		state, err := auditeval.NewStateBudget(limit)
		require.NoError(t, err)
		_, _, err = store.aggregateInClickHouse(context.Background(), boundPlan(t), percentileByType, nil, state)
		require.ErrorContains(t, err, "percentile inputs returned", name)
		require.NotErrorIs(t, err, business.ErrAuditAggregateTooLarge, name)
	}
}

func TestManyGroupsUnderThePerGroupBoundAreBoundedInTotal(t *testing.T) {
	const (
		limit  = 16 << 10
		groups = 30
		each   = 100
	)
	store := boundStore(t, panicConn{}, limit)
	require.Less(t, each, store.maxSamples(), "every group is far under the most inputs one metric may have")
	rows := make([][]any, groups)
	for i := range rows {
		rows[i] = group(fmt.Sprintf("group-%02d", i), inputs(each), 0, 0)
	}
	one := auditeval.BucketBytes([]string{"group-00"}, 1) + auditeval.SampleBytes(each)*2

	// Handed every group, as a server that did not gate would, the service
	// refuses once their total passes the budget, and counts no more than the
	// group that passed it.
	conn := &aggregateConn{rows: rows}
	store.conn = conn
	state, err := auditeval.NewStateBudget(limit)
	require.NoError(t, err)
	_, _, err = store.aggregateInClickHouse(context.Background(), boundPlan(t), percentileByType, nil, state)
	require.ErrorIs(t, err, business.ErrAuditAggregateTooLarge)
	fits := int(limit / one)
	require.Equal(t, fits+1, conn.served, "it stopped at the group that passed the bound")
	require.Less(t, conn.served, groups)
	require.Equal(t, int64(fits+1)*one, state.Held())

	// The same groups, fewer of them, are answered, and every input is counted:
	// the bucket's own, and what each of the inputs costs while it is read.
	conn = &aggregateConn{rows: rows[:fits]}
	store.conn = conn
	state, err = auditeval.NewStateBudget(limit)
	require.NoError(t, err)
	buckets, exact, err := store.aggregateInClickHouse(context.Background(), boundPlan(t), percentileByType, nil, state)
	require.NoError(t, err)
	require.True(t, exact)
	require.Len(t, buckets, fits)
	require.Equal(t, int64(fits)*one, state.Held())
	require.Equal(t, 50.5, buckets[0].Metrics["p50"])
	require.EqualValues(t, each, buckets[0].Samples["p50"])
}
