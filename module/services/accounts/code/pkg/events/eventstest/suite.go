// Package eventstest is the transport conformance suite. Every events.Transport
// implementation runs it; a consumer written against one conforming transport
// runs unchanged against another.
package eventstest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"accounts/pkg/events"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Harness constructs the transport under test. New must return a transport
// backed by durable state shared across every call within one RunConformance
// invocation (a fresh in-memory store for the fake, the same database for
// Postgres), configured with the given subscriptions. LeaseDuration is the
// lease each claim holds, so the suite knows how long to wait for expiry.
type Harness struct {
	New           func(t *testing.T, subscriptions []events.Subscription) events.Transport
	LeaseDuration time.Duration
}

// RunConformance exercises every guarantee a Transport must provide.
func RunConformance(t *testing.T, h Harness) {
	t.Helper()
	t.Run("AtLeastOnce", func(t *testing.T) { testAtLeastOnce(t, h) })
	t.Run("IDDedupe", func(t *testing.T) { testIDDedupe(t, h) })
	t.Run("FIFOPerPartition", func(t *testing.T) { testFIFOPerPartition(t, h) })
	t.Run("CrossPartitionInterleave", func(t *testing.T) { testCrossPartitionInterleave(t, h) })
	t.Run("AckNackHeartbeat", func(t *testing.T) { testAckNackHeartbeat(t, h) })
	t.Run("LeaseExpiry", func(t *testing.T) { testLeaseExpiry(t, h) })
	t.Run("PermanentNackDeadLetter", func(t *testing.T) { testPermanentNackDeadLetter(t, h) })
	t.Run("Replay", func(t *testing.T) { testReplay(t, h) })
	t.Run("TenantIsolation", func(t *testing.T) { testTenantIsolation(t, h) })
	t.Run("ZeroSubscriberPublish", func(t *testing.T) { testZeroSubscriberPublish(t, h) })
	t.Run("IDConflict", func(t *testing.T) { testIDConflict(t, h) })
	t.Run("ClaimBatchLimit", func(t *testing.T) { testClaimBatchLimit(t, h) })
	t.Run("FullEnvelopeFidelity", func(t *testing.T) { testFullEnvelopeFidelity(t, h) })
}

func suffix() string { return strings.ReplaceAll(uuid.NewString(), "-", "")[:12] }

func exactSubscription(queue, eventType string, delivery events.Delivery) events.Subscription {
	tag := suffix()
	return events.Subscription{
		ID:          "sub." + tag,
		TypePattern: eventType,
		Queue:       queue + "." + tag,
		Delivery:    delivery,
	}
}

type envelopeOption func(*events.EventEnvelope)

func withPartition(key string) envelopeOption {
	return func(e *events.EventEnvelope) { e.PartitionKey = key }
}
func withTenant(id string) envelopeOption { return func(e *events.EventEnvelope) { e.TenantId = id } }
func withData(data []byte) envelopeOption { return func(e *events.EventEnvelope) { e.Data = data } }
func withID(id string) envelopeOption     { return func(e *events.EventEnvelope) { e.Id = id } }

func newEnvelope(eventType string, options ...envelopeOption) *events.EventEnvelope {
	e := &events.EventEnvelope{
		Id:              uuid.NewString(),
		Type:            eventType,
		Source:          "urn:codefly:test/events",
		Subject:         "aggregate-" + suffix(),
		Specversion:     "1.0",
		Datacontenttype: "application/protobuf",
		Dataschema:      "codefly/events/" + eventType + "@1",
		TenantId:        uuid.NewString(),
		SchemaVersion:   1,
		Data:            []byte("payload-" + suffix()),
		Time:            timestamppb.New(time.Now().UTC().Truncate(time.Microsecond)),
	}
	for _, option := range options {
		option(e)
	}
	return e
}

func claimAll(t *testing.T, transport events.Transport, queue string, max int) []events.Leased {
	t.Helper()
	leased, err := transport.Claim(context.Background(), queue, max)
	require.NoError(t, err)
	return leased
}

func testAtLeastOnce(t *testing.T, h Harness) {
	subscription := exactSubscription("events.at-least-once", "documents.entry.ingested", events.DeliveryUnordered)
	transport := h.New(t, []events.Subscription{subscription})
	event := newEnvelope("documents.entry.ingested")
	require.NoError(t, transport.Publish(context.Background(), nil, event))

	leased := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, leased, 1)
	require.Equal(t, event.GetId(), leased[0].Envelope.GetId())
	require.Equal(t, event.GetData(), leased[0].Envelope.GetData())
	require.NoError(t, transport.Ack(context.Background(), leased[0].Token))

	require.Empty(t, claimAll(t, transport, subscription.Queue, 10))
}

func testIDDedupe(t *testing.T, h Harness) {
	subscription := exactSubscription("events.dedupe", "documents.entry.ingested", events.DeliveryUnordered)
	transport := h.New(t, []events.Subscription{subscription})
	event := newEnvelope("documents.entry.ingested")
	require.NoError(t, transport.Publish(context.Background(), nil, event))
	require.NoError(t, transport.Publish(context.Background(), nil, event))

	require.Len(t, claimAll(t, transport, subscription.Queue, 10), 1)
}

