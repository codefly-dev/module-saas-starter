package auditeval

import (
	"fmt"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// A decoded payload is several times the text it came from. A store that holds
// events for a window counts their text (Event.Size), so an event it keeps must
// hold no decode: Match may leave one for a store that streams the event on to
// an aggregation, and everything that keeps an event drops it.

// payloadEvent is an event whose payload filter and group-by key both read its
// details, with thirty keys of small integers and an array of a hundred: text
// of about a kilobyte, a decode of several.
func payloadEvent(n int) *Event {
	text := fmt.Sprintf(`{"match":true,"group":%d,"values":[`, n%3)
	for k := 0; k < 100; k++ {
		if k > 0 {
			text += ","
		}
		text += fmt.Sprint((n + k) % 10)
	}
	text += "]"
	for k := 0; k < 30; k++ {
		text += fmt.Sprintf(`,"k%02d":%d`, k, k)
	}
	return &Event{
		Entry: business.AuditEntry{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", n), EventType: business.EventDocumentRead,
			CreatedAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Second),
		},
		Details: text + "}", HasDetails: true,
	}
}

func payloadMatcher(t *testing.T) *Matcher {
	t.Helper()
	m, err := NewMatcher(business.AuditRead{
		Scope: business.PlatformAuditScope(),
		Query: business.AuditQuery{PayloadContains: map[string]any{"match": true}},
	})
	require.NoError(t, err)
	return m
}

var groupByPayload = business.AuditAggregationSpec{
	GroupBy: []string{"payload:group"},
	Metrics: []business.AuditMetric{{Op: "sum", Field: "payload:k07"}, {Op: "count_distinct", Field: "payload:group", Alias: "groups"}},
}

func TestAnEventThatIsKeptHoldsNoDecodedPayload(t *testing.T) {
	m := payloadMatcher(t)

	streamed := payloadEvent(1)
	ok, err := m.Match(streamed)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, streamed.decoded, "a filter leaves its decode for the aggregation that reads the event next")

	// A store that keeps the event for a window detaches it, which drops the decode.
	kept := payloadEvent(2)
	_, err = m.Match(kept)
	require.NoError(t, err)
	kept.Detach()
	require.False(t, kept.decodedSet)
	require.Nil(t, kept.decoded)
	require.Equal(t, payloadEvent(2).Size(), kept.Size(), "what it holds is what Size counts")

	// A page drops it from every event it keeps, detached or not.
	page, err := NewPage(business.AuditQuery{PageSize: 5})
	require.NoError(t, err)
	paged := payloadEvent(3)
	_, err = m.Match(paged)
	require.NoError(t, err)
	require.NotNil(t, paged.decoded)
	require.True(t, page.Offer(paged))
	require.Nil(t, paged.decoded, "a page returns the payload decoded again; the decode would only be held")

	// An aggregation reads an event it does not own the life of: it leaves none.
	aggregator, err := NewAggregator(groupByPayload, nil, 0)
	require.NoError(t, err)
	fresh := payloadEvent(4)
	require.NoError(t, aggregator.Add(fresh))
	require.False(t, fresh.decodedSet, "Add decodes for itself and keeps nothing on the event")
	require.Nil(t, fresh.decoded)
	require.NoError(t, aggregator.Add(kept))
	require.Nil(t, kept.decoded, "a detached event is read by decoding it again")
}

func TestAnAggregationAnswersTheSameWhateverTheEventHoldsOfItsDecode(t *testing.T) {
	m := payloadMatcher(t)
	answer := func(prepare func(*Event)) []business.AuditAggregateBucket {
		aggregator, err := NewAggregator(groupByPayload, nil, 0)
		require.NoError(t, err)
		for n := 1; n <= 30; n++ {
			event := payloadEvent(n)
			prepare(event)
			require.NoError(t, aggregator.Add(event))
		}
		return aggregator.Buckets()
	}
	fresh := answer(func(*Event) {})
	streamed := answer(func(e *Event) {
		ok, err := m.Match(e)
		require.NoError(t, err)
		require.True(t, ok)
	})
	detached := answer(func(e *Event) {
		_, err := m.Match(e)
		require.NoError(t, err)
		e.Detach()
	})
	require.Len(t, fresh, 3)
	require.Equal(t, fresh, streamed, "an event a filter has decoded")
	require.Equal(t, fresh, detached, "an event a window kept, decoded again")
}

func TestAnEventStreamedThroughAFilterAndAnAggregationIsDecodedOnce(t *testing.T) {
	m := payloadMatcher(t)
	aggregator, err := NewAggregator(groupByPayload, nil, 0)
	require.NoError(t, err)

	allocs := func(visit func(*Event)) float64 {
		event := payloadEvent(1)
		return testing.AllocsPerRun(50, func() {
			event.dropDecoded()
			visit(event)
		})
	}
	match := allocs(func(e *Event) { _, _ = m.Match(e) })
	add := allocs(func(e *Event) { _ = aggregator.Add(e) })
	both := allocs(func(e *Event) { _, _ = m.Match(e); _ = aggregator.Add(e) })
	require.Greater(t, match, 100.0, "decoding is where the allocations are")
	require.Less(t, both, match+add-match/2, "Add reads the decode Match made: one decode, not two (match %.0f, add %.0f, both %.0f)", match, add, both)
}
