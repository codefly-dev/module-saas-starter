package business_test

import (
	"context"
	"encoding/json"
	"errors"
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
	moduleUserA    = "33333333-3333-3333-3333-333333333333"
)

// fakeTxStore satisfies business.Store for the enqueue/audit transaction seam
// and org-membership reads without a database: the With*Tx wrappers just run fn
// in the same context, and members answers the membership guard.
type fakeTxStore struct {
	business.Store
	members map[string]bool // "org|user" -> true
}

func (fakeTxStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (fakeTxStore) WithUserTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (fakeTxStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f fakeTxStore) OrgMemberExists(_ context.Context, orgID, userID string) (bool, error) {
	return f.members[orgID+"|"+userID], nil
}

// fakeAuditEmitter lets a test force the audit write to fail so the surface's
// error handling can be asserted.
type fakeAuditEmitter struct {
	emitTxErr error
	emitted   int
}

func (f *fakeAuditEmitter) Emit(context.Context, business.AuditEntry) { f.emitted++ }

func (f *fakeAuditEmitter) EmitTx(context.Context, business.AuditEntry) error {
	f.emitted++
	return f.emitTxErr
}

// newModuleService builds a Service wired to the fake backend with a registry
// granting the principal the datasource and documents queues on its bound tenant
// only (no cross-tenant grant). store may be nil for guards that reject before
// any store access.
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

func newModuleServiceWithStore(t *testing.T, store business.Store, backend *fakeJobBackend, crossTenant bool) *business.Service {
	t.Helper()
	svc, err := business.NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetModuleCapabilities(backend, backend, business.ModulePrincipalRegistry{
		modulePrincSvc: {Queues: []string{"datasource", "documents"}, CrossTenant: crossTenant},
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

func subjectScopedJob(subject, queue string) *jobsv1.EnqueueJobRequest {
	return &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
		Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_SubjectId{SubjectId: subject}},
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
		moduleTenantA, orgScopedJob(moduleTenantA, "datasource"))
	requireCode(t, err, codes.PermissionDenied)
}

func TestModuleEnqueueJob_CrossTenantRejected(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	// The principal is bound to tenant A and holds no cross-tenant grant, so
	// enqueuing tenant B's work must be denied before any store call.
	_, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), moduleTenantB, orgScopedJob(moduleTenantB, "datasource"))
	requireCode(t, err, codes.PermissionDenied)
}

func TestModuleEnqueueJob_DisallowedQueueRejected(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	_, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), moduleTenantA, orgScopedJob(moduleTenantA, "billing"))
	requireCode(t, err, codes.PermissionDenied)
}

// TestModuleEnqueueJob_ScopeTenantMismatchRejected pins the fix for the dropped
// tenant field: even a cross-tenant principal cannot name tenant A while scoping
// the job to tenant B — the scope must equal the authorized tenant.
func TestModuleEnqueueJob_ScopeTenantMismatchRejected(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, true)
	_, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), moduleTenantA, orgScopedJob(moduleTenantB, "documents"))
	requireCode(t, err, codes.InvalidArgument)
}

// TestModuleEnqueueJob_SubjectScopeRequiresMembership pins the fix for the no-op
// subject guard: a subject that is not a member of the tenant is rejected.
func TestModuleEnqueueJob_SubjectScopeRequiresMembership(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleServiceWithStore(t, fakeTxStore{members: map[string]bool{}}, backend, false)
	_, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), moduleTenantA, subjectScopedJob(moduleUserA, "documents"))
	requireCode(t, err, codes.PermissionDenied)
	if len(backend.enqueued) != 0 {
		t.Fatalf("no job should be enqueued for a non-member subject, got %d", len(backend.enqueued))
	}
}

func TestModuleEnqueueJob_SubjectScopeMemberEnqueues(t *testing.T) {
	backend := &fakeJobBackend{}
	store := fakeTxStore{members: map[string]bool{moduleTenantA + "|" + moduleUserA: true}}
	svc := newModuleServiceWithStore(t, store, backend, false)
	_, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), moduleTenantA, subjectScopedJob(moduleUserA, "documents"))
	if err != nil {
		t.Fatalf("member subject enqueue: %v", err)
	}
	if len(backend.enqueued) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(backend.enqueued))
	}
}

func TestModuleEnqueueJob_GlobalRequiresCrossTenant(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	_, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), moduleTenantA, globalJob())
	requireCode(t, err, codes.PermissionDenied)
}

func globalJob() *jobsv1.EnqueueJobRequest {
	return &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction: jobsv1.JobDirection_JOB_DIRECTION_INBOX,
		Scope:     &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		Queue:     "datasource", Topic: "datasource.github.push", Source: "module",
		IdempotencyKey: "k", SchemaVersion: 1, MaxAttempts: 3,
	}}
}

func TestModuleEnqueueJob_GlobalWithCrossTenantUsesPrivilegedProducer(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleServiceWithStore(t, fakeTxStore{}, backend, true)
	resp, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), moduleTenantA, globalJob())
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
	svc := newModuleServiceWithStore(t, fakeTxStore{}, backend, false)
	resp, err := svc.ModuleEnqueueJob(context.Background(), moduleCaller(), moduleTenantA, orgScopedJob(moduleTenantA, "documents"))
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
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "document.ingested", "actor-1", "example-solution", "entry-1", nil)
	if err != nil {
		t.Fatalf("registered audit event should be accepted: %v", err)
	}
}