func testFIFOPerPartition(t *testing.T, h Harness) {
	subscription := exactSubscription("events.fifo", "documents.entry.ingested", events.DeliveryOrdered)
	transport := h.New(t, []events.Subscription{subscription})
	partition := "tenant/one"
	ids := make([]string, 3)
	for index := range ids {
		event := newEnvelope("documents.entry.ingested", withPartition(partition))
		ids[index] = event.GetId()
		require.NoError(t, transport.Publish(context.Background(), nil, event))
	}
	for _, expected := range ids {
		leased := claimAll(t, transport, subscription.Queue, 10)
		require.Len(t, leased, 1, "a partition delivers strictly in order, one at a time")
		require.Equal(t, expected, leased[0].Envelope.GetId())
		require.NoError(t, transport.Ack(context.Background(), leased[0].Token))
	}
}

func testCrossPartitionInterleave(t *testing.T, h Harness) {
	subscription := exactSubscription("events.interleave", "documents.entry.ingested", events.DeliveryOrdered)
	transport := h.New(t, []events.Subscription{subscription})
	for _, partition := range []string{"p1", "p2", "p1", "p2"} {
		require.NoError(t, transport.Publish(context.Background(), nil, newEnvelope("documents.entry.ingested", withPartition(partition))))
	}
	leased := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, leased, 2, "distinct partitions are claimable concurrently")
	partitions := map[string]struct{}{
		leased[0].Envelope.GetPartitionKey(): {},
		leased[1].Envelope.GetPartitionKey(): {},
	}
	require.Len(t, partitions, 2)
}

func testAckNackHeartbeat(t *testing.T, h Harness) {
	subscription := exactSubscription("events.ack-nack", "documents.entry.ingested", events.DeliveryUnordered)
	transport := h.New(t, []events.Subscription{subscription})
	require.NoError(t, transport.Publish(context.Background(), nil, newEnvelope("documents.entry.ingested")))

	leased := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, leased, 1)
	require.NoError(t, transport.Heartbeat(context.Background(), leased[0].Token))
	require.NoError(t, transport.Nack(context.Background(), leased[0].Token, errors.New("transient"), false))

	redelivered := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, redelivered, 1, "a retryable nack redelivers")
	require.NoError(t, transport.Ack(context.Background(), redelivered[0].Token))
	require.Empty(t, claimAll(t, transport, subscription.Queue, 10))
}

func testLeaseExpiry(t *testing.T, h Harness) {
	subscription := exactSubscription("events.lease-expiry", "documents.entry.ingested", events.DeliveryUnordered)
	transport := h.New(t, []events.Subscription{subscription})
	require.NoError(t, transport.Publish(context.Background(), nil, newEnvelope("documents.entry.ingested")))

	leased := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, leased, 1)

	time.Sleep(h.LeaseDuration + h.LeaseDuration/2 + 100*time.Millisecond)

	require.ErrorIs(t, transport.Heartbeat(context.Background(), leased[0].Token), events.ErrLeaseLost, "an expired lease cannot be revived")
	recovered := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, recovered, 1, "an expired lease is reclaimable")
	require.NoError(t, transport.Ack(context.Background(), recovered[0].Token))
}

func testPermanentNackDeadLetter(t *testing.T, h Harness) {
	subscription := exactSubscription("events.dead-letter", "documents.entry.ingested", events.DeliveryUnordered)
	transport := h.New(t, []events.Subscription{subscription})
	require.NoError(t, transport.Publish(context.Background(), nil, newEnvelope("documents.entry.ingested")))

	leased := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, leased, 1)
	require.NoError(t, transport.Nack(context.Background(), leased[0].Token, errors.New("poison"), true))

	require.Empty(t, claimAll(t, transport, subscription.Queue, 10), "a permanent nack dead-letters without redelivery")
}

func testReplay(t *testing.T, h Harness) {
	subscription := exactSubscription("events.replay", "documents.entry.ingested", events.DeliveryUnordered)
	transport := h.New(t, []events.Subscription{subscription})
	event := newEnvelope("documents.entry.ingested")
	require.NoError(t, transport.Publish(context.Background(), nil, event))

	leased := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, leased, 1)
	require.NoError(t, transport.Ack(context.Background(), leased[0].Token))

	replayed, err := transport.Replay(context.Background(), events.ReplaySelector{Type: event.GetType(), TenantID: event.GetTenantId()})
	require.NoError(t, err)
	require.GreaterOrEqual(t, replayed, 1)

	again := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, again, 1, "replay re-fans-out a consumed event")
	require.Equal(t, event.GetId(), again[0].Envelope.GetId())
	require.NoError(t, transport.Ack(context.Background(), again[0].Token))
}

