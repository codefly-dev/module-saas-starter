package infra

import (
	"context"
	"fmt"
	"sort"
	"time"

	"accounts/pkg/eventcatalog"
	"accounts/pkg/events"
	eventsv1 "accounts/pkg/gen/saas/events/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PostgresEventOperations serves the payload-free domain-event administration
// surface (#494 P3) from the isolated app_job_worker pool. Like PostgresJobStore
// it derives every figure from the database, so all replicas present the same
// durable view, and the connection-pool boundary keeps request traffic from ever
// reaching event payloads or cross-tenant subscriptions: only counts, timings,
// and control-plane subscription metadata cross it.
type PostgresEventOperations struct {
	pool *pgxpool.Pool
}

// NewPostgresEventOperations wires the operations surface to the app_job_worker
// pool, which holds BYPASSRLS and the SELECT grants on domain_events,
// event_subscriptions, and job_messages that every query below relies on.
func NewPostgresEventOperations(pool *pgxpool.Pool) *PostgresEventOperations {
	return &PostgresEventOperations{pool: pool}
}

var _ events.Operations = (*PostgresEventOperations)(nil)

// liveEventSubscription is the metadata projection of a non-revoked subscription.
// It deliberately omits filter payload; only what the admin surface reports.
type liveEventSubscription struct {
	id          string
	principalID string
	typePattern string
	queue       string
	delivery    string
	createdAt   time.Time
}

type eventTypeAggregate struct {
	total       uint64
	unpublished uint64
	lastEventAt *time.Time
}

// GetEventOperations returns per-type counters merged with the declared catalog,
// outbox relay health, and per-queue dead-letter counts on the queues that live
// subscriptions consume. It is deliberately computed from the durable tables
// rather than one process's counters.
func (s *PostgresEventOperations) GetEventOperations(
	ctx context.Context,
	_ *eventsv1.GetEventOperationsRequest,
) (*eventsv1.GetEventOperationsResponse, error) {
	w, end := wool.StartSpan(ctx, "events.GetEventOperations")
	defer end()
	ctx = w.Context()

	var observedAt time.Time
	if err := s.pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&observedAt); err != nil {
		return nil, fmt.Errorf("observe event operations clock: %w", err)
	}

	runtime, err := s.aggregateByType(ctx)
	if err != nil {
		return nil, err
	}
	subscriptions, err := s.listLiveSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	relay, err := s.relayHealth(ctx)
	if err != nil {
		return nil, err
	}
	deadLetters, err := s.deadLettersByQueue(ctx, distinctQueues(subscriptions))
	if err != nil {
		return nil, err
	}

	response := &eventsv1.GetEventOperationsResponse{
		EventTypes: s.eventTypes(runtime, subscriptions),
		Relay:      relay,
		ObservedAt: timestamppb.New(observedAt),
	}
	for _, queue := range sortedKeys(deadLetters) {
		response.DeadLetters = append(response.DeadLetters, &eventsv1.QueueDeadLetters{
			Queue:      queue,
			DeadLetter: deadLetters[queue],
		})
	}
	return response, nil
}

// ListEventSubscriptions returns every live subscription across all principals,
// each annotated with the dead-letter depth of the queue it consumes.
func (s *PostgresEventOperations) ListEventSubscriptions(
	ctx context.Context,
	_ *eventsv1.ListEventSubscriptionsRequest,
) (*eventsv1.ListEventSubscriptionsResponse, error) {
	w, end := wool.StartSpan(ctx, "events.ListEventSubscriptions")
	defer end()
	ctx = w.Context()

	subscriptions, err := s.listLiveSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	deadLetters, err := s.deadLettersByQueue(ctx, distinctQueues(subscriptions))
	if err != nil {
		return nil, err
	}

	response := &eventsv1.ListEventSubscriptionsResponse{}
	for _, sub := range subscriptions {
		response.Subscriptions = append(response.Subscriptions, &eventsv1.EventSubscriptionSummary{
			Id:                    sub.id,
			SubscriberPrincipalId: sub.principalID,
			TypePattern:           sub.typePattern,
			Queue:                 sub.queue,
			Delivery:              sub.delivery,
			CreatedAt:             timestamppb.New(sub.createdAt),
			QueueDeadLetter:       deadLetters[sub.queue],
		})
	}
	return response, nil
}

// eventTypes merges the declared catalog (the spine, carrying visibility,
// publisher and major) with runtime aggregates, then appends any type that has
// been emitted but is not declared. Subscriber counts are the number of live
// subscriptions whose pattern matches the type.
func (s *PostgresEventOperations) eventTypes(
	runtime map[string]*eventTypeAggregate,
	subscriptions []liveEventSubscription,
) []*eventsv1.EventTypeSnapshot {
	declared := map[string]struct{}{}
	var snapshots []*eventsv1.EventTypeSnapshot
	for _, published := range eventcatalog.Published() {
		declared[published.Type] = struct{}{}
		snapshot := &eventsv1.EventTypeSnapshot{
			Type:        published.Type,
			Visibility:  published.Visibility,
			Publisher:   published.Namespace,
			Major:       uint32(published.Major),
			Subscribers: countSubscribers(published.Type, subscriptions),
		}
		applyRuntime(snapshot, runtime[published.Type])
		snapshots = append(snapshots, snapshot)
	}

	// Types observed in the outbox but absent from the catalog: surface them so
	// an undeclared emitter cannot hide, with empty catalog metadata.
	var undeclared []string
	for eventType := range runtime {
		if _, ok := declared[eventType]; !ok {
			undeclared = append(undeclared, eventType)
		}
	}
	sort.Strings(undeclared)
	for _, eventType := range undeclared {
		snapshot := &eventsv1.EventTypeSnapshot{
			Type:        eventType,
			Subscribers: countSubscribers(eventType, subscriptions),
		}
		applyRuntime(snapshot, runtime[eventType])
		snapshots = append(snapshots, snapshot)
	}
	return snapshots
}

