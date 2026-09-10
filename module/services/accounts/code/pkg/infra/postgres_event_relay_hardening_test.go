package infra_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/events"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// newRelayTransport builds a transport over its own worker pool, the shape every
// relay test needs.
func newRelayTransport(t *testing.T, name string) (*pgxpool.Pool, *infra.PostgresEventTransport) {
	t.Helper()
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)
	return pool, infra.NewPostgresEventTransport(store, pool, name+"-"+uuid.NewString(), time.Second)
}

func relayTestEvent(eventType, source, tenant string, n int) *events.EventEnvelope {
	return &events.EventEnvelope{
		Id: uuid.NewString(), Type: eventType, Source: source, Specversion: "1.0",
		TenantId: tenant, PartitionKey: tenant, Data: []byte(fmt.Sprintf(`{"n":%d}`, n)),
	}
}

// seedConflictingDelivery pre-enqueues an inbox job carrying the exact
// idempotency tuple the relay will derive for (event, subscription) but a
// different fact, so the relay's own enqueue returns ErrIdempotencyConflict.
// That makes the event a deterministic poison: it fails identically on every
// attempt, which is the only way to observe the retry budget being spent. The
// argument order and casts mirror enqueuePreparedJob's call.
func seedConflictingDelivery(t *testing.T, pool *pgxpool.Pool, e *events.EventEnvelope, eventType, source, queue, subscriptionID string) {
	t.Helper()
	_, err := pool.Exec(testCtx, `SELECT public.enqueue_job_message(
		$1::text,$2::text,$3::uuid,$4::uuid,$5::text,$6::text,$7::text,$8::text,
		$9::text,$10::int,$11::bytea,$12::text,$13::jsonb,$14::smallint,$15::int,
		$16::timestamptz,$17::bytea)`,
		"inbox", "tenant", e.GetTenantId(), nil, queue, eventType, source,
		e.GetId()+":"+subscriptionID, nil, int32(1), []byte(`{"conflict":true}`),
		"application/protobuf", "{}", int16(0), int32(5), nil,
		bytes.Repeat([]byte{0xEE}, 32),
	)
	require.NoError(t, err)
}

// eventRelayState reads the relay bookkeeping migration 121 added.
func eventRelayState(t *testing.T, pool *pgxpool.Pool, eventID string) (attempts int, deadLettered bool) {
	t.Helper()
	require.NoError(t, pool.QueryRow(testCtx,
		`SELECT relay_attempts, dead_lettered_at IS NOT NULL
		 FROM public.domain_events WHERE id = $1`, eventID).Scan(&attempts, &deadLettered))
	return attempts, deadLettered
}

// TestPostgresPublishOrdersConcurrentSamePartitionCommits is the F-ordering
// regression, and it is the guarantee business/events_producer.go prints: an
// ordered subscriber sees one tenant's lifecycle in the order it happened.
//
// domain_events.seq is GENERATED ALWAYS AS IDENTITY, so it is handed out at
// INSERT and not at COMMIT. Two producers writing the same tenant concurrently
// could therefore commit in the opposite order to their seq: the second producer
// took the higher seq but committed first, and a relay tick landing in the gap
// fanned the higher seq out while the lower one was still invisible. The
// subscriber then received the two events backwards, permanently — nothing later
// repairs the order.
//
// The test drives exactly that interleave. The first producer publishes and does
// NOT commit; the second publishes on its own connection and would, without the
// partition advisory lock, insert and commit immediately. Its publish must block
// until the first commits, which is what makes seq order equal commit order.
func TestPostgresPublishOrdersConcurrentSamePartitionCommits(t *testing.T) {
	pool, transport := newRelayTransport(t, "relay-concurrent")

	const source = "urn:codefly:test/relay"
	eventType := "relay.concurrent." + relayToken()
	sub := seedSubscriptionRow(t, eventType, "relay.concurrent.q."+relayToken(), events.DeliveryOrdered)

	tenant := uuid.NewString()
	first := relayTestEvent(eventType, source, tenant, 1)
	second := relayTestEvent(eventType, source, tenant, 2)

	// Producer A opens its transaction and publishes, taking the partition lock.
	txA, err := pool.Begin(testCtx)
	require.NoError(t, err)
	require.NoError(t, transport.Publish(testCtx, txA, first))

	// Producer B races it on another connection. It must not be able to finish
	// while A holds the partition.
	secondDone := make(chan error, 1)
	go func() {
		txB, beginErr := pool.Begin(testCtx)
		if beginErr != nil {
			secondDone <- beginErr
			return
		}
		if publishErr := transport.Publish(testCtx, txB, second); publishErr != nil {
			_ = txB.Rollback(testCtx)
			secondDone <- publishErr
			return
		}
		secondDone <- txB.Commit(testCtx)
	}()

	select {
	case err := <-secondDone:
		// Without the lock this is where the test ends: B took seq=n+1 and
		// committed ahead of A, which still holds seq=n uncommitted.
		t.Fatalf("the later same-partition publish must not complete while the earlier one is in flight (got %v)", err)
	case <-time.After(time.Second):
	}

	require.NoError(t, txA.Commit(testCtx))
	require.NoError(t, <-secondDone, "the blocked publish must succeed once the partition is free")

	relayed, err := transport.RelayOnce(testCtx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, relayed, 2)

	// An ordered partition exposes one head event at a time; the delivery order
	// must be the order the producers committed in.
	acked := make([]string, 0, 2)
	deadline := time.Now().Add(20 * time.Second)
	for len(acked) < 2 && time.Now().Before(deadline) {
		leased, err := transport.Claim(testCtx, sub.Queue, 5)
		require.NoError(t, err)
		require.LessOrEqual(t, len(leased), 1, "an ordered partition never exposes more than one event at a time")
		if len(leased) == 0 {
			continue
		}
		require.NoError(t, transport.Ack(testCtx, leased[0].Token))
		acked = append(acked, leased[0].Envelope.GetId())
	}
	require.Equal(t, []string{first.GetId(), second.GetId()}, acked,
		"concurrent same-partition publishes must be delivered in commit order")
}

