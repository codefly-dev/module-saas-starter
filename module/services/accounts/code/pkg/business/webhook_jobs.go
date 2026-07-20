package business

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	webhooksv1 "accounts/pkg/gen/saas/webhooks/v1"
	"accounts/pkg/jobs"

	"google.golang.org/protobuf/proto"
)

const (
	OutboundWebhookQueue         = "webhooks"
	OutboundWebhookTopic         = "webhook.delivery.send"
	OutboundWebhookSource        = "saas.audit"
	OutboundWebhookSchemaVersion = 1
	OutboundWebhookMaxAttempts   = 5
	OutboundWebhookContentType   = "application/protobuf"
	webhookOrderingNamespace     = "webhook_subscription"
)

var (
	errUnexpectedWebhookEnqueueDisposition = errors.New("webhooks: enqueue did not insert a durable job")
	jobEnqueueInserted                     = jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED
)

func createOutboundWebhookDelivery(
	ctx context.Context,
	store Store,
	producer jobs.Producer,
	orgID string,
	delivery *WebhookDelivery,
	rawBody []byte,
) error {
	if producer == nil {
		return errors.New("webhooks: transactional job producer is required")
	}
	request, err := newOutboundWebhookJob(orgID, delivery, rawBody)
	if err != nil {
		return err
	}
	if err := store.CreateWebhookDelivery(ctx, delivery); err != nil {
		return err
	}
	response, err := producer.EnqueueJob(ctx, request)
	if err != nil {
		return err
	}
	if response.GetDisposition() != jobEnqueueInserted {
		return errUnexpectedWebhookEnqueueDisposition
	}
	return nil
}

func newOutboundWebhookJob(
	orgID string,
	delivery *WebhookDelivery,
	rawBody []byte,
) (*jobsv1.EnqueueJobRequest, error) {
	if delivery == nil {
		return nil, errors.New("webhooks: delivery is required")
	}
	payload := &webhooksv1.OutboundWebhookJob{
		DeliveryId: delivery.ID, SubscriptionId: delivery.SubscriptionID,
		EventId: delivery.EventID, EventType: delivery.EventType, RawBody: rawBody,
	}
	if err := jobs.ValidateCommand(payload); err != nil {
		return nil, fmt.Errorf("webhooks: invalid outbound workload: %w", err)
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("webhooks: encode outbound workload: %w", err)
	}
	request := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope: &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{
			OrganizationId: orgID,
		}},
		Queue: OutboundWebhookQueue, Topic: OutboundWebhookTopic,
		Source: OutboundWebhookSource, IdempotencyKey: delivery.ID,
		Ordering: &jobsv1.JobOrderingKey{
			Namespace: webhookOrderingNamespace, Components: []string{delivery.SubscriptionID},
		},
		SchemaVersion: OutboundWebhookSchemaVersion, Payload: encoded,
		ContentType: OutboundWebhookContentType, MaxAttempts: OutboundWebhookMaxAttempts,
	}}
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, fmt.Errorf("webhooks: invalid outbound enqueue command: %w", err)
	}
	return request, nil
}

// OutboundWebhookProjection is the only product persistence authority held by
// the cross-tenant webhook executor. Generic job tables own all queue state.
type OutboundWebhookProjection interface {
	LoadOutboundWebhookDelivery(context.Context, string) (*WebhookDelivery, *WebhookSubscription, error)
	RecordOutboundWebhookAttempt(context.Context, OutboundWebhookAttempt) error
}

type OutboundWebhookAttempt struct {
	DeliveryID   string
	Attempt      uint32
	HTTPStatus   int
	ResponseBody string
	DeliveredAt  *time.Time
}

