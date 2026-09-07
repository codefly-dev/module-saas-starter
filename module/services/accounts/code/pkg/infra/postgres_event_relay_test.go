package infra_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/events"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// relayTokenSeq gives each type/queue name in this file a process-unique,
// hyphen-free suffix. event_subscriptions.type_pattern is CHECK-constrained to
// dotted lowercase-alphanumeric segments (migration 114), which rules out the
// hyphens in a uuid, so tests mint their own tokens instead.
var relayTokenSeq int64

func relayToken() string {
	return fmt.Sprintf("n%dn%d", time.Now().UnixNano(), atomic.AddInt64(&relayTokenSeq, 1))
}

// seededSubscription is a control-plane subscription row plus the id the relay
// stamps into every delivery's idempotency key.
type seededSubscription struct {
	ID       string
	Queue    string
	Delivery events.Delivery
}

// seedSubscriptionRow inserts one event_subscriptions row under the control-plane
// role (the only authority that may write the table) and returns its generated
// id, so a test can assert the idempotency key the relay derives from it.
func seedSubscriptionRow(t *testing.T, typePattern, queue string, delivery events.Delivery) seededSubscription {
	t.Helper()
	var id string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		return tx.QueryRow(ctx, `
			INSERT INTO public.event_subscriptions
				(subscriber_principal_id, type_pattern, queue, delivery)
			VALUES (gen_random_uuid(), $1, $2, $3)
			RETURNING id`,
			typePattern, queue, string(delivery),
		).Scan(&id)
	}))
	return seededSubscription{ID: id, Queue: queue, Delivery: delivery}
}

// TestPostgresRelayFansOutOnePerSubscription is the relay acceptance case: one
// published event lands exactly one inbox job on every non-revoked subscription
// that matches its type — no more, no fewer — and a queue nobody subscribed
// receives nothing. Each delivery's idempotency key is event.id + ":" +
// subscription.id, the witness that makes redelivery of the same (event,
// subscription) pair a no-op. A unique type keeps the shared subscriptions table
// from other subtests out of this event's fan-out.
func TestPostgresRelayFansOutOnePerSubscription(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	transport := infra.NewPostgresEventTransport(store, pool, "relay-fanout-"+uuid.NewString(), time.Second)

	eventType := "relay.fanout." + relayToken()
	subA := seedSubscriptionRow(t, eventType, "relay.fanout.a."+relayToken(), events.DeliveryUnordered)
	subB := seedSubscriptionRow(t, eventType, "relay.fanout.b."+relayToken(), events.DeliveryUnordered)
	unsubscribedQueue := "relay.fanout.none." + relayToken()

	tenant := uuid.NewString()
	event := &events.EventEnvelope{
		Id:           uuid.NewString(),
		Type:         eventType,
		Source:       "urn:codefly:test/relay",
		Specversion:  "1.0",
		TenantId:     tenant,
		PartitionKey: tenant,
		Data:         []byte(`{"n":1}`),
	}
	// nil caller tx: Publish commits the event and drains the relay inline, so the
	// fan-out is complete when Publish returns.
	require.NoError(t, transport.Publish(testCtx, nil, event))

	// Exactly two inbox jobs exist for this type, one per subscription, each with
	// the derived idempotency key on the right queue.
	wantKeys := map[string]string{
		subA.Queue: event.GetId() + ":" + subA.ID,
		subB.Queue: event.GetId() + ":" + subB.ID,
	}
	gotKeys := map[string]string{}
	rows, err := pool.Query(testCtx,
		`SELECT queue, idempotency_key FROM public.job_messages WHERE topic = $1`, eventType)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var queue, key string
		require.NoError(t, rows.Scan(&queue, &key))
		gotKeys[queue] = key
	}
	require.NoError(t, rows.Err())
	require.Equal(t, wantKeys, gotKeys, "one delivery per matching subscription, keyed on event.id:subscription.id")
	require.NotContains(t, gotKeys, unsubscribedQueue, "a queue nobody subscribed receives nothing")

	// Each subscribed queue yields exactly one delivery of the event, and nothing
	// after it is acked — at-least-once collapses to exactly-once on the happy path.
	for _, sub := range []seededSubscription{subA, subB} {
		leased, err := transport.Claim(testCtx, sub.Queue, 10)
		require.NoError(t, err)
		require.Len(t, leased, 1, "subscription %s receives the event exactly once", sub.Queue)
		require.Equal(t, event.GetId(), leased[0].Envelope.GetId())
		require.NoError(t, transport.Ack(testCtx, leased[0].Token))

		again, err := transport.Claim(testCtx, sub.Queue, 10)
		require.NoError(t, err)
		require.Empty(t, again, "an acked delivery is not redelivered")
	}
}

// TestPostgresRelayOrderedPartitionUnderConcurrentClaimers proves ordered
// delivery holds its guarantee when several workers race for the same queue: an
// ordered subscription hands out at most one event per partition at a time
// (head-of-line), so no two concurrent claimers ever hold two events of one
// partition simultaneously, and the acked order equals the published order.
func TestPostgresRelayOrderedPartitionUnderConcurrentClaimers(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	transport := infra.NewPostgresEventTransport(store, pool, "relay-ordered-"+uuid.NewString(), time.Second)

	eventType := "relay.ordered." + relayToken()
	sub := seedSubscriptionRow(t, eventType, "relay.ordered.q."+relayToken(), events.DeliveryOrdered)

	const count = 6
	tenant := uuid.NewString()
	publishedOrder := make([]string, count)
	for i := 0; i < count; i++ {
		event := &events.EventEnvelope{
			Id:           uuid.NewString(),
			Type:         eventType,
			Source:       "urn:codefly:test/relay",
			Specversion:  "1.0",
			TenantId:     tenant,
			PartitionKey: tenant, // one partition: strict order applies across the whole run
			Data:         []byte(fmt.Sprintf(`{"n":%d}`, i)),
		}
		publishedOrder[i] = event.GetId()
		require.NoError(t, transport.Publish(testCtx, nil, event))
	}

	// Several workers claim the same ordered queue concurrently. The transport must
	// never expose two events of one partition at once, so each claim returns the
	// single head event; the worker acks it before the next becomes claimable.
	var (
		mu       sync.Mutex
		acked    []string
		inFlight int
		maxSeen  int
	)
	deadline := time.Now().Add(20 * time.Second)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				done := len(acked) >= count
				mu.Unlock()
				if done || time.Now().After(deadline) {
					return
				}
				leased, err := transport.Claim(testCtx, sub.Queue, 5)
				if err != nil || len(leased) == 0 {
					continue
				}
				mu.Lock()
				inFlight += len(leased)
				if inFlight > maxSeen {
					maxSeen = inFlight
				}
				mu.Unlock()

				for _, l := range leased {
					require.NoError(t, transport.Ack(testCtx, l.Token))
					mu.Lock()
					acked = append(acked, l.Envelope.GetId())
					inFlight--
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	require.Equal(t, 1, maxSeen, "an ordered partition never exposes more than one event at a time to concurrent claimers")
	require.Equal(t, publishedOrder, acked, "concurrent claimers still observe strict per-partition order")
}
