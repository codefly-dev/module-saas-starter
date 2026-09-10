package business_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/events"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeSubStore is an in-memory event_subscriptions control plane for the
// authority tests: it satisfies the three domain-event subscription methods on
// business.Store (all WithControlPlane in production) without a database, and
// mirrors the real active-unique idempotency on (subscriber, pattern, queue) so
// a re-subscribe or a re-materialization returns the existing row (inserted =
// false). The embedded fakeTxStore supplies the transaction seam.
type fakeSubStore struct {
	fakeTxStore
	rows []*business.EventSubscription
}

func (f *fakeSubStore) CreateEventSubscription(_ context.Context, sub *business.EventSubscription) (*business.EventSubscription, bool, error) {
	for _, existing := range f.rows {
		if existing.SubscriberPrincipalID == sub.SubscriberPrincipalID &&
			existing.TypePattern == sub.TypePattern &&
			existing.Queue == sub.Queue {
			return existing, false, nil // idempotent re-affirm of a live row
		}
	}
	row := &business.EventSubscription{
		ID:                    uuid.NewString(),
		SubscriberPrincipalID: sub.SubscriberPrincipalID,
		TypePattern:           sub.TypePattern,
		Queue:                 sub.Queue,
		Delivery:              sub.Delivery,
		CreatedBy:             sub.CreatedBy,
		CreatedAt:             time.Now().UTC(),
	}
	f.rows = append(f.rows, row)
	return row, true, nil
}

func (f *fakeSubStore) ListEventSubscriptions(_ context.Context, subscriberPrincipalID string) ([]*business.EventSubscription, error) {
	var out []*business.EventSubscription
	for _, row := range f.rows {
		if row.SubscriberPrincipalID == subscriberPrincipalID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeSubStore) CountLiveEventSubscriptions(context.Context) (int, error) {
	return len(f.rows), nil
}

// newEventService wires a Service with the domain-event transport and a registry
// granting the module principal the `reference` and `scope` namespaces (publish)
// and the `reference.ingest` + two demo queues (subscribe/deliver) on its own
// tenant only. store carries the subscription control plane.
func newEventService(t *testing.T, store business.Store, transport events.Transport) *business.Service {
	t.Helper()
	svc, err := business.NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetModuleCapabilities(&fakeJobBackend{}, &fakeJobBackend{}, business.ModulePrincipalRegistry{
		modulePrincSvc: {
			Queues:     []string{"reference.ingest", "events.demo.a", "events.demo.b"},
			Namespaces: []string{"reference", "scope"},
		},
	})
	svc.SetModuleEventTransport(transport)
	return svc
}

// TestVerifyEventWiring is the startup-invariant guard: a module that has
// accepted subscriptions must have a delivery transport wired, or every publish
// would be a silent no-op and subscribers would receive nothing.
func TestVerifyEventWiring(t *testing.T) {
	// A wired transport passes without consulting the store at all. The store
	// here embeds a nil business.Store, so any store access would panic — proving
	// the healthy path short-circuits before the count query.
	t.Run("transport wired short-circuits before the store", func(t *testing.T) {
		svc, err := business.NewService(fakeTxStore{})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		svc.SetModuleEventTransport(events.NewFakeTransport(nil, time.Second))
		if err := svc.VerifyEventWiring(context.Background()); err != nil {
			t.Fatalf("wired transport must verify clean: %v", err)
		}
	})

	// No transport and no subscriptions is a legitimate no-eventing deployment.
	t.Run("no transport and no subscriptions is allowed", func(t *testing.T) {
		svc, err := business.NewService(&fakeSubStore{})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		if err := svc.VerifyEventWiring(context.Background()); err != nil {
			t.Fatalf("no transport with zero subscriptions must verify clean: %v", err)
		}
	})

	// No transport WITH live subscriptions is the silent-loss misconfiguration:
	// startup must refuse it.
	t.Run("no transport with live subscriptions fails", func(t *testing.T) {
		store := &fakeSubStore{rows: []*business.EventSubscription{{
			ID:                    uuid.NewString(),
			SubscriberPrincipalID: modulePrincSvc,
			TypePattern:           "reference.*",
			Queue:                 "reference.ingest",
			Delivery:              "unordered",
		}}}
		svc, err := business.NewService(store)
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		if err := svc.VerifyEventWiring(context.Background()); err == nil {
			t.Fatal("live subscriptions with no transport must fail startup, got nil")
		}
	})
}

func demoEnvelope(eventType string) *events.EventEnvelope {
	return &events.EventEnvelope{
		Id:           uuid.NewString(),
		Type:         eventType,
		Source:       "saas.accounts",
		Specversion:  "1.0",
		Time:         timestamppb.New(time.Now().UTC()),
		PartitionKey: moduleTenantA,
		Data:         []byte(`{"k":"v"}`),
	}
}

// TestModulePublishEventNamespaceAndTenantAuthority is the acceptance guard: a
// publish outside the caller's declared namespace, or on a tenant it is not
// bound to, is PermissionDenied; a publish inside both is accepted and the
// authoritative tenant is stamped onto the envelope regardless of what the
// client wrote.
func TestModulePublishEventNamespaceAndTenantAuthority(t *testing.T) {
	transport := events.NewFakeTransport(nil, time.Second)
	svc := newEventService(t, fakeTxStore{}, transport)
	caller := business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: moduleTenantA}

	// installation.* is not a namespace this principal declares.
	_, err := svc.ModulePublishEvent(context.Background(), caller, moduleTenantA, demoEnvelope("installation.created"))
	requireCode(t, err, codes.PermissionDenied)

	// scope is declared, but tenant B is not this caller's bound tenant.
	_, err = svc.ModulePublishEvent(context.Background(), caller, moduleTenantB, demoEnvelope("scope.granted"))
	requireCode(t, err, codes.PermissionDenied)

	// scope + bound tenant: accepted, and the envelope tenant is forced to the
	// authorized tenant even though the client left it unset.
	env := demoEnvelope("scope.granted")
	id, err := svc.ModulePublishEvent(context.Background(), caller, moduleTenantA, env)
	if err != nil {
		t.Fatalf("in-namespace publish on bound tenant: %v", err)
	}
	if id != env.GetId() {
		t.Fatalf("returned id %q != envelope id %q", id, env.GetId())
	}
	if env.GetTenantId() != moduleTenantA {
		t.Fatalf("envelope tenant not forced to authorized tenant: %q", env.GetTenantId())
	}
}

