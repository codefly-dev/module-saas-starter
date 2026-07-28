package analytics

import (
	"context"
	"errors"
	"fmt"
	"time"

	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"google.golang.org/protobuf/proto"
)

const (
	ExportQueue         = "analytics"
	ExportTopic         = "product_event.export"
	ExportSource        = "saas.analytics"
	ExportSchemaVersion = 1
	ExportMaxAttempts   = 8
	ExportContentType   = "application/protobuf"
)

type Emitter interface {
	Capture(context.Context, *analyticsv1.ProductEvent) error
}

type Outbox struct {
	producer jobs.Producer
	registry *Registry
}

func NewOutbox(producer jobs.Producer, registry *Registry) (*Outbox, error) {
	if producer == nil {
		return nil, errors.New("analytics: job producer is required")
	}
	if registry == nil {
		return nil, errors.New("analytics: event registry is required")
	}
	return &Outbox{producer: producer, registry: registry}, nil
}

func (o *Outbox) Capture(ctx context.Context, event *analyticsv1.ProductEvent) error {
	if err := o.registry.Validate(event); err != nil {
		return err
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(event)
	if err != nil {
		return fmt.Errorf("analytics: encode product event: %w", err)
	}
	scope, err := eventScope(event)
	if err != nil {
		return err
	}
	request := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:          scope,
		Queue:          ExportQueue,
		Topic:          ExportTopic,
		Source:         ExportSource,
		IdempotencyKey: event.GetEventId(),
		SchemaVersion:  ExportSchemaVersion,
		Payload:        payload,
		ContentType:    ExportContentType,
		MaxAttempts:    ExportMaxAttempts,
	}}
	if err := jobs.ValidateCommand(request); err != nil {
		return fmt.Errorf("analytics: invalid export command: %w", err)
	}
	response, err := o.producer.EnqueueJob(ctx, request)
	if err != nil {
		return err
	}
	switch response.GetDisposition() {
	case jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
		jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE:
		return nil
	default:
		return errors.New("analytics: enqueue did not persist a durable event")
	}
}

func eventScope(event *analyticsv1.ProductEvent) (*jobsv1.JobScope, error) {
	if event.GetOrganizationId() != "" {
		return &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{
			OrganizationId: event.GetOrganizationId(),
		}}, nil
	}
	if event.GetActorUserId() != "" {
		return &jobsv1.JobScope{Value: &jobsv1.JobScope_SubjectId{
			SubjectId: event.GetActorUserId(),
		}}, nil
	}
	return nil, errors.New("analytics: durable backend event requires organization or actor identity")
}

func NewExportHandler(registry *Registry, sink Sink) (jobs.Handler, error) {
	if registry == nil {
		return nil, errors.New("analytics: event registry is required")
	}
	if sink == nil {
		return nil, errors.New("analytics: export sink is required")
	}
	return func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
		event, err := validateExportEnvelope(registry, envelope)
		if err != nil {
			return jobs.NewProcessingError(
				"analytics.invalid_event", "analytics event failed contract validation", false,
			)
		}
		if _, err := sink.Capture(ctx, event); err != nil {
			if errors.Is(err, ErrEventConflict) {
				return jobs.NewProcessingError(
					"analytics.idempotency_conflict", "analytics event identity conflict", false,
				)
			}
			return err
		}
		return nil
	}, nil
}

func validateExportEnvelope(
	registry *Registry,
	envelope *jobsv1.JobEnvelope,
) (*analyticsv1.ProductEvent, error) {
	if envelope == nil || jobs.ValidateCommand(envelope) != nil {
		return nil, errors.New("analytics: invalid export envelope")
	}
	if envelope.GetDirection() != jobsv1.JobDirection_JOB_DIRECTION_OUTBOX ||
		envelope.GetQueue() != ExportQueue ||
		envelope.GetTopic() != ExportTopic ||
		envelope.GetSource() != ExportSource ||
		envelope.GetSchemaVersion() != ExportSchemaVersion ||
		envelope.GetMaxAttempts() != ExportMaxAttempts ||
		envelope.GetContentType() != ExportContentType {
		return nil, errors.New("analytics: unexpected export routing")
	}
	event := &analyticsv1.ProductEvent{}
	if err := proto.Unmarshal(envelope.GetPayload(), event); err != nil {
		return nil, err
	}
	if !jobs.PayloadIdentityMatches(envelope, event.GetEventId()) {
		return nil, errors.New("analytics: event identity does not match job")
	}
	switch scope := envelope.GetScope().GetValue().(type) {
	case *jobsv1.JobScope_OrganizationId:
		if scope.OrganizationId != event.GetOrganizationId() {
			return nil, errors.New("analytics: organization scope does not match event")
		}
	case *jobsv1.JobScope_SubjectId:
		if event.GetOrganizationId() != "" || scope.SubjectId != event.GetActorUserId() {
			return nil, errors.New("analytics: subject scope does not match event")
		}
	default:
		return nil, errors.New("analytics: export requires tenant or subject scope")
	}
	if err := registry.Validate(event); err != nil {
		return nil, err
	}
	return event, nil
}

func ExportRetryDelay(attempt uint32) time.Duration {
	schedule := [...]time.Duration{
		time.Second,
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
		6 * time.Hour,
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
