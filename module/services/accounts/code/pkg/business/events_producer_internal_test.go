package business

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/events"

	"github.com/google/uuid"
)

// lifecycleStore satisfies Store without a database: publishLifecycleEvent
// touches only the transport and the transaction handle on the context, so an
// embedded nil Store is never dereferenced.
type lifecycleStore struct{ Store }

const lifecycleQueue = "events.lifecycle.test"

// newLifecycleService wires a Service over a transport whose single subscription
// sweeps the saas.* types the lifecycle producer emits, so what a subscriber would
// receive is observable through the transport's own Claim API.
func newLifecycleService(t *testing.T, transport events.Transport) *Service {
	t.Helper()
	svc, err := NewService(lifecycleStore{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetModuleEventTransport(transport)
	return svc
}

func lifecycleTransport() *events.FakeTransport {
	return events.NewFakeTransport([]events.Subscription{{
		SubscriberPrincipalID: uuid.NewString(),
		TypePattern:           "saas.*",
		Queue:                 lifecycleQueue,
		Delivery:              events.DeliveryUnordered,
	}}, time.Second)
}

// TestPublishLifecycleEventPartitionsOnTheTenant pins the ordering domain of the
// first-party producer, which nothing covered before. The lifecycle events are
// named by the audit registry's EventType constants ("saas.scope.granted"),
// while the composed events catalog declares the unprefixed domain types
// ("scope.granted"), so resolving this producer's partition through the catalog
// silently yields no partition at all and drops the ordering guarantee this
// producer's contract states. The tenant partition is asserted here so that
// swap cannot happen unnoticed again.
func TestPublishLifecycleEventPartitionsOnTheTenant(t *testing.T) {
	transport := lifecycleTransport()
	svc := newLifecycleService(t, transport)

	const tenant = "org-lifecycle-1"
	if err := svc.publishLifecycleEvent(context.Background(), EventScopeGranted, tenant, "node-3", "actor-1", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("publishLifecycleEvent: %v", err)
	}

	leased, err := transport.Claim(context.Background(), lifecycleQueue, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(leased) != 1 {
		t.Fatalf("expected exactly one delivered event, got %d", len(leased))
	}
	if got := leased[0].Envelope.GetPartitionKey(); got != tenant {
		t.Fatalf("partition key = %q, want the tenant %q — an empty key disables ordered delivery entirely", got, tenant)
	}
}
