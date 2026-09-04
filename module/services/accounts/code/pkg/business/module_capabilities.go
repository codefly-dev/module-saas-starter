// Package business — the module-facing platform capability surface (issue #463).
//
// A sibling module on its own database cannot join the saas-starter transaction
// or reach the in-process job store, so it uses this surface to enqueue and
// lease durable jobs, notify users, request approvals, and emit typed audit
// events. The RPC adapter authenticates the caller from the forwarded Work
// Context and hands these methods a ModuleCaller; every method here re-derives
// authority from the per-principal registry, so the guard is enforced once, in
// one place, regardless of transport.
//
// The producer contract is at-least-once with idempotency keys: the caller
// cannot commit its outbox row inside the saas mutation the way the in-process
// producer does, so it retries against the (direction, scope, queue, source,
// idempotency_key) uniqueness and request_fingerprint dedupe described in
// module/JOBS.md.
package business

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ModulePrincipalGrant declares what a module service principal may do on the
// capability surface. Queues bounds the queues it may enqueue to and claim from;
// CrossTenant lets it act on tenants other than its bound org and enqueue global
// (inbox-worker) jobs — the authority an inbox worker needs to service every
// tenant's deliveries on its queue.
type ModulePrincipalGrant struct {
	Queues      []string
	CrossTenant bool
}

func (g ModulePrincipalGrant) allowsQueue(queue string) bool {
	for _, q := range g.Queues {
		if q == queue {
			return true
		}
	}
	return false
}

// ModulePrincipalRegistry maps a module service principal id to its grant.
type ModulePrincipalRegistry map[string]ModulePrincipalGrant

