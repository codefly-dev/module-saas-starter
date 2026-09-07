package infra_test

import (
	"bytes"
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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// relayTokenSeq gives each type/queue name in this file a process-unique,
// hyphen-free suffix. event_subscriptions.type_pattern is CHECK-constrained to
// dotted lowercase-alphanumeric segments (migration 119), which rules out the
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

// TestPostgresRelayOrderedPartitionPreservesBatchEnqueueOrder is the F1
// regression. When several same-partition ordered events are published inside ONE
// producer transaction and then drained by a single relay batch, every resulting
// inbox job shares one created_at (Postgres NOW() is transaction-start time), so
// the claim fence and ORDER BY can distinguish them only by their enqueue
// position. Before the fix the tiebreak fell to job_messages.id
// (gen_random_uuid()) and the worker observed a random order; enqueue_seq makes
// the tiebreak follow enqueue order. The sibling
// TestPostgresRelayOrderedPartitionUnderConcurrentClaimers masks this: it
// publishes each event with its own inline drain (nil caller tx), giving every
// job a distinct created_at, so the id tiebreak is never reached.
func TestPostgresRelayOrderedPartitionPreservesBatchEnqueueOrder(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	transport := infra.NewPostgresEventTransport(store, pool, "relay-batch-order-"+uuid.NewString(), time.Second)

	eventType := "relay.batchorder." + relayToken()
	sub := seedSubscriptionRow(t, eventType, "relay.batchorder.q."+relayToken(), events.DeliveryOrdered)

	const count = 8
	tenant := uuid.NewString()
	publishedOrder := make([]string, count)

	// Publish every event inside a SINGLE producer transaction: passing a caller tx
	// makes Publish join the insert and defer fan-out, so the relay drains all of
	// them in one batch. Without a caller tx Publish drains inline per event, giving
	// each job its own created_at and masking the bug.
	tx, err := pool.Begin(testCtx)
	require.NoError(t, err)
	for i := 0; i < count; i++ {
		event := &events.EventEnvelope{
			Id:           uuid.NewString(),
			Type:         eventType,
			Source:       "urn:codefly:test/relay",
			Specversion:  "1.0",
			TenantId:     tenant,
			PartitionKey: tenant, // one partition: strict order applies across the run
			Data:         []byte(fmt.Sprintf(`{"n":%d}`, i)),
		}
		publishedOrder[i] = event.GetId()
		require.NoError(t, transport.Publish(testCtx, tx, event))
	}
	require.NoError(t, tx.Commit(testCtx))

	// One relay pass fans all eight out in a single transaction, so every delivery
	// shares created_at and only enqueue_seq can carry the publish order.
	relayed, err := transport.RelayOnce(testCtx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, relayed, count)

	// An ordered partition exposes one head event at a time; claim-ack in a loop and
	// record the delivery order. It must equal the publish order.
	acked := make([]string, 0, count)
	deadline := time.Now().Add(20 * time.Second)
	for len(acked) < count && time.Now().Before(deadline) {
		leased, err := transport.Claim(testCtx, sub.Queue, 5)
		require.NoError(t, err)
		require.LessOrEqual(t, len(leased), 1, "an ordered partition never exposes more than one event at a time")
		if len(leased) == 0 {
			continue
		}
		require.NoError(t, transport.Ack(testCtx, leased[0].Token))
		acked = append(acked, leased[0].Envelope.GetId())
	}
	require.Equal(t, publishedOrder, acked, "same-partition ordered events drained in one relay batch keep their publish order")
}

// eventPublished reports whether the domain_events row has been marked published.
// The worker pool bypasses RLS, so it sees rows for every tenant.
func eventPublished(t *testing.T, pool *pgxpool.Pool, eventID string) bool {
	t.Helper()
	var published bool
	require.NoError(t, pool.QueryRow(testCtx,
		`SELECT published_at IS NOT NULL FROM public.domain_events WHERE id = $1`, eventID).Scan(&published))
	return published
}

// deliveryExists reports whether an inbox job with the given idempotency key was
// enqueued.
func deliveryExists(t *testing.T, pool *pgxpool.Pool, key string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, pool.QueryRow(testCtx,
		`SELECT EXISTS(SELECT 1 FROM public.job_messages WHERE idempotency_key = $1)`, key).Scan(&exists))
	return exists
}

