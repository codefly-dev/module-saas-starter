package bigquerystore

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"accounts/pkg/auditstore/auditeval"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// A window counts what it keeps, so what it keeps must be what it counted. An
// event held for a window is held as its text: reading its payload to match a
// filter or to aggregate it makes a decoded copy several times the size of the
// text, and a copy left on the event for the rest of the window would be
// memory the budget never counted. These tests keep a window of events that a
// payload filter or a payload group-by has read, and compare the heap the
// window holds, measured after a collection, with what it charged.

// retainedWindowEvents is how many events a window of these tests holds.
const retainedWindowEvents = 1500

// retainedFactor is how far the heap a window holds may pass what it counted.
// The count is an approximation of the text (a fixed overhead per event and the
// length of its strings), and the heap lands just under it (0.85 to 0.9 times);
// a window that kept the decoded payloads of its events lands at three and a
// half to seven times it.
const retainedFactor = 1.5

// retainedPayloads are the shapes of payload whose decode is far larger than
// their text: many small numbers, each of which the decode boxes in an interface
// of its own.
var retainedPayloads = map[string]func(i int) map[string]any{
	"thirty integer keys": func(i int) map[string]any {
		payload := map[string]any{"match": true, "group": i % 7}
		for k := 0; k < 30; k++ {
			payload[fmt.Sprintf("k%02d", k)] = 100000 + i*31 + k
		}
		return payload
	},
	"an array of two hundred integers": func(i int) map[string]any {
		values := make([]any, 200)
		for k := range values {
			values[k] = (i + k) % 100
		}
		return map[string]any{"match": true, "group": i % 7, "values": values}
	},
}

// retainedReads are the reads that decode a payload: a filter, a group-by, and
// the two together.
var retainedReads = map[string]struct {
	query business.AuditQuery
	spec  *business.AuditAggregationSpec
}{
	"a payload filter":   {query: business.AuditQuery{PayloadContains: map[string]any{"match": true}}},
	"a payload group-by": {spec: &business.AuditAggregationSpec{GroupBy: []string{"payload:group"}}},
	"a filter and a group-by": {
		query: business.AuditQuery{PayloadContains: map[string]any{"match": true}},
		spec:  &business.AuditAggregationSpec{GroupBy: []string{"payload:group"}, Metrics: []business.AuditMetric{{Op: "sum", Field: "payload:group"}}},
	},
}

// heapInUse is the live heap after two collections.
func heapInUse() int64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return int64(stats.HeapAlloc)
}

func TestAWindowHoldsInMemoryWhatItCountedNotTheDecodeOfItsPayloads(t *testing.T) {
	for payloadName, payload := range retainedPayloads {
		for readName, read := range retainedReads {
			t.Run(payloadName+"/"+readName, func(t *testing.T) {
				w := newBoundWarehouse(t)
				var records []business.AuditRecord
				for i := 0; i < retainedWindowEvents; i++ {
					records = append(records, boundEvent(t, i+1, boundNow.Add(-time.Hour-time.Duration(i)*time.Second), payload(i)))
				}
				w.append(t, records...)
				reader := w.reader(t, 1<<40, 0)

				audit := orgScoped(read.query)
				m, err := auditeval.NewMatcher(audit)
				require.NoError(t, err)
				var aggregator *auditeval.Aggregator
				if read.spec != nil {
					aggregator, err = auditeval.NewAggregator(*read.spec, nil, 1<<40)
					require.NoError(t, err)
				}

				events, err := reader.loadWindow(context.Background(), m, audit.Query, window{}, true)
				require.NoError(t, err)
				if aggregator != nil {
					for _, event := range events {
						require.NoError(t, aggregator.Add(event))
					}
				}
				var counted int64
				for _, event := range events {
					counted += int64(event.Size() + dedupeEntryBytes)
				}
				count := len(events)

				// What the events hold is what the heap loses when they go: the
				// warehouse fake keeps the batches it served, and they stay in
				// both measurements.
				with := heapInUse()
				runtime.KeepAlive(events)
				events = nil
				held := with - heapInUse()
				runtime.KeepAlive(aggregator)

				require.Equal(t, retainedWindowEvents, count)
				// The slack is the runtime's noise: the aggregator, whose buckets
				// the events do not hold, is in both measurements.
				const slack = 64 << 10
				require.LessOrEqual(t, float64(held), retainedFactor*float64(counted)+slack,
					"the window counted %d bytes and held %d (%.1fx)", counted, held, float64(held)/float64(counted))
			})
		}
	}
}