func applyRuntime(snapshot *eventsv1.EventTypeSnapshot, aggregate *eventTypeAggregate) {
	if aggregate == nil {
		return
	}
	snapshot.TotalEvents = aggregate.total
	snapshot.Unpublished = aggregate.unpublished
	snapshot.LastEventAt = optionalTimestamp(aggregate.lastEventAt)
}

func countSubscribers(eventType string, subscriptions []liveEventSubscription) uint64 {
	var n uint64
	for _, sub := range subscriptions {
		if events.Matches(sub.typePattern, eventType) {
			n++
		}
	}
	return n
}

func (s *PostgresEventOperations) aggregateByType(ctx context.Context) (map[string]*eventTypeAggregate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT type,
		       COUNT(*),
		       COUNT(*) FILTER (WHERE published_at IS NULL),
		       MAX(created_at)
		FROM public.domain_events
		GROUP BY type`)
	if err != nil {
		return nil, fmt.Errorf("aggregate domain events by type: %w", err)
	}
	defer rows.Close()

	aggregates := map[string]*eventTypeAggregate{}
	for rows.Next() {
		var eventType string
		var total, unpublished int64
		var lastEventAt *time.Time
		if err := rows.Scan(&eventType, &total, &unpublished, &lastEventAt); err != nil {
			return nil, fmt.Errorf("scan event type aggregate: %w", err)
		}
		aggregates[eventType] = &eventTypeAggregate{
			total:       uint64(total),
			unpublished: uint64(unpublished),
			lastEventAt: lastEventAt,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("aggregate domain events by type: %w", err)
	}
	return aggregates, nil
}

func (s *PostgresEventOperations) relayHealth(ctx context.Context) (*eventsv1.EventRelayHealth, error) {
	var total, published, backlog int64
	var oldestUnpublishedAt *time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE published_at IS NOT NULL),
		       COUNT(*) FILTER (WHERE published_at IS NULL),
		       MIN(created_at) FILTER (WHERE published_at IS NULL)
		FROM public.domain_events`).Scan(&total, &published, &backlog, &oldestUnpublishedAt); err != nil {
		return nil, fmt.Errorf("observe event relay health: %w", err)
	}
	return &eventsv1.EventRelayHealth{
		TotalEvents:         uint64(total),
		PublishedEvents:     uint64(published),
		Backlog:             uint64(backlog),
		OldestUnpublishedAt: optionalTimestamp(oldestUnpublishedAt),
	}, nil
}

// listLiveSubscriptions materializes the live subscriptions the admin surface
// reports on, bounded by maxEventSubscriptionsRead. The row count grows with how
// many subscriptions callers create, so without a bound one admin request loads
// an unbounded result set into memory; the cap is far above any legitimate
// working set, and reaching it is logged rather than hidden.
func (s *PostgresEventOperations) listLiveSubscriptions(ctx context.Context) ([]liveEventSubscription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, subscriber_principal_id, type_pattern, queue, delivery, created_at
		FROM public.event_subscriptions
		WHERE revoked_at IS NULL
		ORDER BY created_at, id
		LIMIT $1`, maxEventSubscriptionsRead+1)
	if err != nil {
		return nil, fmt.Errorf("list live event subscriptions: %w", err)
	}
	defer rows.Close()

	var subscriptions []liveEventSubscription
	for rows.Next() {
		var sub liveEventSubscription
		if err := rows.Scan(
			&sub.id, &sub.principalID, &sub.typePattern, &sub.queue, &sub.delivery, &sub.createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan live event subscription: %w", err)
		}
		subscriptions = append(subscriptions, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list live event subscriptions: %w", err)
	}
	if len(subscriptions) > maxEventSubscriptionsRead {
		subscriptions = subscriptions[:maxEventSubscriptionsRead]
		wool.Get(ctx).In("events.operations").Warn("live event subscription list truncated at the read cap",
			wool.Field("cap", maxEventSubscriptionsRead))
	}
	return subscriptions, nil
}

// deadLettersByQueue counts inbox job_messages parked in dead_letter for the
// given consumer queues. The relay delivers one inbox job per subscription, so a
// dead-lettered delivery is a subscriber that could not accept an event.
func (s *PostgresEventOperations) deadLettersByQueue(ctx context.Context, queues []string) (map[string]uint64, error) {
	counts := map[string]uint64{}
	if len(queues) == 0 {
		return counts, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT queue, COUNT(*)
		FROM public.job_messages
		WHERE direction = 'inbox' AND state = 'dead_letter' AND queue = ANY($1::text[])
		GROUP BY queue`, queues)
	if err != nil {
		return nil, fmt.Errorf("count event dead letters: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var queue string
		var deadLetter int64
		if err := rows.Scan(&queue, &deadLetter); err != nil {
			return nil, fmt.Errorf("scan event dead letters: %w", err)
		}
		counts[queue] = uint64(deadLetter)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count event dead letters: %w", err)
	}
	// Report every consumer queue, including those with a clean (zero) depth, so
	// the admin surface lists all live queues rather than only failing ones.
	for _, queue := range queues {
		if _, ok := counts[queue]; !ok {
			counts[queue] = 0
		}
	}
	return counts, nil
}

func distinctQueues(subscriptions []liveEventSubscription) []string {
	seen := map[string]struct{}{}
	var queues []string
	for _, sub := range subscriptions {
		if _, ok := seen[sub.queue]; ok {
			continue
		}
		seen[sub.queue] = struct{}{}
		queues = append(queues, sub.queue)
	}
	return queues
}

func sortedKeys(counts map[string]uint64) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
