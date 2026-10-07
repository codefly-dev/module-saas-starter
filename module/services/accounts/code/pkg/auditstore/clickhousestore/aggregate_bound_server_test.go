package clickhousestore

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"accounts/pkg/auditstore/auditeval"
	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// meteredConn totals what an aggregation's statements return to the service:
// the percentile inputs and the bytes of the keys the driver hands to Scan. It is
// what the store would hold, seen from the service's side of the connection.
type meteredConn struct {
	Conn
	mu       sync.Mutex
	inputs   int
	keyBytes int
}

func (c *meteredConn) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	rows, err := c.Conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return &meteredRows{Rows: rows, conn: c}, nil
}

func (c *meteredConn) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs, c.keyBytes = 0, 0
}

func (c *meteredConn) returned() (inputs, keyBytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputs, c.keyBytes
}

type meteredRows struct {
	driver.Rows
	conn *meteredConn
}

func (r *meteredRows) Scan(dest ...any) error {
	err := r.Rows.Scan(dest...)
	r.conn.mu.Lock()
	defer r.conn.mu.Unlock()
	for _, d := range dest {
		switch d := d.(type) {
		case *[]float64:
			r.conn.inputs += len(*d)
		case *string:
			r.conn.keyBytes += len(*d)
		}
	}
	return err
}

// stateOf is what the service counts for an answer, by the arithmetic of the
// read it came from: each bucket, and each percentile input at what it costs the
// service while it is read.
func stateOf(buckets []business.AuditAggregateBucket, spec business.AuditAggregationSpec) int64 {
	var held int64
	for _, bucket := range buckets {
		held += auditeval.BucketBytes(bucket.Keys, len(spec.Metrics))
		for _, metric := range spec.Metrics {
			if metric.Op == "percentile" {
				held += bucket.Samples[metric.ResolvedAlias()] * scannedSampleBytes
			}
		}
	}
	return held
}

