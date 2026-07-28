package analytics_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/analytics"
	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type recordingProducer struct {
	requests []*jobsv1.EnqueueJobRequest
}

func (p *recordingProducer) EnqueueJob(
	_ context.Context,
	request *jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	p.requests = append(p.requests, proto.Clone(request).(*jobsv1.EnqueueJobRequest))
	return &jobsv1.EnqueueJobResponse{
		JobId:       uuid.NewString(),
		Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
	}, nil
}

func TestOutboxBuildsTenantBoundIdempotentExport(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	producer := &recordingProducer{}
	outbox, err := analytics.NewOutbox(producer, registry)
	require.NoError(t, err)
	event, err := registry.NewEvent(analytics.NewEventInput{
		Name:           "invite_created",
		ActorUserID:    uuid.NewString(),
		OrganizationID: uuid.NewString(),
		Source:         analyticsv1.EventSource_EVENT_SOURCE_API,
		Properties:     map[string]any{"role": "admin"},
	})
	require.NoError(t, err)

	require.NoError(t, outbox.Capture(t.Context(), event))
	job := producer.requests[0].GetJob()
	require.Equal(t, event.GetEventId(), job.GetIdempotencyKey())
	require.Equal(t, analytics.ExportQueue, job.GetQueue())
	require.Equal(t, analytics.ExportTopic, job.GetTopic())
	require.Equal(t, event.GetOrganizationId(), job.GetScope().GetOrganizationId())
	require.NotContains(t, job.GetAttributes(), "organization_id")
}

func TestOutboxSupportsAnonymousGlobalEvents(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	producer := &recordingProducer{}
	outbox, err := analytics.NewOutbox(producer, registry)
	require.NoError(t, err)
	event, err := registry.NewEvent(analytics.NewEventInput{
		Name:        "waitlist_joined",
		AnonymousID: "anonymous-browser-identity",
		Source:      analyticsv1.EventSource_EVENT_SOURCE_API,
		Properties: map[string]any{
			"flow_version":     "v1",
			"referral_present": false,
		},
	})
	require.NoError(t, err)

	require.NoError(t, outbox.Capture(t.Context(), event))
	require.True(t, producer.requests[0].GetJob().GetScope().GetGlobal())
}

func TestOutboxRetryFingerprintIgnoresIngestionTime(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	producer := &recordingProducer{}
	outbox, err := analytics.NewOutbox(producer, registry)
	require.NoError(t, err)
	eventID := uuid.NewString()
	occurredAt := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	input := analytics.NewEventInput{
		EventID:        eventID,
		Name:           "invite_created",
		ActorUserID:    uuid.NewString(),
		OrganizationID: uuid.NewString(),
		OccurredAt:     occurredAt,
		Source:         analyticsv1.EventSource_EVENT_SOURCE_API,
		Properties:     map[string]any{"role": "member"},
	}
	first, err := registry.NewEvent(input)
	require.NoError(t, err)
	time.Sleep(time.Millisecond)
	second, err := registry.NewEvent(input)
	require.NoError(t, err)
	require.NotEqual(t, first.GetReceivedAt(), second.GetReceivedAt())

	require.NoError(t, outbox.Capture(t.Context(), first))
	require.NoError(t, outbox.Capture(t.Context(), second))
	firstFingerprint, err := jobs.EnqueueFingerprint(producer.requests[0])
	require.NoError(t, err)
	secondFingerprint, err := jobs.EnqueueFingerprint(producer.requests[1])
	require.NoError(t, err)
	require.Equal(t, firstFingerprint, secondFingerprint)
}

