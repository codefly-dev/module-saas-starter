package infra

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"accounts/pkg/events"
	eventsv1 "accounts/pkg/gen/saas/events/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestEventFingerprintIgnoresPerAttemptMetadata is the idempotency-retry guard:
// two publishes of one logical event carry the same fact but legitimately differ
// in the per-attempt transport/trace metadata (event_time, traceparent,
// correlation/causation ids). The fingerprint must be identical so a retry is
// treated as the idempotent re-publish it is, not a spurious conflict.
func TestEventFingerprintIgnoresPerAttemptMetadata(t *testing.T) {
	base := func() *eventsv1.EventEnvelope {
		return &eventsv1.EventEnvelope{
			Id:          "11111111-1111-1111-1111-111111111111",
			Type:        "scope.granted",
			Source:      "saas.accounts",
			Subject:     "scope/abc",
			Specversion: "1.0",
			Data:        []byte(`{"scope":"abc"}`),
			TenantId:    "22222222-2222-2222-2222-222222222222",
			BoundaryId:  "root",
		}
	}
	first := base()
	first.Time = timestamppb.New(time.Unix(1000, 0))
	first.Traceparent = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	first.CorrelationId = "corr-1"
	first.CausationId = "cause-1"

	second := base()
	second.Time = timestamppb.New(time.Unix(9999, 0))
	second.Traceparent = "00-cccccccccccccccccccccccccccccccc-dddddddddddddddd-01"
	second.CorrelationId = "corr-2"
	second.CausationId = "cause-2"

	fpFirst, err := eventFingerprint(first)
	require.NoError(t, err)
	fpSecond, err := eventFingerprint(second)
	require.NoError(t, err)
	require.Equal(t, fpFirst, fpSecond,
		"fingerprint must ignore per-attempt event_time/traceparent/correlation/causation metadata")
}

// TestEventFingerprintTracksSemanticFact guards the other direction: a change to
// the event's actual fact (its payload here) must change the fingerprint, so a
// genuine same-id redefinition is still caught as a conflict.
func TestEventFingerprintTracksSemanticFact(t *testing.T) {
	base := &eventsv1.EventEnvelope{
		Id:          "11111111-1111-1111-1111-111111111111",
		Type:        "scope.granted",
		Source:      "saas.accounts",
		Specversion: "1.0",
		Data:        []byte(`{"scope":"abc"}`),
		TenantId:    "22222222-2222-2222-2222-222222222222",
	}
	changed := &eventsv1.EventEnvelope{
		Id:          "11111111-1111-1111-1111-111111111111",
		Type:        "scope.granted",
		Source:      "saas.accounts",
		Specversion: "1.0",
		Data:        []byte(`{"scope":"xyz"}`),
		TenantId:    "22222222-2222-2222-2222-222222222222",
	}
	fpBase, err := eventFingerprint(base)
	require.NoError(t, err)
	fpChanged, err := eventFingerprint(changed)
	require.NoError(t, err)
	require.NotEqual(t, fpBase, fpChanged,
		"a change to the event's data must change the fingerprint")
}

