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

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

// ModuleEnqueueJob appends durable work on behalf of a module. Tenant- and
// subject-scoped work goes through the request-scoped producer inside a matching
// transaction so the security-definer enqueue verifies the scope; global inbox
// work requires the cross-tenant grant and uses the privileged worker producer.
func (s *Service) ModuleEnqueueJob(ctx context.Context, caller ModuleCaller, req *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
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
		if err := authorizeTenant(caller, grant, orgID); err != nil {
			return nil, err
		}
		err = s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
			resp, err = s.moduleProducer.EnqueueJob(ctx, req)
			return err
		})
	case scope.GetSubjectId() != "":
		if err := authorizeTenant(caller, grant, caller.BoundOrg); err != nil {
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
// Claiming is queue-scoped: a queue belongs to the module that declares it.
func (s *Service) ModuleClaimJobs(ctx context.Context, caller ModuleCaller, req *jobsv1.ClaimJobsRequest) (*jobsv1.ClaimJobsResponse, error) {
	w := wool.Get(ctx).In("ModuleClaimJobs")
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return nil, err
	}
	if !grant.allowsQueue(req.GetQueue()) {
		return nil, status.Errorf(codes.PermissionDenied, "principal %s may not claim queue %q", caller.PrincipalID, req.GetQueue())
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

// ModuleNotifyUserResult reports whether the notification was delivered or
// suppressed by category policy, plus the row id when delivered.
type ModuleNotifyUserResult struct {
	NotificationID string
	Delivered      bool
}

// ModuleNotifyUser routes a notification through the same category policy
// internal callers use.
func (s *Service) ModuleNotifyUser(ctx context.Context, caller ModuleCaller, tenant, userID, title, body, notificationType, actionURL, category, idempotencyKey string) (ModuleNotifyUserResult, error) {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return ModuleNotifyUserResult{}, err
	}
	if err := authorizeTenant(caller, grant, tenant); err != nil {
		return ModuleNotifyUserResult{}, err
	}
	if _, err := notificationCategoryIsMandatory(NotificationCategory(category)); err != nil {
		return ModuleNotifyUserResult{}, status.Errorf(codes.InvalidArgument, "invalid notification category %q", category)
	}
	notification, err := s.CreateNotification(ctx, CreateNotificationInput{
		UserID:         userID,
		OrgID:          tenant,
		Title:          title,
		Body:           body,
		Type:           notificationType,
		ActionURL:      actionURL,
		Category:       NotificationCategory(category),
		IdempotencyKey: idempotencyKey,
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
		return "", status.Error(codes.InvalidArgument, err.Error())
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
	return s.GetApprovalRequest(ctx, tenant, id)
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
	return s.CancelApprovalRequest(ctx, tenant, id, reason)
}

// enqueueApprovalResume appends the resume outbox job for an approved request.
// It runs inside the Decide transaction (moduleProducer is request-scoped), so
// the approved transition and the resume job commit atomically. The approval id
// is the idempotency key: a retried decision resolves to the same durable job
// rather than resuming the gated action twice. No-op when no resume queue is
// declared or the producer is not wired.
func (s *Service) enqueueApprovalResume(ctx context.Context, req *ApprovalRequest) error {
	if req.ResumeRef.Queue == "" || s.moduleProducer == nil {
		return nil
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
	if _, ok := LookupAuditEvent(EventType(eventType)); !ok {
		return status.Errorf(codes.InvalidArgument, "audit event type %q is not registered", eventType)
	}
	payload := map[string]any{"solution": solution}
	for k, v := range fields.AsMap() {
		payload[k] = v
	}
	s.emit(ctx, actor, "agent", EventType(eventType), solution, entryID, tenant, payload)
	return nil
}