func TestOutboxDurablyExportsSuppression(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	producer := &recordingProducer{}
	outbox, err := analytics.NewOutbox(producer, registry)
	require.NoError(t, err)
	userID := uuid.NewString()
	suppression := analytics.Suppression{
		CommandID: analytics.DeterministicEventID("identity_suppressed", "user", userID),
		UserID:    userID,
	}

	require.NoError(t, outbox.Suppress(
		t.Context(),
		suppression,
		analytics.SubjectScope(userID),
	))
	job := producer.requests[0].GetJob()
	require.Equal(t, analytics.SuppressionTopic, job.GetTopic())
	require.Equal(t, userID, job.GetScope().GetSubjectId())

	envelope := envelopeFromRequest(job)
	sink := analytics.NewMemorySink()
	deliveries := analytics.NewMemoryDeliveryRecorder()
	handler, err := analytics.NewExportHandler(analytics.ExportHandlerConfig{
		Destination: sink,
		Deliveries:  deliveries,
	})
	require.NoError(t, err)
	require.NoError(t, handler(t.Context(), envelope))
	require.NoError(t, handler(t.Context(), envelope))
	require.Equal(t, []analytics.Suppression{suppression}, sink.Suppressions())
	require.Equal(t, suppression.CommandID, deliveries.Records()[0].CommandID)
	require.True(t, deliveries.Records()[1].Duplicate)
}

func TestExportHandlerValidatesAndDeliversCanonicalEvent(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	producer := &recordingProducer{}
	outbox, err := analytics.NewOutbox(producer, registry)
	require.NoError(t, err)
	event, err := registry.NewEvent(analytics.NewEventInput{
		Name:           "invite_accepted",
		ActorUserID:    uuid.NewString(),
		OrganizationID: uuid.NewString(),
		Source:         analyticsv1.EventSource_EVENT_SOURCE_API,
		Properties:     map[string]any{"role": "member"},
	})
	require.NoError(t, err)
	require.NoError(t, outbox.Capture(t.Context(), event))

	envelope := envelopeFromRequest(producer.requests[0].GetJob())
	sink := analytics.NewMemorySink()
	deliveries := analytics.NewMemoryDeliveryRecorder()
	handler, err := analytics.NewExportHandler(analytics.ExportHandlerConfig{
		Destination: sink,
		Deliveries:  deliveries,
	})
	require.NoError(t, err)
	require.NoError(t, handler(t.Context(), envelope))
	require.Equal(t, event.GetEventId(), sink.Events()[0].GetEventId())
	require.Equal(t, event.GetEventId(), deliveries.Records()[0].CommandID)

	envelope.Topic = "analytics.unregistered"
	err = handler(t.Context(), envelope)
	require.Error(t, err)
}

func TestExportHandlerDeliversEventValidatedBeforeSchemaRollout(t *testing.T) {
	previous := mustParseRegistry(t, `{
		"contract_version":1,
		"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},
		"events":[{"name":"fact_completed","owner":"product","description":"fact","sources":["api"],"purpose":"product","properties":["old_kind"]}]
	}`)
	current := mustParseRegistry(t, `{
		"contract_version":1,
		"defaults":{"schema_version":1,"pii_classification":"pseudonymous","retention_days":30,"property_type":"string"},
		"events":[{"name":"fact_completed","owner":"product","description":"fact","schema_version":2,"sources":["api"],"purpose":"product","properties":["new_kind"]}]
	}`)
	producer := &recordingProducer{}
	outbox, err := analytics.NewOutbox(producer, previous)
	require.NoError(t, err)
	event, err := previous.NewEvent(analytics.NewEventInput{
		Name:        "fact_completed",
		ActorUserID: uuid.NewString(),
		Source:      analyticsv1.EventSource_EVENT_SOURCE_API,
		Properties:  map[string]any{"old_kind": "legacy"},
	})
	require.NoError(t, err)
	require.NoError(t, outbox.Capture(t.Context(), event))
	require.Error(t, current.Validate(event))

	sink := analytics.NewMemorySink()
	handler, err := analytics.NewExportHandler(analytics.ExportHandlerConfig{
		Destination: sink,
		Deliveries:  analytics.NewMemoryDeliveryRecorder(),
	})
	require.NoError(t, err)
	require.NoError(t, handler(t.Context(), envelopeFromRequest(producer.requests[0].GetJob())))
	require.Equal(t, event.GetEventId(), sink.Events()[0].GetEventId())
}