// NewOutboundWebhookJobHandler adapts the generated workload to the shared job
// runtime and the existing hardened signing/transport implementation.
func NewOutboundWebhookJobHandler(
	projection OutboundWebhookProjection,
	sender *WebhookSender,
) (jobs.Handler, error) {
	if projection == nil {
		return nil, errors.New("webhooks: outbound projection is required")
	}
	if sender == nil {
		return nil, errors.New("webhooks: sender is required")
	}
	return func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
		orgID, err := validateOutboundWebhookEnvelope(envelope)
		if err != nil {
			return jobs.NewProcessingError("webhooks.invalid_job", "invalid outbound webhook job", false)
		}
		payload := &webhooksv1.OutboundWebhookJob{}
		if err := proto.Unmarshal(envelope.GetPayload(), payload); err != nil {
			return jobs.NewProcessingError("webhooks.invalid_job", "invalid outbound webhook job", false)
		}
		if err := jobs.ValidateCommand(payload); err != nil ||
			!jobs.PayloadIdentityMatches(envelope, payload.GetDeliveryId()) {
			return jobs.NewProcessingError("webhooks.invalid_job", "invalid outbound webhook job", false)
		}
		expectedOrdering, err := jobs.CanonicalOrderingKey(&jobsv1.JobOrderingKey{
			Namespace: webhookOrderingNamespace, Components: []string{payload.GetSubscriptionId()},
		})
		if err != nil || expectedOrdering != envelope.GetOrderingKey() {
			return jobs.NewProcessingError("webhooks.invalid_job", "invalid outbound webhook job", false)
		}

		delivery, subscription, err := projection.LoadOutboundWebhookDelivery(ctx, payload.GetDeliveryId())
		if err != nil {
			return err
		}
		if delivery == nil || subscription == nil {
			return jobs.NewProcessingError("webhooks.delivery_missing", "webhook delivery is unavailable", false)
		}
		if subscription.ID != payload.GetSubscriptionId() || subscription.OrgID != orgID ||
			delivery.ID != payload.GetDeliveryId() || delivery.SubscriptionID != subscription.ID ||
			delivery.EventID != payload.GetEventId() ||
			(delivery.OutboxEventID != "" && delivery.OutboxEventID != payload.GetEventId()) ||
			delivery.EventType != payload.GetEventType() ||
			!bytes.Equal([]byte(delivery.Payload), payload.GetRawBody()) {
			return jobs.NewProcessingError("webhooks.invalid_job", "invalid outbound webhook job", false)
		}
		if delivery.Status == "delivered" {
			return nil
		}
		if !subscription.Active {
			if err := projection.RecordOutboundWebhookAttempt(ctx, OutboundWebhookAttempt{
				DeliveryID: delivery.ID, Attempt: envelope.GetAttemptCount(),
				ResponseBody: "webhook subscription is inactive",
			}); err != nil {
				return err
			}
			return jobs.NewProcessingError("webhooks.subscription_inactive", "webhook subscription is inactive", false)
		}

		result, attemptErr := sender.attempt(ctx, subscription, delivery, payload.GetRawBody())
		now := sender.now().UTC()
		attempt := OutboundWebhookAttempt{
			DeliveryID: delivery.ID, Attempt: envelope.GetAttemptCount(),
			HTTPStatus: result.HTTPStatus, ResponseBody: result.ResponseBody,
		}
		if attemptErr != nil {
			attempt.ResponseBody = "delivery request failed"
		} else if result.HTTPStatus >= 200 && result.HTTPStatus < 300 {
			attempt.DeliveredAt = &now
		}
		if err := projection.RecordOutboundWebhookAttempt(ctx, attempt); err != nil {
			return err
		}
		if attemptErr != nil {
			return attemptErr
		}
		if attempt.DeliveredAt == nil {
			return fmt.Errorf("webhooks: endpoint returned HTTP %d", result.HTTPStatus)
		}
		return nil
	}, nil
}

func validateOutboundWebhookEnvelope(envelope *jobsv1.JobEnvelope) (string, error) {
	if envelope == nil {
		return "", errors.New("webhooks: missing outbound job envelope")
	}
	if err := jobs.ValidateCommand(envelope); err != nil {
		return "", fmt.Errorf("webhooks: invalid outbound job envelope: %w", err)
	}
	scope, ok := envelope.GetScope().GetValue().(*jobsv1.JobScope_OrganizationId)
	if envelope.GetDirection() != jobsv1.JobDirection_JOB_DIRECTION_OUTBOX ||
		!ok || scope.OrganizationId == "" ||
		envelope.GetQueue() != OutboundWebhookQueue ||
		envelope.GetTopic() != OutboundWebhookTopic ||
		envelope.GetSource() != OutboundWebhookSource ||
		envelope.GetSchemaVersion() != OutboundWebhookSchemaVersion ||
		envelope.GetMaxAttempts() != OutboundWebhookMaxAttempts ||
		envelope.GetContentType() != OutboundWebhookContentType {
		return "", errors.New("webhooks: unexpected outbound job routing")
	}
	return scope.OrganizationId, nil
}

// OutboundWebhookRetryDelay preserves the workload's bounded endpoint retry
// cadence while scheduling and exhaustion remain generic worker concerns.
func OutboundWebhookRetryDelay(attempt uint32) time.Duration {
	schedule := [...]time.Duration{
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
	}
	if attempt == 0 {
		return schedule[0]
	}
	index := int(attempt - 1)
	if index >= len(schedule) {
		index = len(schedule) - 1
	}
	return schedule[index]
}