func testTenantIsolation(t *testing.T, h Harness) {
	subscription := exactSubscription("events.tenant", "documents.entry.ingested", events.DeliveryUnordered)
	transport := h.New(t, []events.Subscription{subscription})
	tenantA := uuid.NewString()
	tenantB := uuid.NewString()
	dataA := []byte("tenant-a-secret")
	dataB := []byte("tenant-b-secret")
	require.NoError(t, transport.Publish(context.Background(), nil, newEnvelope("documents.entry.ingested", withTenant(tenantA), withData(dataA))))
	require.NoError(t, transport.Publish(context.Background(), nil, newEnvelope("documents.entry.ingested", withTenant(tenantB), withData(dataB))))

	leased := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, leased, 2)
	byTenant := map[string][]byte{}
	for _, delivery := range leased {
		byTenant[delivery.Envelope.GetTenantId()] = delivery.Envelope.GetData()
		require.NoError(t, transport.Ack(context.Background(), delivery.Token))
	}
	require.Equal(t, dataA, byTenant[tenantA], "tenant A payload is delivered only under tenant A")
	require.Equal(t, dataB, byTenant[tenantB], "tenant B payload is delivered only under tenant B")
	require.Len(t, byTenant, 2)
}

func testZeroSubscriberPublish(t *testing.T, h Harness) {
	transport := h.New(t, nil)
	event := newEnvelope("documents.entry.ingested")
	require.NoError(t, transport.Publish(context.Background(), nil, event), "publishing with no subscriber is legal")

	replayed, err := transport.Replay(context.Background(), events.ReplaySelector{Type: event.GetType(), TenantID: event.GetTenantId()})
	require.NoError(t, err)
	require.GreaterOrEqual(t, replayed, 1, "a zero-subscriber event is still durable and replayable")
}

func testIDConflict(t *testing.T, h Harness) {
	subscription := exactSubscription("events.conflict", "documents.entry.ingested", events.DeliveryUnordered)
	transport := h.New(t, []events.Subscription{subscription})
	tenant := uuid.NewString()
	event := newEnvelope("documents.entry.ingested", withTenant(tenant), withData([]byte("first")))
	require.NoError(t, transport.Publish(context.Background(), nil, event))
	require.NoError(t, transport.Publish(context.Background(), nil, event), "republishing the identical event is a no-op")

	conflict := newEnvelope("documents.entry.ingested", withID(event.GetId()), withTenant(tenant), withData([]byte("second")))
	err := transport.Publish(context.Background(), nil, conflict)
	require.ErrorIs(t, err, events.ErrIdempotencyConflict, "reusing an id for a different envelope must be rejected")

	require.Len(t, claimAll(t, transport, subscription.Queue, 10), 1, "only the first envelope is delivered")
}

func testClaimBatchLimit(t *testing.T, h Harness) {
	subscription := exactSubscription("events.batch", "documents.entry.ingested", events.DeliveryUnordered)
	transport := h.New(t, []events.Subscription{subscription})
	require.NoError(t, transport.Publish(context.Background(), nil, newEnvelope("documents.entry.ingested")))
	require.NoError(t, transport.Publish(context.Background(), nil, newEnvelope("documents.entry.ingested")))

	empty, err := transport.Claim(context.Background(), subscription.Queue, 0)
	require.NoError(t, err, "a zero-sized claim is empty, not an error")
	require.Empty(t, empty)

	one := claimAll(t, transport, subscription.Queue, 1)
	require.Len(t, one, 1, "a claim honors its batch limit")
	require.NoError(t, transport.Ack(context.Background(), one[0].Token))
}

func testFullEnvelopeFidelity(t *testing.T, h Harness) {
	subscription := exactSubscription("events.fidelity", "documents.entry.ingested", events.DeliveryUnordered)
	transport := h.New(t, []events.Subscription{subscription})
	event := &events.EventEnvelope{
		Id:               uuid.NewString(),
		Type:             "documents.entry.ingested",
		Source:           "urn:codefly:documents/ingest",
		Subject:          "entry-77",
		Time:             timestamppb.New(time.Now().UTC().Truncate(time.Microsecond)),
		Specversion:      "1.0",
		Datacontenttype:  "application/protobuf",
		Dataschema:       "codefly/events/documents.entry.ingested@1",
		Data:             []byte{0x00, 0x01, 0xff},
		TenantId:         uuid.NewString(),
		BoundaryId:       "scope-node-3",
		PartitionKey:     "tenant/source",
		CorrelationId:    "corr-9",
		CausationId:      "cause-9",
		ActorPrincipalId: "actor-9",
		OwnerPrincipalId: "owner-9",
		Traceparent:      "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SchemaVersion:    4,
	}
	require.NoError(t, transport.Publish(context.Background(), nil, event))

	leased := claimAll(t, transport, subscription.Queue, 10)
	require.Len(t, leased, 1)
	require.True(t, proto.Equal(event, leased[0].Envelope), "the transport delivers every envelope attribute unchanged")
	require.NoError(t, transport.Ack(context.Background(), leased[0].Token))
}
