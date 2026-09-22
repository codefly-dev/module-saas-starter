// Package events is the SDK boundary for domain events. It owns the transport
// port every producer and consumer goes through, the CloudEvents envelope
// codec, an in-memory FakeTransport for unit tests, a PostgresTransport that
// maps the envelope onto the durable jobs platform, and the conformance suite
// every transport must pass. See EVENTS.md.
package events

import (
	"context"
	"errors"
	"time"

	eventsv1 "accounts/pkg/gen/saas/events/v1"
)

// EventEnvelope is the generated saas.events.v1 wire type. The SDK re-exports it
// so call sites read against the transport port without importing the generated
// package directly.
type EventEnvelope = eventsv1.EventEnvelope

// Delivery is the ordering guarantee a subscription requests.
type Delivery string

const (
	DeliveryOrdered   Delivery = "ordered"
	DeliveryUnordered Delivery = "unordered"
	// DeliveryWebhook is an outbound webhook endpoint. Its consumer is the shared
	// dispatcher rather than a queue a principal claims from, so it carries an
	// organization and an endpoint registration instead of a principal.
	DeliveryWebhook Delivery = "webhook"
)

// Subscription is a durable declaration that a subscriber receives events
// matching TypePattern on Queue. SubscriberPrincipalID names the principal the
// subscription belongs to, so a replay can be scoped to one subscriber.
type Subscription struct {
	ID                    string
	SubscriberPrincipalID string
	TypePattern           string
	Queue                 string
	Delivery              Delivery
	// OrgID is set only for DeliveryWebhook and confines the subscription to one
	// tenant's events; a module subscription is cross-tenant and leaves it empty.
	OrgID string
	// WebhookSubscriptionID names the endpoint registration a DeliveryWebhook
	// subscription delivers to.
	WebhookSubscriptionID string
}

// Leased is one at-least-once delivery of an envelope, fenced by an opaque
// Token the transport issued for this lease.
type Leased struct {
	Token    string
	Envelope *EventEnvelope
}

// ReplaySelector bounds a replay to one type within one tenant since a point in
// time. A zero TenantID replays across tenants for a platform principal. A
// non-empty SubscriberPrincipalID restricts the re-fan-out to subscriptions
// owned by that principal, so ReplayEvents re-delivers only to the caller's own
// subscriptions; empty re-fans to every matching subscription.
type ReplaySelector struct {
	Type                  string
	TenantID              string
	Since                 time.Time
	SubscriberPrincipalID string
}

// TxHandle is an opaque producer transaction. Publish is transactional when it
// is non-nil; each transport interprets the concrete type it understands.
type TxHandle any

// Transport stores and delivers envelopes. The Postgres outbox implements it
// today; a broker later implements the same port without touching a call site.
type Transport interface {
	Publish(ctx context.Context, tx TxHandle, e *EventEnvelope) error
	Claim(ctx context.Context, queue string, max int) ([]Leased, error)
	Heartbeat(ctx context.Context, token string) error
	Ack(ctx context.Context, token string) error
	Nack(ctx context.Context, token string, cause error, permanent bool) error
	Replay(ctx context.Context, sel ReplaySelector) (int, error)
}

var (
	// ErrUnknownToken is returned when a finalizer names a lease the transport
	// no longer holds, either because it never issued the token or because the
	// lease already expired or was finalized.
	ErrLeaseLost = errors.New("events: lease lost")
	// ErrInvalidEnvelope rejects an envelope that cannot be routed: missing id,
	// type, or source, or a tenant id that is not a scope the transport accepts.
	ErrInvalidEnvelope = errors.New("events: invalid envelope")
	// ErrIdempotencyConflict means an id was republished with a different
	// envelope. The id is the idempotency key at every hop, so the same id must
	// carry the same fact; every transport rejects a reuse that does not.
	ErrIdempotencyConflict = errors.New("events: id reused with a different envelope")
)

// Matches reports whether a subscription type pattern matches an event type. A
// pattern is either exact or a single trailing ".*" that matches any type
// sharing the prefix before it. A "*" anywhere else is never valid.
func Matches(pattern, eventType string) bool {
	if prefix, found := trailingWildcard(pattern); found {
		return eventType == prefix || hasDotPrefix(eventType, prefix)
	}
	return pattern == eventType
}

func trailingWildcard(pattern string) (string, bool) {
	const suffix = ".*"
	if len(pattern) <= len(suffix) || pattern[len(pattern)-len(suffix):] != suffix {
		return "", false
	}
	return pattern[:len(pattern)-len(suffix)], true
}

func hasDotPrefix(value, prefix string) bool {
	return len(value) > len(prefix)+1 && value[:len(prefix)] == prefix && value[len(prefix)] == '.'
}
