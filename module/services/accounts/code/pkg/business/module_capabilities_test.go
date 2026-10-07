package business_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
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
	moduleUserB    = "44444444-4444-4444-4444-444444444444"
)

// fakeTxStore satisfies business.Store for the enqueue/audit transaction seam
// and org-membership reads without a database: the With*Tx wrappers just run fn
// in the same context, and members answers the membership guard.
type fakeTxStore struct {
	business.Store
	members    map[string]bool // "org|user" -> true
	membersErr error           // when set, the membership read fails
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

// GetDeclaredAuditEventType: no solution has declared a type in these tests, so
// a type outside the code catalog resolves to nothing and is refused as
// unregistered, exactly as before declared types existed.
func (fakeTxStore) GetDeclaredAuditEventType(context.Context, business.EventType) (*business.DeclaredAuditEventType, error) {
	return nil, nil
}

func (f fakeTxStore) OrgMemberExists(_ context.Context, orgID, userID string) (bool, error) {
	if f.membersErr != nil {
		return false, f.membersErr
	}
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
		modulePrincSvc: {Prefix: "content", Queues: []string{"datasource", "documents"}},
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
		modulePrincSvc: {Prefix: "content", Queues: []string{"datasource", "documents"}, CrossTenant: crossTenant},
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

// The documents store audits ArchiveDocument and UnarchiveDocument as
// saas.document.archived / saas.document.unarchived with the caller's initiator
// field; the host must accept both, or every archive's audit row is refused.
func TestModuleEmitAuditEvent_ArchiveEventsAccepted(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	fields, err := structpb.NewStruct(map[string]any{"initiator": "caller"})
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{"saas.document.archived", "saas.document.unarchived"} {
		if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
			moduleTenantA, eventType, "system:ingest", "example-solution", "entry-1", "", fields); err != nil {
			t.Fatalf("%s should be accepted: %v", eventType, err)
		}
	}
}

// The documents store audits every entry its ingest writes, tombstones or
// refuses, the receipts of its atomic effects, and the outcome of its ownership
// and subscription mutations under these names, with exactly these payloads (the
// store sends counts as numbers). Each must be accepted, or the store's outbox
// settles the row as refused and the fact never reaches the spine.
func TestModuleEmitAuditEvent_DocumentStoreVocabularyAccepted(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	cases := map[string]map[string]any{
		"saas.document.deleted":                {"version": "v1", "initiator": "ApplySnapshot"},
		"saas.document.ingested":               {"version": "v1", "initiator": "Ingest"},
		"saas.document.version_minted":         {"version": "v1", "initiator": "Ingest"},
		"saas.document.renamed":                {"version": "v1", "initiator": "ApplyEffect"},
		"saas.document.quarantined":            {"version": "v1", "initiator": "Ingest"},
		"saas.document.subscribed":             {"outcome": "failure", "reason": "not found"},
		"saas.document.unsubscribed":           {"outcome": "success"},
		"saas.document.ownership_transferred":  {"outcome": "success", "new_owner_subject_id": "subject-2"},
		"saas.document.frozen":                 {"outcome": "failure", "reason": "permission denied"},
		"saas.document.unfrozen":               {"outcome": "success"},
		"saas.document.ingest_skipped_stale":   {"path": "loans/a.xlsx", "ordinal": float64(3), "initiator": "Ingest"},
		"saas.document.quarantine_released":    {"version": "v1", "outcome": "success"},
		"saas.document.payload_conflict":       {"producer": "parse", "producer_version": "1", "entry": "e", "entry_version": "v", "stored_bytes": float64(10), "offered_bytes": float64(11)},
		"saas.document.snapshot.committed":     {"digest": "d", "deleted": float64(1), "retained": float64(2), "ordinal": float64(7)},
		"saas.document.snapshot.skipped_stale": {"digest": "d", "deleted": float64(0), "retained": float64(0), "ordinal": float64(6)},
		"saas.document.effect.committed":       {"digest": "d", "applied": float64(1), "deleted": float64(0)},
		"saas.document.production.committed":   {"effect_key": "k", "task_id": "t", "digest": "d", "producer": "index", "producer_version": "1"},
		"saas.document.knowledge.published":    {"version": "v1", "effect_key": "k", "run_id": "r", "digest": "d"},
	}
	for eventType, payload := range cases {
		fields, err := structpb.NewStruct(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
			moduleTenantA, eventType, "system:ingest", "example-solution", "entry-1", "", fields); err != nil {
			t.Fatalf("%s should be accepted: %v", eventType, err)
		}
	}
	// A count sent as a string is the mistake the store's typed delivery exists
	// to prevent; the registry refuses it rather than storing it free-form.
	fields, err := structpb.NewStruct(map[string]any{"digest": "d", "deleted": "1"})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "saas.document.snapshot.committed", "system:ingest", "example-solution", "k", "", fields)
	requireCode(t, err, codes.InvalidArgument)
	// A refused release is the release type with outcome failure and the fact of
	// the mismatch, never a type of its own and never the other tenant's id.
	fields, err = structpb.NewStruct(map[string]any{"version": "v1", "outcome": "failure", "reason": "tenant mismatch", "tenant_mismatch": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "saas.document.quarantine_released", "system:ingest", "example-solution", "entry-1", "", fields); err != nil {
		t.Fatalf("a refused release should be accepted: %v", err)
	}
	for _, refused := range []struct {
		eventType string
		payload   map[string]any
	}{
		// The superseded refusal type is not registered.
		{"saas.document.quarantine_release_refused", map[string]any{"version": "v1"}},
		// Another tenant's or solution's identity has no field to travel in.
		{"saas.document.quarantine_released", map[string]any{"outcome": "failure", "claimed_tenant": "other"}},
		{"saas.document.quarantine_released", map[string]any{"outcome": "failure", "claimed_solution": "other"}},
	} {
		fields, err := structpb.NewStruct(refused.payload)
		if err != nil {
			t.Fatal(err)
		}
		err = svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
			moduleTenantA, refused.eventType, "system:ingest", "example-solution", "entry-1", "", fields)
		requireCode(t, err, codes.InvalidArgument)
	}
	// A governance action with no outcome would read as a success on an
	// append-only trail, so every type that tells success from refusal requires it.
	for _, eventType := range []string{
		"saas.document.subscribed", "saas.document.unsubscribed", "saas.document.quarantine_released",
		"saas.document.ownership_transferred", "saas.document.frozen", "saas.document.unfrozen",
	} {
		fields, err := structpb.NewStruct(map[string]any{"reason": "permission denied"})
		if err != nil {
			t.Fatal(err)
		}
		err = svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
			moduleTenantA, eventType, "system:ingest", "example-solution", "entry-1", "", fields)
		requireCode(t, err, codes.InvalidArgument)
	}
	// The name the store used before this vocabulary was registered stays refused.
	err = svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "documents.snapshot.committed", "system:ingest", "example-solution", "k", "", nil)
	requireCode(t, err, codes.InvalidArgument)
}

