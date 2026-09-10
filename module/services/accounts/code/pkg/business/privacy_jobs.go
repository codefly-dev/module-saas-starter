package business

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
)

// Privacy export and deletion run as durable subject-scoped work. Their
// execution contract:
//
//   - Atomic acceptance: the request row and its job commit in the same
//     subject transaction, so an accepted request always has an owner and a
//     rejected one leaves nothing behind.
//   - Leased execution: the generic worker owns claims, heartbeats, retry
//     scheduling, and dead-lettering. The job lease token is carried onto the
//     request row, so only the attempt that currently holds it can transition
//     the request — a worker that lost its lease cannot finalize the attempt
//     that replaced it.
//   - Replay-safe effects: the request UUID is the stable logical operation ID
//     across every attempt. Adapters derive per-step idempotency keys from it
//     and record a receipt per completed step, so a crash between an external
//     effect and its receipt repeats the call under the same key instead of
//     duplicating it.
//   - Ordering: strict FIFO per subject, so a deletion cannot interleave with
//     an export of the same person's data.
//   - Completion: only after every step the adapter declares required carries a
//     durable receipt. An adapter that returns success without them is a
//     permanent failure, not a completed request.
const (
	PrivacyWorkflowQueue         = "privacy"
	PrivacyExportTopic           = "privacy.export.run"
	PrivacyDeletionTopic         = "privacy.deletion.run"
	PrivacyWorkflowSource        = "saas.privacy"
	PrivacyWorkflowSchemaVersion = 1
	PrivacyWorkflowMaxAttempts   = 8
	PrivacyWorkflowContentType   = "application/json"
	privacyOrderingNamespace     = "privacy_subject"
)

type privacyWorkflowPayload struct {
	RequestID string          `json:"request_id"`
	UserID    string          `json:"user_id"`
	Type      GDPRRequestType `json:"type"`
}

func privacyTopic(kind GDPRRequestType) string {
	if kind == GDPRDeletion {
		return PrivacyDeletionTopic
	}
	return PrivacyExportTopic
}

func newPrivacyWorkflowJob(req *GDPRRequest) (*jobsv1.EnqueueJobRequest, error) {
	payload, err := json.Marshal(privacyWorkflowPayload{
		RequestID: req.ID, UserID: req.UserID, Type: req.Type,
	})
	if err != nil {
		return nil, err
	}
	request := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_SubjectId{SubjectId: req.UserID}},
		Queue:          PrivacyWorkflowQueue,
		Topic:          privacyTopic(req.Type),
		Source:         PrivacyWorkflowSource,
		IdempotencyKey: req.ID,
		Ordering: &jobsv1.JobOrderingKey{
			Namespace:  privacyOrderingNamespace,
			Components: []string{req.UserID},
		},
		SchemaVersion: PrivacyWorkflowSchemaVersion,
		Payload:       payload,
		ContentType:   PrivacyWorkflowContentType,
		MaxAttempts:   PrivacyWorkflowMaxAttempts,
	}}
	if err := jobs.ValidateCommand(request); err != nil {
		return nil, err
	}
	return request, nil
}

func decodePrivacyWorkflowEnvelope(envelope *jobsv1.JobEnvelope) (privacyWorkflowPayload, error) {
	if err := jobs.ValidateCommand(envelope); err != nil {
		return privacyWorkflowPayload{}, err
	}
	scope, ok := envelope.GetScope().GetValue().(*jobsv1.JobScope_SubjectId)
	if envelope.GetDirection() != jobsv1.JobDirection_JOB_DIRECTION_OUTBOX ||
		!ok || scope.SubjectId == "" ||
		envelope.GetQueue() != PrivacyWorkflowQueue ||
		envelope.GetSource() != PrivacyWorkflowSource ||
		envelope.GetSchemaVersion() != PrivacyWorkflowSchemaVersion ||
		envelope.GetContentType() != PrivacyWorkflowContentType {
		return privacyWorkflowPayload{}, errors.New("privacy: unexpected workflow routing")
	}
	var payload privacyWorkflowPayload
	if err := json.Unmarshal(envelope.GetPayload(), &payload); err != nil {
		return privacyWorkflowPayload{}, err
	}
	if payload.RequestID == "" || payload.UserID != scope.SubjectId ||
		envelope.GetTopic() != privacyTopic(payload.Type) ||
		!jobs.PayloadIdentityMatches(envelope, payload.RequestID) {
		return privacyWorkflowPayload{}, errors.New("privacy: workflow identity does not match job")
	}
	return payload, nil
}

