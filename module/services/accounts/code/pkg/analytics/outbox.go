package analytics

import (
	"context"
	"errors"
	"fmt"
	"time"

	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/proto"
)

const (
	ExportQueue         = "analytics"
	ExportTopic         = "product_event.export"
	SuppressionTopic    = "identity_suppression.export"
	ExportSource        = "saas.analytics"
	ExportSchemaVersion = 1
	ExportMaxAttempts   = 8
	ExportContentType   = "application/protobuf"
)

var ErrCommandScopeRequired = errors.New("analytics: command scope is required")

type Emitter interface {
	Capture(context.Context, *analyticsv1.ProductEvent) error
	Suppress(context.Context, Suppression, CommandScope) error
}

type CommandScope struct {
	jobScope *jobsv1.JobScope
}

func TenantScope(organizationID string) CommandScope {
	return CommandScope{jobScope: &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{
		OrganizationId: organizationID,
	}}}
}

func SubjectScope(subjectID string) CommandScope {
	return CommandScope{jobScope: &jobsv1.JobScope{Value: &jobsv1.JobScope_SubjectId{
		SubjectId: subjectID,
	}}}
}

func GlobalScope() CommandScope {
	return CommandScope{jobScope: &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}}}
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
	queuedEvent := proto.Clone(event).(*analyticsv1.ProductEvent)
	queuedEvent.ReceivedAt = nil
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(queuedEvent)
	if err != nil {
		return fmt.Errorf("analytics: encode product event: %w", err)
	}
	scope, err := eventScope(event)
	if err != nil {
		return err
	}
	return o.enqueue(ctx, &jobsv1.NewJob{
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
	}, "event")
}

func (o *Outbox) Suppress(
	ctx context.Context,
	suppression Suppression,
	scope CommandScope,
) error {
	command := &analyticsv1.SuppressionCommand{CommandId: suppression.CommandID}
	switch {
	case suppression.UserID != "" && suppression.OrganizationID == "":
		command.Target = &analyticsv1.SuppressionCommand_UserId{UserId: suppression.UserID}
	case suppression.OrganizationID != "" && suppression.UserID == "":
		command.Target = &analyticsv1.SuppressionCommand_OrganizationId{
			OrganizationId: suppression.OrganizationID,
		}
	default:
		return errors.New("analytics: suppression requires exactly one identity")
	}
	if err := validateSuppressionCommand(command); err != nil {
		return err
	}
	if scope.jobScope == nil {
		return ErrCommandScopeRequired
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(command)
	if err != nil {
		return fmt.Errorf("analytics: encode suppression command: %w", err)
	}
	return o.enqueue(ctx, &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:          scope.jobScope,
		Queue:          ExportQueue,
		Topic:          SuppressionTopic,
		Source:         ExportSource,
		IdempotencyKey: command.GetCommandId(),
		SchemaVersion:  ExportSchemaVersion,
		Payload:        payload,
		ContentType:    ExportContentType,
		MaxAttempts:    ExportMaxAttempts,
	}, "suppression")
}

func (o *Outbox) enqueue(ctx context.Context, job *jobsv1.NewJob, kind string) error {
	request := &jobsv1.EnqueueJobRequest{Job: job}
	if err := jobs.ValidateCommand(request); err != nil {
		return fmt.Errorf("analytics: invalid %s export command: %w", kind, err)
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
		return fmt.Errorf("analytics: enqueue did not persist a durable %s command", kind)
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
	if event.GetAnonymousId() != "" {
		return &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}}, nil
	}
	return nil, errors.New("analytics: durable backend event requires an organization, actor, or anonymous identity")
}

type ExportHandlerConfig struct {
	Destination Destination
	Deliveries  DeliveryRecorder
	Meter       metric.Meter
	Now         func() time.Time
}

func NewExportHandler(config ExportHandlerConfig) (jobs.Handler, error) {
	if config.Destination == nil {
		return nil, errors.New("analytics: export destination is required")
	}
	if config.Deliveries == nil {
		return nil, errors.New("analytics: delivery recorder is required")
	}
	if config.Meter == nil {
		config.Meter = otel.Meter("github.com/codefly-dev/module-saas-starter/analytics")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	telemetry, err := newExportTelemetry(config.Meter)
	if err != nil {
		return nil, fmt.Errorf("analytics: initialize export metrics: %w", err)
	}
	return func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
		commandID, kind, delivery, rejectReason, err := deliverEnvelope(
			ctx,
			config.Destination,
			telemetry,
			envelope,
		)
		if err != nil {
			if rejectReason != "" {
				telemetry.recordRejected(ctx, rejectReason)
			}
			if errors.Is(err, ErrEventConflict) {
				return jobs.NewProcessingError(
					"analytics.idempotency_conflict", "analytics event identity conflict", false,
				)
			}
			if errors.Is(err, ErrSuppressionUnsupported) {
				return jobs.NewProcessingError(
					"analytics.suppression_unsupported",
					"analytics destination does not support the suppression target",
					false,
				)
			}
			return err
		}
		telemetry.recordDelivery(ctx, kind, delivery)
		return config.Deliveries.RecordDelivery(ctx, DeliveryRecord{
			JobID:             envelope.GetId(),
			CommandID:         commandID,
			Kind:              kind,
			ProviderReference: delivery.Reference,
			Duplicate:         delivery.Duplicate,
			DeliveredAt:       config.Now().UTC(),
		})
	}, nil
}