// TestModuleEmitAuditEvent_WriteFailureSurfaces pins the fix for silent audit
// loss: when the durable write fails, the RPC must return an error rather than
// reporting success on a fire-and-forget emit.
func TestModuleEmitAuditEvent_WriteFailureSurfaces(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	svc.SetAuditEmitter(&fakeAuditEmitter{emitTxErr: errors.New("audit spine unavailable")})
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "document.ingested", "actor-1", "example-solution", "entry-1", nil)
	requireCode(t, err, codes.Internal)
}

func TestModuleClaimJobs_DisallowedQueueRejected(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	_, err := svc.ModuleClaimJobs(context.Background(), moduleCaller(), &jobsv1.ClaimJobsRequest{
		Queue: "billing", WorkerId: "w1", Limit: 1, LeaseDuration: durationpb.New(time.Minute),
	})
	requireCode(t, err, codes.PermissionDenied)
}

// TestModuleClaimJobs_RequiresCrossTenant pins the fix for the claim isolation
// leak: claiming a queue reads across tenants, so a principal without the cross-
// tenant grant is denied even on a queue it is otherwise allowed to use.
func TestModuleClaimJobs_RequiresCrossTenant(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleService(t, backend)
	_, err := svc.ModuleClaimJobs(context.Background(), moduleCaller(), &jobsv1.ClaimJobsRequest{
		Queue: "datasource", WorkerId: "w1", Limit: 1, LeaseDuration: durationpb.New(time.Minute),
	})
	requireCode(t, err, codes.PermissionDenied)
	if backend.claimQueue != "" {
		t.Fatalf("no claim should have reached the store, got queue %q", backend.claimQueue)
	}
}

func TestModuleClaimJobs_CrossTenantClaims(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleServiceWithStore(t, fakeTxStore{}, backend, true)
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

func moduleLease() *jobsv1.JobLeaseReference {
	return &jobsv1.JobLeaseReference{
		JobId: "00000000-0000-0000-0000-000000000009", WorkerId: "w1",
		LeaseToken: "00000000-0000-0000-0000-0000000000aa",
	}
}

func TestModuleNackJob_RetryableRequiresRetryAt(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	err := svc.ModuleNackJob(context.Background(), moduleCaller(), moduleLease(), &jobsv1.JobFailure{Code: "boom", Message: "x"}, true, nil)
	requireCode(t, err, codes.InvalidArgument)
}

func TestModuleNackJob_PermanentDeadLetters(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleService(t, backend)
	if err := svc.ModuleNackJob(context.Background(), moduleCaller(), moduleLease(), &jobsv1.JobFailure{Code: "boom", Message: "x"}, false, nil); err != nil {
		t.Fatalf("nack: %v", err)
	}
	if len(backend.deadLetter) != 1 {
		t.Fatalf("expected 1 dead-letter, got %d", len(backend.deadLetter))
	}
}

func TestModuleNackJob_RetryableRetries(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleService(t, backend)
	if err := svc.ModuleNackJob(context.Background(), moduleCaller(), moduleLease(), &jobsv1.JobFailure{Code: "boom", Message: "x"}, true, timestamppb.New(time.Now().Add(time.Minute))); err != nil {
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
	_, err := svc.ModuleNotifyUser(context.Background(), moduleCaller(), business.ModuleNotifyUserInput{
		Tenant: moduleTenantA, UserID: moduleUserA, Title: "Title", Body: "Body", Type: "info", Category: "not-a-category",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestModuleNotifyUser_NonMemberRejected pins the fix for cross-tenant
// notification injection: notifying a user who is not a member of the tenant is
// denied before any notification is created.
func TestModuleNotifyUser_NonMemberRejected(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{members: map[string]bool{}}, &fakeJobBackend{}, false)
	_, err := svc.ModuleNotifyUser(context.Background(), moduleCaller(), business.ModuleNotifyUserInput{
		Tenant: moduleTenantA, UserID: moduleUserA, Title: "Reset your password", Body: "click", Type: "info", Category: "security",
	})
	requireCode(t, err, codes.PermissionDenied)
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
	var payload map[string]any
	if err := json.Unmarshal(job.GetPayload(), &payload); err != nil {
		t.Fatalf("resume payload is not valid JSON: %v", err)
	}
	if payload["approval_id"] != id {
		t.Fatalf("resume payload approval_id = %v, want %q", payload["approval_id"], id)
	}
	if payload["decision"] != "approve" {
		t.Fatalf("resume payload decision = %v, want %q", payload["decision"], "approve")
	}
	if payload["decider"] != "approver-1" {
		t.Fatalf("resume payload decider = %v, want %q", payload["decider"], "approver-1")
	}
}

// TestApprovalResume_ErrorsWhenProducerUnwired pins the fix for silently dropping
// a resume: a request that declares a resume queue must fail its decision when no
// producer is wired, rather than transitioning to approved with no resume job.
func TestApprovalResume_ErrorsWhenProducerUnwired(t *testing.T) {
	store := newApprovalEngineStore()
	svc, err := business.NewService(store) // no SetModuleCapabilities: moduleProducer is nil
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	id, err := svc.CreateApprovalRequest(context.Background(), &business.CreateApprovalRequestInput{
		OrgID: moduleTenantA, Resource: "document_quarantine", Action: "release", RequestedBy: "requester-1",
		ResumeRef: business.ResumeRef{Queue: "documents", Topic: "document.quarantine_released"},
	})
	if err != nil {
		t.Fatalf("CreateApprovalRequest: %v", err)
	}
	_, err = svc.Decide(context.Background(), moduleTenantA, id, business.DecideInput{Decider: "approver-1", Decision: business.DecisionApprove})
	if err == nil {
		t.Fatal("expected Decide to fail when a resume queue is declared but no producer is wired")
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