// TestModulePublishEventPartitionKeyFollowsTheCatalog pins where a publish's
// ordering domain comes from when the caller does not name one: the event
// type's declared partition template, not the tenant. A partition is paid for
// with an advisory lock held for the producing transaction, so a type that
// declares none must publish unordered rather than serialize the tenant's
// publishes behind an ordering nobody asked for. A key the caller set
// deliberately still wins over the declaration.
func TestModulePublishEventPartitionKeyFollowsTheCatalog(t *testing.T) {
	svc := newEventService(t, fakeTxStore{}, events.NewFakeTransport(nil, time.Second))
	caller := business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: moduleTenantA}

	// scope.granted declares partition "{tenant_id}", which resolves to the
	// publishing tenant.
	declared := demoEnvelope("scope.granted")
	declared.PartitionKey = ""
	if _, err := svc.ModulePublishEvent(context.Background(), caller, moduleTenantA, declared); err != nil {
		t.Fatalf("publish with empty partition key: %v", err)
	}
	if declared.GetPartitionKey() != moduleTenantA {
		t.Fatalf("declared {tenant_id} partition must resolve to the tenant, got %q", declared.GetPartitionKey())
	}

	// scope.unregistered is inside the caller's namespace but carries no catalog
	// declaration, so it promises no ordering and must take no partition.
	undeclared := demoEnvelope("scope.unregistered")
	undeclared.PartitionKey = ""
	if _, err := svc.ModulePublishEvent(context.Background(), caller, moduleTenantA, undeclared); err != nil {
		t.Fatalf("publish of an undeclared-partition type: %v", err)
	}
	if undeclared.GetPartitionKey() != "" {
		t.Fatalf("an event declaring no partition must publish unpartitioned, got %q", undeclared.GetPartitionKey())
	}

	fine := demoEnvelope("scope.granted")
	fine.PartitionKey = "scope/aggregate-7"
	if _, err := svc.ModulePublishEvent(context.Background(), caller, moduleTenantA, fine); err != nil {
		t.Fatalf("publish with explicit partition key: %v", err)
	}
	if fine.GetPartitionKey() != "scope/aggregate-7" {
		t.Fatalf("explicit partition key must be preserved, got %q", fine.GetPartitionKey())
	}
}

