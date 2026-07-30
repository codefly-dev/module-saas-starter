package email

import (
	"context"
	"errors"
	"testing"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	notificationsv1 "accounts/pkg/gen/saas/notifications/v1"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type recordingProducer struct {
	request  *jobsv1.EnqueueJobRequest
	response *jobsv1.EnqueueJobResponse
	err      error
}

func (producer *recordingProducer) EnqueueJob(
	_ context.Context,
	request *jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	producer.request = proto.Clone(request).(*jobsv1.EnqueueJobRequest)
	if producer.err != nil {
		return nil, producer.err
	}
	if producer.response != nil {
		return producer.response, nil
	}
	return &jobsv1.EnqueueJobResponse{
		JobId:       uuid.NewString(),
		Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
	}, nil
}

type memoryTemplateStore struct {
	templates map[string]*Template
	err       error
}

func (store *memoryTemplateStore) GetTemplate(_ context.Context, name string) (*Template, error) {
	if store.err != nil {
		return nil, store.err
	}
	template, ok := store.templates[name]
	if !ok {
		return nil, errors.New("template not found")
	}
	copy := *template
	return &copy, nil
}

func TestRenderTemplateEscapesHTMLAndRejectsInvalidPlaceholders(t *testing.T) {
	store := &memoryTemplateStore{templates: map[string]*Template{
		"safe": {
			Name:            "safe",
			SubjectTemplate: "Welcome {{name}}",
			HTMLTemplate:    `<p data-name="{{name}}">{{name}}</p>`,
			TextTemplate:    "Welcome {{name}}",
		},
		"missing": {
			Name:            "missing",
			SubjectTemplate: "Welcome {{name}}",
			HTMLTemplate:    "<p>{{unknown}}</p>",
			TextTemplate:    "Welcome {{name}}",
		},
		"malformed": {
			Name:            "malformed",
			SubjectTemplate: "Welcome {{ name }}",
			HTMLTemplate:    "<p>Welcome</p>",
			TextTemplate:    "Welcome",
		},
	}}

	message, err := RenderTemplate(
		context.Background(), store, "no-reply@example.com", "safe",
		"user@example.com", map[string]string{"name": `<img src=x onerror="alert(1)">`},
	)
	require.NoError(t, err)
	require.Equal(t, `Welcome <img src=x onerror="alert(1)">`, message.Subject)
	require.Equal(t,
		`<p data-name="&lt;img src=x onerror=&#34;alert(1)&#34;&gt;">&lt;img src=x onerror=&#34;alert(1)&#34;&gt;</p>`,
		message.HTMLBody,
	)
	require.Equal(t, `Welcome <img src=x onerror="alert(1)">`, message.TextBody)

	_, err = RenderTemplate(
		context.Background(), store, "no-reply@example.com", "missing",
		"user@example.com", map[string]string{"name": "Ada"},
	)
	require.ErrorContains(t, err, `missing variable "unknown"`)

	_, err = RenderTemplate(
		context.Background(), store, "no-reply@example.com", "malformed",
		"user@example.com", map[string]string{"name": "Ada"},
	)
	require.ErrorContains(t, err, "malformed template placeholder")
}

func TestOutboxPersistsExactRenderedGeneratedMessage(t *testing.T) {
	producer := &recordingProducer{}
	outbox, err := NewOutbox(producer, &memoryTemplateStore{templates: map[string]*Template{
		"welcome": {
			Name: "welcome", Version: 7, SubjectTemplate: "Welcome {{name}}",
			HTMLTemplate: "<p>Hello {{name}}</p>", TextTemplate: "Hello {{name}}",
		},
	}}, "Acme <no-reply@example.com>")
	require.NoError(t, err)

	deliveryKey := uuid.NewString()
	orgID := uuid.NewString()
	require.NoError(t, outbox.EnqueueTemplate(context.Background(), TemplateRequest{
		DeliveryKey: deliveryKey,
		Scope:       TenantScope(orgID),
		Source:      "saas.accounts.users",
		Template:    "welcome",
		To:          "user@example.com",
		Variables:   map[string]string{"name": "Ada"},
		Tags:        map[string]string{"invitation_id": deliveryKey},
	}))

	job := producer.request.GetJob()
	require.Equal(t, jobsv1.JobDirection_JOB_DIRECTION_OUTBOX, job.GetDirection())
	require.Equal(t, orgID, job.GetScope().GetOrganizationId())
	require.Equal(t, DeliveryQueue, job.GetQueue())
	require.Equal(t, DeliveryTopic, job.GetTopic())
	require.Equal(t, deliveryKey, job.GetIdempotencyKey())
	require.NotContains(t, job.GetOrdering().GetComponents()[0], "user@example.com")

	payload := &notificationsv1.EmailDeliveryJob{}
	require.NoError(t, proto.Unmarshal(job.GetPayload(), payload))
	require.Equal(t, deliveryKey, payload.GetDeliveryKey())
	require.Equal(t, "Acme <no-reply@example.com>", payload.GetFrom())
	require.Equal(t, []string{"user@example.com"}, payload.GetTo())
	require.Equal(t, "Welcome Ada", payload.GetSubject())
	require.Equal(t, "<p>Hello Ada</p>", payload.GetHtmlBody())
	require.Equal(t, "Hello Ada", payload.GetTextBody())
	require.Equal(t, "welcome", payload.GetTags()["template"])
	require.Equal(t, "7", payload.GetTags()["template_version"])
	require.Equal(t, deliveryKey, payload.GetTags()["invitation_id"])
}

func TestOutboxUsesValidatedFallbackAndRequiresDurableDisposition(t *testing.T) {
	producer := &recordingProducer{}
	outbox, err := NewOutbox(
		producer,
		&memoryTemplateStore{err: errors.New("catalog unavailable")},
		"no-reply@example.com",
	)
	require.NoError(t, err)
	require.NoError(t, outbox.EnqueueTemplate(context.Background(), TemplateRequest{
		DeliveryKey: uuid.NewString(), Scope: GlobalScope(),
		Source: "saas.accounts.authentication", Template: "magic_link",
		To: "user@example.com", Fallback: &Message{
			To: []string{"user@example.com"}, Subject: "Sign in", TextBody: "link",
		},
	}))
	payload := &notificationsv1.EmailDeliveryJob{}
	require.NoError(t, proto.Unmarshal(producer.request.GetJob().GetPayload(), payload))
	require.Equal(t, "no-reply@example.com", payload.GetFrom())
	require.True(t, producer.request.GetJob().GetScope().GetGlobal())

	producer.response = &jobsv1.EnqueueJobResponse{}
	err = outbox.Enqueue(context.Background(), uuid.NewString(), GlobalScope(),
		"saas.accounts.authentication", &Message{
			From: "no-reply@example.com", To: []string{"user@example.com"},
			Subject: "Sign in", TextBody: "link",
		})
	require.ErrorContains(t, err, "did not persist")
}

func TestJobHandlerDeliversExactMessageWithStableProviderKey(t *testing.T) {
	producer := &recordingProducer{}
	outbox, err := NewOutbox(producer, nil, "no-reply@example.com")
	require.NoError(t, err)
	deliveryKey := uuid.NewString()
	require.NoError(t, outbox.Enqueue(context.Background(), deliveryKey, SubjectScope(uuid.NewString()),
		"saas.accounts.security", &Message{
			From: "no-reply@example.com", To: []string{"user@example.com"},
			Subject: "Security alert", HTMLBody: "<p>Alert</p>",
			Tags: map[string]string{"type": "security"},
		}))

	sender := NewFakeSender()
	handler, err := NewJobHandler(sender)
	require.NoError(t, err)
	envelope := envelopeFromRequest(producer.request)
	require.NoError(t, handler(context.Background(), envelope))
	require.Len(t, sender.Sent, 1)
	require.Equal(t, envelope.GetId(), sender.Sent[0].IdempotencyKey)
	require.Equal(t, "<p>Alert</p>", sender.Sent[0].HTMLBody)

	replay := proto.Clone(envelope).(*jobsv1.JobEnvelope)
	replay.Id = uuid.NewString()
	replay.IdempotencyKey = "operator-replay-1"
	replay.ReplayOf = envelope.GetId()
	require.NoError(t, handler(context.Background(), replay))
	require.Len(t, sender.Sent, 2)
	require.Equal(t, replay.GetId(), sender.Sent[1].IdempotencyKey)
	require.Equal(t, sender.Sent[0].HTMLBody, sender.Sent[1].HTMLBody)

	envelope.Topic = "notification.email.invalid"
	err = handler(context.Background(), envelope)
	var processingError *jobs.ProcessingError
	require.ErrorAs(t, err, &processingError)
	require.False(t, processingError.Retryable)
}

type rejectingSender struct{}

func (rejectingSender) Send(context.Context, *Message) (string, error) {
	return "", &DeliveryError{StatusCode: 422, Retryable: false}
}

func TestJobHandlerClassifiesProviderRejectionAsPermanent(t *testing.T) {
	producer := &recordingProducer{}
	outbox, err := NewOutbox(producer, nil, "no-reply@example.com")
	require.NoError(t, err)
	require.NoError(t, outbox.Enqueue(context.Background(), uuid.NewString(), GlobalScope(),
		"saas.accounts.authentication", &Message{
			From: "no-reply@example.com", To: []string{"user@example.com"},
			Subject: "Sign in", TextBody: "link",
		}))
	handler, err := NewJobHandler(rejectingSender{})
	require.NoError(t, err)
	err = handler(context.Background(), envelopeFromRequest(producer.request))
	var processingError *jobs.ProcessingError
	require.ErrorAs(t, err, &processingError)
	require.False(t, processingError.Retryable)
	require.Equal(t, "email.provider_rejected", processingError.Failure.GetCode())
}

func envelopeFromRequest(request *jobsv1.EnqueueJobRequest) *jobsv1.JobEnvelope {
	job := request.GetJob()
	ordering, _ := jobs.CanonicalOrderingKey(job.GetOrdering())
	return &jobsv1.JobEnvelope{
		Id: uuid.NewString(), Direction: job.GetDirection(), Scope: job.GetScope(),
		Queue: job.GetQueue(), Topic: job.GetTopic(), Source: job.GetSource(),
		IdempotencyKey: job.GetIdempotencyKey(), OrderingKey: ordering,
		SchemaVersion: job.GetSchemaVersion(), Payload: job.GetPayload(),
		ContentType: job.GetContentType(), Attributes: job.GetAttributes(),
		State: jobsv1.JobState_JOB_STATE_PROCESSING, AttemptCount: 1,
		MaxAttempts: job.GetMaxAttempts(),
	}
}
