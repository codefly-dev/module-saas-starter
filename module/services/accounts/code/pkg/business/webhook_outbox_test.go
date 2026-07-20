package business

import (
	"context"
	"fmt"
	"sync"
	"testing"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	webhooksv1 "accounts/pkg/gen/saas/webhooks/v1"

	"google.golang.org/protobuf/proto"
)

type burstOutboxStore struct {
	Store
	mu         sync.Mutex
	audits     map[string]struct{}
	deliveries map[string]struct{}
	jobs       map[string]*jobsv1.EnqueueJobRequest
	sub        *WebhookSubscription
}

func (s *burstOutboxStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *burstOutboxStore) InsertAuditEvent(_ context.Context, entry AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits[entry.ID] = struct{}{}
	return nil
}

func (s *burstOutboxStore) GetActiveWebhookSubscriptions(_ context.Context, _ string) ([]*WebhookSubscription, error) {
	return []*WebhookSubscription{s.sub}, nil
}

func (s *burstOutboxStore) CreateWebhookDelivery(_ context.Context, delivery *WebhookDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveries[delivery.OutboxEventID] = struct{}{}
	return nil
}

func (s *burstOutboxStore) EnqueueJob(
	_ context.Context,
	request *jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := request.GetJob()
	s.jobs[job.GetIdempotencyKey()] = proto.Clone(request).(*jobsv1.EnqueueJobRequest)
	return &jobsv1.EnqueueJobResponse{
		JobId:       NewIDString(),
		Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
	}, nil
}

func TestDurableAuditEmitterBurstHasNoQueueSaturationLoss(t *testing.T) {
	store := &burstOutboxStore{
		audits:     map[string]struct{}{},
		deliveries: map[string]struct{}{},
		jobs:       map[string]*jobsv1.EnqueueJobRequest{},
		sub: &WebhookSubscription{
			ID: "00000000-0000-0000-0000-000000000002",
		},
	}
	emitter, err := NewDurableAuditEmitter(store, store)
	if err != nil {
		t.Fatalf("NewDurableAuditEmitter: %v", err)
	}
	const eventCount = 256

	var wait sync.WaitGroup
	for i := 0; i < eventCount; i++ {
		i := i
		wait.Add(1)
		go func() {
			defer wait.Done()
			emitter.Emit(t.Context(), AuditEntry{
				ID: NewIDString(), OrgID: "00000000-0000-0000-0000-000000000001",
				ActorType: "system", Action: fmt.Sprintf("burst.event.%d", i),
			})
		}()
	}
	wait.Wait()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.audits) != eventCount || len(store.deliveries) != eventCount || len(store.jobs) != eventCount {
		t.Fatalf("audits/deliveries/jobs = %d/%d/%d, want %d each",
			len(store.audits), len(store.deliveries), len(store.jobs), eventCount)
	}
	for deliveryID, request := range store.jobs {
		job := request.GetJob()
		if job.GetDirection() != jobsv1.JobDirection_JOB_DIRECTION_OUTBOX ||
			job.GetQueue() != OutboundWebhookQueue || job.GetIdempotencyKey() != deliveryID {
			t.Fatalf("invalid outbound job routing for delivery %s", deliveryID)
		}
		payload := &webhooksv1.OutboundWebhookJob{}
		if err := proto.Unmarshal(job.GetPayload(), payload); err != nil {
			t.Fatalf("decode outbound job: %v", err)
		}
		if payload.GetDeliveryId() != deliveryID || len(payload.GetRawBody()) == 0 {
			t.Fatalf("invalid outbound workload for delivery %s", deliveryID)
		}
	}
}
