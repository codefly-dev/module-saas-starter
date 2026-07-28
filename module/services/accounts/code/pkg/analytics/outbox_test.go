package analytics_test

import (
	"context"
	"testing"

	"accounts/pkg/analytics"
	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type recordingProducer struct {
	request *jobsv1.EnqueueJobRequest
}

func (p *recordingProducer) EnqueueJob(
	_ context.Context,
	request *jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	p.request = proto.Clone(request).(*jobsv1.EnqueueJobRequest)
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
	job := producer.request.GetJob()
	require.Equal(t, event.GetEventId(), job.GetIdempotencyKey())
	require.Equal(t, analytics.ExportQueue, job.GetQueue())
	require.Equal(t, analytics.ExportTopic, job.GetTopic())
	require.Equal(t, event.GetOrganizationId(), job.GetScope().GetOrganizationId())
	require.NotContains(t, job.GetAttributes(), "organization_id")
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

	envelope := &jobsv1.JobEnvelope{
		Id:             uuid.NewString(),
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:          producer.request.GetJob().GetScope(),
		Queue:          producer.request.GetJob().GetQueue(),
		Topic:          producer.request.GetJob().GetTopic(),
		Source:         producer.request.GetJob().GetSource(),
		IdempotencyKey: producer.request.GetJob().GetIdempotencyKey(),
		SchemaVersion:  producer.request.GetJob().GetSchemaVersion(),
		Payload:        producer.request.GetJob().GetPayload(),
		ContentType:    producer.request.GetJob().GetContentType(),
		State:          jobsv1.JobState_JOB_STATE_PROCESSING,
		MaxAttempts:    producer.request.GetJob().GetMaxAttempts(),
		Lease: &jobsv1.JobLease{
			Owner: "worker", Token: uuid.NewString(),
		},
	}
	sink := analytics.NewMemorySink()
	handler, err := analytics.NewExportHandler(registry, sink)
	require.NoError(t, err)
	require.NoError(t, handler(t.Context(), envelope))
	require.Equal(t, event.GetEventId(), sink.Events()[0].GetEventId())

	envelope.Topic = "analytics.unregistered"
	err = handler(t.Context(), envelope)
	require.Error(t, err)
}
