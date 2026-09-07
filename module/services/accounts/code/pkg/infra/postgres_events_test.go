package infra_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/events"
	"accounts/pkg/events/eventstest"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// seedSubscriptions materializes the harness's in-memory subscriptions as rows
// in event_subscriptions, the relay's real resolution source. Request traffic
// can only SELECT that table, so the seed runs under the control-plane role, the
// same authority ModuleCapabilitiesService.Subscribe uses in production. The
// harness gives every subtest a unique queue, so rows from earlier subtests stay
// in the shared table harmlessly: their deliveries land on queues nobody claims.
func seedSubscriptions(t *testing.T, subscriptions []events.Subscription) {
	t.Helper()
	if len(subscriptions) == 0 {
		return
	}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		for _, subscription := range subscriptions {
			if _, err := tx.Exec(ctx, `
				INSERT INTO public.event_subscriptions
					(subscriber_principal_id, type_pattern, queue, delivery)
				VALUES (gen_random_uuid(), $1, $2, $3)`,
				subscription.TypePattern, subscription.Queue, string(subscription.Delivery),
			); err != nil {
				return err
			}
		}
		return nil
	}))
}

func TestPostgresEventTransportConformance(t *testing.T) {
	const lease = 300 * time.Millisecond
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)

	eventstest.RunConformance(t, eventstest.Harness{
		New: func(t *testing.T, subscriptions []events.Subscription) events.Transport {
			seedSubscriptions(t, subscriptions)
			return infra.NewPostgresEventTransport(store, pool, "events-conformance-"+uuid.NewString(), lease)
		},
		LeaseDuration: lease,
	})
}

// TestPostgresEventTransportPublishJoinsCallerTransaction proves the outbox
// rule: Publish with a caller transaction writes the event-of-record inside it,
// so a rolled-back producer transaction leaves nothing durable and nothing to
// replay. Durability of the event is independent of fan-out — the relay reads
// domain_events after commit — so this is the guarantee that matters.
func TestPostgresEventTransportPublishJoinsCallerTransaction(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	transport := infra.NewPostgresEventTransport(store, pool, "events-atomic-"+uuid.NewString(), 300*time.Millisecond)

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

	tx, err := pool.Begin(testCtx)
	require.NoError(t, err)
	require.NoError(t, transport.Publish(testCtx, tx, event), "publish within a caller tx writes the event-of-record into it")
	require.NoError(t, tx.Rollback(testCtx), "the producer transaction rolls back")

	replayed, err := transport.Replay(testCtx, events.ReplaySelector{Type: event.GetType(), TenantID: tenant})
	require.NoError(t, err)
	require.Zero(t, replayed, "a rolled-back producer leaves no durable event-of-record behind")
}