func deliverEnvelope(
	ctx context.Context,
	destination Destination,
	telemetry exportTelemetry,
	envelope *jobsv1.JobEnvelope,
) (string, string, Delivery, string, error) {
	switch envelope.GetTopic() {
	case ExportTopic:
		event, err := validateEventExportEnvelope(envelope)
		if err != nil {
			return "", "", Delivery{}, "event_schema", jobs.NewProcessingError(
				"analytics.invalid_event", "analytics event failed contract validation", false,
			)
		}
		startedAt := time.Now()
		delivery, err := destination.Capture(ctx, event)
		telemetry.recordProviderDuration(ctx, "event", startedAt)
		if errors.Is(err, ErrEventConflict) {
			return "", "", Delivery{}, "idempotency_conflict", err
		}
		return event.GetEventId(), "event", delivery, "", err
	case SuppressionTopic:
		command, err := validateSuppressionExportEnvelope(envelope)
		if err != nil {
			return "", "", Delivery{}, "suppression_schema", jobs.NewProcessingError(
				"analytics.invalid_suppression",
				"analytics suppression failed contract validation",
				false,
			)
		}
		suppression := suppressionFromProto(command)
		startedAt := time.Now()
		delivery, err := destination.Suppress(ctx, suppression)
		telemetry.recordProviderDuration(ctx, "suppression", startedAt)
		return command.GetCommandId(), "suppression", delivery, "", err
	default:
		return "", "", Delivery{}, "route", jobs.NewProcessingError(
			"analytics.invalid_route", "analytics export route is not registered", false,
		)
	}
}

func validateExportRouting(envelope *jobsv1.JobEnvelope) error {
	if envelope == nil || jobs.ValidateCommand(envelope) != nil {
		return errors.New("analytics: invalid export envelope")
	}
	if envelope.GetDirection() != jobsv1.JobDirection_JOB_DIRECTION_OUTBOX ||
		envelope.GetQueue() != ExportQueue ||
		envelope.GetSource() != ExportSource ||
		envelope.GetSchemaVersion() != ExportSchemaVersion ||
		envelope.GetMaxAttempts() != ExportMaxAttempts ||
		envelope.GetContentType() != ExportContentType {
		return errors.New("analytics: unexpected export routing")
	}
	return nil
}

func validateEventExportEnvelope(
	envelope *jobsv1.JobEnvelope,
) (*analyticsv1.ProductEvent, error) {
	if err := validateExportRouting(envelope); err != nil {
		return nil, err
	}
	event := &analyticsv1.ProductEvent{}
	if err := proto.Unmarshal(envelope.GetPayload(), event); err != nil {
		return nil, err
	}
	if event.GetReceivedAt() == nil {
		if envelope.GetCreatedAt() == nil {
			return nil, errors.New("analytics: export envelope has no ingestion time")
		}
		event.ReceivedAt = envelope.GetCreatedAt()
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
	case *jobsv1.JobScope_Global:
		if !scope.Global || event.GetOrganizationId() != "" ||
			event.GetActorUserId() != "" || event.GetAnonymousId() == "" {
			return nil, errors.New("analytics: global scope does not match anonymous event")
		}
	default:
		return nil, errors.New("analytics: export has an unsupported scope")
	}
	if err := validatePersistedEvent(event); err != nil {
		return nil, err
	}
	return event, nil
}

func validateSuppressionExportEnvelope(
	envelope *jobsv1.JobEnvelope,
) (*analyticsv1.SuppressionCommand, error) {
	if err := validateExportRouting(envelope); err != nil {
		return nil, err
	}
	command := &analyticsv1.SuppressionCommand{}
	if err := proto.Unmarshal(envelope.GetPayload(), command); err != nil {
		return nil, err
	}
	if !jobs.PayloadIdentityMatches(envelope, command.GetCommandId()) {
		return nil, errors.New("analytics: suppression identity does not match job")
	}
	if err := validateSuppressionCommand(command); err != nil {
		return nil, err
	}
	return command, nil
}

func suppressionFromProto(command *analyticsv1.SuppressionCommand) Suppression {
	suppression := Suppression{CommandID: command.GetCommandId()}
	switch target := command.GetTarget().(type) {
	case *analyticsv1.SuppressionCommand_UserId:
		suppression.UserID = target.UserId
	case *analyticsv1.SuppressionCommand_OrganizationId:
		suppression.OrganizationID = target.OrganizationId
	}
	return suppression
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