// TestModuleSubscribeInternalVisibilityDenied proves a solution principal can
// never subscribe a pattern that matches an internal-visibility published type,
// even on a queue it holds — internal events stay intra-platform. A trailing
// wildcard that would sweep in an internal type is rejected too.
func TestModuleSubscribeInternalVisibilityDenied(t *testing.T) {
	svc := newEventService(t, &fakeSubStore{}, events.NewFakeTransport(nil, time.Second))
	caller := business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: moduleTenantA}

	_, err := svc.ModuleSubscribe(context.Background(), caller, "installation.created", "reference.ingest", "unordered")
	requireCode(t, err, codes.PermissionDenied)

	_, err = svc.ModuleSubscribe(context.Background(), caller, "installation.*", "reference.ingest", "unordered")
	requireCode(t, err, codes.PermissionDenied)
}

// TestModuleSubscribeQueueNotGranted proves the queue grant gates Subscribe: a
// principal may only bind a subscription to a queue it holds.
func TestModuleSubscribeQueueNotGranted(t *testing.T) {
	svc := newEventService(t, &fakeSubStore{}, events.NewFakeTransport(nil, time.Second))
	caller := business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: moduleTenantA}

	_, err := svc.ModuleSubscribe(context.Background(), caller, "scope.granted", "someone.elses.queue", "unordered")
	requireCode(t, err, codes.PermissionDenied)
}

// TestModuleSubscribeTenantVisibleCreatesRow proves a tenant-visible type on a
// granted queue subscribes, and a re-subscribe of the same shape is idempotent.
func TestModuleSubscribeTenantVisibleCreatesRow(t *testing.T) {
	store := &fakeSubStore{}
	svc := newEventService(t, store, events.NewFakeTransport(nil, time.Second))
	caller := business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: moduleTenantA}

	first, err := svc.ModuleSubscribe(context.Background(), caller, "scope.granted", "reference.ingest", "unordered")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	second, err := svc.ModuleSubscribe(context.Background(), caller, "scope.granted", "reference.ingest", "unordered")
	if err != nil {
		t.Fatalf("re-subscribe: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("re-subscribe created a second row: %q != %q", first.ID, second.ID)
	}
	if len(store.rows) != 1 {
		t.Fatalf("expected exactly one subscription row, got %d", len(store.rows))
	}
}

// TestPublishOnceTwoConsumersEachOnceUnsubscribedNever is the headline
// acceptance case: one publish fans out to every subscribed queue exactly once
// and never to a queue nobody subscribed, and a consumed delivery is not
// redelivered.
func TestPublishOnceTwoConsumersEachOnceUnsubscribedNever(t *testing.T) {
	const eventType = "scope.granted"
	transport := events.NewFakeTransport([]events.Subscription{
		{SubscriberPrincipalID: uuid.NewString(), TypePattern: eventType, Queue: "events.demo.a", Delivery: events.DeliveryUnordered},
		{SubscriberPrincipalID: uuid.NewString(), TypePattern: eventType, Queue: "events.demo.b", Delivery: events.DeliveryUnordered},
	}, time.Second)
	svc := newEventService(t, fakeTxStore{}, transport)
	caller := business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: moduleTenantA}

	if _, err := svc.ModulePublishEvent(context.Background(), caller, moduleTenantA, demoEnvelope(eventType)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	a := claimOne(t, transport, "events.demo.a")
	b := claimOne(t, transport, "events.demo.b")
	if a == nil || b == nil {
		t.Fatalf("both subscribed consumers must receive the event (a=%v b=%v)", a != nil, b != nil)
	}
	// A queue nobody subscribed never receives the event.
	if got, _ := transport.Claim(context.Background(), "events.demo.unsubscribed", 10); len(got) != 0 {
		t.Fatalf("an unsubscribed queue received %d deliveries", len(got))
	}
	// After each consumer acks, there is nothing more to claim — exactly once.
	if err := transport.Ack(context.Background(), a.Token); err != nil {
		t.Fatalf("ack a: %v", err)
	}
	if err := transport.Ack(context.Background(), b.Token); err != nil {
		t.Fatalf("ack b: %v", err)
	}
	if got, _ := transport.Claim(context.Background(), "events.demo.a", 10); len(got) != 0 {
		t.Fatalf("consumer a saw a redelivery of an acked event")
	}
}