func TestModuleEmitAuditEvent_RegisteredTypeAccepted(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "saas.document.ingested", modulePrincSvc, "example-solution", "entry-1", "", nil)
	if err != nil {
		t.Fatalf("registered audit event should be accepted: %v", err)
	}
}

// capturingAuditEmitter records every entry the surface hands the spine, so a
// test can assert what the row would carry without a database.
type capturingAuditEmitter struct{ entries []business.AuditEntry }

func (c *capturingAuditEmitter) Emit(_ context.Context, e business.AuditEntry) {
	c.entries = append(c.entries, e)
}

func (c *capturingAuditEmitter) EmitTx(_ context.Context, e business.AuditEntry) error {
	c.entries = append(c.entries, e)
	return nil
}

// A module attributes the work it did on its own to its own principal. The row
// names that principal as a system actor, and the entry the event concerns is
// its resource id — a document entry is a ULID, not a UUID, and it must survive.
func TestModuleEmitAuditEvent_OwnPrincipalIsASystemActor(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	spine := &capturingAuditEmitter{}
	svc.SetAuditEmitter(spine)
	const entry = "01M3C1E527S6Z98WFBN1B8VRG4"
	if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "saas.document.ingested", modulePrincSvc, "example-solution", entry, "", nil); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(spine.entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(spine.entries))
	}
	got := spine.entries[0]
	if got.ActorID != modulePrincSvc || got.ActorType != business.ActorTypeSystem {
		t.Fatalf("actor = %q/%q, want the module principal as a system actor", got.ActorID, got.ActorType)
	}
	if got.ResourceID != entry {
		t.Fatalf("resource id = %q, want the entry %q", got.ResourceID, entry)
	}
}

// A module that acted for a subject names the subject, recorded as work done
// through an agent.
func TestModuleEmitAuditEvent_SubjectIsAnAgentActor(t *testing.T) {
	store := fakeTxStore{members: map[string]bool{moduleTenantA + "|" + moduleUserA: true}}
	svc := newModuleServiceWithStore(t, store, &fakeJobBackend{}, false)
	spine := &capturingAuditEmitter{}
	svc.SetAuditEmitter(spine)
	if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "saas.document.ingested", moduleUserA, "example-solution", "entry-1", "", nil); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if got := spine.entries[0]; got.ActorID != moduleUserA || got.ActorType != business.ActorTypeAgent {
		t.Fatalf("actor = %q/%q, want the subject as an agent actor", got.ActorID, got.ActorType)
	}
}

// A module's own process label is its own work: the host knows which module
// is calling, so system:<process> is recorded under that module's principal as
// a system actor instead of being refused. A document producer sends
// system:ingest on every ingested entry; refusing it would hold every one of
// those events at the producer until it changed, and a producer that settles a
// refusal as final would lose them.
func TestModuleEmitAuditEvent_ProcessLabelMapsToTheModulePrincipal(t *testing.T) {
	for _, actor := range []string{"system:ingest", "system:producers", "system:knowledge-card", "system:sync.v2"} {
		t.Run(actor, func(t *testing.T) {
			svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
			spine := &capturingAuditEmitter{}
			svc.SetAuditEmitter(spine)
			if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
				moduleTenantA, "saas.document.ingested", actor, "example-solution", "entry-1", "", nil); err != nil {
				t.Fatalf("a module's own process label must be accepted: %v", err)
			}
			if len(spine.entries) != 1 {
				t.Fatalf("expected one entry, got %d", len(spine.entries))
			}
			if got := spine.entries[0]; got.ActorID != modulePrincSvc || got.ActorType != business.ActorTypeSystem {
				t.Fatalf("actor = %q/%q, want the calling module's principal as a system actor", got.ActorID, got.ActorType)
			}
		})
	}
}

// uuid.Parse accepts several spellings of one id, and Postgres stores all of
// them as the same uuid. The row carries the canonical form, and the calling
// module's own principal in any spelling is still the module (system), so an
// uppercase id is neither refused nor misfiled as a subject the module acted for.
func TestModuleEmitAuditEvent_PrincipalSpellingsAreCanonicalized(t *testing.T) {
	const subjectUpper = "33333333-3333-3333-3333-333333333333"
	for name, tc := range map[string]struct{ actor, wantID, wantType string }{
		"canonical own":    {modulePrincSvc, modulePrincSvc, business.ActorTypeSystem},
		"uppercase own":    {"AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", modulePrincSvc, business.ActorTypeSystem},
		"braced own":       {"{" + modulePrincSvc + "}", modulePrincSvc, business.ActorTypeSystem},
		"urn own":          {"urn:uuid:" + modulePrincSvc, modulePrincSvc, business.ActorTypeSystem},
		"unhyphenated own": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", modulePrincSvc, business.ActorTypeSystem},
		"braced subject":   {"{" + subjectUpper + "}", moduleUserA, business.ActorTypeAgent},
		"uppercase subject": {"ABCDEF01-2345-6789-ABCD-EF0123456789",
			"abcdef01-2345-6789-abcd-ef0123456789", business.ActorTypeAgent},
	} {
		t.Run(name, func(t *testing.T) {
			// A subject is recorded only once it is a member of the tenant, so the
			// spelling under test is the only thing left for the case to prove.
			store := fakeTxStore{members: map[string]bool{moduleTenantA + "|" + tc.wantID: true}}
			svc := newModuleServiceWithStore(t, store, &fakeJobBackend{}, false)
			spine := &capturingAuditEmitter{}
			svc.SetAuditEmitter(spine)
			if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
				moduleTenantA, "saas.document.ingested", tc.actor, "example-solution", "entry-1", "", nil); err != nil {
				t.Fatalf("emit: %v", err)
			}
			if got := spine.entries[0]; got.ActorID != tc.wantID || got.ActorType != tc.wantType {
				t.Fatalf("actor = %q/%q, want %q/%q", got.ActorID, got.ActorType, tc.wantID, tc.wantType)
			}
		})
	}
}

