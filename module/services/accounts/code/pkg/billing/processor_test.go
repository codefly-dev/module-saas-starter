package billing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/billing"
	billingv1 "accounts/pkg/gen/saas/billing/v1"
)

type fakeProcessingStore struct {
	mu            sync.Mutex
	plans         map[string]billing.PlanRef
	orgs          map[string]string
	ownerEmails   map[string]string
	subscriptions []billing.SubscriptionUpsert
	upsertErr     error
}

type fakeSubscriptionReader struct {
	mu            sync.Mutex
	subscriptions map[string]*billing.Subscription
	err           error
}

func (f *fakeSubscriptionReader) GetSubscription(_ context.Context, id string) (*billing.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	subscription, ok := f.subscriptions[id]
	if !ok {
		return nil, errors.New("subscription not found")
	}
	copy := *subscription
	return &copy, nil
}

func currentSubscription(id, customer, priceID, status string) *billing.Subscription {
	payload := []byte(fmt.Sprintf(`{
		"id":%q,
		"customer":%q,
		"status":%q,
		"current_period_start":1700000000,
		"current_period_end":1702592000,
		"items":{"data":[{"price":{"id":%q}}]}
	}`, id, customer, status, priceID))
	var subscription billing.Subscription
	if err := json.Unmarshal(payload, &subscription); err != nil {
		panic(err)
	}
	return &subscription
}

func readerWith(subscription *billing.Subscription) *fakeSubscriptionReader {
	return &fakeSubscriptionReader{subscriptions: map[string]*billing.Subscription{
		subscription.ID: subscription,
	}}
}

func newFakeProcessingStore() *fakeProcessingStore {
	return &fakeProcessingStore{
		plans:       make(map[string]billing.PlanRef),
		orgs:        make(map[string]string),
		ownerEmails: make(map[string]string),
	}
}

func (f *fakeProcessingStore) PlanByStripePriceID(_ context.Context, priceID string) (*billing.PlanRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	plan, ok := f.plans[priceID]
	if !ok {
		return nil, billing.ErrPlanNotFound
	}
	return &plan, nil
}

func (f *fakeProcessingStore) OrgByStripeCustomerID(_ context.Context, customerID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	orgID, ok := f.orgs[customerID]
	if !ok {
		return "", billing.ErrOrgNotFound
	}
	return orgID, nil
}

func (f *fakeProcessingStore) UpsertSubscription(_ context.Context, subscription billing.SubscriptionUpsert) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscriptions = append(f.subscriptions, subscription)
	return nil
}

func (f *fakeProcessingStore) OwnerEmailByStripeCustomerID(_ context.Context, customerID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ownerEmails[customerID], nil
}

type recordingEmailNotifier struct {
	messages []billing.BillingEmail
	err      error
}

func (notifier *recordingEmailNotifier) EnqueueBillingEmail(_ context.Context, message billing.BillingEmail) error {
	notifier.messages = append(notifier.messages, message)
	return notifier.err
}

func (f *fakeProcessingStore) subscriptionsFor(orgID string) []billing.SubscriptionUpsert {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matches []billing.SubscriptionUpsert
	for _, subscription := range f.subscriptions {
		if subscription.OrgID == orgID {
			matches = append(matches, subscription)
		}
	}
	return matches
}

func subscriptionEvent(eventID, eventType, customer, priceID, status string) *billingv1.StripeWebhookJob {
	payload := []byte(fmt.Sprintf(`{
		"id": %q,
		"type": %q,
		"data": {"object": {
			"id": "sub_01ABC",
			"status": %q,
			"customer": %q,
			"current_period_start": 1700000000,
			"current_period_end": 1702592000,
			"items": {"data": [{"price": {"id": %q, "product": "prod_01"}}]}
		}}
	}`, eventID, eventType, status, customer, priceID))
	return &billingv1.StripeWebhookJob{EventId: eventID, EventType: eventType, RawBody: payload}
}