// TestPostgresPublishDoesNotSerializeUnpartitionedTenantWrites is the other half
// of the ordering guarantee above, and the reason a partition has to be opt-in.
// pg_advisory_xact_lock is transaction-scoped: it is held until the producing
// transaction commits, not until the insert returns. So a partition every event
// of a tenant shares would queue that tenant's publishes behind whatever long
// mutation each producer happens to be inside — and two producers each holding a
// row the other wants would deadlock on the publish itself.
//
// An event that declares no partition takes no lock. The test drives the exact
// interleave the partitioned test asserts blocking on, and requires the opposite:
// the second producer runs to commit while the first's transaction is still open.
func TestPostgresPublishDoesNotSerializeUnpartitionedTenantWrites(t *testing.T) {
	pool, transport := newRelayTransport(t, "relay-unpartitioned")

	const source = "urn:codefly:test/relay"
	eventType := "relay.unpartitioned." + relayToken()

	tenant := uuid.NewString()
	first := relayTestEvent(eventType, source, tenant, 1)
	first.PartitionKey = ""
	second := relayTestEvent(eventType, source, tenant, 2)
	second.PartitionKey = ""

	// Producer A publishes and stays open, standing in for a publish inside a
	// long-running mutation.
	txA, err := pool.Begin(testCtx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = txA.Rollback(testCtx) })
	require.NoError(t, transport.Publish(testCtx, txA, first))

	secondDone := make(chan error, 1)
	go func() {
		txB, beginErr := pool.Begin(testCtx)
		if beginErr != nil {
			secondDone <- beginErr
			return
		}
		if publishErr := transport.Publish(testCtx, txB, second); publishErr != nil {
			_ = txB.Rollback(testCtx)
			secondDone <- publishErr
			return
		}
		secondDone <- txB.Commit(testCtx)
	}()

	select {
	case err := <-secondDone:
		require.NoError(t, err, "the unpartitioned publish must succeed on its own")
	case <-time.After(10 * time.Second):
		t.Fatal("an unpartitioned publish blocked behind another producer's open transaction in the same tenant: it took a partition lock its event never declared")
	}

	require.NoError(t, txA.Commit(testCtx))

	// Both rows are stored unpartitioned, so nothing downstream advertises an
	// ordering the producer did not provide.
	for _, e := range []*events.EventEnvelope{first, second} {
		var partition string
		require.NoError(t, pool.QueryRow(testCtx,
			`SELECT partition_key FROM public.domain_events WHERE id = $1`, e.GetId()).Scan(&partition))
		require.Empty(t, partition, "an event published with no partition key must be stored with none")
	}
}