// An actor that resolves to no principal — not a principal id in any spelling,
// not a system:<process> label — fails loudly, before anything is written: it
// used to be accepted and blanked on write, so the row said nobody did it and
// the module was told it had succeeded. The refusal is FailedPrecondition, not
// InvalidArgument: the event is well formed and must stay pending at the
// producer until the actor is named, not be settled as permanently invalid.
func TestModuleEmitAuditEvent_UnresolvedActorFailsLoudlyAndStaysRetryable(t *testing.T) {
	for name, actor := range map[string]string{
		"a free-form name":         "actor-1",
		"an empty process":         "system:",
		"an uppercase process":     "system:Ingest",
		"an uppercase prefix":      "SYSTEM:ingest",
		"a process with a space":   "system:in gest",
		"another namespace":        "user:ingest",
		"a malformed uuid":         "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaz",
		"a trailing process colon": "system:ingest:",
	} {
		t.Run(name, func(t *testing.T) {
			svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
			spine := &capturingAuditEmitter{}
			svc.SetAuditEmitter(spine)
			err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
				moduleTenantA, "saas.document.ingested", actor, "example-solution", "entry-1", "", nil)
			requireCode(t, err, codes.FailedPrecondition)
			if !strings.Contains(err.Error(), business.ErrModuleAuditActorUnresolved.Error()) {
				t.Fatalf("the refusal must name its cause, got %v", err)
			}
			if len(spine.entries) != 0 {
				t.Fatalf("an unresolved actor reached the spine: %+v", spine.entries)
			}
		})
	}
}

// The module path is where registry validation actually enforces, so it is where
// a consuming solution still carrying the pre-#520 vocabulary is told.
func TestModuleEmitAuditEvent_LegacyTypeRejected(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "document.ingested", moduleUserA, "example-solution", "entry-1", "", nil)
	requireCode(t, err, codes.InvalidArgument)
}

// TestModuleEmitAuditEvent_WriteFailureSurfaces pins the fix for silent audit
// loss: when the durable write fails, the RPC must return an error rather than
// reporting success on a fire-and-forget emit.
func TestModuleEmitAuditEvent_WriteFailureSurfaces(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	svc.SetAuditEmitter(&fakeAuditEmitter{emitTxErr: errors.New("audit spine unavailable")})
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "saas.document.ingested", modulePrincSvc, "example-solution", "entry-1", "", nil)
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