// ParseModulePrincipalRegistry decodes the deployment-provided registry of
// module service principals. The document its JSON describes is a map of
// principal id to {"queues": [...], "cross_tenant": bool}. An empty string
// yields an empty registry, which denies every caller (fail-closed).
func ParseModulePrincipalRegistry(raw string) (ModulePrincipalRegistry, error) {
	if raw == "" {
		return ModulePrincipalRegistry{}, nil
	}
	var wire map[string]struct {
		Queues      []string `json:"queues"`
		CrossTenant bool     `json:"cross_tenant"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil, err
	}
	registry := make(ModulePrincipalRegistry, len(wire))
	for id, grant := range wire {
		registry[id] = ModulePrincipalGrant{Queues: grant.Queues, CrossTenant: grant.CrossTenant}
	}
	return registry, nil
}

// ModuleCaller is the authenticated module service principal, as the RPC adapter
// derived it from the forwarded Work Context.
type ModuleCaller struct {
	PrincipalID string // the acting service principal (subject) id
	BoundOrg    string // the tenant the principal is bound to
}

// grant resolves the caller's declared authority. An unknown principal is
// denied — the registry is the allowlist, so a nil/empty registry fails closed.
func (s *Service) moduleGrant(caller ModuleCaller) (ModulePrincipalGrant, error) {
	if caller.PrincipalID == "" {
		return ModulePrincipalGrant{}, status.Error(codes.Unauthenticated, "module caller identity required")
	}
	grant, ok := s.modulePrincipals[caller.PrincipalID]
	if !ok {
		return ModulePrincipalGrant{}, status.Errorf(codes.PermissionDenied, "principal %s is not a registered module principal", caller.PrincipalID)
	}
	return grant, nil
}

// authorizeTenant resolves the tenant a call targets. A call may only name its
// own bound tenant unless the principal holds a cross-tenant grant.
func authorizeTenant(caller ModuleCaller, grant ModulePrincipalGrant, requested string) error {
	if requested == caller.BoundOrg {
		return nil
	}
	if grant.CrossTenant {
		return nil
	}
	return status.Errorf(codes.PermissionDenied, "principal %s may not act on tenant %s", caller.PrincipalID, requested)
}

// requireTenantMember verifies a user/subject actually belongs to the named
// tenant before the surface acts on it, so a module bound to tenant A cannot
// target a user or subject in tenant B (which the tenant guard alone does not
// prevent — org membership is a separate fact). The membership read runs under
// the control-plane role because organization_members is RLS-scoped and the
// caller carries no tenant GUC of its own.
func (s *Service) requireTenantMember(ctx context.Context, tenant, userID string) error {
	var member bool
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var e error
		member, e = s.store.OrgMemberExists(ctx, tenant, userID)
		return e
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	if !member {
		return status.Errorf(codes.PermissionDenied, "user %s is not a member of tenant %s", userID, tenant)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

// ModuleEnqueueJob appends durable work on behalf of a module. tenant is the
// authoritative tenant the caller claims to act on: org-scoped work must name
// that same tenant, and subject-scoped work must target a member of it, so the
// job's scope can never reach a tenant the caller was not authorized for. Tenant-
// and subject-scoped work goes through the request-scoped producer inside a
// matching transaction so the security-definer enqueue verifies the scope;
// global inbox work requires the cross-tenant grant and uses the privileged
// worker producer.
func (s *Service) ModuleEnqueueJob(ctx context.Context, caller ModuleCaller, tenant string, req *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
	w := wool.Get(ctx).In("ModuleEnqueueJob")
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return nil, err
	}
	job := req.GetJob()
	if job == nil {
		return nil, status.Error(codes.InvalidArgument, "job is required")
	}
	if !grant.allowsQueue(job.GetQueue()) {
		return nil, status.Errorf(codes.PermissionDenied, "principal %s may not use queue %q", caller.PrincipalID, job.GetQueue())
	}

	scope := job.GetScope()
	var resp *jobsv1.EnqueueJobResponse
	switch {
	case scope.GetOrganizationId() != "":
		orgID := scope.GetOrganizationId()
		// The scope's org must be the tenant the caller is authorized for; a
		// mismatch would let an authorized tenant name smuggle work into another.
		if orgID != tenant {
			return nil, status.Error(codes.InvalidArgument, "job organization scope must equal the request tenant")
		}
		if err := authorizeTenant(caller, grant, tenant); err != nil {
			return nil, err
		}
		err = s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
			resp, err = s.moduleProducer.EnqueueJob(ctx, req)
			return err
		})
	case scope.GetSubjectId() != "":
		if err := authorizeTenant(caller, grant, tenant); err != nil {
			return nil, err
		}
		// A subject-scoped job names a user; without this the tenant guard is a
		// no-op (WithUserTx sets current_user_id to the subject, so the security-
		// definer's subject==current_user_id check is tautological). Bind the
		// subject to the authorized tenant explicitly.
		if err := s.requireTenantMember(ctx, tenant, scope.GetSubjectId()); err != nil {
			return nil, err
		}
		err = s.store.WithUserTx(ctx, scope.GetSubjectId(), func(ctx context.Context) error {
			resp, err = s.moduleProducer.EnqueueJob(ctx, req)
			return err
		})
	case scope.GetGlobal():
		if !grant.CrossTenant {
			return nil, status.Errorf(codes.PermissionDenied, "principal %s may not enqueue global work", caller.PrincipalID)
		}
		// Global/inbox work bypasses the request-scoped producer: the privileged
		// worker store opens its own short transaction, the only path allowed to
		// append global or inbox rows (module/JOBS.md authority model).
		globalProducer, ok := s.moduleJobStore.(jobs.Producer)
		if !ok {
			return nil, status.Error(codes.Internal, "module job store cannot enqueue global work")
		}
		resp, err = globalProducer.EnqueueJob(ctx, req)
	default:
		return nil, status.Error(codes.InvalidArgument, "job scope is required")
	}
	if err != nil {
		return nil, moduleJobError(w, err)
	}
	return resp, nil
}

// ModuleClaimJobs leases a bounded batch from one queue the principal owns.
// Claiming a queue reads every scope on it — global work and every tenant's
// org- and subject-scoped jobs (the claim selects by queue with no tenant
// filter) — so a claimed payload can belong to any tenant. That makes claiming
// an inherently cross-tenant, inbox-worker operation: it requires the cross-
// tenant grant, not merely the queue grant. Without this a tenant-bound
// principal could read other tenants' confidential job payloads off a shared
// queue (e.g. datasource, which carries every tenant's ingest jobs).
func (s *Service) ModuleClaimJobs(ctx context.Context, caller ModuleCaller, req *jobsv1.ClaimJobsRequest) (*jobsv1.ClaimJobsResponse, error) {
	w := wool.Get(ctx).In("ModuleClaimJobs")
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return nil, err
	}
	if !grant.allowsQueue(req.GetQueue()) {
		return nil, status.Errorf(codes.PermissionDenied, "principal %s may not claim queue %q", caller.PrincipalID, req.GetQueue())
	}
	if !grant.CrossTenant {
		return nil, status.Errorf(codes.PermissionDenied, "principal %s may not claim queue %q: claiming reads across tenants and requires a cross-tenant grant", caller.PrincipalID, req.GetQueue())
	}
	resp, err := s.moduleJobStore.Claim(ctx, req)
	if err != nil {
		return nil, moduleJobError(w, err)
	}
	return resp, nil
}

// ModuleHeartbeatJob renews a live lease. The fencing token is the authority: a
// caller without the current, unexpired token cannot renew.
func (s *Service) ModuleHeartbeatJob(ctx context.Context, caller ModuleCaller, req *jobsv1.HeartbeatJobRequest) (*jobsv1.HeartbeatJobResponse, error) {
	w := wool.Get(ctx).In("ModuleHeartbeatJob")
	if _, err := s.moduleGrant(caller); err != nil {
		return nil, err
	}
	resp, err := s.moduleJobStore.Heartbeat(ctx, req)
	if err != nil {
		return nil, moduleJobError(w, err)
	}
	return resp, nil
}

// ModuleAckJob completes a leased job successfully.
func (s *Service) ModuleAckJob(ctx context.Context, caller ModuleCaller, req *jobsv1.CompleteJobRequest) error {
	w := wool.Get(ctx).In("ModuleAckJob")
	if _, err := s.moduleGrant(caller); err != nil {
		return err
	}
	if err := s.moduleJobStore.Complete(ctx, req); err != nil {
		return moduleJobError(w, err)
	}
	return nil
}

// ModuleNackJob fails a leased job: retryable reschedules (or dead-letters when
// the attempt budget is exhausted); otherwise it dead-letters immediately.
func (s *Service) ModuleNackJob(ctx context.Context, caller ModuleCaller, lease *jobsv1.JobLeaseReference, failure *jobsv1.JobFailure, retryable bool, retryAt *timestamppb.Timestamp) error {
	w := wool.Get(ctx).In("ModuleNackJob")
	if _, err := s.moduleGrant(caller); err != nil {
		return err
	}
	if retryable {
		if retryAt == nil {
			return status.Error(codes.InvalidArgument, "retry_at is required for a retryable nack")
		}
		if _, err := s.moduleJobStore.Retry(ctx, &jobsv1.RetryJobRequest{Lease: lease, Failure: failure, RetryAt: retryAt}); err != nil {
			return moduleJobError(w, err)
		}
		return nil
	}
	if err := s.moduleJobStore.DeadLetter(ctx, &jobsv1.DeadLetterJobRequest{Lease: lease, Failure: failure}); err != nil {
		return moduleJobError(w, err)
	}
	return nil
}

// moduleJobError maps the product-neutral job sentinels onto gRPC codes so the
// module sees a stable, transport-independent contract.
func moduleJobError(w *wool.Wool, err error) error {
	switch {
	case errors.Is(err, jobs.ErrInvalidCommand), errors.Is(err, jobs.ErrOrderingKeyTooLong):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, jobs.ErrIdempotencyConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, jobs.ErrLeaseLost):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, jobs.ErrJobNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, jobs.ErrTransactionRequired):
		return status.Error(codes.Internal, err.Error())
	}
	return w.Wrap(err)
}

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

// ModuleNotifyUserInput is a named struct rather than a long positional string
// list so a call site cannot silently transpose title/body/type/category.
type ModuleNotifyUserInput struct {
	Tenant         string
	UserID         string
	Title          string
	Body           string
	Type           string
	ActionURL      string
	Category       string
	IdempotencyKey string
}

// ModuleNotifyUserResult reports whether the notification was delivered or
// suppressed by category policy, plus the row id when delivered.
type ModuleNotifyUserResult struct {
	NotificationID string
	Delivered      bool
}

// ModuleNotifyUser routes a notification through the same category policy
// internal callers use. The target user must belong to the named tenant:
// notifications are user-scoped, so the tenant guard alone does not stop a
// module bound to tenant A from notifying a user in tenant B.
func (s *Service) ModuleNotifyUser(ctx context.Context, caller ModuleCaller, in ModuleNotifyUserInput) (ModuleNotifyUserResult, error) {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return ModuleNotifyUserResult{}, err
	}
	if err := authorizeTenant(caller, grant, in.Tenant); err != nil {
		return ModuleNotifyUserResult{}, err
	}
	if _, err := notificationCategoryIsMandatory(NotificationCategory(in.Category)); err != nil {
		return ModuleNotifyUserResult{}, status.Errorf(codes.InvalidArgument, "invalid notification category %q", in.Category)
	}
	if err := s.requireTenantMember(ctx, in.Tenant, in.UserID); err != nil {
		return ModuleNotifyUserResult{}, err
	}
	notification, err := s.CreateNotification(ctx, CreateNotificationInput{
		UserID:         in.UserID,
		OrgID:          in.Tenant,
		Title:          in.Title,
		Body:           in.Body,
		Type:           in.Type,
		ActionURL:      in.ActionURL,
		Category:       NotificationCategory(in.Category),
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return ModuleNotifyUserResult{}, err
	}
	if notification == nil {
		return ModuleNotifyUserResult{Delivered: false}, nil
	}
	return ModuleNotifyUserResult{NotificationID: notification.ID, Delivered: true}, nil
}

// ---------------------------------------------------------------------------
// Approvals
// ---------------------------------------------------------------------------

// ModuleRequestApprovalInput is the module-facing half of the approval
// primitive: it opens a pending request whose resume outbox job the module
// consumes on its own queue once the request is approved.
type ModuleRequestApprovalInput struct {
	Tenant      string
	Resource    string
	Action      string
	Subject     map[string]any
	RequestedBy string
	Quorum      int
	ApproverSet []string
	AllowSelf   bool
	ResumeQueue string
	ResumeTopic string
	ResumePayload map[string]any
	ExpiresAt   *time.Time
	EscalateAt  *time.Time
}

// ModuleRequestApproval creates a pending approval on the caller's tenant. The
// resume queue must be one the principal may claim, so a decision cannot direct
// work onto a queue the module does not own.
func (s *Service) ModuleRequestApproval(ctx context.Context, caller ModuleCaller, in ModuleRequestApprovalInput) (string, error) {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return "", err
	}
	if err := authorizeTenant(caller, grant, in.Tenant); err != nil {
		return "", err
	}
	if !grant.allowsQueue(in.ResumeQueue) {
		return "", status.Errorf(codes.PermissionDenied, "principal %s may not resume onto queue %q", caller.PrincipalID, in.ResumeQueue)
	}
	id, err := s.CreateApprovalRequest(ctx, &CreateApprovalRequestInput{
		OrgID:       in.Tenant,
		Resource:    in.Resource,
		Action:      in.Action,
		Subject:     in.Subject,
		RequestedBy: in.RequestedBy,
		Quorum:      in.Quorum,
		Policy:      ApprovalPolicy{ApproverSet: in.ApproverSet, AllowSelf: in.AllowSelf},
		ResumeRef:   ResumeRef{Queue: in.ResumeQueue, Topic: in.ResumeTopic, Payload: in.ResumePayload},
		ExpiresAt:   in.ExpiresAt,
		EscalateAt:  in.EscalateAt,
	})
	if err != nil {
		return "", moduleApprovalError(err)
	}
	return id, nil
}

// ModuleGetApproval returns one approval request on the caller's tenant.
func (s *Service) ModuleGetApproval(ctx context.Context, caller ModuleCaller, tenant, id string) (*ApprovalRequest, error) {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return nil, err
	}
	if err := authorizeTenant(caller, grant, tenant); err != nil {
		return nil, err
	}
	approval, err := s.GetApprovalRequest(ctx, tenant, id)
	if err != nil {
		return nil, moduleApprovalError(err)
	}
	return approval, nil
}

// ModuleCancelApproval withdraws a still-open approval request.
func (s *Service) ModuleCancelApproval(ctx context.Context, caller ModuleCaller, tenant, id, reason string) error {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return err
	}
	if err := authorizeTenant(caller, grant, tenant); err != nil {
		return err
	}
	if err := s.CancelApprovalRequest(ctx, tenant, id, reason); err != nil {
		return moduleApprovalError(err)
	}
	return nil
}

// moduleApprovalError maps approval-engine errors onto gRPC codes by their typed
// StoreError. An untyped error is an internal fault, NOT bad client input: the
// previous blanket InvalidArgument told a module its request was malformed even
// when the database failed, so it would retry the same request forever.
func moduleApprovalError(err error) error {
	var se *StoreError
	if errors.As(err, &se) {
		switch se.StoreErrorType {
		case ErrTypeValidation:
			return status.Error(codes.InvalidArgument, err.Error())
		case ErrTypeNotFound:
			return status.Error(codes.NotFound, err.Error())
		case ErrTypeConflict:
			return status.Error(codes.FailedPrecondition, err.Error())
		case ErrTypePermission:
			return status.Error(codes.PermissionDenied, err.Error())
		}
	}
	return status.Error(codes.Internal, err.Error())
}

// enqueueApprovalResume appends the resume outbox job for an approved request.
// It runs inside the Decide transaction (moduleProducer is request-scoped), so
// the approved transition and the resume job commit atomically. The approval id
// is the idempotency key: a retried decision resolves to the same durable job
// rather than resuming the gated action twice. No-op when no resume queue is
// declared or the producer is not wired.
func (s *Service) enqueueApprovalResume(ctx context.Context, req *ApprovalRequest) error {
	if req.ResumeRef.Queue == "" {
		return nil
	}
	// A request that declares a resume queue MUST get its resume job or the whole
	// decision aborts: silently approving without enqueuing the resume strands the
	// gated action with no signal, which is exactly the loss this primitive exists
	// to prevent. Fail the decision instead of dropping the resume.
	if s.moduleProducer == nil {
		return fmt.Errorf("approval %s declares resume queue %q but the module producer is not wired", req.ID, req.ResumeRef.Queue)
	}
	payload, err := json.Marshal(map[string]any{
		"approval_id": req.ID,
		"resource":    req.Resource,
		"action":      req.Action,
		"subject":     req.Subject,
		"payload":     req.ResumeRef.Payload,
	})
	if err != nil {
		return err
	}
	topic := req.ResumeRef.Topic
	if topic == "" {
		topic = "approval.resumed"
	}
	_, err = s.moduleProducer.EnqueueJob(ctx, &jobsv1.EnqueueJobRequest{
		Job: &jobsv1.NewJob{
			Direction:      jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
			Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: req.OrgID}},
			Queue:          req.ResumeRef.Queue,
			Topic:          topic,
			Source:         "approvals",
			IdempotencyKey: "approval-resume:" + req.ID,
			SchemaVersion:  1,
			Payload:        payload,
			ContentType:    "application/json",
			MaxAttempts:    24,
		},
	})
	return err
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// ModuleEmitAuditEvent emits a registered audit event onto the tenant's audit
// spine. The event type must be registered in the code-owned catalog;
// unregistered types are rejected, not stored free-form. An empty tenant emits a
// system-scoped event and requires the cross-tenant grant.
func (s *Service) ModuleEmitAuditEvent(ctx context.Context, caller ModuleCaller, tenant, eventType, actor, solution, entryID string, fields *structpb.Struct) error {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return err
	}
	if tenant == "" {
		if !grant.CrossTenant {
			return status.Errorf(codes.PermissionDenied, "principal %s may not emit system-scoped audit events", caller.PrincipalID)
		}
	} else if err := authorizeTenant(caller, grant, tenant); err != nil {
		return err
	}
	payload := make(map[string]any)
	for k, v := range fields.AsMap() {
		payload[k] = v
	}
	// The scope solution wins over any client-supplied "solution" field: the
	// scope is the trusted value, and it is set last so a field cannot shadow it.
	payload["solution"] = solution
	// Enforce the registered schema at the boundary: an unregistered type or an
	// unknown/mistyped field is rejected, not stored free-form. (Downstream
	// registry validation is only advisory; this is where the module's typed-
	// fields contract is actually enforced.)
	if err := ValidatePayload(EventType(eventType), payload); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	// The emission IS the operation the module requested, so a failed write must
	// surface as an error — not the fire-and-forget emit(), which swallows the
	// error and would report success while the event was silently lost.
	emit := func(ctx context.Context) error {
		return s.emitTx(ctx, actor, "agent", EventType(eventType), solution, entryID, tenant, payload)
	}
	if tenant == "" {
		if err := s.store.WithControlPlane(ctx, emit); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		return nil
	}
	if err := s.store.WithOrgTx(ctx, tenant, emit); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	return nil
}