func TestProcessor_SubscriptionCreatedProjectsLocalState(t *testing.T) {
	store := newFakeProcessingStore()
	store.plans["price_pro"] = billing.PlanRef{ID: "plan-pro", Name: "pro"}
	store.orgs["cus_acme"] = "org-acme"
	processor := billing.NewProcessor(billing.ProcessorDeps{
		Store:  store,
		Client: readerWith(currentSubscription("sub_01ABC", "cus_acme", "price_pro", "active")),
	})

	err := processor.ProcessWebhook(context.Background(), subscriptionEvent(
		"evt_created", "customer.subscription.created", "cus_acme", "price_pro", "active",
	))

	require.NoError(t, err)
	subscriptions := store.subscriptionsFor("org-acme")
	require.Len(t, subscriptions, 1)
	require.Equal(t, "plan-pro", subscriptions[0].PlanID)
	require.Equal(t, "active", subscriptions[0].Status)
	require.Equal(t, "sub_01ABC", subscriptions[0].StripeSubscriptionID)
	require.NotNil(t, subscriptions[0].CurrentPeriodStart)
	require.NotNil(t, subscriptions[0].CurrentPeriodEnd)
}

func TestProcessor_SubscriptionDeletedForcesCanceled(t *testing.T) {
	store := newFakeProcessingStore()
	store.plans["price_pro"] = billing.PlanRef{ID: "plan-pro"}
	store.orgs["cus_acme"] = "org-acme"
	processor := billing.NewProcessor(billing.ProcessorDeps{
		Store:  store,
		Client: readerWith(currentSubscription("sub_01ABC", "cus_acme", "price_pro", "canceled")),
	})

	err := processor.ProcessWebhook(context.Background(), subscriptionEvent(
		"evt_deleted", "customer.subscription.deleted", "cus_acme", "price_pro", "active",
	))

	require.NoError(t, err)
	require.Equal(t, "canceled", store.subscriptionsFor("org-acme")[0].Status)
}

func TestProcessor_InvoicePaymentFailedMarksPastDue(t *testing.T) {
	store := newFakeProcessingStore()
	store.orgs["cus_acme"] = "org-acme"
	store.plans["price_pro"] = billing.PlanRef{ID: "plan-pro"}
	processor := billing.NewProcessor(billing.ProcessorDeps{
		Store:  store,
		Client: readerWith(currentSubscription("sub_01", "cus_acme", "price_pro", "past_due")),
	})
	payload := []byte(`{
		"id":"evt_failed",
		"type":"invoice.payment_failed",
		"data":{"object":{"subscription":"sub_01","customer":"cus_acme"}}
	}`)

	err := processor.ProcessWebhook(context.Background(), &billingv1.StripeWebhookJob{
		EventId: "evt_failed", EventType: "invoice.payment_failed", RawBody: payload,
	})

	require.NoError(t, err)
	subscription := store.subscriptionsFor("org-acme")[0]
	require.Equal(t, "past_due", subscription.Status)
	require.Equal(t, "plan-pro", subscription.PlanID)
}

func TestProcessorBillingEmailUsesStableEventOutboxIdentity(t *testing.T) {
	store := newFakeProcessingStore()
	store.orgs["cus_acme"] = "org-acme"
	store.ownerEmails["cus_acme"] = "owner@example.com"
	store.plans["price_pro"] = billing.PlanRef{ID: "plan-pro"}
	notifier := &recordingEmailNotifier{}
	processor := billing.NewProcessor(billing.ProcessorDeps{
		Store: store,
		Client: readerWith(currentSubscription(
			"sub_01", "cus_acme", "price_pro", "past_due",
		)),
		Notifier: notifier,
	})
	payload := []byte(`{
		"id":"evt_email",
		"type":"invoice.payment_failed",
		"data":{"object":{"subscription":"sub_01","customer":"cus_acme"}}
	}`)
	event := &billingv1.StripeWebhookJob{
		EventId: "evt_email", EventType: "invoice.payment_failed", RawBody: payload,
	}

	require.NoError(t, processor.ProcessWebhook(context.Background(), event))
	require.NoError(t, processor.ProcessWebhook(context.Background(), event))
	require.Len(t, notifier.messages, 2)
	require.NotEmpty(t, notifier.messages[0].DeliveryKey)
	require.Equal(t, notifier.messages[0].DeliveryKey, notifier.messages[1].DeliveryKey)
	require.Equal(t, "org-acme", notifier.messages[0].OrganizationID)
	require.Equal(t, "payment_failed", notifier.messages[0].Template)
	require.Equal(t, "owner@example.com", notifier.messages[0].To)
}

