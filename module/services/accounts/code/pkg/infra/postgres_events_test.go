package infra_test

import (
	"testing"
	"time"

	"accounts/pkg/events"
	"accounts/pkg/events/eventstest"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostgresEventTransportConformance(t *testing.T) {
	const lease = 300 * time.Millisecond
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)

	eventstest.RunConformance(t, eventstest.Harness{
		New: func(_ *testing.T, subscriptions []events.Subscription) events.Transport {
			return infra.NewPostgresEventTransport(store, pool, subscriptions, "events-conformance-"+uuid.NewString(), lease)
		},
		LeaseDuration: lease,
	})
}

func TestPostgresEventTransportPublishIsAtomicAcrossFanOut(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)

	// A subscription whose queue name fails the jobs command validation makes the
	// second enqueue in the fan-out fail. The event-of-record enqueued first must
	// roll back with it, leaving nothing durable to replay.
	poison := events.Subscription{ID: "poison", TypePattern: "documents.entry.ingested", Queue: "Not A Queue", Delivery: events.DeliveryUnordered}
	transport := infra.NewPostgresEventTransport(store, pool, []events.Subscription{poison}, "events-atomic-"+uuid.NewString(), 300*time.Millisecond)

	tenant := uuid.NewString()
	event := &events.EventEnvelope{
		Id:            uuid.NewString(),
		Type:          "documents.entry.ingested",
		Source:        "urn:codefly:documents/ingest",
		Specversion:   "1.0",
		TenantId:      tenant,
		SchemaVersion: 1,
		Data:          []byte("payload"),
	}
	require.Error(t, transport.Publish(testCtx, nil, event), "a delivery that fails validation must fail the publish")

	replayed, err := transport.Replay(testCtx, events.ReplaySelector{Type: event.GetType(), TenantID: tenant})
	require.NoError(t, err)
	require.Zero(t, replayed, "a failed fan-out leaves no durable event-of-record behind")
}