// NewPrivacyJobHandler executes one leased privacy request. Every durable
// transition it makes is fenced by the job's lease token, and a failure to
// persist one is returned rather than logged away: the job then retries, so the
// request cannot silently diverge from what the adapter actually did.
func (s *Service) NewPrivacyJobHandler() jobs.Handler {
	return func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
		payload, err := decodePrivacyWorkflowEnvelope(envelope)
		if err != nil {
			return jobs.NewProcessingError(
				"privacy.invalid_job", "unexpected privacy workflow job", false)
		}
		return s.runPrivacyWorkflow(ctx, payload, GDPRLease{
			JobID:     envelope.GetId(),
			Owner:     envelope.GetLease().GetOwner(),
			Token:     envelope.GetLease().GetToken(),
			Attempt:   envelope.GetAttemptCount(),
			ExpiresAt: envelope.GetLease().GetExpiresAt().AsTime(),
		}, envelope.GetAttemptCount() >= envelope.GetMaxAttempts())
	}
}

func (s *Service) runPrivacyWorkflow(
	ctx context.Context,
	payload privacyWorkflowPayload,
	lease GDPRLease,
	lastAttempt bool,
) error {
	w := wool.Get(ctx).In("runPrivacyWorkflow")

	gdprStore, ok := s.store.(GDPRStore)
	if !ok {
		return jobs.NewProcessingError(
			"privacy.store_unavailable", "store does not implement GDPRStore", false)
	}

	var request *GDPRRequest
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var claimErr error
		request, claimErr = gdprStore.ClaimGDPRRequest(ctx, payload.RequestID, lease)
		return claimErr
	}); err != nil {
		switch {
		case errors.Is(err, ErrPrivacyRequestNotFound):
			return jobs.NewProcessingError(
				"privacy.request_missing", "privacy request no longer exists", false)
		case errors.Is(err, ErrPrivacyLeaseLost):
			// A newer attempt already owns this request. Returning before the
			// adapter runs is what keeps a stale worker from producing effects
			// for work it no longer holds.
			return err
		default:
			return w.Wrapf(err, "cannot claim privacy request")
		}
	}
	// A redelivered job for finished work must not run the adapter again.
	if request.Status == GDPRCompleted {
		return nil
	}

	// The capability is unavailable without an adapter, so a job that reaches a
	// runtime with none is unexecutable: dead-letter it and say so on the
	// request rather than leaving it processing forever.
	workflow := s.privacy
	if workflow == nil {
		return s.finishPrivacyWorkflow(ctx, gdprStore, request, lease, GDPROutcome{
			Status:      GDPRFailed,
			FailureCode: "privacy.workflow_unavailable",
			Error:       "no privacy workflow adapter is configured in this runtime",
		}, jobs.NewProcessingError(
			"privacy.workflow_unavailable", "privacy workflow is not configured", false))
	}

	operation := newPrivacyOperation(request, func(ctx context.Context, step, receipt string) error {
		return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			return gdprStore.RecordGDPRStepReceipt(ctx, request.ID, lease, step, receipt)
		})
	})

	var artifact PrivacyExportArtifact
	var runErr error
	switch request.Type {
	case GDPRExport:
		artifact, runErr = workflow.Export(ctx, operation)
	case GDPRDeletion:
		runErr = workflow.Delete(ctx, operation)
	default:
		runErr = NewPrivacyFailure(
			"privacy.unknown_request_type", "unknown privacy request type", true)
	}
	if runErr == nil {
		if missing := missingPrivacyReceipts(workflow.RequiredSteps(request.Type), operation); len(missing) > 0 {
			runErr = NewPrivacyFailure(
				"privacy.incomplete_receipts",
				fmt.Sprintf("workflow reported success without receipts for: %v", missing),
				true,
			)
		}
	}
	if runErr != nil {
		return s.failPrivacyWorkflow(ctx, gdprStore, request, lease, runErr, lastAttempt)
	}

	outcome := GDPROutcome{Status: GDPRCompleted}
	if request.Type == GDPRExport {
		outcome.DownloadURL = artifact.DownloadURL
		expiresAt := artifact.ExpiresAt
		outcome.ExpiresAt = &expiresAt
	}
	return s.finishPrivacyWorkflow(ctx, gdprStore, request, lease, outcome, nil)
}

