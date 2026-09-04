package business_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeJobBackend implements both jobs.Producer and jobs.Store, recording the
// calls the module surface makes so the guard and delegation can be asserted
// without a database.
type fakeJobBackend struct {
	enqueued   []*jobsv1.EnqueueJobRequest
	claimQueue string
	completed  []*jobsv1.CompleteJobRequest
	retried    []*jobsv1.RetryJobRequest
	deadLetter []*jobsv1.DeadLetterJobRequest
}

func (f *fakeJobBackend) EnqueueJob(_ context.Context, req *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
	f.enqueued = append(f.enqueued, req)
	return &jobsv1.EnqueueJobResponse{JobId: "00000000-0000-0000-0000-000000000001", Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED}, nil
}

func (f *fakeJobBackend) Claim(_ context.Context, req *jobsv1.ClaimJobsRequest) (*jobsv1.ClaimJobsResponse, error) {
	f.claimQueue = req.GetQueue()
	return &jobsv1.ClaimJobsResponse{Jobs: []*jobsv1.JobEnvelope{{Id: "00000000-0000-0000-0000-000000000009"}}}, nil
}

func (f *fakeJobBackend) Heartbeat(_ context.Context, _ *jobsv1.HeartbeatJobRequest) (*jobsv1.HeartbeatJobResponse, error) {
	return &jobsv1.HeartbeatJobResponse{Lease: &jobsv1.JobLease{}}, nil
}

func (f *fakeJobBackend) Complete(_ context.Context, req *jobsv1.CompleteJobRequest) error {
	f.completed = append(f.completed, req)
	return nil
}

func (f *fakeJobBackend) Retry(_ context.Context, req *jobsv1.RetryJobRequest) (*jobsv1.RetryJobResponse, error) {
	f.retried = append(f.retried, req)
	return &jobsv1.RetryJobResponse{State: jobsv1.JobState_JOB_STATE_RETRYING}, nil
}

func (f *fakeJobBackend) DeadLetter(_ context.Context, req *jobsv1.DeadLetterJobRequest) error {
	f.deadLetter = append(f.deadLetter, req)
	return nil
}

const (
	moduleTenantA  = "11111111-1111-1111-1111-111111111111"
	moduleTenantB  = "22222222-2222-2222-2222-222222222222"
	modulePrincSvc = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

// fakeTxStore satisfies business.Store for the enqueue transaction seam without
// a database: WithOrgTx / WithUserTx just run fn in the same context, so a
// request-scoped producer wired behind them still fires.
type fakeTxStore struct {
	business.Store
}

func (fakeTxStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (fakeTxStore) WithUserTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

// newModuleService builds a Service whose module surface is wired to the fake
// backend and a registry granting the document-store-shaped principal the
// datasource and documents queues on tenant A only (no cross-tenant grant).
func newModuleService(t *testing.T, backend *fakeJobBackend) *business.Service {
	t.Helper()
	svc, err := business.NewService(nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetModuleCapabilities(backend, backend, business.ModulePrincipalRegistry{
		modulePrincSvc: {Queues: []string{"datasource", "documents"}},
	})
	return svc
}

func moduleCaller() business.ModuleCaller {
	return business.ModuleCaller{PrincipalID: modulePrincSvc, BoundOrg: moduleTenantA}
}

func orgScopedJob(orgID, queue string) *jobsv1.EnqueueJobRequest {
	return &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: orgID}},
		Queue:          queue,
		Topic:          "documents.reindex",
		Source:         "module",
		IdempotencyKey: "k1",
		SchemaVersion:  1,
		MaxAttempts:    3,
	}}
}

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("expected code %s, got %s (%v)", want, got, err)
	}
}

func TestModuleEnqueueJob_UnknownPrincipalRejected(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	_, err := svc.ModuleEnqueueJob(context.Background(),
		business.ModuleCaller{PrincipalID: "someone-else", BoundOrg: moduleTenantA},
		orgScopedJob(moduleTenantA, "datasource"))
	requireCode(t, err, codes.PermissionDenied)
}

func TestModuleEnqueueJob_CrossTenantRejected(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	// The principal is bound to tenant A and holds no cross-tenant grant, so
	// enqueuing tenant B's work must be denied before any store call.
	_, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), orgScopedJob(moduleTenantB, "datasource"))
	requireCode(t, err, codes.PermissionDenied)
}