// TestPostgresRelayDeadLettersPoisonEventAndReleasesItsPartition is the
// permanent-stall regression. A deterministically-failing event was rolled back
// and left unpublished on every tick forever, and because a failed event holds
// its ordered partition back, every later event for that tenant queued up behind
// it — silently, with no counter, no terminal state, and no way out but manual
// surgery. The relay now charges each failure to the row and parks the event
// once its budget is spent, which releases the partition.
func TestPostgresRelayDeadLettersPoisonEventAndReleasesItsPartition(t *testing.T) {
	pool, transport := newRelayTransport(t, "relay-deadletter")

	const source = "urn:codefly:test/relay"
	eventType := "relay.deadletter." + relayToken()
	sub := seedSubscriptionRow(t, eventType, "relay.deadletter.q."+relayToken(), events.DeliveryOrdered)

	tenant := uuid.NewString()
	poison := relayTestEvent(eventType, source, tenant, 1)
	follower := relayTestEvent(eventType, source, tenant, 2)
	seedConflictingDelivery(t, pool, poison, eventType, source, sub.Queue, sub.ID)

	// One producer transaction, so a single relay batch sees both.
	tx, err := pool.Begin(testCtx)
	require.NoError(t, err)
	for _, e := range []*events.EventEnvelope{poison, follower} {
		require.NoError(t, transport.Publish(testCtx, tx, e))
	}
	require.NoError(t, tx.Commit(testCtx))

	// The first drain isolates the poison and holds its partition: the follower
	// is owed order behind it, so it must not be delivered yet.
	_, err = transport.RelayOnce(testCtx)
	require.NoError(t, err)
	attempts, deadLettered := eventRelayState(t, pool, poison.GetId())
	require.Equal(t, 1, attempts, "one drain must cost the event exactly one attempt")
	require.False(t, deadLettered, "one failure must not exhaust the budget")
	require.False(t, eventPublished(t, pool, follower.GetId()),
		"an ordered follower waits while the poison still has attempts left")

	// Keep draining. Each drain costs one more attempt until the budget is spent.
	// The bound is generous so the test does not encode the exact budget.
	parked := false
	for i := 0; i < 20 && !parked; i++ {
		_, err = transport.RelayOnce(testCtx)
		require.NoError(t, err)
		_, parked = eventRelayState(t, pool, poison.GetId())
	}
	require.True(t, parked, "a deterministically-failing event must eventually be dead-lettered")

	// Parking releases the partition, so the follower now drains normally.
	_, err = transport.RelayOnce(testCtx)
	require.NoError(t, err)
	require.True(t, eventPublished(t, pool, follower.GetId()),
		"parking the poison must release its partition")
	require.True(t, deliveryExists(t, pool, follower.GetId()+":"+sub.ID),
		"the follower must be delivered once the poison is parked")

	// The parked event stays unpublished and out of the relay's way — durable,
	// visible, and recoverable through Replay rather than silently dropped.
	require.False(t, eventPublished(t, pool, poison.GetId()),
		"a parked event is not published; it was never fanned out")

	// Drain once and judge the whole delivery set: a second drain would find an
	// empty queue and make any negative assertion pass for the wrong reason.
	delivered := claimedEventIDs(t, transport, sub.Queue)
	require.True(t, delivered[follower.GetId()],
		"the follower must actually be claimable once the poison is parked")
	require.False(t, delivered[poison.GetId()],
		"a parked event is never delivered to a subscriber")
}

// claimedEventIDs drains a queue and returns the set of event ids it delivered,
// acking each one. It reports a set rather than a list because these tests
// pre-seed a conflicting job on the same queue to poison the fan-out, and that
// job is itself claimable — so what matters is which events arrived, not how
// many messages the queue held.
func claimedEventIDs(t *testing.T, transport *infra.PostgresEventTransport, queue string) map[string]bool {
	t.Helper()
	delivered := map[string]bool{}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		leased, err := transport.Claim(testCtx, queue, 10)
		require.NoError(t, err)
		if len(leased) == 0 {
			break
		}
		for _, l := range leased {
			delivered[l.Envelope.GetId()] = true
			require.NoError(t, transport.Ack(testCtx, l.Token))
		}
	}
	return delivered
}

