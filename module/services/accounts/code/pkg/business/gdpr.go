package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
)

// ── GDPR domain types ──────────────────────────────────────────

type GDPRRequestType string

const (
	GDPRExport   GDPRRequestType = "export"
	GDPRDeletion GDPRRequestType = "deletion"
)

// GDPRRequestStatus is the durable progress of one privacy request. Pending is
// accepted-but-unclaimed, processing is leased by a worker, retrying is a
// retryable failure the jobs platform will re-lease, and failed is terminal
// until an operator replays the dead-lettered job.
type GDPRRequestStatus string

const (
	GDPRPending    GDPRRequestStatus = "pending"
	GDPRProcessing GDPRRequestStatus = "processing"
	GDPRRetrying   GDPRRequestStatus = "retrying"
	GDPRCompleted  GDPRRequestStatus = "completed"
	GDPRFailed     GDPRRequestStatus = "failed"
)

type GDPRRequest struct {
	ID           string
	UserID       string
	Type         GDPRRequestType
	Status       GDPRRequestStatus
	JobID        string
	DownloadURL  string
	ExpiresAt    *time.Time
	Error        string
	FailureCode  string
	Attempt      uint32
	LeaseOwner   string
	LeaseToken   string
	StepReceipts map[string]string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CompletedAt  *time.Time
}

// GDPRLease is the fencing token one worker attempt holds. It is the job lease
// carried onto the product row: a transition applies only while the row still
// records this exact token, so a worker whose lease expired cannot finalize the
// attempt that replaced it. JobID and ExpiresAt fence the claim itself — see
// ClaimGDPRRequest.
type GDPRLease struct {
	JobID     string
	Owner     string
	Token     string
	Attempt   uint32
	ExpiresAt time.Time
}

// GDPROutcome is one terminal-or-retryable transition of a leased request.
type GDPROutcome struct {
	Status      GDPRRequestStatus
	DownloadURL string
	ExpiresAt   *time.Time
	FailureCode string
	Error       string
}

// GDPRStore abstracts persistence for GDPR data requests. Creation and reads
// run as the subject; every execution transition runs under the control plane,
// because the workflow may have removed the subject's own identity before it
// reports what it did.
type GDPRStore interface {
	CreateGDPRRequest(ctx context.Context, req *GDPRRequest) error
	GetGDPRRequest(ctx context.Context, id string) (*GDPRRequest, error)
	GetUserGDPRRequests(ctx context.Context, userID string) ([]*GDPRRequest, error)

	// ClaimGDPRRequest takes the request into processing under lease. An
	// already-completed request is returned unchanged so a redelivered job is a
	// no-op rather than a second execution.
	ClaimGDPRRequest(ctx context.Context, id string, lease GDPRLease) (*GDPRRequest, error)
	// RecordGDPRStepReceipt durably records one completed provider effect.
	RecordGDPRStepReceipt(ctx context.Context, id string, lease GDPRLease, step, receipt string) error
	// FinishGDPRRequest applies a terminal or retryable transition under lease.
	FinishGDPRRequest(ctx context.Context, id string, lease GDPRLease, outcome GDPROutcome) error

	ListExpiredGDPRExports(ctx context.Context, before time.Time, limit int) ([]*GDPRRequest, error)
	ClearGDPRExportArtifact(ctx context.Context, id string) error
}

var (
	// ErrPrivacyWorkflowUnavailable is the fail-closed default: no adapter is
	// configured, so the capability is unavailable rather than partially done.
	ErrPrivacyWorkflowUnavailable = errors.New("privacy workflow is not configured")
	// ErrPrivacyLeaseLost means the request no longer records this worker's
	// fencing token. The attempt must stop without recording anything.
	ErrPrivacyLeaseLost = errors.New("privacy request lease lost")
	// ErrPrivacyRequestNotFound identifies a job whose request row is gone.
	ErrPrivacyRequestNotFound = errors.New("privacy request not found")
)

type PrivacyExportArtifact struct {
	DownloadURL string
	ExpiresAt   time.Time
}

// PrivacyWorkflow is the adapter seam for real export and deletion. It is
// invoked only from the leased worker, never from request traffic, and may be
// called again for the same operation after a crash or lease loss — see
// PrivacyOperation for the idempotency contract it must honor.
type PrivacyWorkflow interface {
	// RequiredSteps declares the provider effects that must each carry a
	// durable receipt before a request of this type may be reported complete.
	RequiredSteps(kind GDPRRequestType) []string
	Export(ctx context.Context, op *PrivacyOperation) (PrivacyExportArtifact, error)
	Delete(ctx context.Context, op *PrivacyOperation) error
}

