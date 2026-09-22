package business

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"accounts/pkg/events"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

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

func (s *burstOutboxStore) SyncWebhookEventSubscriptions(context.Context, string, string, []string) error {
	return nil
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

const burstOrgID = "00000000-0000-0000-0000-000000000001"

func TestDurableAuditEmitterBurstHasNoQueueSaturationLoss(t *testing.T) {
	store := &burstOutboxStore{
		audits:     map[string]struct{}{},
		deliveries: map[string]struct{}{},
		jobs:       map[string]*jobsv1.EnqueueJobRequest{},
	}
	queue := "burst.events.test"
	transport := events.NewFakeTransport([]events.Subscription{{
		ID: NewIDString(), SubscriberPrincipalID: NewIDString(),
		TypePattern: "saas.*", Queue: queue, Delivery: events.DeliveryUnordered,
	}}, time.Minute)
	emitter, err := NewDurableAuditEmitter(store, store, WithDomainEventTransport(transport))
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
				ID: NewIDString(), OrgID: burstOrgID,
				ActorType: "system", EventType: EventSessionRevoked,
				ResourceID: fmt.Sprintf("burst-%d", i),
			})
		}()
	}
	wait.Wait()

	store.mu.Lock()
	audits := len(store.audits)
	store.mu.Unlock()
	if audits != eventCount {
		t.Fatalf("audits committed = %d, want %d", audits, eventCount)
	}

	// A claim is capped at one batch, so the subscriber drains and acks until the
	// queue is empty — every emit has to show up exactly once across the drain.
	seen := map[string]struct{}{}
	for {
		leased, err := transport.Claim(t.Context(), queue, eventCount)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if len(leased) == 0 {
			break
		}
		for _, delivery := range leased {
			id := delivery.Envelope.GetId()
			if _, duplicate := seen[id]; duplicate {
				t.Fatalf("event %s published more than once", id)
			}
			seen[id] = struct{}{}
			if err := transport.Ack(t.Context(), delivery.Token); err != nil {
				t.Fatalf("Ack: %v", err)
			}
		}
	}
	if len(seen) != eventCount {
		t.Fatalf("published domain events = %d, want %d", len(seen), eventCount)
	}
}