// TestPostgresRelayUnorderedFailureDoesNotBlockItsPartition is the sibling of the
// F4 poison test and asserts the opposite outcome for the opposite promise. The
// relay used to block a failed event's partition unconditionally, so a failure on
// an event whose only subscribers were UNORDERED still held back every later
// event sharing its partition key — head-of-line blocking imposed on a delivery
// mode whose entire contract is that order does not matter. Only an event with an
// ordered subscriber may hold its partition.
func TestPostgresRelayUnorderedFailureDoesNotBlockItsPartition(t *testing.T) {
	pool, transport := newRelayTransport(t, "relay-unordered")

	const source = "urn:codefly:test/relay"
	eventType := "relay.unordered." + relayToken()
	sub := seedSubscriptionRow(t, eventType, "relay.unordered.q."+relayToken(), events.DeliveryUnordered)

	tenant := uuid.NewString()
	poison := relayTestEvent(eventType, source, tenant, 1)
	follower := relayTestEvent(eventType, source, tenant, 2)
	seedConflictingDelivery(t, pool, poison, eventType, source, sub.Queue, sub.ID)

	tx, err := pool.Begin(testCtx)
	require.NoError(t, err)
	for _, e := range []*events.EventEnvelope{poison, follower} {
		require.NoError(t, transport.Publish(testCtx, tx, e))
	}
	require.NoError(t, tx.Commit(testCtx))

	_, err = transport.RelayOnce(testCtx)
	require.NoError(t, err, "a poison event must not fail the whole relay pass")

	require.False(t, eventPublished(t, pool, poison.GetId()),
		"the poison event still stays unpublished for a later attempt")
	require.True(t, eventPublished(t, pool, follower.GetId()),
		"an unordered follower is owed no order and must not wait behind the poison")
	require.True(t, deliveryExists(t, pool, follower.GetId()+":"+sub.ID),
		"the unordered follower must be delivered")

	delivered := claimedEventIDs(t, transport, sub.Queue)
	require.True(t, delivered[follower.GetId()],
		"the unordered follower must be claimable without waiting for the poison")
	require.False(t, delivered[poison.GetId()],
		"the poison event itself is still undelivered")
}

// TestPostgresReplayWalksEveryPage covers the unbounded-read fix. Replay used to
// materialize every row matching its selector at once and fan them all out in a
// single transaction, so a wide selector — a platform-scope replay, or any tenant
// with a long retained history — was an unbounded allocation behind an unbounded
// transaction. It now walks the history in bounded pages keyed on seq, and the
// walk has to cross page boundaries without skipping or repeating a row.
func TestPostgresReplayWalksEveryPage(t *testing.T) {
	pool, transport := newRelayTransport(t, "relay-replaypage")

	// Two per page over five events: the walk must cross three pages, and the last
	// is short, which is how it learns to stop.
	infra.SetEventReplayPageSizeForTest(t, 2)

	const source = "urn:codefly:test/relay"
	const count = 5
	eventType := "relay.replaypage." + relayToken()
	sub := seedSubscriptionRow(t, eventType, "relay.replaypage.q."+relayToken(), events.DeliveryUnordered)

	tenant := uuid.NewString()
	published := make([]string, 0, count)
	for i := 0; i < count; i++ {
		e := relayTestEvent(eventType, source, tenant, i)
		// nil caller tx: each publish commits and drains inline, so the original
		// delivery already exists before the replay below.
		require.NoError(t, transport.Publish(testCtx, nil, e))
		published = append(published, e.GetId())
	}

	replayed, err := transport.Replay(testCtx, events.ReplaySelector{Type: eventType, TenantID: tenant})
	require.NoError(t, err)
	require.Equal(t, count, replayed,
		"every page of matching history must be replayed exactly once")

	// Each event now carries its original delivery plus one replay delivery. A
	// cursor that failed to advance would repeat a page; one that advanced too far
	// would skip events. Both show up as the wrong count here.
	var deliveries int
	require.NoError(t, pool.QueryRow(testCtx,
		`SELECT COUNT(*) FROM public.job_messages WHERE topic = $1`, eventType).Scan(&deliveries))
	require.Equal(t, 2*count, deliveries,
		"one original and exactly one replay delivery per event")

	for _, id := range published {
		require.True(t, deliveryExists(t, pool, id+":"+sub.ID),
			"the original delivery of %s must still exist", id)
	}
}

// TestListEventSubscriptionsIsBoundedByTheReadCap covers the unbounded-read half
// of the control-plane surface. Subscription rows are created by ordinary
// Subscribe calls, so their number tracks caller behaviour rather than any fixed
// platform dimension, and the list read had no bound at all: one request
// materialized every row a principal owned. The cap sits far above any real
// working set, but it has to actually apply.
func TestListEventSubscriptionsIsBoundedByTheReadCap(t *testing.T) {
	infra.SetMaxEventSubscriptionsReadForTest(t, 2)

	principal := uuid.NewString()
	queue := "sub.cap.q." + relayToken()
	for i := 0; i < 3; i++ {
		pattern := fmt.Sprintf("subcap.%s.n%d", relayToken(), i)
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			_, _, err := testStore.CreateEventSubscription(ctx, &business.EventSubscription{
				SubscriberPrincipalID: principal,
				TypePattern:           pattern,
				Queue:                 queue,
				Delivery:              string(events.DeliveryUnordered),
			})
			return err
		}))
	}

	var listed []*business.EventSubscription
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		listed, err = testStore.ListEventSubscriptions(ctx, principal)
		return err
	}))
	require.Len(t, listed, 2, "the read must stop at the cap rather than returning every row")
}