// finishPrivacyWorkflow applies one durable transition and the side effects that
// must commit with it. A deletion's analytics suppression and completion audit
// record share the transaction that marks it complete, so the record cannot
// claim more than the database holds.
func (s *Service) finishPrivacyWorkflow(
	ctx context.Context,
	gdprStore GDPRStore,
	request *GDPRRequest,
	lease GDPRLease,
	outcome GDPROutcome,
	handlerResult error,
) error {
	completingDeletion := outcome.Status == GDPRCompleted && request.Type == GDPRDeletion
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		if completingDeletion {
			if err := s.suppressProductIdentity(
				ctx, userAnalyticsSuppression(request.UserID), System(),
			); err != nil {
				return err
			}
		}
		if err := gdprStore.FinishGDPRRequest(ctx, request.ID, lease, outcome); err != nil {
			return err
		}
		if completingDeletion {
			return s.emitTx(
				ctx, request.UserID, "system",
				EventGDPRDeletionDone, "gdpr_request", request.ID, "",
			)
		}
		return nil
	}); err != nil {
		return errors.Join(err, handlerResult)
	}
	return handlerResult
}

func (s *Service) failPrivacyWorkflow(
	ctx context.Context,
	gdprStore GDPRStore,
	request *GDPRRequest,
	lease GDPRLease,
	runErr error,
	lastAttempt bool,
) error {
	code, message, permanent := privacyFailureDiagnostics(runErr)
	terminal := permanent || lastAttempt
	status := GDPRRetrying
	if terminal {
		status = GDPRFailed
	}
	return s.finishPrivacyWorkflow(ctx, gdprStore, request, lease, GDPROutcome{
		Status:      status,
		FailureCode: code,
		Error:       message,
	}, jobs.NewProcessingError(code, message, !permanent))
}

// privacyFailureDiagnostics keeps arbitrary adapter errors out of durable
// history. Only an explicitly declared PrivacyFailure carries its own code and
// message; anything else becomes a generic retryable diagnostic, because a
// provider error can contain credentials or the personal data being exported.
func privacyFailureDiagnostics(err error) (code, message string, permanent bool) {
	var failure *PrivacyFailure
	if errors.As(err, &failure) && failure != nil {
		declared := &jobsv1.JobFailure{Code: failure.Code, Message: failure.Message}
		if jobs.ValidateCommand(declared) == nil {
			return failure.Code, failure.Message, failure.Permanent
		}
	}
	if errors.Is(err, ErrPrivacyLeaseLost) {
		return "privacy.lease_lost", "the worker lost its lease on this request", false
	}
	return "privacy.workflow_failed", "privacy workflow adapter failed", false
}

func privacyFailureCode(err error) string {
	code, _, _ := privacyFailureDiagnostics(err)
	return code
}

func missingPrivacyReceipts(required []string, operation *PrivacyOperation) []string {
	var missing []string
	for _, step := range required {
		if _, ok := operation.Receipt(step); !ok {
			missing = append(missing, step)
		}
	}
	sort.Strings(missing)
	return missing
}

// PrivacyWorkflowRetryDelay backs a failed attempt off far enough to outlast a
// provider outage before the attempt budget is spent.
func PrivacyWorkflowRetryDelay(attempt uint32) time.Duration {
	schedule := [...]time.Duration{
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
		6 * time.Hour,
		12 * time.Hour,
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
