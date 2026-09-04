// Package business — saas/approvals/v1: the general Approval primitive
// (issue #232).
//
// An approval request gates an action behind "pause until an authorized
// decision (or timeout / escalation) arrives, then resume". This file is the
// engine: it owns the quorum arithmetic, the self-approval / approver-set
// checks, the terminal-state guards, and the audit emission. Persistence lives
// in the infra layer (postgres_approvals.go), behind ApprovalStore.
//
// The primitive generalizes delegation_grants (a single-decider, org-admin-
// gated approval) into an N-of-M, permission-gated one. Quorum is a count of
// DISTINCT approve decisions; the append-only approval_decisions table plus a
// UNIQUE (request_id, decider) constraint make double-voting impossible in SQL.
//
// Decide runs under a single org tx that locks the request head row FOR UPDATE,
// so concurrent deciders on the same request serialize and the Nth approve
// transitions the request exactly once. Resuming the gated action (the outbox
// job) and wiring the timeout/escalation sweeper to a delayed job are the next
// phases (see APPROVALS_DESIGN.md §6, §11); the state machine and its arithmetic
// are complete and tested here.
package business

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"
)

// =====================================================================
// Types
// =====================================================================

// ApprovalState mirrors the SQL CHECK enum on approval_requests.state.
// Renaming a value requires the migration and this file to change in
// lock-step.
type ApprovalState string

const (
	ApprovalPending   ApprovalState = "pending"
	ApprovalApproved  ApprovalState = "approved"
	ApprovalDenied    ApprovalState = "denied"
	ApprovalExpired   ApprovalState = "expired"
	ApprovalEscalated ApprovalState = "escalated"
	ApprovalCancelled ApprovalState = "cancelled"
)

// ApprovalDecisionKind mirrors approval_decisions.decision.
type ApprovalDecisionKind string

const (
	DecisionApprove ApprovalDecisionKind = "approve"
	DecisionDeny    ApprovalDecisionKind = "deny"
)

// ApprovalPolicy is the non-quorum part of the request policy, persisted as the
// policy JSONB. Quorum itself is a first-class column (it is what SQL compares).
type ApprovalPolicy struct {
	// ApproverSet, when non-empty, restricts who may decide to this explicit
	// list of actor ids. Empty = anyone the handler admitted (i.e. anyone
	// holding approvals:decide, enforced at the RPC boundary).
	ApproverSet []string `json:"approver_set,omitempty"`
	// AllowSelf permits the requester to decide their own request. It defaults
	// off (zero value), so separation of duties is the default: the requester
	// cannot self-approve unless a flow explicitly opts in. A malformed policy
	// JSONB that fails to unmarshal therefore fails closed on self-approval.
	AllowSelf bool `json:"allow_self,omitempty"`
	// DecidePermission records the RBAC permission the handler gate required, for
	// audit/traceability. Not enforced here (the handler is the enforcer).
	DecidePermission string `json:"decide_permission,omitempty"`
}