// TestReplayScopedToRequestingSubscriber proves ReplayEvents re-delivers only to
// the subscriptions owned by the calling principal — a second principal's queue
// on the same type is untouched by the first principal's replay.
func TestReplayScopedToRequestingSubscriber(t *testing.T) {
	const eventType = "scope.granted"
	callerPrincipal := modulePrincSvc
	otherPrincipal := uuid.NewString()
	transport := events.NewFakeTransport([]events.Subscription{
		{SubscriberPrincipalID: callerPrincipal, TypePattern: eventType, Queue: "events.demo.a", Delivery: events.DeliveryUnordered},
		{SubscriberPrincipalID: otherPrincipal, TypePattern: eventType, Queue: "events.demo.b", Delivery: events.DeliveryUnordered},
	}, time.Second)
	svc := newEventService(t, fakeTxStore{}, transport)
	caller := business.ModuleCaller{PrincipalID: callerPrincipal, BoundOrg: moduleTenantA}

	if _, err := svc.ModulePublishEvent(context.Background(), caller, moduleTenantA, demoEnvelope(eventType)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Drain the initial fan-out from both queues.
	claimOne(t, transport, "events.demo.a")
	claimOne(t, transport, "events.demo.b")

	redelivered, err := svc.ModuleReplayEvents(context.Background(), caller, moduleTenantA, eventType, time.Time{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if redelivered < 1 {
		t.Fatalf("replay reported %d redelivered, want >= 1", redelivered)
	}
	if got := claimOne(t, transport, "events.demo.a"); got == nil {
		t.Fatalf("the requesting subscriber's queue must receive the replay")
	}
	if got, _ := transport.Claim(context.Background(), "events.demo.b", 10); len(got) != 0 {
		t.Fatalf("replay leaked to another principal's subscription (%d deliveries)", len(got))
	}
}

// TestMaterializeSubscriptionsFromCatalog proves the compose/install grant: a
// solution consuming the `reference` namespace materializes exactly the catalog
// `consumes` entry as a durable subscription for its principal, an
// internal-only namespace materializes nothing, and re-materialization is
// idempotent.
func TestMaterializeSubscriptionsFromCatalog(t *testing.T) {
	store := &fakeSubStore{}
	svc := newEventService(t, store, events.NewFakeTransport(nil, time.Second))
	principal := uuid.NewString()

	if err := svc.MaterializeSubscriptionsFromCatalog(context.Background(), principal, principal, []string{"reference"}); err != nil {
		t.Fatalf("materialize reference: %v", err)
	}
	subs, _ := store.ListEventSubscriptions(context.Background(), principal)
	if len(subs) != 1 {
		t.Fatalf("reference consume should materialize exactly one subscription, got %d", len(subs))
	}
	if subs[0].TypePattern != "reference.console.viewed" || subs[0].Queue != "reference.ingest" {
		t.Fatalf("unexpected materialized subscription: %+v", subs[0])
	}

	// installation publishes only internal types and declares no consumes, so it
	// materializes nothing for a fresh principal.
	other := uuid.NewString()
	if err := svc.MaterializeSubscriptionsFromCatalog(context.Background(), other, other, []string{"installation"}); err != nil {
		t.Fatalf("materialize installation: %v", err)
	}
	if subs, _ := store.ListEventSubscriptions(context.Background(), other); len(subs) != 0 {
		t.Fatalf("an internal/no-consume namespace must materialize nothing, got %d", len(subs))
	}

	// Re-materializing the same namespace is idempotent — no second row.
	if err := svc.MaterializeSubscriptionsFromCatalog(context.Background(), principal, principal, []string{"reference"}); err != nil {
		t.Fatalf("re-materialize: %v", err)
	}
	if subs, _ := store.ListEventSubscriptions(context.Background(), principal); len(subs) != 1 {
		t.Fatalf("re-materialization must be idempotent, got %d rows", len(subs))
	}
}

// claimOne claims a single delivery from queue, or returns nil when the queue is
// empty. It fails the test on a transport error.
func claimOne(t *testing.T, transport events.Transport, queue string) *events.Leased {
	t.Helper()
	leased, err := transport.Claim(context.Background(), queue, 1)
	if err != nil {
		t.Fatalf("claim %s: %v", queue, err)
	}
	if len(leased) == 0 {
		return nil
	}
	return &leased[0]
}