// TestPostgresRelayIsolatesPoisonEventPreservingOrdering is the F4 regression. An
// event whose fan-out keeps failing must not drag the rest of the batch down:
// before the fix a single failed enqueue rolled the whole relay transaction back,
// so every other event — including those for unrelated partitions — stayed
// unpublished and undelivered, wedging the entire outbox behind one bad row. The
// fix runs each event's fan-out in its own savepoint: the poison event is rolled
// back and left unpublished while a healthy event in another partition is still
// delivered. Isolation must not break per-partition ordering, so a later event in
// the poison's own partition is held back rather than published ahead of it.
func TestPostgresRelayIsolatesPoisonEventPreservingOrdering(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	transport := infra.NewPostgresEventTransport(store, pool, "relay-poison-"+uuid.NewString(), time.Second)

	eventType := "relay.poison." + relayToken()
	sub := seedSubscriptionRow(t, eventType, "relay.poison.q."+relayToken(), events.DeliveryUnordered)

	const source = "urn:codefly:test/relay"
	poisonTenant := uuid.NewString()
	healthyTenant := uuid.NewString()
	newEvent := func(tenant string, n int) *events.EventEnvelope {
		return &events.EventEnvelope{
			Id: uuid.NewString(), Type: eventType, Source: source, Specversion: "1.0",
			TenantId: tenant, PartitionKey: tenant, Data: []byte(fmt.Sprintf(`{"n":%d}`, n)),
		}
	}
	poison := newEvent(poisonTenant, 1)   // fan-out will conflict
	blocked := newEvent(poisonTenant, 2)  // same partition, must wait behind the poison
	healthy := newEvent(healthyTenant, 3) // other partition, must be delivered anyway

	// Pre-seed a conflicting inbox delivery for the poison event: the same
	// idempotency tuple the relay derives (event.id + ":" + subscription.id, on the
	// subscription's queue and the event's tenant scope) but a different fact, so
	// the relay's enqueue returns ErrIdempotencyConflict — a deterministic poison.
	// The argument order and casts mirror enqueuePreparedJob's call.
	_, err = pool.Exec(testCtx, `SELECT public.enqueue_job_message(
		$1::text,$2::text,$3::uuid,$4::uuid,$5::text,$6::text,$7::text,$8::text,
		$9::text,$10::int,$11::bytea,$12::text,$13::jsonb,$14::smallint,$15::int,
		$16::timestamptz,$17::bytea)`,
		"inbox", "tenant", poisonTenant, nil, sub.Queue, eventType, source,
		poison.GetId()+":"+sub.ID, nil, int32(1), []byte(`{"conflict":true}`),
		"application/protobuf", "{}", int16(0), int32(5), nil,
		bytes.Repeat([]byte{0xEE}, 32),
	)
	require.NoError(t, err)

	// Publish all three inside ONE producer transaction so the relay drains them in
	// a single batch — the case a whole-batch rollback would wedge.
	tx, err := pool.Begin(testCtx)
	require.NoError(t, err)
	for _, e := range []*events.EventEnvelope{poison, blocked, healthy} {
		require.NoError(t, transport.Publish(testCtx, tx, e))
	}
	require.NoError(t, tx.Commit(testCtx))

	_, err = transport.RelayOnce(testCtx)
	require.NoError(t, err, "a poison event must not fail the whole relay pass")

	require.True(t, eventPublished(t, pool, healthy.GetId()),
		"healthy event in another partition must be published despite the poison")
	require.False(t, eventPublished(t, pool, poison.GetId()),
		"poison event must stay unpublished for a later tick")
	require.False(t, eventPublished(t, pool, blocked.GetId()),
		"a later event in the poison's partition must be held back to preserve order")

	require.True(t, deliveryExists(t, pool, healthy.GetId()+":"+sub.ID),
		"healthy event must be delivered")
	require.False(t, deliveryExists(t, pool, blocked.GetId()+":"+sub.ID),
		"blocked event must not be delivered ahead of the poison")
}

// TestPostgresRelaySuppressesInternalVisibilityFanOut is the F2 regression. An
// internal-visibility event must never be delivered to a subscriber, even when a
// matching subscription row already exists — the state a wildcard subscription
// created before the type was registered (or before it turned internal) leaves
// behind, which the subscribe-time gate cannot retract. seedSubscriptionRow
// inserts directly under the control-plane role, bypassing that gate exactly as a
// pre-existing row would, so this reproduces the TOCTOU. The relay re-checks
// visibility at fan-out time: the internal event reaches nobody, while an
// equivalent tenant-visibility event is still delivered (proving the skip is
// specific to internal, not a blanket drop).
func TestPostgresRelaySuppressesInternalVisibilityFanOut(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	transport := infra.NewPostgresEventTransport(store, pool, "relay-visibility-"+uuid.NewString(), time.Second)

	// installation.created is declared internal in the composed catalog;
	// scope.granted is tenant-visible (module/.../eventcatalog/catalog_gen.go).
	subInternal := seedSubscriptionRow(t, "installation.created", "relay.visibility.internal."+relayToken(), events.DeliveryUnordered)
	subTenant := seedSubscriptionRow(t, "scope.granted", "relay.visibility.tenant."+relayToken(), events.DeliveryUnordered)

	tenant := uuid.NewString()
	internalEvent := &events.EventEnvelope{
		Id: uuid.NewString(), Type: "installation.created", Source: "urn:codefly:test/relay",
		Specversion: "1.0", TenantId: tenant, PartitionKey: tenant, Data: []byte(`{}`),
	}
	tenantEvent := &events.EventEnvelope{
		Id: uuid.NewString(), Type: "scope.granted", Source: "urn:codefly:test/relay",
		Specversion: "1.0", TenantId: tenant, PartitionKey: tenant, Data: []byte(`{}`),
	}
	require.NoError(t, transport.Publish(testCtx, nil, internalEvent))
	require.NoError(t, transport.Publish(testCtx, nil, tenantEvent))

	internalLeased, err := transport.Claim(testCtx, subInternal.Queue, 10)
	require.NoError(t, err)
	require.Empty(t, internalLeased, "an internal-visibility event is never delivered to a subscriber")

	tenantLeased, err := transport.Claim(testCtx, subTenant.Queue, 10)
	require.NoError(t, err)
	require.Len(t, tenantLeased, 1, "a tenant-visibility event is still delivered")
	require.Equal(t, tenantEvent.GetId(), tenantLeased[0].Envelope.GetId())
	require.NoError(t, transport.Ack(testCtx, tenantLeased[0].Token))
}