// TestOwesOrderedDeliveryOnlyForOrderedSubscribers pins the rule that decides
// whether a failed fan-out holds the rest of its partition back. The relay used
// to block the partition of every failed event, whatever its subscribers had
// been promised — so one undeliverable event stalled later events belonging to
// subscriptions whose whole contract is that order does not matter, which is
// head-of-line blocking charged to the wrong delivery mode. Only an event with a
// live ordered subscriber owes its neighbours anything.
func TestOwesOrderedDeliveryOnlyForOrderedSubscribers(t *testing.T) {
	envelope := func(eventType, partition string) *eventsv1.EventEnvelope {
		return &eventsv1.EventEnvelope{
			Id:           "11111111-1111-1111-1111-111111111111",
			Type:         eventType,
			Source:       "saas.accounts",
			PartitionKey: partition,
		}
	}
	subscription := func(pattern string, delivery events.Delivery) events.Subscription {
		return events.Subscription{ID: "sub", TypePattern: pattern, Queue: "q", Delivery: delivery}
	}
	const tenant = "22222222-2222-2222-2222-222222222222"

	// scope.granted is tenant-visible in the composed catalog; installation.created
	// is internal (see pkg/eventcatalog/catalog_gen.go).
	for _, testCase := range []struct {
		name          string
		event         *eventsv1.EventEnvelope
		subscriptions []events.Subscription
		want          bool
	}{
		{
			name:          "ordered subscriber on a partitioned event",
			event:         envelope("scope.granted", tenant),
			subscriptions: []events.Subscription{subscription("scope.granted", events.DeliveryOrdered)},
			want:          true,
		},
		{
			name:          "ordered subscriber matched by a wildcard pattern",
			event:         envelope("scope.granted", tenant),
			subscriptions: []events.Subscription{subscription("scope.*", events.DeliveryOrdered)},
			want:          true,
		},
		{
			name:  "only unordered subscribers",
			event: envelope("scope.granted", tenant),
			subscriptions: []events.Subscription{
				subscription("scope.granted", events.DeliveryUnordered),
				subscription("scope.*", events.DeliveryUnordered),
			},
			want: false,
		},
		{
			name:          "ordered subscriber on another type",
			event:         envelope("scope.granted", tenant),
			subscriptions: []events.Subscription{subscription("scope.revoked", events.DeliveryOrdered)},
			want:          false,
		},
		{
			name: "no partition key means unordered by construction",
			// eventOrdering yields no ordering key for an empty partition, so the
			// delivery carries no ordering guarantee to protect.
			event:         envelope("scope.granted", ""),
			subscriptions: []events.Subscription{subscription("scope.granted", events.DeliveryOrdered)},
			want:          false,
		},
		{
			name: "internal events are never delivered at all",
			// A subscription row can outlive the type turning internal; the relay
			// suppresses its fan-out, so it owes no order to anyone either.
			event:         envelope("installation.created", tenant),
			subscriptions: []events.Subscription{subscription("installation.*", events.DeliveryOrdered)},
			want:          false,
		},
		{
			name:          "no subscribers",
			event:         envelope("scope.granted", tenant),
			subscriptions: nil,
			want:          false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, owesOrderedDelivery(testCase.event, testCase.subscriptions))
		})
	}
}

// SetEventReplayPageSizeForTest shrinks the Replay page bound for one test, so
// the multi-page walk can be exercised without publishing a full page of
// history. Both bounds below are vars purely to make this possible.
func SetEventReplayPageSizeForTest(t *testing.T, size int) {
	previous := replayPageSize
	replayPageSize = size
	t.Cleanup(func() { replayPageSize = previous })
}

// SetMaxEventSubscriptionsReadForTest shrinks the subscription read cap for one
// test, so truncation can be exercised without inserting a cap's worth of rows
// into a table other tests share.
func SetMaxEventSubscriptionsReadForTest(t *testing.T, size int) {
	previous := maxEventSubscriptionsRead
	maxEventSubscriptionsRead = size
	t.Cleanup(func() { maxEventSubscriptionsRead = previous })
}

func TestNackMessageTruncatesOnRuneBoundary(t *testing.T) {
	// A cause whose byte length exceeds the failure-message cap and whose final
	// rune straddles the 4096th byte would, on a naive byte slice, become
	// invalid UTF-8 and make the nack command fail its protobuf validation.
	cause := errors.New(strings.Repeat("a", 4095) + "é")
	message := nackMessage(cause)
	require.LessOrEqual(t, len(message), 4096)
	require.True(t, utf8.ValidString(message), "a truncated nack message must remain valid UTF-8")
	require.Equal(t, strings.Repeat("a", 4095), message)
}