func TestExportHandlerEmitsDeliveryHealthMetrics(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	producer := &recordingProducer{}
	outbox, err := analytics.NewOutbox(producer, registry)
	require.NoError(t, err)
	event, err := registry.NewEvent(analytics.NewEventInput{
		Name:        "account_created",
		ActorUserID: uuid.NewString(),
		Source:      analyticsv1.EventSource_EVENT_SOURCE_API,
		Properties:  map[string]any{"provider": "oidc"},
	})
	require.NoError(t, err)
	require.NoError(t, outbox.Capture(t.Context(), event))

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(t.Context())) })
	handler, err := analytics.NewExportHandler(analytics.ExportHandlerConfig{
		Destination: analytics.NewMemorySink(),
		Deliveries:  analytics.NewMemoryDeliveryRecorder(),
		Meter:       provider.Meter("analytics-test"),
	})
	require.NoError(t, err)
	envelope := envelopeFromRequest(producer.requests[0].GetJob())
	require.NoError(t, handler(t.Context(), envelope))
	require.NoError(t, handler(t.Context(), envelope))
	envelope.Topic = "analytics.unregistered"
	require.Error(t, handler(t.Context(), envelope))

	var exported metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &exported))
	names := map[string]bool{}
	for _, scope := range exported.ScopeMetrics {
		for _, metric := range scope.Metrics {
			names[metric.Name] = true
		}
	}
	require.Equal(t, map[string]bool{
		"saas.analytics.delivered":         true,
		"saas.analytics.duplicates":        true,
		"saas.analytics.provider.duration": true,
		"saas.analytics.rejected":          true,
	}, names)
}

func TestNoopExportStillRecordsACompleteDeliveryProjection(t *testing.T) {
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	producer := &recordingProducer{}
	outbox, err := analytics.NewOutbox(producer, registry)
	require.NoError(t, err)
	event, err := registry.NewEvent(analytics.NewEventInput{
		Name:        "account_created",
		ActorUserID: uuid.NewString(),
		Source:      analyticsv1.EventSource_EVENT_SOURCE_API,
		Properties:  map[string]any{"provider": "oidc"},
	})
	require.NoError(t, err)
	require.NoError(t, outbox.Capture(t.Context(), event))
	deliveries := analytics.NewMemoryDeliveryRecorder()
	handler, err := analytics.NewExportHandler(analytics.ExportHandlerConfig{
		Destination: analytics.NoopSink{},
		Deliveries:  deliveries,
	})
	require.NoError(t, err)

	require.NoError(t, handler(
		t.Context(),
		envelopeFromRequest(producer.requests[0].GetJob()),
	))

	require.Equal(t, event.GetEventId(), deliveries.Records()[0].ProviderReference)
}

func envelopeFromRequest(job *jobsv1.NewJob) *jobsv1.JobEnvelope {
	return &jobsv1.JobEnvelope{
		Id:             uuid.NewString(),
		Direction:      job.GetDirection(),
		Scope:          job.GetScope(),
		Queue:          job.GetQueue(),
		Topic:          job.GetTopic(),
		Source:         job.GetSource(),
		IdempotencyKey: job.GetIdempotencyKey(),
		SchemaVersion:  job.GetSchemaVersion(),
		Payload:        job.GetPayload(),
		ContentType:    job.GetContentType(),
		State:          jobsv1.JobState_JOB_STATE_PROCESSING,
		MaxAttempts:    job.GetMaxAttempts(),
		Lease: &jobsv1.JobLease{
			Owner: "worker", Token: uuid.NewString(),
		},
		CreatedAt: timestamppb.Now(),
	}
}
