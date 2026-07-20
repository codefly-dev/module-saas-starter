package email

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	notificationsv1 "accounts/pkg/gen/saas/notifications/v1"
	"accounts/pkg/jobs"

	"google.golang.org/protobuf/proto"
)

const (
	DeliveryQueue         = "notifications"
	DeliveryTopic         = "notification.email.send"
	DeliverySchemaVersion = 1
	DeliveryMaxAttempts   = 5
	DeliveryContentType   = "application/protobuf"
	orderingNamespace     = "email_recipient"
)

// Scope selects the authority attached to an email job. Pre-authentication
// messages use GlobalScope; authenticated user and tenant flows should use the
// narrowest subject or tenant scope available.
type Scope struct {
	OrganizationID string
	SubjectID      string
	Global         bool
}

func TenantScope(organizationID string) Scope { return Scope{OrganizationID: organizationID} }
func SubjectScope(subjectID string) Scope     { return Scope{SubjectID: subjectID} }
func GlobalScope() Scope                      { return Scope{Global: true} }

// Outbox renders messages before enqueue and persists only generated command
// types. The producer determines whether enqueue joins a request transaction
// or uses the isolated job-worker transaction.
type Outbox struct {
	producer  jobs.Producer
	templates TemplateStore
	from      string
}

func NewOutbox(producer jobs.Producer, templates TemplateStore, from string) (*Outbox, error) {
	if producer == nil {
		return nil, errors.New("email: job producer is required")
	}
	if strings.TrimSpace(from) == "" {
		return nil, errors.New("email: from address is required")
	}
	return &Outbox{producer: producer, templates: templates, from: from}, nil
}

type TemplateRequest struct {
	DeliveryKey string
	Scope       Scope
	Source      string
	Template    string
	To          string
	Variables   map[string]string
	Fallback    *Message
}

// EnqueueTemplate resolves the template now and stores the exact rendered
// result. A fallback is useful for bootstrap/security templates that must still
// work when a deployment has not seeded an optional catalog entry.
func (o *Outbox) EnqueueTemplate(ctx context.Context, request TemplateRequest) error {
	message, err := RenderTemplate(
		ctx, o.templates, o.from, request.Template, request.To, request.Variables,
	)
	if err != nil {
		if request.Fallback == nil {
			return err
		}
		message = cloneMessage(request.Fallback)
		if message.From == "" {
			message.From = o.from
		}
	}
	return o.Enqueue(ctx, request.DeliveryKey, request.Scope, request.Source, message)
}

// Enqueue validates and persists one exact rendered message.
func (o *Outbox) Enqueue(
	ctx context.Context,
	deliveryKey string,
	scope Scope,
	source string,
	message *Message,
) error {
	request, err := newDeliveryJob(deliveryKey, scope, source, message)
	if err != nil {
		return err
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
		return errors.New("email: enqueue did not persist a durable job")
	}
}

func newDeliveryJob(
	deliveryKey string,
	scope Scope,
	source string,
	message *Message,
) (*jobsv1.EnqueueJobRequest, error) {
	if message == nil {
		return nil, errors.New("email: message is required")
	}
	if deliveryKey == "" {
		return nil, errors.New("email: delivery key is required")
	}
	if err := message.Validate(); err != nil {
		return nil, err
	}
	jobScope, err := generatedScope(scope)
	if err != nil {
		return nil, err
	}
	payload := &notificationsv1.EmailDeliveryJob{
		DeliveryKey: deliveryKey,
		From:        message.From,
		To:          append([]string(nil), message.To...),
		ReplyTo:     message.ReplyTo,
		Subject:     message.Subject,
		HtmlBody:    message.HTMLBody,
		TextBody:    message.TextBody,
		Tags:        cloneTags(message.Tags),
	}
	if err := jobs.ValidateCommand(payload); err != nil {
		return nil, fmt.Errorf("email: invalid delivery workload: %w", err)
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("email: encode delivery workload: %w", err)
	}
	request := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:          jobScope,
		Queue:          DeliveryQueue,
		Topic:          DeliveryTopic,
		Source:         source,
		IdempotencyKey: deliveryKey,
		Ordering: &jobsv1.JobOrderingKey{
			Namespace:  orderingNamespace,
			Components: []string{recipientDigest(message.To)},
		},
		SchemaVersion: DeliverySchemaVersion,
		Payload:       encoded,
		ContentType:   DeliveryContentType,
		MaxAttempts:   DeliveryMaxAttempts,
	}}
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, fmt.Errorf("email: invalid enqueue command: %w", err)
	}
	return request, nil
}

