package business

import (
	"context"
	"testing"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	webhooksv1 "accounts/pkg/gen/saas/webhooks/v1"

	"google.golang.org/protobuf/proto"
)

type manualWebhookJobStore struct {
	Store
	subscription *WebhookSubscription
	deliveries   map[string]*WebhookDelivery
	jobs         []*jobsv1.EnqueueJobRequest
}

func (s *manualWebhookJobStore) WithOrgTx(
	ctx context.Context,
	_ string,
	fn func(context.Context) error,
) error {
	return fn(ctx)
}

func (s *manualWebhookJobStore) GetWebhookSubscription(
	_ context.Context,
	id string,
) (*WebhookSubscription, error) {
	if s.subscription == nil || s.subscription.ID != id {
		return nil, nil
	}
	copy := *s.subscription
	return &copy, nil
}

func (s *manualWebhookJobStore) GetWebhookDelivery(
	_ context.Context,
	id string,
) (*WebhookDelivery, error) {
	delivery := s.deliveries[id]
	if delivery == nil {
		return nil, nil
	}
	copy := *delivery
	return &copy, nil
}

func (s *manualWebhookJobStore) CreateWebhookDelivery(
	_ context.Context,
	delivery *WebhookDelivery,
) error {
	copy := *delivery
	s.deliveries[delivery.ID] = &copy
	return nil
}

func (s *manualWebhookJobStore) EnqueueJob(
	_ context.Context,
	request *jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	s.jobs = append(s.jobs, proto.Clone(request).(*jobsv1.EnqueueJobRequest))
	return &jobsv1.EnqueueJobResponse{
		JobId:       NewIDString(),
		Disposition: jobEnqueueInserted,
	}, nil
}

func TestWebhookCommandCreatesHistoryAndGeneratedJobAtomically(t *testing.T) {
	orgID := NewIDString()
	store := &manualWebhookJobStore{
		subscription: &WebhookSubscription{ID: NewIDString(), OrgID: orgID, Active: true},
		deliveries:   map[string]*WebhookDelivery{},
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.SetWebhookJobProducer(store)

	delivery, err := service.TestWebhook(t.Context(), orgID, store.subscription.ID, "")
	if err != nil {
		t.Fatalf("TestWebhook: %v", err)
	}
	if delivery.Status != "pending" || delivery.EventType != "webhook.test" {
		t.Fatalf("delivery status/type = %q/%q", delivery.Status, delivery.EventType)
	}
	if delivery.OutboxEventID != "" {
		t.Fatalf("manual delivery outbox event id = %q, want empty", delivery.OutboxEventID)
	}
	if len(store.deliveries) != 1 || len(store.jobs) != 1 {
		t.Fatalf("deliveries/jobs = %d/%d, want 1/1", len(store.deliveries), len(store.jobs))
	}

	job := store.jobs[0].GetJob()
	if job.GetIdempotencyKey() != delivery.ID || job.GetQueue() != OutboundWebhookQueue {
		t.Fatalf("job routing/idempotency = %q/%q", job.GetQueue(), job.GetIdempotencyKey())
	}
	payload := &webhooksv1.OutboundWebhookJob{}
	if err := proto.Unmarshal(job.GetPayload(), payload); err != nil {
		t.Fatalf("decode workload: %v", err)
	}
	if payload.GetDeliveryId() != delivery.ID || payload.GetEventId() != delivery.EventID ||
		payload.GetEventType() != "webhook.test" || string(payload.GetRawBody()) != delivery.Payload {
		t.Fatalf("workload does not preserve test delivery: %+v", payload)
	}
}

func TestReplayWebhookCommandPreservesExactEventAndQueuesNewDelivery(t *testing.T) {
	orgID := NewIDString()
	subscriptionID := NewIDString()
	original := &WebhookDelivery{
		ID: NewIDString(), SubscriptionID: subscriptionID, EventID: NewIDString(),
		OutboxEventID: NewIDString(), EventType: "user.created",
		Payload: `{"exact":"original bytes"}`, Status: "failed",
	}
	store := &manualWebhookJobStore{
		subscription: &WebhookSubscription{ID: subscriptionID, OrgID: orgID, Active: true},
		deliveries:   map[string]*WebhookDelivery{original.ID: original},
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.SetWebhookJobProducer(store)

	replay, err := service.ReplayWebhookDelivery(t.Context(), orgID, original.ID)
	if err != nil {
		t.Fatalf("ReplayWebhookDelivery: %v", err)
	}
	if replay.ID == original.ID || replay.EventID != original.EventID || replay.Payload != original.Payload {
		t.Fatalf("replay did not preserve immutable event content: %+v", replay)
	}
	if replay.OutboxEventID != "" || replay.Status != "pending" {
		t.Fatalf("replay outbox/status = %q/%q", replay.OutboxEventID, replay.Status)
	}
	if len(store.deliveries) != 2 || len(store.jobs) != 1 {
		t.Fatalf("deliveries/jobs = %d/%d, want 2/1", len(store.deliveries), len(store.jobs))
	}
	payload := &webhooksv1.OutboundWebhookJob{}
	if err := proto.Unmarshal(store.jobs[0].GetJob().GetPayload(), payload); err != nil {
		t.Fatalf("decode workload: %v", err)
	}
	if payload.GetDeliveryId() != replay.ID || payload.GetEventId() != original.EventID ||
		string(payload.GetRawBody()) != original.Payload {
		t.Fatalf("replay workload does not preserve exact content: %+v", payload)
	}
}

func TestManualWebhookCommandFailsBeforeHistoryWithoutProducer(t *testing.T) {
	orgID := NewIDString()
	store := &manualWebhookJobStore{
		subscription: &WebhookSubscription{ID: NewIDString(), OrgID: orgID, Active: true},
		deliveries:   map[string]*WebhookDelivery{},
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := service.TestWebhook(t.Context(), orgID, store.subscription.ID, "webhook.test"); err == nil {
		t.Fatal("TestWebhook succeeded without transactional producer")
	}
	if len(store.deliveries) != 0 {
		t.Fatalf("delivery history mutated without producer: %d", len(store.deliveries))
	}
}
