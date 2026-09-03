package business

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"
)

// The audit export feed is the asynchronous tee to an external analytics or
// warehouse destination. Its delivery contract:
//
//   - Source of truth: Postgres. Each org-scoped audit row enqueues one export
//     job in the same transaction, so the tee commits atomically with the audit
//     record but delivery happens off the mutation path.
//   - At-least-once: the job is retried until the sink accepts it or the attempt
//     budget is exhausted; the sink must dedupe on AuditEntry.ID (the job's
//     idempotency key), so it may observe an event more than once.
//   - Ordering: none across events. Each event carries CreatedAt; the sink must
//     not assume delivery order.
//   - Batching: per event. The generic worker leases a batch of jobs but the
//     sink's Emit contract is one event per call.
//   - Scope: org-scoped events only, matching the webhook fan-out. Control-plane
//     (platform) audit events stay Postgres-only.
const (
	AuditExportQueue         = "audit_export"
	AuditExportTopic         = "audit.event.export"
	AuditExportSource        = "saas.audit"
	AuditExportSchemaVersion = 1
	AuditExportMaxAttempts   = 8
	AuditExportContentType   = "application/json"
)

// ExternalAuditSink delivers one audit event to a destination outside the
// Postgres source of truth. It is the single-method Emit(ctx, AuditEntry)
// contract, but — unlike AuditEmitter.Emit — it returns an error so the durable
// feed can retry: delivery is at-least-once and the sink must dedupe on ID.
type ExternalAuditSink interface {
	Emit(ctx context.Context, entry AuditEntry) error
}

// auditExportPayload is the JSON job payload teed to the external sink. Payload
// is PII-redacted at enqueue time (like every other export path) so nothing
// leaving the audit store carries personally identifying fields.
type auditExportPayload struct {
	ID             string         `json:"id"`
	ActorID        string         `json:"actor_id"`
	ActorType      string         `json:"actor_type"`
	EventType      string         `json:"event_type"`
	SchemaVersion  int            `json:"schema_version"`
	Resource       string         `json:"resource"`
	ResourceID     string         `json:"resource_id"`
	OrgID          string         `json:"org_id"`
	Payload        map[string]any `json:"payload,omitempty"`
	IPAddress      string         `json:"ip_address,omitempty"`
	ImpersonatedBy string         `json:"impersonated_by,omitempty"`
	IsImpersonated bool           `json:"is_impersonated,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

func newAuditExportPayload(entry AuditEntry) auditExportPayload {
	return auditExportPayload{
		ID:             entry.ID,
		ActorID:        entry.ActorID,
		ActorType:      entry.ActorType,
		EventType:      string(entry.EventType),
		SchemaVersion:  entry.SchemaVersion,
		Resource:       entry.Resource,
		ResourceID:     entry.ResourceID,
		OrgID:          entry.OrgID,
		Payload:        entry.Payload,
		IPAddress:      entry.IPAddress,
		ImpersonatedBy: entry.ImpersonatedBy,
		IsImpersonated: entry.IsImpersonated,
		CreatedAt:      entry.CreatedAt.UTC(),
	}
}

func (p auditExportPayload) toEntry() AuditEntry {
	return AuditEntry{
		ID:             p.ID,
		ActorID:        p.ActorID,
		ActorType:      p.ActorType,
		EventType:      EventType(p.EventType),
		SchemaVersion:  p.SchemaVersion,
		Resource:       p.Resource,
		ResourceID:     p.ResourceID,
		OrgID:          p.OrgID,
		Payload:        p.Payload,
		IPAddress:      p.IPAddress,
		ImpersonatedBy: p.ImpersonatedBy,
		IsImpersonated: p.IsImpersonated,
		CreatedAt:      p.CreatedAt,
	}
}

// enqueueAuditExport appends the export job using the caller's ambient
// transaction, so it commits with the audit row or not at all.
func enqueueAuditExport(ctx context.Context, producer jobs.Producer, entry AuditEntry) error {
	entry.Payload = RedactPayload(entry.EventType, entry.Payload)
	payload, err := json.Marshal(newAuditExportPayload(entry))
	if err != nil {
		return fmt.Errorf("audit: encode export payload: %w", err)
	}
	request := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope: &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{
			OrganizationId: entry.OrgID,
		}},
		Queue:          AuditExportQueue,
		Topic:          AuditExportTopic,
		Source:         AuditExportSource,
		IdempotencyKey: entry.ID,
		SchemaVersion:  AuditExportSchemaVersion,
		Payload:        payload,
		ContentType:    AuditExportContentType,
		MaxAttempts:    AuditExportMaxAttempts,
	}}
	if err := jobs.ValidateCommand(request); err != nil {
		return fmt.Errorf("audit: invalid export enqueue command: %w", err)
	}
	response, err := producer.EnqueueJob(ctx, request)
	if err != nil {
		return err
	}
	switch response.GetDisposition() {
	case jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
		jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE:
		return nil
	default:
		return errors.New("audit: enqueue did not persist a durable export command")
	}
}

// NewAuditExportJobHandler drains the export queue into the external sink. A
// sink failure is returned so the generic worker retries the job (at-least-once);
// a malformed job is a non-retryable ProcessingError.
func NewAuditExportJobHandler(sink ExternalAuditSink) (jobs.Handler, error) {
	if sink == nil {
		return nil, errors.New("audit: external sink is required")
	}
	return func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
		entry, err := decodeAuditExportEnvelope(envelope)
		if err != nil {
			return jobs.NewProcessingError("audit.invalid_job", "invalid audit export job", false)
		}
		return sink.Emit(ctx, entry)
	}, nil
}

func decodeAuditExportEnvelope(envelope *jobsv1.JobEnvelope) (AuditEntry, error) {
	if envelope == nil {
		return AuditEntry{}, errors.New("audit: missing export envelope")
	}
	if err := jobs.ValidateCommand(envelope); err != nil {
		return AuditEntry{}, err
	}
	scope, ok := envelope.GetScope().GetValue().(*jobsv1.JobScope_OrganizationId)
	if envelope.GetDirection() != jobsv1.JobDirection_JOB_DIRECTION_OUTBOX ||
		!ok || scope.OrganizationId == "" ||
		envelope.GetQueue() != AuditExportQueue ||
		envelope.GetTopic() != AuditExportTopic ||
		envelope.GetSource() != AuditExportSource ||
		envelope.GetSchemaVersion() != AuditExportSchemaVersion ||
		envelope.GetMaxAttempts() != AuditExportMaxAttempts ||
		envelope.GetContentType() != AuditExportContentType {
		return AuditEntry{}, errors.New("audit: unexpected export routing")
	}
	var payload auditExportPayload
	if err := json.Unmarshal(envelope.GetPayload(), &payload); err != nil {
		return AuditEntry{}, err
	}
	if !jobs.PayloadIdentityMatches(envelope, payload.ID) || payload.OrgID != scope.OrganizationId {
		return AuditEntry{}, errors.New("audit: export identity does not match job")
	}
	return payload.toEntry(), nil
}

// AuditExportRetryDelay is the bounded backoff for a destination that is slow or
// briefly unavailable; scheduling and exhaustion remain generic worker concerns.
func AuditExportRetryDelay(attempt uint32) time.Duration {
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