// PrivacyArtifactCleaner is the optional half of PrivacyWorkflow that deletes a
// stored export artifact once its download window has lapsed. A workflow that
// does not implement it leaves artifact lifetime entirely to its storage.
type PrivacyArtifactCleaner interface {
	DeleteExportArtifact(ctx context.Context, requestID string) error
}

// PrivacyFailure is the only workflow error whose code and message enter
// durable request history. Every other error is replaced by a generic
// diagnostic, so a provider error carrying credentials or personal data is
// never persisted.
type PrivacyFailure struct {
	Code      string
	Message   string
	Permanent bool
}

func (e *PrivacyFailure) Error() string {
	if e == nil {
		return "privacy workflow failed"
	}
	return e.Code
}

// NewPrivacyFailure declares a bounded, operator-safe workflow failure.
// A permanent failure stops the attempt budget immediately; anything else is
// retried on the platform's backoff schedule.
func NewPrivacyFailure(code, message string, permanent bool) error {
	return &PrivacyFailure{Code: code, Message: message, Permanent: permanent}
}

// PrivacyOperation is the stable logical identity of one privacy request across
// every attempt that executes it. The request UUID is that identity: it does
// not change when a lease expires, a process restarts, or an operator replays
// the dead-lettered job.
//
// An adapter makes external effects replay-safe by pairing the two halves it
// carries: IdempotencyKey derives a deterministic per-step key to hand the
// provider, and RecordReceipt durably records what that step produced. An
// attempt that crashes between the effect and its receipt repeats the call
// under the same key; one that crashes after it skips the step entirely.
type PrivacyOperation struct {
	RequestID string
	UserID    string
	Attempt   uint32

	receipts map[string]string
	record   func(ctx context.Context, step, receipt string) error
}

// IdempotencyKey is the deterministic key for one step of this operation.
func (o *PrivacyOperation) IdempotencyKey(step string) string {
	digest := sha256.Sum256([]byte(o.RequestID + "\x00" + step))
	return "privacy/" + hex.EncodeToString(digest[:])
}

// Receipt returns what an earlier attempt recorded for a step, if anything.
func (o *PrivacyOperation) Receipt(step string) (string, bool) {
	receipt, ok := o.receipts[step]
	return receipt, ok
}

// RecordReceipt durably records one completed step. It fails with
// ErrPrivacyLeaseLost when this attempt no longer owns the request, which is
// the signal to abandon the operation rather than continue producing effects.
func (o *PrivacyOperation) RecordReceipt(ctx context.Context, step, receipt string) error {
	if err := o.record(ctx, step, receipt); err != nil {
		return err
	}
	o.receipts[step] = receipt
	return nil
}

func newPrivacyOperation(
	request *GDPRRequest,
	record func(ctx context.Context, step, receipt string) error,
) *PrivacyOperation {
	receipts := make(map[string]string, len(request.StepReceipts))
	for step, receipt := range request.StepReceipts {
		receipts[step] = receipt
	}
	return &PrivacyOperation{
		RequestID: request.ID,
		UserID:    request.UserID,
		Attempt:   request.Attempt,
		receipts:  receipts,
		record:    record,
	}
}

// ── Business logic ─────────────────────────────────────────────

// RequestExport accepts a data-export request. The request row and the durable
// job that executes it commit together, so an accepted request always has a
// worker that will pick it up and no job ever refers to a request that was
// never accepted.
func (s *Service) RequestExport(ctx context.Context, userID string) (*GDPRRequest, error) {
	return s.acceptPrivacyRequest(ctx, userID, GDPRExport)
}

// RequestDeletion accepts a data-deletion request on the same durable terms as
// RequestExport.
func (s *Service) RequestDeletion(ctx context.Context, userID string) (*GDPRRequest, error) {
	return s.acceptPrivacyRequest(ctx, userID, GDPRDeletion)
}