func TestModuleAckJob_DerivesExecutionOwnerFromAuthenticatedPrincipal(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleService(t, backend)
	if err := svc.ModuleAckJob(context.Background(), moduleCaller(), moduleLease(), "task", "task-42"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if len(backend.completed) != 1 {
		t.Fatalf("expected one completion, got %d", len(backend.completed))
	}
	execution := backend.completed[0].GetExecution()
	if execution.GetOwner() != "content" || execution.GetKind() != "task" || execution.GetId() != "task-42" {
		t.Fatalf("execution = %+v", execution)
	}
}

func TestModuleAckJob_RejectsPartialExecutionReference(t *testing.T) {
	backend := &fakeJobBackend{}
	svc := newModuleService(t, backend)
	err := svc.ModuleAckJob(context.Background(), moduleCaller(), moduleLease(), "task", "")
	requireCode(t, err, codes.InvalidArgument)
	if len(backend.completed) != 0 {
		t.Fatal("partial execution reached the store")
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
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA, "document.made_up", moduleUserA, "example-solution", "entry-1", "", nil)
	requireCode(t, err, codes.InvalidArgument)
}

// entry_id is optional on ModuleEmitAuditEventRequest because the surface is
// shared with types that have no entry to name, so a type whose whole subject is
// one resource has to be refused here. Without this the emit succeeded and wrote
// a redrive row that named no document at all.
func TestModuleEmitAuditEvent_EntryRequiredWhenTheTypeRecordsAResource(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	fields, err := structpb.NewStruct(map[string]any{
		"version": "v1", "correlation_id": "redrive-7", "producer": "embed", "error_class": "exhausted",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		"saas.document.dead_letter_redriven", "system:redrive", "example-solution", "", "redrive-7:entry-1:embed", fields)
	requireCode(t, err, codes.InvalidArgument)

	if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		"saas.document.dead_letter_redriven", "system:redrive", "example-solution", "entry-1", "redrive-7:entry-1:embed", fields); err != nil {
		t.Fatalf("the same emit naming its entry should be accepted: %v", err)
	}
}

// An empty idempotency_key deduplicates nothing (see the field's contract on
// ModuleEmitAuditEventRequest), so a type emitted once per queue item — where a
// replayed response is ordinary — is refused without one. Before this, a redrive
// whose response was lost wrote the same re-queue twice and nothing said so.
func TestModuleEmitAuditEvent_IdempotencyKeyRequiredForPerItemTypes(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	fields, err := structpb.NewStruct(map[string]any{
		"version": "v1", "correlation_id": "redrive-7", "producer": "embed", "error_class": "exhausted",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		"saas.document.dead_letter_redriven", "system:redrive", "example-solution", "entry-1", "", fields)
	requireCode(t, err, codes.InvalidArgument)

	// A type nobody declared retry-prone still emits without one, so the
	// requirement stayed with the event rather than spreading to the surface.
	if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		"saas.document.ingested", "system:redrive", "example-solution", "entry-1", "", nil); err != nil {
		t.Fatalf("an undeclared type must keep emitting without a key: %v", err)
	}
}

// The blank a Go emitter sends when it never set the field. The payload is
// otherwise complete, so nothing else can be what refuses it.
func TestModuleEmitAuditEvent_BlankRequiredFieldRefused(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	fields, err := structpb.NewStruct(map[string]any{
		"version": "v1", "correlation_id": "redrive-7", "producer": "", "error_class": "exhausted",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(), moduleTenantA,
		"saas.document.dead_letter_redriven", "actor-1", "example-solution", "entry-1", "redrive-7:entry-1:embed", fields)
	requireCode(t, err, codes.InvalidArgument)
}

func TestModuleNotifyUser_InvalidCategoryRejected(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	_, err := svc.ModuleNotifyUser(context.Background(), moduleCaller(), business.ModuleNotifyUserInput{
		Tenant: moduleTenantA, UserID: moduleUserA, Title: "Title", Body: "Body", Type: "info", Category: "not-a-category",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// A type outside the store's set is refused as the caller's error, before any
// read or write, rather than reaching the store's CHECK constraint as Internal.
func TestModuleNotifyUser_InvalidTypeRejectedBeforeAnyWrite(t *testing.T) {
	store := &notifyRecordingStore{fakeTxStore: fakeTxStore{members: map[string]bool{moduleTenantA + "|" + moduleUserA: true}}}
	svc := newModuleServiceWithStore(t, store, &fakeJobBackend{}, false)
	for _, bad := range []string{"sync", "Info", "document", " info"} {
		_, err := svc.ModuleNotifyUser(context.Background(), moduleCaller(), business.ModuleNotifyUserInput{
			Tenant: moduleTenantA, UserID: moduleUserA, Title: "Title", Body: "Body", Type: bad, Category: "product",
		})
		requireCode(t, err, codes.InvalidArgument)
	}
	if store.created != 0 || store.membershipReads != 0 {
		t.Fatalf("a refused type read %d memberships and wrote %d notifications", store.membershipReads, store.created)
	}
	for _, good := range append([]string{""}, business.NotificationTypes...) {
		if !business.ValidNotificationType(good) {
			t.Errorf("%q should be a valid notification type", good)
		}
	}
}

// A module-supplied destination that a browser would resolve to another origin
// never reaches an inbox: both notification capabilities refuse it as the
// caller's error, before the membership read and before any row is written.
func TestModuleNotify_OffOriginActionURLRejectedBeforeAnyWrite(t *testing.T) {
	offOrigin := []string{
		"https://evil.example/steal",
		"//evil.example/steal",
		"/\\evil.example/steal",
		"/\t/evil.example/steal",
		"/../../admin/billing",
		"javascript:alert(document.cookie)",
		// Encoded forms: harmless to a browser, but url.URL.Path hands a Go
		// caller "///evil.example/steal" and "/\\evil.example", which are not.
		"/%2F%2Fevil.example/steal",
		"/%5Cevil.example",
	}

	store := &notifyRecordingStore{fakeTxStore: fakeTxStore{members: map[string]bool{moduleTenantA + "|" + moduleUserA: true}}}
	svc := newModuleServiceWithStore(t, store, &fakeJobBackend{}, false)
	for _, actionURL := range offOrigin {
		// A mandatory category, so a missing guard reaches the recording store's
		// write and fails this assertion, rather than the optional-category
		// settings read the fake does not implement.
		_, err := svc.ModuleNotifyUser(context.Background(), moduleCaller(), business.ModuleNotifyUserInput{
			Tenant: moduleTenantA, UserID: moduleUserA, Title: "Review this", Body: "Body",
			Type: "info", Category: "security", ActionURL: actionURL,
		})
		requireCode(t, err, codes.InvalidArgument)

		_, err = svc.ModuleNotifyOrgAdmins(context.Background(), moduleCaller(), business.ModuleNotifyOrgAdminsInput{
			Tenant: moduleTenantA, Title: "Review this", Body: "Body",
			Type: "info", Category: "security", ActionURL: actionURL,
		})
		requireCode(t, err, codes.InvalidArgument)
	}
	if store.created != 0 || store.membershipReads != 0 {
		t.Fatalf("a refused destination read %d memberships and wrote %d notifications", store.membershipReads, store.created)
	}

	result, err := svc.ModuleNotifyUser(context.Background(), moduleCaller(), business.ModuleNotifyUserInput{
		Tenant: moduleTenantA, UserID: moduleUserA, Title: "Review this", Body: "Body",
		Type: "info", Category: "security", ActionURL: "/documents/doc-7?comment=3",
	})
	if err != nil {
		t.Fatalf("a same-origin path should be delivered: %v", err)
	}
	if !result.Delivered || store.created != 1 {
		t.Fatalf("delivered = %v after %d writes, want one write", result.Delivered, store.created)
	}
}

// notifyRecordingStore counts the membership reads and notification writes a
// ModuleNotifyUser call makes.
type notifyRecordingStore struct {
	fakeTxStore
	created, membershipReads int
}

func (s *notifyRecordingStore) OrgMemberExists(ctx context.Context, orgID, userID string) (bool, error) {
	s.membershipReads++
	return s.fakeTxStore.OrgMemberExists(ctx, orgID, userID)
}

// NotifyOrgAdmins resolves its recipients through the listing rather than the
// point check, so a refusal that runs before any recipient is read has to be
// counted here too — fakeTxStore does not implement it, and without this a
// missing guard surfaces as a nil dereference instead of a failed assertion.
func (s *notifyRecordingStore) ListOrgMembers(context.Context, string) ([]*gen.OrgMembership, error) {
	s.membershipReads++
	return nil, nil
}

func (s *notifyRecordingStore) CreateNotification(context.Context, *business.Notification) error {
	s.created++
	return nil
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

// TestApprovalResumeStampsCompletingDecider pins the resume-outcome contract for
// a multi-approver quorum: no resume enqueues until quorum is reached, and the
// resume stamps the approver whose vote completed quorum — not the first
// approver — so the module's handler attributes the decision to the right actor.
func TestApprovalResumeStampsCompletingDecider(t *testing.T) {
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
		Quorum:    2,
		Policy:    business.ApprovalPolicy{ApproverSet: []string{"approver-1", "approver-2"}},
		ResumeRef: business.ResumeRef{Queue: "documents", Topic: "document.quarantine_released"},
	})
	if err != nil {
		t.Fatalf("CreateApprovalRequest: %v", err)
	}

	first, err := svc.Decide(context.Background(), moduleTenantA, id, business.DecideInput{Decider: "approver-1", Decision: business.DecisionApprove})
	if err != nil {
		t.Fatalf("Decide (first): %v", err)
	}
	if first.Approved {
		t.Fatal("first sub-quorum decision must not approve")
	}
	if len(backend.enqueued) != 0 {
		t.Fatalf("no resume must enqueue before quorum, got %d", len(backend.enqueued))
	}

	second, err := svc.Decide(context.Background(), moduleTenantA, id, business.DecideInput{Decider: "approver-2", Decision: business.DecisionApprove})
	if err != nil {
		t.Fatalf("Decide (second): %v", err)
	}
	if !second.Approved {
		t.Fatal("second decision must reach quorum and approve")
	}
	if len(backend.enqueued) != 1 {
		t.Fatalf("expected exactly 1 resume enqueue on quorum, got %d", len(backend.enqueued))
	}
	var payload map[string]any
	if err := json.Unmarshal(backend.enqueued[0].GetJob().GetPayload(), &payload); err != nil {
		t.Fatalf("resume payload is not valid JSON: %v", err)
	}
	if payload["decider"] != "approver-2" {
		t.Fatalf("resume payload decider = %v, want the completing approver %q", payload["decider"], "approver-2")
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

// The registry is authored under the prefix a module already federates with, and
// indexed by the principal id derived from it, so a deployment never hand-writes
// an opaque id and every side computes the same one.
func TestParseModulePrincipalRegistry_IndexesByDerivedPrincipal(t *testing.T) {
	registry, err := business.ParseModulePrincipalRegistry(
		`{"documents":{"queues":["datasource"],"namespaces":["document"],"tenant":"` + moduleTenantA + `"}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	grant, registered := registry[business.ModulePrincipalID("documents")]
	if !registered {
		t.Fatalf("registry is not indexed by the derived principal id: %v", registry)
	}
	if grant.Prefix != "documents" || grant.Tenant != moduleTenantA {
		t.Fatalf("grant = %+v, want the declared prefix and tenant", grant)
	}
	if business.ModulePrincipalID("documents") == business.ModulePrincipalID("billing") {
		t.Fatal("two modules must not derive the same principal id")
	}
}

// A module's own content types are untouched by the reservation above: only the
// two the host reads out of a sealed capability are refused.
func TestParseModulePrincipalRegistry_AcceptsOrdinaryContentResources(t *testing.T) {
	registry, err := business.ParseModulePrincipalRegistry(
		`{"documents":{"resources":["documents.files","rolesets"],"tenant":"` + moduleTenantA + `"}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	grant := registry[business.ModulePrincipalID("documents")]
	if len(grant.Resources) != 2 {
		t.Fatalf("resources = %v, want both declarations kept", grant.Resources)
	}
	if business.IsHostSealedScopeResource("rolesets") || business.IsHostSealedScopeResource("documents.files") {
		t.Fatal("the reservation must match the exact host resource type, not a prefix of it")
	}
	if !business.IsHostSealedScopeResource(business.HostScopeRoles) ||
		!business.IsHostSealedScopeResource(business.HostScopeAudit) {
		t.Fatal("both host sealed-scope resources must be reserved")
	}
}

func TestParseModulePrincipalRegistry_RejectsUnusableDeclarations(t *testing.T) {
	tests := map[string]string{
		"invalid prefix": `{"Documents/v1":{"queues":["datasource"],"tenant":"` + moduleTenantA + `"}}`,
		// The tenant is sealed into a signed capability and compared against
		// organization ids: a malformed one signs, then matches no tenant and drops
		// the org from its own audit record, so it is rejected where it is read.
		"no tenant":       `{"documents":{"queues":["datasource"]}}`,
		"non-uuid tenant": `{"documents":{"queues":["datasource"],"tenant":"acme-org"}}`,
		// A principal id is a valid prefix by pattern, so an entry keyed the way the
		// registry used to be would otherwise parse into a principal no module can
		// ever be, denying every call for a reason that names the caller.
		"keyed by principal id": `{"` + modulePrincSvc + `":{"queues":["datasource"],"cross_tenant":true,"tenant":"` + moduleTenantA + `"}}`,
		// `roles` and `audit` are read off a sealed capability by the host itself
		// (the collection-metadata disclosure) rather than authorized per node, so
		// the content-read branch's "every read is re-authorized per node"
		// justification does not cover them. Declaring one as module content would
		// let a single collection grant mint an unscoped `roles:read` and disclose
		// who holds a grant on every readable collection, which only an
		// organization-wide assignment could reach before. The registry is operator
		// text this host cannot otherwise check, so it is refused at parse time.
		"host-sealed scope resource roles": `{"documents":{"resources":["documents.files","roles"],"tenant":"` + moduleTenantA + `"}}`,
		"host-sealed scope resource audit": `{"documents":{"resources":["audit"],"tenant":"` + moduleTenantA + `"}}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := business.ParseModulePrincipalRegistry(raw); err == nil {
				t.Fatal("expected an unusable module principal declaration to be rejected")
			}
		})
	}
}

// fakeVisibilityStore answers the visible-subject read without a database,
// recording the limit the surface asked for so the one-over-the-cap read that
// distinguishes a servable set from an oversized one is pinned rather than
// inferred from the returned value.
type fakeVisibilityStore struct {
	fakeTxStore
	subjects   []string
	gotLimit   int
	gotOrg     string
	gotViewer  string
	orgTxCount int
}

func (f *fakeVisibilityStore) ListVisibleSubjects(_ context.Context, orgID, viewerID string, limit int) ([]string, error) {
	f.gotOrg, f.gotViewer, f.gotLimit = orgID, viewerID, limit
	out := f.subjects
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// WithOrgTx overrides the embedded no-op so a test can count the transactions
// one call opens.
func (f *fakeVisibilityStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	f.orgTxCount++
	return fn(ctx)
}

func newVisibilityService(t *testing.T, store business.Store, crossTenant bool) *business.Service {
	t.Helper()
	svc, err := business.NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{
		modulePrincSvc: {Prefix: "content", CrossTenant: crossTenant},
	})
	return svc
}

func TestModuleListSubjectVisibility_UnknownPrincipalRejected(t *testing.T) {
	svc := newVisibilityService(t, &fakeVisibilityStore{}, false)
	_, err := svc.ModuleListSubjectVisibility(context.Background(),
		business.ModuleCaller{PrincipalID: "someone-else", BoundOrg: moduleTenantA},
		moduleTenantA, moduleUserA)
	requireCode(t, err, codes.PermissionDenied)
}

func TestModuleListSubjectVisibility_CrossTenantRejected(t *testing.T) {
	svc := newVisibilityService(t, &fakeVisibilityStore{}, false)
	_, err := svc.ModuleListSubjectVisibility(context.Background(), moduleCaller(), moduleTenantB, moduleUserA)
	requireCode(t, err, codes.PermissionDenied)
}

// A module bound to one tenant must not learn anything about a subject in
// another: the tenant guard alone does not settle that, because a cross-tenant
// principal may legitimately name tenant B while the viewer belongs to neither.
func TestModuleListSubjectVisibility_ViewerOutsideTenantRejected(t *testing.T) {
	store := &fakeVisibilityStore{fakeTxStore: fakeTxStore{members: map[string]bool{}}}
	svc := newVisibilityService(t, store, true)
	_, err := svc.ModuleListSubjectVisibility(context.Background(), moduleCaller(), moduleTenantB, moduleUserA)
	requireCode(t, err, codes.PermissionDenied)
	if store.gotViewer != "" {
		t.Fatal("membership must be settled before the visibility read runs")
	}
}

// The whole set comes back in one value, and the read asks for one entry more
// than the cap so an oversized set is detectable rather than silently truncated.
func TestModuleListSubjectVisibility_ReturnsTheWholeSet(t *testing.T) {
	store := &fakeVisibilityStore{
		fakeTxStore: fakeTxStore{members: map[string]bool{moduleTenantA + "|" + moduleUserA: true}},
		subjects:    []string{"s1", "s2", "s3"},
	}
	svc := newVisibilityService(t, store, false)
	grants, err := svc.ModuleListSubjectVisibility(context.Background(), moduleCaller(), moduleTenantA, moduleUserA)
	if err != nil {
		t.Fatalf("ModuleListSubjectVisibility: %v", err)
	}
	if store.gotLimit != business.ModuleSubjectVisibilityMaxSet+1 {
		t.Fatalf("store limit = %d, want the cap plus one", store.gotLimit)
	}
	if len(grants) != 3 {
		t.Fatalf("grants = %+v, want every subject in one value", grants)
	}
	if store.gotOrg != moduleTenantA || store.gotViewer != moduleUserA {
		t.Fatalf("store read (%q, %q), want the requested tenant and viewer", store.gotOrg, store.gotViewer)
	}
	for _, grant := range grants {
		if !grant.ExpiresAt.IsZero() {
			t.Fatalf("grant %q carries an expiry the team tree cannot produce", grant.VisibleSubjectID)
		}
	}
}

// The whole set is read in exactly ONE transaction. This is the invariant the
// surface exists to hold: when the set was paginated, a consumer reassembling it
// read each page in its own transaction, and a membership revoked between two
// pages still reached the bulk replace — reinstating an authority an
// administrator had withdrawn. A second transaction here is that bug returning.
func TestModuleListSubjectVisibility_ReadsInOneTransaction(t *testing.T) {
	store := &fakeVisibilityStore{
		fakeTxStore: fakeTxStore{members: map[string]bool{moduleTenantA + "|" + moduleUserA: true}},
		subjects:    []string{"s1", "s2", "s3"},
	}
	svc := newVisibilityService(t, store, false)
	if _, err := svc.ModuleListSubjectVisibility(context.Background(), moduleCaller(), moduleTenantA, moduleUserA); err != nil {
		t.Fatalf("ModuleListSubjectVisibility: %v", err)
	}
	if store.orgTxCount != 1 {
		t.Fatalf("opened %d org transactions, want exactly one: the set must be one snapshot", store.orgTxCount)
	}
}

// A set past the cap is refused, not truncated: a bulk replace fed a truncated
// set would withdraw grants that are still live, and one fed a torn set would
// reinstate grants that are not. FailedPrecondition says the tenant's hierarchy
// is the thing to change, which no retry or backoff can fix.
func TestModuleListSubjectVisibility_OversizedSetRefused(t *testing.T) {
	oversized := make([]string, business.ModuleSubjectVisibilityMaxSet+1)
	for i := range oversized {
		oversized[i] = fmt.Sprintf("s%d", i)
	}
	store := &fakeVisibilityStore{
		fakeTxStore: fakeTxStore{members: map[string]bool{moduleTenantA + "|" + moduleUserA: true}},
		subjects:    oversized,
	}
	svc := newVisibilityService(t, store, false)
	_, err := svc.ModuleListSubjectVisibility(context.Background(), moduleCaller(), moduleTenantA, moduleUserA)
	requireCode(t, err, codes.FailedPrecondition)
}

// A set exactly at the cap is servable — the boundary is not off by one.
func TestModuleListSubjectVisibility_SetAtTheCapIsServed(t *testing.T) {
	atCap := make([]string, business.ModuleSubjectVisibilityMaxSet)
	for i := range atCap {
		atCap[i] = fmt.Sprintf("s%d", i)
	}
	store := &fakeVisibilityStore{
		fakeTxStore: fakeTxStore{members: map[string]bool{moduleTenantA + "|" + moduleUserA: true}},
		subjects:    atCap,
	}
	svc := newVisibilityService(t, store, false)
	grants, err := svc.ModuleListSubjectVisibility(context.Background(), moduleCaller(), moduleTenantA, moduleUserA)
	if err != nil {
		t.Fatalf("ModuleListSubjectVisibility: %v", err)
	}
	if len(grants) != business.ModuleSubjectVisibilityMaxSet {
		t.Fatalf("grants = %d, want the whole set at the cap", len(grants))
	}
}

// A module naming a subject is making a claim the host cannot check: actor_id
// means "the verified initiator" everywhere else on the spine, and here the
// module is the only witness that the subject acted. The host checks the one
// thing it can — that the subject is a party in the organization the row lands
// in — so a module bound to one tenant cannot write another tenant's user id, or
// any id at all, into a trail that is append-only and never correctable.
// moduleUserB is a well-formed principal id that is a member of nothing here.
func TestModuleEmitAuditEvent_SubjectMustBelongToTheTenant(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
	spine := &capturingAuditEmitter{}
	svc.SetAuditEmitter(spine)
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "saas.document.ingested", moduleUserB, "example-solution", "entry-1", "", nil)
	requireCode(t, err, codes.FailedPrecondition)
	if !errors.Is(err, business.ErrModuleAuditActorUnresolved) && !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("error must say the subject is not of this tenant: %v", err)
	}
	if len(spine.entries) != 0 {
		t.Fatalf("an unverified subject reached the spine: %+v", spine.entries)
	}
}

// A system-scoped row has no organization to place a subject in, so there is no
// subject it could verify and none it will record. Only the module's own work
// belongs on one.
func TestModuleEmitAuditEvent_SystemScopedRowTakesNoSubject(t *testing.T) {
	svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, true)
	spine := &capturingAuditEmitter{}
	svc.SetAuditEmitter(spine)
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		"", "saas.document.ingested", moduleUserA, "example-solution", "entry-1", "", nil)
	requireCode(t, err, codes.FailedPrecondition)

	if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		"", "saas.document.ingested", "system:ingest", "example-solution", "entry-1", "", nil); err != nil {
		t.Fatalf("the module's own work is what a system-scoped row records: %v", err)
	}
	if got := spine.entries[0]; got.ActorID != modulePrincSvc || got.ActorType != business.ActorTypeSystem {
		t.Fatalf("actor = %q/%q, want the module principal as a system actor", got.ActorID, got.ActorType)
	}
}

// A membership read that failed proves neither that the subject belongs to the
// tenant nor that it does not. audit_events_no_update means a row written under a
// guessed actor can never be put right, so the emit surfaces the error and
// nothing reaches the spine.
func TestModuleEmitAuditEvent_MembershipReadFailureSurfaces(t *testing.T) {
	store := fakeTxStore{membersErr: errors.New("organization_members unavailable")}
	svc := newModuleServiceWithStore(t, store, &fakeJobBackend{}, false)
	spine := &capturingAuditEmitter{}
	svc.SetAuditEmitter(spine)
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "saas.document.ingested", moduleUserA, "example-solution", "entry-1", "", nil)
	requireCode(t, err, codes.Internal)
	if len(spine.entries) != 0 {
		t.Fatalf("an unresolved actor reached the spine: %+v", spine.entries)
	}
}

// resource_id is the one identifier field on an audit row that no declaration
// mechanism covers: payload fields declare PII and RedactPayload strips it on the
// CSV download, the JSON download and the S3 JSONL exporter, while resource_id is
// written verbatim into a table audit_events_no_update and audit_events_no_delete
// make append-only for 365 days. Storing the entry id as given is what keeps a
// document's ULID; it also replaced an implicit constraint with none, so an
// identifier is kept and a locator — record content, carrying a path or an
// address — is dropped to no resource id, which is what the writer used to do to
// it. Neither refuses the emit.
func TestModuleEmitAuditEvent_EntryIDKeepsIdentifiersAndDropsLocators(t *testing.T) {
	emit := func(t *testing.T, entry string) business.AuditEntry {
		t.Helper()
		svc := newModuleServiceWithStore(t, fakeTxStore{}, &fakeJobBackend{}, false)
		spine := &capturingAuditEmitter{}
		svc.SetAuditEmitter(spine)
		if err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
			moduleTenantA, "saas.document.ingested", modulePrincSvc, "example-solution", entry, "", nil); err != nil {
			t.Fatalf("entry id %q must not refuse the emit: %v", entry, err)
		}
		if len(spine.entries) != 1 {
			t.Fatalf("the event must still land, got %d entries", len(spine.entries))
		}
		return spine.entries[0]
	}
	for name, entry := range map[string]string{
		"a ULID":             "01M3C1E527S6Z98WFBN1B8VRG4",
		"a uuid":             moduleUserA,
		"a provider id":      "cus_NffrFeUfNV2Hib",
		"a numeric id":       "88214417",
		"a digest":           "sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
		"a dotted version":   "entry-1.v3",
		"a plus-scoped id":   "run+42",
		"an unpadded base64": "aGVsbG8-d29ybGQ_",
	} {
		t.Run("keeps "+name, func(t *testing.T) {
			if got := emit(t, entry).ResourceID; got != entry {
				t.Fatalf("resource id = %q, want the entry %q", got, entry)
			}
		})
	}
	for name, entry := range map[string]string{
		"a path":                 "clients/2026-Q1.pdf",
		"a path with an email":   "clients/jane.doe@example.com/2026-Q1.pdf",
		"a windows path":         `clients\2026-Q1.pdf`,
		"a bare address":         "jane.doe@example.com",
		"a value with a space":   "Q1 report",
		"a value with a tab":     "entry\treport",
		"a value with a newline": "entry\nreport",
	} {
		t.Run("drops "+name, func(t *testing.T) {
			if got := emit(t, entry).ResourceID; got != "" {
				t.Fatalf("resource id = %q, want it dropped", got)
			}
		})
	}
}

// The registry and the payload registry reject on the request alone, so they must
// still do so before this surface touches the store — the subject case now needs a
// membership read, and newModuleService is documented as safe with a nil store for
// exactly these guards. A nil store panics rather than erring, so this pins the
// order rather than the message.
func TestModuleEmitAuditEvent_RequestOnlyGuardsRejectBeforeAnyStoreAccess(t *testing.T) {
	svc := newModuleService(t, &fakeJobBackend{})
	err := svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "document.made_up", moduleUserA, "example-solution", "entry-1", "", nil)
	requireCode(t, err, codes.InvalidArgument)

	fields, ferr := structpb.NewStruct(map[string]any{"not_a_declared_field": "x"})
	if ferr != nil {
		t.Fatal(ferr)
	}
	err = svc.ModuleEmitAuditEvent(context.Background(), moduleCaller(),
		moduleTenantA, "saas.document.ingested", moduleUserA, "example-solution", "entry-1", "", fields)
	requireCode(t, err, codes.InvalidArgument)
}

// orgAdminsStore serves a tenant's membership to NotifyOrgAdmins and records
// every notification written, so a test can say exactly who was notified.
type orgAdminsStore struct {
	fakeTxStore
	members  map[string][]*gen.OrgMembership
	notified []*business.Notification
}

func (s *orgAdminsStore) ListOrgMembers(_ context.Context, orgID string) ([]*gen.OrgMembership, error) {
	return s.members[orgID], nil
}

// CreateNotification keeps the store's idempotency contract: a repeated row id
// with the same content converges, and with different content conflicts.
func (s *orgAdminsStore) CreateNotification(_ context.Context, n *business.Notification) error {
	for _, existing := range s.notified {
		if existing.ID == n.ID && existing.Body != n.Body {
			return business.ErrNotificationIdempotencyConflict
		}
	}
	s.notified = append(s.notified, n)
	return nil
}

const (
	orgOwner  = "55555555-5555-5555-5555-555555555555"
	orgAdmin  = "66666666-6666-6666-6666-666666666666"
	orgMember = "77777777-7777-7777-7777-777777777777"
)

func newOrgAdminsFixture(t *testing.T) (*business.Service, *orgAdminsStore, *capturingAuditEmitter) {
	t.Helper()
	store := &orgAdminsStore{members: map[string][]*gen.OrgMembership{
		moduleTenantA: {
			{OrgId: moduleTenantA, UserId: orgOwner, Role: gen.OrgRole_ORG_ROLE_OWNER},
			{OrgId: moduleTenantA, UserId: orgAdmin, Role: gen.OrgRole_ORG_ROLE_ADMIN},
			{OrgId: moduleTenantA, UserId: orgMember, Role: gen.OrgRole_ORG_ROLE_MEMBER},
			// The same administrator listed twice is notified once.
			{OrgId: moduleTenantA, UserId: orgAdmin, Role: gen.OrgRole_ORG_ROLE_ADMIN},
		},
		moduleTenantB: {{OrgId: moduleTenantB, UserId: moduleUserB, Role: gen.OrgRole_ORG_ROLE_OWNER}},
	}}
	svc := newModuleServiceWithStore(t, store, &fakeJobBackend{}, false)
	spine := &capturingAuditEmitter{}
	svc.SetAuditEmitter(spine)
	return svc, store, spine
}

func adminNotice(tenant, notificationType, key string) business.ModuleNotifyOrgAdminsInput {
	return business.ModuleNotifyOrgAdminsInput{
		Tenant: tenant, Title: "A delegation was revoked", Body: "Reconnect the source.",
		Type: notificationType, Category: "security", IdempotencyKey: key,
	}
}

// Only the tenant's administrators — owner and admin, once each — are notified;
// a plain member gets nothing, and the module learns only that someone did.
func TestModuleNotifyOrgAdmins_NotifiesOnlyAdministratorsOnce(t *testing.T) {
	svc, store, spine := newOrgAdminsFixture(t)
	delivered, err := svc.ModuleNotifyOrgAdmins(context.Background(), moduleCaller(), adminNotice(moduleTenantA, "warning", ""))
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if !delivered {
		t.Fatal("delivered = false, want true")
	}
	var recipients []string
	for _, n := range store.notified {
		recipients = append(recipients, n.UserID)
		if n.OrgID != moduleTenantA || n.Type != "warning" {
			t.Errorf("notification %+v", n)
		}
	}
	sort.Strings(recipients)
	if want := []string{orgOwner, orgAdmin}; !slices.Equal(recipients, want) {
		t.Fatalf("recipients = %v, want %v", recipients, want)
	}
	// Audited with counts; the recipients are the host's and appear nowhere.
	var audited *business.AuditEntry
	for i := range spine.entries {
		if spine.entries[i].EventType == business.EventModuleOrgAdminsNotified {
			audited = &spine.entries[i]
		}
	}
	if audited == nil {
		t.Fatal("no saas.module.org_admins_notified entry")
	}
	if audited.Payload["recipients"] != 2 || audited.Payload["delivered"] != 2 {
		t.Errorf("audit payload %v", audited.Payload)
	}
	for _, value := range audited.Payload {
		if value == orgOwner || value == orgAdmin {
			t.Errorf("audit payload names a recipient: %v", audited.Payload)
		}
	}
}

// With an idempotency key, a redelivered call converges on the rows the first
// wrote: each recipient's row id is the same.
func TestModuleNotifyOrgAdmins_IdempotencyKeyIsPerRecipient(t *testing.T) {
	svc, store, _ := newOrgAdminsFixture(t)
	for i := 0; i < 2; i++ {
		if _, err := svc.ModuleNotifyOrgAdmins(context.Background(), moduleCaller(), adminNotice(moduleTenantA, "", "delegation-revoked-7")); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]map[string]bool{}
	for _, n := range store.notified {
		if ids[n.UserID] == nil {
			ids[n.UserID] = map[string]bool{}
		}
		ids[n.UserID][n.ID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("recipients = %d, want 2", len(ids))
	}
	seen := map[string]bool{}
	for user, rows := range ids {
		if len(rows) != 1 {
			t.Errorf("%s got %d distinct rows across a redelivery, want 1", user, len(rows))
		}
		for id := range rows {
			if seen[id] {
				t.Errorf("two recipients share row id %s", id)
			}
			seen[id] = true
		}
	}
}

// A different notification under a key already used is the caller's error,
// FailedPrecondition, never Internal.
func TestModuleNotifyOrgAdmins_KeyReusedForDifferentContentIsFailedPrecondition(t *testing.T) {
	svc, _, _ := newOrgAdminsFixture(t)
	if _, err := svc.ModuleNotifyOrgAdmins(context.Background(), moduleCaller(), adminNotice(moduleTenantA, "info", "key-1")); err != nil {
		t.Fatal(err)
	}
	changed := adminNotice(moduleTenantA, "info", "key-1")
	changed.Body = "something else"
	_, err := svc.ModuleNotifyOrgAdmins(context.Background(), moduleCaller(), changed)
	requireCode(t, err, codes.FailedPrecondition)
}

// A module bound to one tenant cannot notify another's administrators, and an
// unknown type or category is the caller's error; none of them writes anything.
func TestModuleNotifyOrgAdmins_RefusedBeforeAnyWrite(t *testing.T) {
	svc, store, _ := newOrgAdminsFixture(t)
	_, err := svc.ModuleNotifyOrgAdmins(context.Background(), moduleCaller(), adminNotice(moduleTenantB, "info", ""))
	requireCode(t, err, codes.PermissionDenied)
	_, err = svc.ModuleNotifyOrgAdmins(context.Background(), moduleCaller(), adminNotice(moduleTenantA, "sync", ""))
	requireCode(t, err, codes.InvalidArgument)
	bad := adminNotice(moduleTenantA, "info", "")
	bad.Category = "not-a-category"
	_, err = svc.ModuleNotifyOrgAdmins(context.Background(), moduleCaller(), bad)
	requireCode(t, err, codes.InvalidArgument)
	if len(store.notified) != 0 {
		t.Fatalf("a refused call wrote %d notifications", len(store.notified))
	}
}