// ResumeRef is the outbox target persisted as the resume_ref JSONB: how the
// gated action resumes once approved. Mode selects borrow-capability (mint a
// scoped grant, requester replays) vs system-outbox (the system actor performs
// the mutation) — see APPROVALS_DESIGN.md §7.
type ResumeRef struct {
	Mode    string         `json:"mode,omitempty"`
	JobKind string         `json:"job_kind,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
	// Queue and Topic address the durable outbox job the primitive enqueues when
	// the request is approved. A requesting module consumes it on its own queue
	// via the module-facing ClaimJobs (issue #463). When Queue is empty no resume
	// job is enqueued — the flow observes the terminal state some other way.
	Queue string `json:"queue,omitempty"`
	Topic string `json:"topic,omitempty"`
}

// ApprovalRequest mirrors the approval_requests row 1:1.
type ApprovalRequest struct {
	ID             string
	OrgID          string
	Resource       string
	Action         string
	Subject        map[string]any
	RequestedBy    string
	Quorum         int
	Policy         ApprovalPolicy
	State          ApprovalState
	ResumeRef      ResumeRef
	ExpiresAt      *time.Time
	EscalateAt     *time.Time
	DecisionReason string
	DecidedAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// IsTerminal reports whether the request has reached a final state (no further
// decisions accepted).
func (r *ApprovalRequest) IsTerminal() bool {
	switch r.State {
	case ApprovalApproved, ApprovalDenied, ApprovalExpired, ApprovalCancelled:
		return true
	}
	return false
}

// IsDecidable reports whether a decision may still be recorded. Escalated is
// non-terminal — escalation widens the approver set but the request stays open.
func (r *ApprovalRequest) IsDecidable() bool {
	return r.State == ApprovalPending || r.State == ApprovalEscalated
}

// ApprovalDecision mirrors an approval_decisions row.
type ApprovalDecision struct {
	ID                string
	RequestID         string
	OrgID             string
	Decider           string
	Decision          ApprovalDecisionKind
	Reason            string
	DelegationGrantID string // optional; links to the actor chain when borrow-capability minted a grant
	DecidedAt         time.Time
}

// =====================================================================
// Inputs / outputs
// =====================================================================

// CreateApprovalRequestInput is the engine-layer input; RPC adapters convert
// from the gRPC type to this.
type CreateApprovalRequestInput struct {
	OrgID       string
	Resource    string
	Action      string
	Subject     map[string]any
	RequestedBy string
	Quorum      int
	Policy      ApprovalPolicy
	ResumeRef   ResumeRef
	ExpiresAt   *time.Time
	EscalateAt  *time.Time
}

func (in *CreateApprovalRequestInput) validate() error {
	if in == nil {
		return errors.New("approval: nil input")
	}
	if in.OrgID == "" {
		return errors.New("approval: org_id required")
	}
	if in.Resource == "" || in.Action == "" {
		return errors.New("approval: resource and action required")
	}
	if in.RequestedBy == "" {
		return errors.New("approval: requested_by required")
	}
	if in.Quorum < 0 {
		return errors.New("approval: quorum must be >= 0")
	}
	// A pinned approver set must hold enough DISTINCT, eligible deciders to reach
	// quorum, or the request can never be approved and would sit pending until it
	// expires — reject that at creation rather than minting a permanently-stuck
	// gate. Counting len() is not enough: duplicate entries do not add a decider
	// (UNIQUE(request_id, decider) caps each at one approve), and the requester is
	// not an eligible decider unless AllowSelf is set. Both are excluded here so a
	// requester-only set or a padded-with-duplicates set is rejected up front.
	if len(in.Policy.ApproverSet) > 0 {
		eligible := make(map[string]struct{}, len(in.Policy.ApproverSet))
		for _, a := range in.Policy.ApproverSet {
			if !in.Policy.AllowSelf && a == in.RequestedBy {
				continue
			}
			eligible[a] = struct{}{}
		}
		if q := resolvedQuorum(in.Quorum); q > len(eligible) {
			return fmt.Errorf("approval: quorum %d exceeds %d eligible distinct approver(s) in the approver set", q, len(eligible))
		}
	}
	return nil
}

// DecideInput records one approver's decision.
type DecideInput struct {
	Decider           string
	Decision          ApprovalDecisionKind
	Reason            string
	DelegationGrantID string // optional actor-chain link
}

// DecideOutcome reports the request state after a decision, so callers know
// whether the gated action should now resume.
type DecideOutcome struct {
	State ApprovalState
	// Approved is true iff this decision reached quorum and flipped the request
	// to approved — the signal to enqueue the resume job.
	Approved bool
	// Approvals / Quorum expose progress while still pending.
	Approvals int
	Quorum    int
}

// =====================================================================
// Store surface
// =====================================================================

// ApprovalStore is the persistence surface this file calls into. The concrete
// implementation is *infra.PostgresStore (postgres_approvals.go); the interface
// keeps the engine testable without a live DB.
type ApprovalStore interface {
	// InsertApprovalRequest persists a new pending request (id pre-assigned).
	InsertApprovalRequest(ctx context.Context, r *ApprovalRequest) error

	// GetApprovalRequest returns one request by id. ErrTypeNotFound when absent
	// or in another org (RLS blocks cross-org reads upstream).
	GetApprovalRequest(ctx context.Context, id, orgID string) (*ApprovalRequest, error)

	// LockApprovalRequest is GetApprovalRequest with SELECT ... FOR UPDATE, so
	// concurrent deciders on the same request serialize on the head row.
	LockApprovalRequest(ctx context.Context, id, orgID string) (*ApprovalRequest, error)

	// ListApprovalRequests returns paginated requests for an org, newest first.
	// state == "" lists all states; otherwise it filters to that state.
	ListApprovalRequests(ctx context.Context, orgID, state string, pageSize int32, pageToken string) ([]*ApprovalRequest, string, error)

	// InsertDecision appends a decision. The UNIQUE (request_id, decider)
	// constraint surfaces as ErrTypeConflict on a double-vote.
	InsertDecision(ctx context.Context, d *ApprovalDecision) error

	// CountApprovals returns the number of distinct approve decisions.
	CountApprovals(ctx context.Context, requestID, orgID string) (int, error)

	// UpdateApprovalState transitions the head row. Callers hold the FOR UPDATE
	// lock, so no WHERE-state guard is needed for correctness. setDecided stamps
	// decided_at (used for approved/denied, not for escalated).
	UpdateApprovalState(ctx context.Context, id, orgID string, to ApprovalState, reason string, setDecided bool) error
}

// approvalStore returns the store half of the Service that implements
// ApprovalStore. Mirrors principalStore(): the concrete *PostgresStore
// implements it (compile-time asserted in postgres_approvals.go).
func (s *Service) approvalStore() ApprovalStore {
	if as, ok := s.store.(ApprovalStore); ok {
		return as
	}
	panic("Service.store does not implement ApprovalStore; see postgres_approvals.go")
}

// =====================================================================
// Service methods
// =====================================================================

// CreateApprovalRequest opens a pending request gating an action. Quorum
// defaults to 1 (single approver) when unset. Emits approval.asked.
func (s *Service) CreateApprovalRequest(ctx context.Context, in *CreateApprovalRequestInput) (string, error) {
	w := wool.Get(ctx).In("CreateApprovalRequest",
		wool.Field("resource", in.Resource),
		wool.Field("action", in.Action))
	if err := in.validate(); err != nil {
		return "", w.Wrapf(err, "validate")
	}
	quorum := resolvedQuorum(in.Quorum)

	r := &ApprovalRequest{
		ID:          NewIDString(),
		OrgID:       in.OrgID,
		Resource:    in.Resource,
		Action:      in.Action,
		Subject:     in.Subject,
		RequestedBy: in.RequestedBy,
		Quorum:      quorum,
		Policy:      in.Policy,
		State:       ApprovalPending,
		ResumeRef:   in.ResumeRef,
		ExpiresAt:   in.ExpiresAt,
		EscalateAt:  in.EscalateAt,
	}
	// The audit event is written in the same tx as the insert (emitTx), so the
	// request and its approval.asked record commit atomically — a crash can't
	// leave a gated request with no audit trail.
	if err := s.store.As(Identity{OrgID: in.OrgID}).Within(ctx, func(ctx context.Context) error {
		if err := s.approvalStore().InsertApprovalRequest(ctx, r); err != nil {
			return err
		}
		return s.emitTx(ctx, in.RequestedBy, "user", EventApprovalAsked, "approval_request", r.ID, in.OrgID,
			map[string]any{"resource": in.Resource, "action": in.Action})
	}); err != nil {
		return "", w.Wrapf(err, "insert approval request")
	}
	return r.ID, nil
}

// Decide records one approver's decision and, when the Nth distinct approve
// lands, transitions the request to approved. The whole read-decide-transition
// runs under one org tx that locks the request FOR UPDATE, so quorum is exact
// under concurrency. A deny transitions straight to denied. Emits
// approval.approved / approval.denied on a terminal transition.
//
// SECURITY INVARIANT: in.Decider is trusted verbatim — distinct-approver quorum,
// the approver-set check, and the self-approval block are only as sound as it is.
// The caller MUST set Decider from the authenticated actor identity, never from
// client input. A caller that forwards a client-supplied decider lets one party
// forge N distinct approvers to reach quorum, or claim someone else's id to
// approve their own request. This is the RPC/handler layer's responsibility (the
// engine cannot authenticate).
func (s *Service) Decide(ctx context.Context, orgID, id string, in DecideInput) (DecideOutcome, error) {
	w := wool.Get(ctx).In("DecideApproval",
		wool.Field("approval_id", id),
		wool.Field("decision", string(in.Decision)))
	if orgID == "" {
		return DecideOutcome{}, w.NewError("org_id required")
	}
	if in.Decider == "" {
		return DecideOutcome{}, w.NewError("decider required")
	}
	if in.Decision != DecisionApprove && in.Decision != DecisionDeny {
		return DecideOutcome{}, w.NewError("decision must be approve or deny")
	}

	var outcome DecideOutcome
	err := s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		req, err := s.approvalStore().LockApprovalRequest(ctx, id, orgID)
		if err != nil {
			return err
		}
		if !req.IsDecidable() {
			return NewStoreError(
				fmt.Errorf("approval %s is not decidable (state=%s)", id, req.State),
				ErrTypeConflict,
			)
		}
		// Enforce the decision window here, not only in the sweeper: the sweeper
		// job is not yet wired, so without this an approver could reach quorum on
		// a request long past its expires_at. Same predicate the sweeper uses.
		if req.ExpiresAt != nil && !time.Now().Before(*req.ExpiresAt) {
			return NewStoreError(
				fmt.Errorf("approval %s decision window has expired", id),
				ErrTypeConflict,
			)
		}
		if !req.Policy.AllowSelf && in.Decider == req.RequestedBy {
			return NewStoreError(
				fmt.Errorf("requester %s cannot decide their own approval", in.Decider),
				ErrTypePermission,
			)
		}
		if len(req.Policy.ApproverSet) > 0 && !containsString(req.Policy.ApproverSet, in.Decider) {
			return NewStoreError(
				fmt.Errorf("decider %s is not in the approver set", in.Decider),
				ErrTypePermission,
			)
		}

		if err := s.approvalStore().InsertDecision(ctx, &ApprovalDecision{
			ID:                NewIDString(),
			RequestID:         id,
			OrgID:             orgID,
			Decider:           in.Decider,
			Decision:          in.Decision,
			Reason:            in.Reason,
			DelegationGrantID: in.DelegationGrantID,
		}); err != nil {
			return err // ErrTypeConflict on a double-vote
		}

		outcome.Quorum = req.Quorum
		// Audit is emitted in this same tx (emitTx) on a terminal transition, so
		// the state change and its approval.approved / approval.denied record are
		// atomic.
		if in.Decision == DecisionDeny {
			if err := s.approvalStore().UpdateApprovalState(ctx, id, orgID, ApprovalDenied, in.Reason, true); err != nil {
				return err
			}
			outcome.State = ApprovalDenied
			return s.emitTx(ctx, in.Decider, "user", EventApprovalDenied, "approval_request", id, orgID, nil)
		}

		count, err := s.approvalStore().CountApprovals(ctx, id, orgID)
		if err != nil {
			return err
		}
		outcome.Approvals = count
		if count >= req.Quorum {
			if err := s.approvalStore().UpdateApprovalState(ctx, id, orgID, ApprovalApproved, "", true); err != nil {
				return err
			}
			outcome.State = ApprovalApproved
			outcome.Approved = true
			// Enqueue the resume outbox job in this same tx, so the gated action
			// can never be resumed twice or lost: the approved transition and its
			// resume job commit together or not at all (APPROVALS_DESIGN.md §6).
			if err := s.enqueueApprovalResume(ctx, req); err != nil {
				return err
			}
			return s.emitTx(ctx, in.Decider, "user", EventApprovalApproved, "approval_request", id, orgID,
				map[string]any{"resource": req.Resource, "action": req.Action})
		}
		outcome.State = req.State
		return nil
	})
	if err != nil {
		return DecideOutcome{}, err
	}
	return outcome, nil
}

// CancelApprovalRequest withdraws a still-open request. No-op error
// (ErrTypeConflict) if already terminal.
func (s *Service) CancelApprovalRequest(ctx context.Context, orgID, id, reason string) error {
	w := wool.Get(ctx).In("CancelApprovalRequest", wool.Field("approval_id", id))
	if orgID == "" {
		return w.NewError("org_id required")
	}
	return s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		req, err := s.approvalStore().LockApprovalRequest(ctx, id, orgID)
		if err != nil {
			return err
		}
		if !req.IsDecidable() {
			return NewStoreError(
				fmt.Errorf("approval %s is not cancellable (state=%s)", id, req.State),
				ErrTypeConflict,
			)
		}
		// setDecided is false: a cancellation is a withdrawal, not a decision, so
		// decided_at stays null (updated_at records when it went terminal).
		if err := s.approvalStore().UpdateApprovalState(ctx, id, orgID, ApprovalCancelled, reason, false); err != nil {
			return w.Wrapf(err, "cancel approval")
		}
		// Audit in the same tx so the cancellation and its record are atomic.
		return s.emitTx(ctx, "system", "system", EventApprovalCancelled, "approval_request", id, orgID,
			map[string]any{"reason": reason})
	})
}

// GetApprovalRequest returns one request by id (org-scoped).
func (s *Service) GetApprovalRequest(ctx context.Context, orgID, id string) (*ApprovalRequest, error) {
	var out *ApprovalRequest
	err := s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		r, e := s.approvalStore().GetApprovalRequest(ctx, id, orgID)
		out = r
		return e
	})
	return out, err
}

// ListApprovalRequests returns paginated requests for an org. state == "" lists
// every state; otherwise it filters (e.g. "pending" for the approval queue).
func (s *Service) ListApprovalRequests(ctx context.Context, orgID, state string, pageSize int32, pageToken string) ([]*ApprovalRequest, string, error) {
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	var out []*ApprovalRequest
	var next string
	err := s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		rs, n, e := s.approvalStore().ListApprovalRequests(ctx, orgID, state, pageSize, pageToken)
		out, next = rs, n
		return e
	})
	return out, next, err
}

// SweepApprovalRequest applies the decision-window deadlines to one request:
// past expires_at → expired (emits approval.timeout); else past escalate_at
// while still pending → escalated (emits approval.escalated). Idempotent: a
// terminal request is a no-op. This is the pure state logic; wiring it to a
// delayed sweeper job is the jobs-integration phase (APPROVALS_DESIGN.md §6).
func (s *Service) SweepApprovalRequest(ctx context.Context, orgID, id string, now time.Time) (ApprovalState, error) {
	if orgID == "" {
		return "", wool.Get(ctx).In("SweepApprovalRequest").NewError("org_id required")
	}
	var newState ApprovalState
	// The audit event is emitted in the SAME tx as the transition, inside the
	// branch that performs it — never after. So a re-run of this at-least-once
	// delayed job (escalation re-enqueues a second sweep, and job retries re-run
	// the handler) that observes an already-expired or already-escalated request
	// falls through both branches and emits nothing: no duplicate
	// approval.timeout / approval.escalated, and the record commits atomically.
	err := s.store.As(Identity{OrgID: orgID}).Within(ctx, func(ctx context.Context) error {
		req, err := s.approvalStore().LockApprovalRequest(ctx, id, orgID)
		if err != nil {
			return err
		}
		newState = req.State
		if !req.IsDecidable() {
			return nil // terminal; nothing to sweep
		}
		if req.ExpiresAt != nil && !now.Before(*req.ExpiresAt) {
			if err := s.approvalStore().UpdateApprovalState(ctx, id, orgID, ApprovalExpired, "expired: no quorum before deadline", false); err != nil {
				return err
			}
			newState = ApprovalExpired
			return s.emitTx(ctx, "system", "system", EventApprovalTimeout, "approval_request", id, orgID, nil)
		}
		if req.State == ApprovalPending && req.EscalateAt != nil && !now.Before(*req.EscalateAt) {
			if err := s.approvalStore().UpdateApprovalState(ctx, id, orgID, ApprovalEscalated, "escalated: no quorum before escalate_at", false); err != nil {
				return err
			}
			newState = ApprovalEscalated
			return s.emitTx(ctx, "system", "system", EventApprovalEscalated, "approval_request", id, orgID, nil)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return newState, nil
}

// resolvedQuorum applies the "0 means the single-approver default" rule in one
// place, so validate() and CreateApprovalRequest can never disagree on the
// effective quorum.
func resolvedQuorum(q int) int {
	if q == 0 {
		return 1
	}
	return q
}

func containsString(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