func TestServerAPercentileReadNeverReturnsMoreThanTheBound(t *testing.T) {
	conn, database := serverDatabase(t)
	ctx := context.Background()
	meter := &meteredConn{Conn: conn}
	org, actor := uuid.NewString(), uuid.NewString()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	f := &fixture{org: org}
	const events, groups = 1000, 20
	for i := range events {
		f.records = append(f.records, newRecord(t, org, actor, business.EventDocumentRead, "doc", base.Add(-time.Duration(i)*time.Second),
			business.RetentionContent, map[string]any{"n": i, "group": fmt.Sprintf("group-%02d", i%groups), "key": fmt.Sprintf("key-%04d", i)}))
	}
	setup := serverStore(t, conn, database)
	require.NoError(t, setup.Ensure(ctx))
	appendBatch(t, setup, testDeployment, f.records...)
	read := business.AuditRead{Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{OrgID: org}}

	storeAt := func(limit int64) *Store {
		store, err := New(meter, Config{Database: database, DeploymentID: testDeployment, EventsRetention: 2555 * 24 * time.Hour,
			ContentDetailRetention: 30 * 24 * time.Hour, AggregateMaxBytes: limit})
		require.NoError(t, err)
		return store
	}
	percentiles := []business.AuditMetric{
		{Op: "percentile", Field: "payload:n", Percentile: 0.5, Alias: "p50"},
		{Op: "percentile", Field: "payload:n", Percentile: 0.99, Alias: "p99"},
	}

	for name, tc := range map[string]struct {
		spec business.AuditAggregationSpec
		// inputs is how many percentile inputs a read of the whole history holds.
		inputs int
	}{
		"one group of every event":            {business.AuditAggregationSpec{Metrics: percentiles[:1]}, events},
		"twenty groups, two percentiles each": {business.AuditAggregationSpec{GroupBy: []string{"payload:group"}, Metrics: percentiles}, 2 * events},
		"a group of its own for every event":  {business.AuditAggregationSpec{GroupBy: []string{"payload:key"}, Metrics: percentiles[:1]}, events},
		"keys alone, a group for every event": {business.AuditAggregationSpec{GroupBy: []string{"payload:key"}}, 0},
	} {
		want := f.referenceAggregate(t, read, tc.spec)
		exact := stateOf(want, tc.spec)

		// A budget of exactly what the service counts for the answer returns it,
		// as the reference semantics answer it, and the server returned every
		// input to do so, and no more.
		meter.reset()
		got, err := storeAt(exact).AggregateAuditEvents(ctx, read, tc.spec)
		require.NoError(t, err, name)
		require.Equal(t, want, got, name)
		inputs, _ := meter.returned()
		require.Equal(t, tc.inputs, inputs, "%s: the inputs the answer is made of", name)
		require.LessOrEqual(t, int64(inputs)*scannedSampleBytes, exact, "%s: and they fit the budget", name)

		// One byte less is refused, and the server returned neither an input nor
		// a key: the statement withheld them, so neither the driver nor the
		// service ever held what the read could not keep.
		meter.reset()
		_, err = storeAt(exact-1).AggregateAuditEvents(ctx, read, tc.spec)
		require.ErrorIs(t, err, business.ErrAuditAggregateTooLarge, name)
		inputs, keyBytes := meter.returned()
		require.Zero(t, inputs, "%s: no input left the server", name)
		require.Zero(t, keyBytes, "%s: no key left the server", name)

		// A budget far under it, however the history is grouped.
		meter.reset()
		_, err = storeAt(4<<10).AggregateAuditEvents(ctx, read, tc.spec)
		require.ErrorIs(t, err, business.ErrAuditAggregateTooLarge, name)
		inputs, keyBytes = meter.returned()
		require.Zero(t, inputs+keyBytes, "%s: nothing left the server", name)
	}

	// One group past the most inputs a budget holds is refused for its count, and
	// the server never builds it into an array it returns: of the 1000 inputs
	// a 4 KiB budget holds 256.
	small := storeAt(4 << 10)
	require.Equal(t, 256, small.maxSamples())
	meter.reset()
	_, err := small.AggregateAuditEvents(ctx, read, business.AuditAggregationSpec{Metrics: percentiles[:1]})
	require.ErrorIs(t, err, business.ErrAuditAggregateTooLarge)
	inputs, _ := meter.returned()
	require.Zero(t, inputs)

	// Many groups, each far under the most inputs a budget holds (50 of 1024),
	// are refused when their total passes it.
	medium := storeAt(16 << 10)
	require.Equal(t, 1024, medium.maxSamples())
	meter.reset()
	spec := business.AuditAggregationSpec{GroupBy: []string{"payload:group"}, Metrics: percentiles[:1]}
	_, err = medium.AggregateAuditEvents(ctx, read, spec)
	require.ErrorIs(t, err, business.ErrAuditAggregateTooLarge, "20 groups of 50 inputs, 1000 inputs at 16 bytes and 20 buckets, in 16 KiB")
	inputs, _ = meter.returned()
	require.Zero(t, inputs)

	// An inexact payload (an integer beyond 64 bits, which the server's JSON
	// parser cannot read) makes the service evaluate the aggregation itself,
	// whatever the statement withheld on the way: the answer is the reference's.
	odd := newRecord(t, org, actor, business.EventDocumentRead, "doc", base.Add(time.Second),
		business.RetentionContent, map[string]any{"n": 7, "big": jsonNumber("123456789012345678901234567890")})
	appendBatch(t, setup, testDeployment, odd)
	f.records = append(f.records, odd)
	inService := 0
	roomy := storeAt(0)
	roomy.inService = func() { inService++ }
	spec = business.AuditAggregationSpec{GroupBy: []string{"payload:big"}, Metrics: percentiles[:1]}
	got, err := roomy.AggregateAuditEvents(ctx, read, spec)
	require.NoError(t, err)
	require.Equal(t, f.referenceAggregate(t, read, spec), got)
	require.Equal(t, 1, inService, "the service evaluated it")
}