func TestModuleEnqueueJob_DisallowedQueueRejected(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	_, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), orgScopedJob(moduleTenantA, "billing"))
	requireCode(t, err, codes.PermissionDenied)
}

func TestModuleEnqueueJob_GlobalRequiresCrossTenant(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	globalJob := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:     "datasource", Topic: "datasource.github.push", Source: "module",
		IdempotencyKey: "k", SchemaVersion: 1, MaxAttempts: 3,
	}}
	_, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), globalJob)
	requireCode(t, err, codes.PermissionDenied)
}

func TestModuleEnqueueJob_GlobalWithCrossTenantUsesPrivilegedProducer(t *testing.T) {
	backend := &fakeJobBackend{}
	svc, err := business.NewService(nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetModuleCapabilities(backend, backend, business.ModulePrincipalRegistry{
		modulePrincSvc: {Queues: []string{"datasource"}, CrossTenant: true},
	})
	globalJob := &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:     "datasource", Topic: "datasource.github.push", Source: "module",
		IdempotencyKey: "k", SchemaVersion: 1, MaxAttempts: 3,
	}}
	resp, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), globalJob)
	if err != nil {
		t.Fatalf("global enqueue: %v", err)
	}
	if resp.GetDisposition() != jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED {
		t.Fatalf("unexpected disposition %v", resp.GetDisposition())
	}
	if len(backend.enqueued) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(backend.enqueued))
	}
}

func TestModuleEnqueueJob_OrgScopedHappyPath(t *testing.T) {
	backend := &fakeJobBackend{}
	svc, err := business.NewService(fakeTxStore{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetModuleCapabilities(backend, backend, business.ModulePrincipalRegistry{
		modulePrincSvc: {Queues: []string{"documents"}},
	})
	resp, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), orgScopedJob(moduleTenantA, "documents"))
	if err != nil {
		t.Fatalf("org-scoped enqueue: %v", err)
	}
	if resp.GetDisposition() != jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED {
		t.Fatalf("unexpected disposition %v", resp.GetDisposition())
	}
	if len(backend.enqueued) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(backend.enqueued))
	}
}

func TestModuleEmitAuditEvent_RegisteredTypeAccepted(t *testing.T) {
	// With no audit emitter wired the emit is a no-op, but the registered-type
	// gate still runs: a document.* event registered in the catalog is accepted.
	svc := newModuleService(t, &fakeJobBackend{})
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "document.ingested", "actor-1", "example-solution", "entry-1", nil)
	if err != nil {
		t.Fatalf("registered audit event should be accepted: %v", err)
	}
}

func TestModuleClaimJobs_DisallowedQueueRejected(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	_, err := svc.ModuleClaimJobs(context.Background(), moduleCaller(), &jobsv1.ClaimJobsRequest{
		Queue: "billing", WorkerId: "w1", Limit: 1, LeaseDuration: durationpb.New(time.Minute),
	})
	requireCode(t, err, codes.PermissionDenied)
}