func generatedScope(scope Scope) (*jobsv1.JobScope, error) {
	switch {
	case scope.OrganizationID != "" && scope.SubjectID == "" && !scope.Global:
		return &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{
			OrganizationId: scope.OrganizationID,
		}}, nil
	case scope.SubjectID != "" && scope.OrganizationID == "" && !scope.Global:
		return &jobsv1.JobScope{Value: &jobsv1.JobScope_SubjectId{
			SubjectId: scope.SubjectID,
		}}, nil
	case scope.Global && scope.OrganizationID == "" && scope.SubjectID == "":
		return &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}}, nil
	default:
		return nil, errors.New("email: exactly one delivery scope is required")
	}
}

// NewJobHandler is the only path that invokes the transport adapter.
func NewJobHandler(sender Sender) (jobs.Handler, error) {
	if sender == nil {
		return nil, errors.New("email: sender is required")
	}
	return func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
		payload, err := validateDeliveryEnvelope(envelope)
		if err != nil {
			return jobs.NewProcessingError("email.invalid_job", "invalid email delivery job", false)
		}
		message := &Message{
			// The job UUID is stable for automatic retries and new for an
			// intentional operator replay of the exact copied payload.
			IdempotencyKey: envelope.GetId(),
			From:           payload.GetFrom(),
			To:             append([]string(nil), payload.GetTo()...),
			ReplyTo:        payload.GetReplyTo(),
			Subject:        payload.GetSubject(),
			HTMLBody:       payload.GetHtmlBody(),
			TextBody:       payload.GetTextBody(),
			Tags:           cloneTags(payload.GetTags()),
		}
		if err := message.Validate(); err != nil {
			return jobs.NewProcessingError("email.invalid_message", "invalid email message", false)
		}
		if _, err := sender.Send(ctx, message); err != nil {
			var deliveryError *DeliveryError
			if errors.As(err, &deliveryError) && !deliveryError.Retryable {
				return jobs.NewProcessingError("email.provider_rejected", "email provider rejected the message", false)
			}
			return err
		}
		return nil
	}, nil
}

func validateDeliveryEnvelope(envelope *jobsv1.JobEnvelope) (*notificationsv1.EmailDeliveryJob, error) {
	if envelope == nil || jobs.ValidateCommand(envelope) != nil {
		return nil, errors.New("email: invalid envelope")
	}
	if envelope.GetDirection() != jobsv1.JobDirection_JOB_DIRECTION_OUTBOX ||
		envelope.GetQueue() != DeliveryQueue ||
		envelope.GetTopic() != DeliveryTopic ||
		envelope.GetSchemaVersion() != DeliverySchemaVersion ||
		envelope.GetMaxAttempts() != DeliveryMaxAttempts ||
		envelope.GetContentType() != DeliveryContentType {
		return nil, errors.New("email: unexpected job routing")
	}
	payload := &notificationsv1.EmailDeliveryJob{}
	if err := proto.Unmarshal(envelope.GetPayload(), payload); err != nil {
		return nil, err
	}
	if jobs.ValidateCommand(payload) != nil ||
		!jobs.PayloadIdentityMatches(envelope, payload.GetDeliveryKey()) {
		return nil, errors.New("email: workload does not match envelope")
	}
	expectedOrdering, err := jobs.CanonicalOrderingKey(&jobsv1.JobOrderingKey{
		Namespace:  orderingNamespace,
		Components: []string{recipientDigest(payload.GetTo())},
	})
	if err != nil || expectedOrdering != envelope.GetOrderingKey() {
		return nil, errors.New("email: ordering key does not match recipients")
	}
	return payload, nil
}

// DeliveryRetryDelay keeps provider cadence configurable while the generic
// worker remains the sole owner of schedules, attempts, and exhaustion.
func DeliveryRetryDelay(attempt uint32) time.Duration {
	schedule := [...]time.Duration{
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
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

func recipientDigest(recipients []string) string {
	canonical := make([]string, len(recipients))
	for index, recipient := range recipients {
		canonical[index] = strings.ToLower(strings.TrimSpace(recipient))
	}
	sort.Strings(canonical)
	digest := sha256.Sum256([]byte(strings.Join(canonical, "\x00")))
	return hex.EncodeToString(digest[:])
}

func cloneMessage(message *Message) *Message {
	if message == nil {
		return nil
	}
	copy := *message
	copy.To = append([]string(nil), message.To...)
	copy.Tags = cloneTags(message.Tags)
	return &copy
}

func cloneTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	copy := make(map[string]string, len(tags))
	for key, value := range tags {
		copy[key] = value
	}
	return copy
}