func TestProcessorBillingEmailEnqueueFailureRemainsRetryable(t *testing.T) {
	store := newFakeProcessingStore()
	store.orgs["cus_acme"] = "org-acme"
	store.ownerEmails["cus_acme"] = "owner@example.com"
	store.plans["price_pro"] = billing.PlanRef{ID: "plan-pro"}
	notifier := &recordingEmailNotifier{err: errors.New("job store unavailable")}
	processor := billing.NewProcessor(billing.ProcessorDeps{
		Store: store,
		Client: readerWith(currentSubscription(
			"sub_01", "cus_acme", "price_pro", "past_due",
		)),
		Notifier: notifier,
	})
	payload := []byte(`{
		"id":"evt_retry_email",
		"type":"invoice.payment_failed",
		"data":{"object":{"subscription":"sub_01","customer":"cus_acme"}}
	}`)

	err := processor.ProcessWebhook(context.Background(), &billingv1.StripeWebhookJob{
		EventId: "evt_retry_email", EventType: "invoice.payment_failed", RawBody: payload,
	})
	require.ErrorContains(t, err, "job store unavailable")
}

func TestProcessor_UnknownCustomerAndPlanRemainRetryableErrors(t *testing.T) {
	store := newFakeProcessingStore()
	processor := billing.NewProcessor(billing.ProcessorDeps{
		Store:  store,
		Client: readerWith(currentSubscription("sub_01ABC", "cus_missing", "price_missing", "active")),
	})
	event := subscriptionEvent("evt_missing", "customer.subscription.created", "cus_missing", "price_missing", "active")

	err := processor.ProcessWebhook(context.Background(), event)
	require.ErrorIs(t, err, billing.ErrOrgNotFound)

	store.orgs["cus_missing"] = "org-found"
	err = processor.ProcessWebhook(context.Background(), event)
	require.ErrorIs(t, err, billing.ErrPlanNotFound)
}

func TestProcessor_StoreFailureIsReturnedToWorker(t *testing.T) {
	store := newFakeProcessingStore()
	store.plans["price_pro"] = billing.PlanRef{ID: "plan-pro"}
	store.orgs["cus_acme"] = "org-acme"
	store.upsertErr = errors.New("database unavailable")
	processor := billing.NewProcessor(billing.ProcessorDeps{
		Store:  store,
		Client: readerWith(currentSubscription("sub_01ABC", "cus_acme", "price_pro", "active")),
	})

	err := processor.ProcessWebhook(context.Background(), subscriptionEvent(
		"evt_db", "customer.subscription.updated", "cus_acme", "price_pro", "active",
	))

	require.ErrorContains(t, err, "database unavailable")
}

func TestProcessor_RejectsPayloadMetadataMismatch(t *testing.T) {
	processor := billing.NewProcessor(billing.ProcessorDeps{Store: newFakeProcessingStore()})
	event := subscriptionEvent("evt_payload", "customer.subscription.created", "cus", "price", "active")
	event.EventId = "evt_row"

	err := processor.ProcessWebhook(context.Background(), event)

	require.ErrorContains(t, err, "metadata does not match")
}

func TestProcessor_UnknownEventSucceedsWithoutMutation(t *testing.T) {
	store := newFakeProcessingStore()
	processor := billing.NewProcessor(billing.ProcessorDeps{Store: store})
	payload := []byte(`{"id":"evt_unknown","type":"customer.tax_id.created","data":{"object":{}}}`)

	err := processor.ProcessWebhook(context.Background(), &billingv1.StripeWebhookJob{
		EventId: "evt_unknown", EventType: "customer.tax_id.created", RawBody: payload,
	})

	require.NoError(t, err)
	require.Empty(t, store.subscriptions)
}

func TestProcessor_UsesCurrentStripeStateInsteadOfEventSnapshot(t *testing.T) {
	store := newFakeProcessingStore()
	store.plans["price_enterprise"] = billing.PlanRef{ID: "plan-enterprise"}
	store.orgs["cus_acme"] = "org-acme"
	processor := billing.NewProcessor(billing.ProcessorDeps{
		Store: store,
		Client: readerWith(currentSubscription(
			"sub_01ABC", "cus_acme", "price_enterprise", "canceled",
		)),
	})
	// The delayed webhook snapshot says active/pro, but the provider's current
	// object has already moved to canceled/enterprise.
	stale := subscriptionEvent(
		"evt_stale", "customer.subscription.updated", "cus_acme", "price_pro", "active",
	)

	err := processor.ProcessWebhook(context.Background(), stale)

	require.NoError(t, err)
	subscription := store.subscriptionsFor("org-acme")[0]
	require.Equal(t, "canceled", subscription.Status)
	require.Equal(t, "plan-enterprise", subscription.PlanID)
	require.False(t, subscription.StateObservedAt.IsZero())
}