func TestModuleClaimJobs_AllowedQueueClaims(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleService(t, backend)
	resp, err := svc.ModuleClaimJobs(context.Background(), moduleCaller(), &jobsv1.ClaimJobsRequest{
		Queue: "datasource", WorkerId: "w1", Limit: 1, LeaseDuration: durationpb.New(time.Minute),
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(resp.GetJobs()) != 1 {
		t.Fatalf("expected 1 job, got %d", len(resp.GetJobs()))
	}
	if backend.claimQueue != "datasource" {
		t.Fatalf("expected claim on datasource, got %q", backend.claimQueue)
	}
}

func TestModuleNackJob_RetryableRequiresRetryAt(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	lease := &jobsv1.JobLeaseReference{
		JobId: "00000000-0000-0000-0000-000000000009", WorkerId: "w1",
		LeaseToken: "00000000-0000-0000-0000-0000000000aa",
	}
	err := svc.ModuleNackJob(context.Background(), moduleCaller(), lease, &jobsv1.JobFailure{Code: "boom", Message: "x"}, true, nil)
	requireCode(t, err, codes.InvalidArgument)
}

func TestModuleNackJob_PermanentDeadLetters(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleService(t, backend)
	lease := &jobsv1.JobLeaseReference{
		JobId: "00000000-0000-0000-0000-000000000009", WorkerId: "w1",
		LeaseToken: "00000000-0000-0000-0000-0000000000aa",
	}
	if err := svc.ModuleNackJob(context.Background(), moduleCaller(), lease, &jobsv1.JobFailure{Code: "boom", Message: "x"}, false, nil); err != nil {
		t.Fatalf("nack: %v", err)
	}
	if len(backend.deadLetter) != 1 {
		t.Fatalf("expected 1 dead-letter, got %d", len(backend.deadLetter))
	}
}

func TestModuleNackJob_RetryableRetries(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleService(t, backend)
	lease := &jobsv1.JobLeaseReference{
		JobId: "00000000-0000-0000-0000-000000000009", WorkerId: "w1",
		LeaseToken: "00000000-0000-0000-0000-0000000000aa",
	}
	if err := svc.ModuleNackJob(context.Background(), moduleCaller(), lease, &jobsv1.JobFailure{Code: "boom", Message: "x"}, true, timestamppb.New(time.Now().Add(time.Minute))); err != nil {
		t.Fatalf("nack: %v", err)
	}
	if len(backend.retried) != 1 {
		t.Fatalf("expected 1 retry, got %d", len(backend.retried))
	}
}

func TestModuleEmitAuditEvent_UnregisteredTypeRejected(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA, "document.made_up", "actor-1", "example-solution", "entry-1", nil)
	requireCode(t, err, codes.InvalidArgument)
}

func TestModuleNotifyUser_InvalidCategoryRejected(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	_, err := svc.ModuleNotifyUser(context.Background(), moduleCaller(), moduleTenantA,
		"33333333-3333-3333-3333-333333333333", "Title", "Body", "info", "", "not-a-category", "")
	requireCode(t, err, codes.InvalidArgument)
}

// TestApprovalResumeEnqueuedOnQuorum proves the primitive wiring the module
// surface depends on: when a decision reaches quorum, Decide enqueues the resume
// outbox job addressed to the module's queue, keyed by the approval id so a
// retried decision resolves to the same durable job.
func TestApprovalResumeEnqueuedOnQuorum(t *testing.T) {
	store := newApprovalEngineStore()
	svc, err := business.NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	backend := &fakeJobBackend{}
	svc.SetModuleCapabilities(backend, backend, business.ModulePrincipalRegistry{
		modulePrincSvc: {Queues: []string{"documents"}},
	})
	id, err := svc.CreateApprovalRequest(context.Background(), &business.CreateApprovalRequestInput{
		OrgID: moduleTenantA, Resource: "document_quarantine", Action: "release", RequestedBy: "requester-1",
		ResumeRef: business.ResumeRef{Queue: "documents", Topic: "document.quarantine_released", Payload: map[string]any{"entry_id": "e1"}},
	})
	if err != nil {
		t.Fatalf("CreateApprovalRequest: %v", err)
	}
	outcome, err := svc.Decide(context.Background(), moduleTenantA, id, business.DecideInput{Decider: "approver-1", Decision: business.DecisionApprove})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !outcome.Approved {
		t.Fatalf("expected approved outcome")
	}
	if len(backend.enqueued) != 1 {
		t.Fatalf("expected 1 resume enqueue, got %d", len(backend.enqueued))
	}
	job := backend.enqueued[0].GetJob()
	if job.GetQueue() != "documents" || job.GetTopic() != "document.quarantine_released" || job.GetSource() != "approvals" {
		t.Fatalf("unexpected resume job queue=%q topic=%q source=%q", job.GetQueue(), job.GetTopic(), job.GetSource())
	}
	if job.GetIdempotencyKey() != "approval-resume:"+id {
		t.Fatalf("unexpected resume idempotency key %q", job.GetIdempotencyKey())
	}
}

func TestModuleRequestApproval_ResumeQueueMustBeAllowed(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	_, err := svc.ModuleRequestApproval(context.Background(), moduleCaller(), business.ModuleRequestApprovalInput{
		Tenant:      moduleTenantA,
		Resource:    "document_quarantine",
		Action:      "release",
		RequestedBy: "requester-1",
		ResumeQueue: "billing", // not an allowed queue
		ResumeTopic: "document.quarantine_released",
	})
	requireCode(t, err, codes.PermissionDenied)
}