func (s *Service) acceptPrivacyRequest(
	ctx context.Context,
	userID string,
	kind GDPRRequestType,
) (*GDPRRequest, error) {
	w := wool.Get(ctx).In("acceptPrivacyRequest")

	if s.privacy == nil || s.privacyJobs == nil {
		return nil, ErrPrivacyWorkflowUnavailable
	}
	gdprStore, ok := s.store.(GDPRStore)
	if !ok {
		return nil, w.NewError("store does not implement GDPRStore")
	}

	req := &GDPRRequest{
		ID:     NewIDString(),
		UserID: userID,
		Type:   kind,
		Status: GDPRPending,
	}
	event := EventGDPRExportReq
	if kind == GDPRDeletion {
		event = EventGDPRDeletionReq
	}

	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		jobID, err := enqueuePrivacyWorkflow(ctx, s.privacyJobs, req)
		if err != nil {
			return err
		}
		req.JobID = jobID
		if err := gdprStore.CreateGDPRRequest(ctx, req); err != nil {
			return err
		}
		return s.emitTx(ctx, userID, "user", event, "gdpr_request", req.ID, "")
	}); err != nil {
		return nil, w.Wrapf(err, "cannot accept privacy request")
	}
	return req, nil
}

// GetExportStatus returns a caller-owned export request.
func (s *Service) GetExportStatus(ctx context.Context, userID, requestID string) (*GDPRRequest, error) {
	return s.getGDPRStatus(ctx, userID, requestID, GDPRExport)
}

// GetDeletionStatus returns a caller-owned deletion request.
func (s *Service) GetDeletionStatus(ctx context.Context, userID, requestID string) (*GDPRRequest, error) {
	return s.getGDPRStatus(ctx, userID, requestID, GDPRDeletion)
}

func (s *Service) getGDPRStatus(ctx context.Context, userID, requestID string, expectedType GDPRRequestType) (*GDPRRequest, error) {
	w := wool.Get(ctx).In("GetGDPRStatus")

	gdprStore, ok := s.store.(GDPRStore)
	if !ok {
		return nil, w.NewError("store does not implement GDPRStore")
	}

	if userID == "" {
		return nil, w.NewError("authenticated user required")
	}

	// The user-scoped RLS policy makes another subject's request invisible even
	// when its UUID is supplied directly.
	var req *GDPRRequest
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		var e error
		req, e = gdprStore.GetGDPRRequest(ctx, requestID)
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot get GDPR request")
	}
	if req == nil || req.UserID != userID || req.Type != expectedType {
		return nil, w.NewError("GDPR request not found")
	}
	// A lapsed download window closes here as well as in storage: the artifact
	// sweep runs on a cycle, and the caller must not receive a reference to an
	// artifact whose authorized window has already ended.
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		req.DownloadURL = ""
	}
	return req, nil
}

// PurgeExpiredPrivacyArtifacts deletes export artifacts whose download window
// has lapsed and drops their stored reference. A cleanup the workflow refuses
// leaves the reference in place so the next sweep tries again rather than
// losing track of an artifact that still exists.
func (s *Service) PurgeExpiredPrivacyArtifacts(ctx context.Context) (int, error) {
	w := wool.Get(ctx).In("PurgeExpiredPrivacyArtifacts")

	gdprStore, ok := s.store.(GDPRStore)
	if !ok || s.privacy == nil {
		return 0, nil
	}
	var expired []*GDPRRequest
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var e error
		expired, e = gdprStore.ListExpiredGDPRExports(ctx, time.Now(), privacyArtifactSweepLimit)
		return e
	}); err != nil {
		return 0, w.Wrapf(err, "cannot list expired export artifacts")
	}

	cleaner, _ := s.privacy.(PrivacyArtifactCleaner)
	purged := 0
	for _, request := range expired {
		if cleaner != nil {
			if err := cleaner.DeleteExportArtifact(ctx, request.ID); err != nil {
				w.Warn("expired export artifact was not deleted",
					wool.Field("request_id", request.ID),
					wool.Field("failure", privacyFailureCode(err)),
				)
				continue
			}
		}
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			return gdprStore.ClearGDPRExportArtifact(ctx, request.ID)
		}); err != nil {
			return purged, w.Wrapf(err, "cannot clear expired export artifact")
		}
		purged++
	}
	return purged, nil
}

const privacyArtifactSweepLimit = 500

func enqueuePrivacyWorkflow(
	ctx context.Context,
	producer jobs.Producer,
	req *GDPRRequest,
) (string, error) {
	request, err := newPrivacyWorkflowJob(req)
	if err != nil {
		return "", err
	}
	response, err := producer.EnqueueJob(ctx, request)
	if err != nil {
		return "", err
	}
	switch response.GetDisposition() {
	case jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
		jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE:
		return response.GetJobId(), nil
	default:
		return "", errors.New("privacy: enqueue did not persist a durable workflow job")
	}
}
