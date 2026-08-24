package business_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// approvalEngineStore is an in-memory business.ApprovalStore for testing the
// engine (quorum arithmetic, self-block, deny short-circuit, terminal guards,
// sweep) without a database. It faithfully enforces the two invariants the SQL
// schema enforces in production: UNIQUE (request_id, decider) on decisions, and
// distinct-approve counting toward quorum.
type approvalEngineStore struct {
	business.Store
	requests  map[string]*business.ApprovalRequest
	decisions []*business.ApprovalDecision
}

func newApprovalEngineStore() *approvalEngineStore {
	return &approvalEngineStore{requests: map[string]*business.ApprovalRequest{}}
}

func (f *approvalEngineStore) As(identity business.Identity) business.Scoped {
	return waitlistScopedFake{identity: identity}
}

func (f *approvalEngineStore) InsertApprovalRequest(_ context.Context, r *business.ApprovalRequest) error {
	cp := *r
	f.requests[r.ID] = &cp
	return nil
}

func (f *approvalEngineStore) get(id string) (*business.ApprovalRequest, error) {
	r, ok := f.requests[id]
	if !ok {
		return nil, business.NewStoreError(errors.New("not found"), business.ErrTypeNotFound)
	}
	return r, nil
}

func (f *approvalEngineStore) GetApprovalRequest(_ context.Context, id, _ string) (*business.ApprovalRequest, error) {
	r, err := f.get(id)
	if err != nil {
		return nil, err
	}
	cp := *r
	return &cp, nil
}

func (f *approvalEngineStore) LockApprovalRequest(_ context.Context, id, _ string) (*business.ApprovalRequest, error) {
	// Return the live pointer: the engine reads it, then mutates via
	// UpdateApprovalState. Tests are single-threaded, so the FOR UPDATE
	// serialization the real store provides is not exercised here (that is the
	// integration test's job).
	return f.get(id)
}

func (f *approvalEngineStore) ListApprovalRequests(_ context.Context, orgID, state string, _ int32, _ string) ([]*business.ApprovalRequest, string, error) {
	var out []*business.ApprovalRequest
	for _, r := range f.requests {
		if r.OrgID != orgID {
			continue
		}
		if state != "" && string(r.State) != state {
			continue
		}
		cp := *r
		out = append(out, &cp)
	}
	return out, "", nil
}

func (f *approvalEngineStore) InsertDecision(_ context.Context, d *business.ApprovalDecision) error {
	for _, e := range f.decisions {
		if e.RequestID == d.RequestID && e.Decider == d.Decider {
			return business.NewStoreError(errors.New("already decided"), business.ErrTypeConflict)
		}
	}
	cp := *d
	f.decisions = append(f.decisions, &cp)
	return nil
}

func (f *approvalEngineStore) CountApprovals(_ context.Context, requestID, _ string) (int, error) {
	seen := map[string]bool{}
	for _, d := range f.decisions {
		if d.RequestID == requestID && d.Decision == business.DecisionApprove {
			seen[d.Decider] = true
		}
	}
	return len(seen), nil
}

func (f *approvalEngineStore) UpdateApprovalState(_ context.Context, id, _ string, to business.ApprovalState, reason string, setDecided bool) error {
	r, err := f.get(id)
	if err != nil {
		return err
	}
	r.State = to
	r.DecisionReason = reason
	if setDecided {
		now := time.Now()
		r.DecidedAt = &now
	}
	return nil
}

// var _ ensures the fake stays in lock-step with the interface.
var _ business.ApprovalStore = (*approvalEngineStore)(nil)

func newApprovalService(t *testing.T) (*business.Service, *approvalEngineStore) {
	t.Helper()
	store := newApprovalEngineStore()
	svc, err := business.NewService(store)
	require.NoError(t, err)
	return svc, store
}

func mustCreate(t *testing.T, svc *business.Service, in *business.CreateApprovalRequestInput) string {
	t.Helper()
	id, err := svc.CreateApprovalRequest(context.Background(), in)
	require.NoError(t, err)
	require.NotEmpty(t, id)
	return id
}

func storeErrType(t *testing.T, err error) business.StoreErrorType {
	t.Helper()
	var se *business.StoreError
	require.True(t, errors.As(err, &se), "expected a *business.StoreError, got %v", err)
	return se.StoreErrorType
}

func TestApprovalEngine_SingleApprover_Approves(t *testing.T) {
	svc, _ := newApprovalService(t)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "entitlement_override", Action: "grant", RequestedBy: "user-1",
	})

	out, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.NoError(t, err)
	require.True(t, out.Approved)
	require.Equal(t, business.ApprovalApproved, out.State)
}

func TestApprovalEngine_Quorum_TwoOfN(t *testing.T) {
	svc, _ := newApprovalService(t)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1", Quorum: 2,
	})

	// First distinct approve: still pending, 1/2.
	out, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.NoError(t, err)
	require.False(t, out.Approved)
	require.Equal(t, business.ApprovalPending, out.State)
	require.Equal(t, 1, out.Approvals)
	require.Equal(t, 2, out.Quorum)

	// Second distinct approve: quorum reached.
	out, err = svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-2", Decision: business.DecisionApprove,
	})
	require.NoError(t, err)
	require.True(t, out.Approved)
	require.Equal(t, business.ApprovalApproved, out.State)
}

func TestApprovalEngine_DoubleVote_Rejected(t *testing.T) {
	svc, _ := newApprovalService(t)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1", Quorum: 2,
	})

	_, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.NoError(t, err)

	// Same approver again — the UNIQUE (request_id, decider) constraint (modeled
	// by the fake) rejects the double-vote, so quorum can't be gamed by one party.
	_, err = svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.Error(t, err)
	require.Equal(t, business.ErrTypeConflict, storeErrType(t, err))
}

func TestApprovalEngine_Deny_ShortCircuits(t *testing.T) {
	svc, _ := newApprovalService(t)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1", Quorum: 3,
	})

	out, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionDeny, Reason: "too risky",
	})
	require.NoError(t, err)
	require.False(t, out.Approved)
	require.Equal(t, business.ApprovalDenied, out.State)
}

func TestApprovalEngine_SelfApproval_BlockedByDefault(t *testing.T) {
	svc, _ := newApprovalService(t)
	// No AllowSelf in the policy: separation of duties is the default, so the
	// requester cannot approve their own request.
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "spend_override", Action: "grant", RequestedBy: "user-1",
	})

	_, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "user-1", Decision: business.DecisionApprove,
	})
	require.Error(t, err)
	require.Equal(t, business.ErrTypePermission, storeErrType(t, err))
}

func TestApprovalEngine_SelfApproval_AllowedWhenOptedIn(t *testing.T) {
	svc, _ := newApprovalService(t)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "spend_override", Action: "grant", RequestedBy: "user-1",
		Policy: business.ApprovalPolicy{AllowSelf: true},
	})

	out, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "user-1", Decision: business.DecisionApprove,
	})
	require.NoError(t, err)
	require.True(t, out.Approved)
}

func TestApprovalEngine_ApproverSet(t *testing.T) {
	svc, _ := newApprovalService(t)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		Policy: business.ApprovalPolicy{ApproverSet: []string{"approver-1"}},
	})

	// Not in the set → rejected.
	_, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "intruder", Decision: business.DecisionApprove,
	})
	require.Error(t, err)
	require.Equal(t, business.ErrTypePermission, storeErrType(t, err))

	// In the set → approved.
	out, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.NoError(t, err)
	require.True(t, out.Approved)
}

func TestApprovalEngine_TerminalGuard(t *testing.T) {
	svc, _ := newApprovalService(t)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
	})
	_, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.NoError(t, err)

	// Deciding an already-approved request is a conflict.
	_, err = svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-2", Decision: business.DecisionApprove,
	})
	require.Error(t, err)
	require.Equal(t, business.ErrTypeConflict, storeErrType(t, err))
}

func TestApprovalEngine_Cancel(t *testing.T) {
	svc, _ := newApprovalService(t)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
	})

	require.NoError(t, svc.CancelApprovalRequest(context.Background(), "org-a", id, "withdrawn"))

	// Cancelled is terminal — no further decisions.
	_, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.Error(t, err)
	require.Equal(t, business.ErrTypeConflict, storeErrType(t, err))
}

func TestApprovalEngine_Sweep_Expires(t *testing.T) {
	svc, _ := newApprovalService(t)
	past := time.Now().Add(-time.Minute)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		ExpiresAt: &past,
	})

	state, err := svc.SweepApprovalRequest(context.Background(), "org-a", id, time.Now())
	require.NoError(t, err)
	require.Equal(t, business.ApprovalExpired, state)
}

func countEvents(entries []business.AuditEntry, typ business.EventType) int {
	n := 0
	for _, e := range entries {
		if e.EventType == typ {
			n++
		}
	}
	return n
}

func TestApprovalEngine_Sweep_NoDuplicateTimeoutOnRerun(t *testing.T) {
	svc, _ := newApprovalService(t)
	spy := &recordingAuditEmitter{}
	svc.SetAuditEmitter(spy)
	past := time.Now().Add(-time.Minute)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		ExpiresAt: &past,
	})

	// First sweep transitions pending → expired and emits exactly one timeout.
	state, err := svc.SweepApprovalRequest(context.Background(), "org-a", id, time.Now())
	require.NoError(t, err)
	require.Equal(t, business.ApprovalExpired, state)

	// A re-run (the sweeper is an at-least-once delayed job) observes the already-
	// expired request and must NOT re-emit approval.timeout.
	state, err = svc.SweepApprovalRequest(context.Background(), "org-a", id, time.Now())
	require.NoError(t, err)
	require.Equal(t, business.ApprovalExpired, state)

	require.Equal(t, 1, countEvents(spy.entries, business.EventApprovalTimeout),
		"approval.timeout must be emitted once, not on every re-sweep of an expired request")
}

func TestApprovalEngine_Sweep_NoDuplicateEscalatedOnRerun(t *testing.T) {
	svc, _ := newApprovalService(t)
	spy := &recordingAuditEmitter{}
	svc.SetAuditEmitter(spy)
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		EscalateAt: &past, ExpiresAt: &future,
	})

	state, err := svc.SweepApprovalRequest(context.Background(), "org-a", id, time.Now())
	require.NoError(t, err)
	require.Equal(t, business.ApprovalEscalated, state)

	// Re-sweep while still escalated (not yet past expires_at): no re-emit.
	state, err = svc.SweepApprovalRequest(context.Background(), "org-a", id, time.Now())
	require.NoError(t, err)
	require.Equal(t, business.ApprovalEscalated, state)

	require.Equal(t, 1, countEvents(spy.entries, business.EventApprovalEscalated),
		"approval.escalated must be emitted once, not on every re-sweep of an escalated request")
}

func TestApprovalEngine_Cancel_EmitsCancelled(t *testing.T) {
	svc, _ := newApprovalService(t)
	spy := &recordingAuditEmitter{}
	svc.SetAuditEmitter(spy)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
	})

	require.NoError(t, svc.CancelApprovalRequest(context.Background(), "org-a", id, "withdrawn"))
	require.Equal(t, 1, countEvents(spy.entries, business.EventApprovalCancelled),
		"cancellation must be audited")
}

func TestApprovalEngine_Sweep_Escalates_StaysDecidable(t *testing.T) {
	svc, _ := newApprovalService(t)
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		EscalateAt: &past, ExpiresAt: &future,
	})

	state, err := svc.SweepApprovalRequest(context.Background(), "org-a", id, time.Now())
	require.NoError(t, err)
	require.Equal(t, business.ApprovalEscalated, state)

	// Escalated is non-terminal: a decision still lands and can approve.
	out, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.NoError(t, err)
	require.True(t, out.Approved)
}

func TestApprovalEngine_Decide_RejectsExpired(t *testing.T) {
	svc, _ := newApprovalService(t)
	past := time.Now().Add(-time.Minute)
	// The request is still pending in the store (the sweeper is unwired), yet its
	// decision window has closed: Decide must reject rather than reach quorum.
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		ExpiresAt: &past,
	})

	_, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.Error(t, err)
	require.Equal(t, business.ErrTypeConflict, storeErrType(t, err))
}

func TestApprovalEngine_Create_RejectsQuorumAboveApproverSet(t *testing.T) {
	svc, _ := newApprovalService(t)
	_, err := svc.CreateApprovalRequest(context.Background(), &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		Quorum: 3,
		Policy: business.ApprovalPolicy{ApproverSet: []string{"approver-1", "approver-2"}},
	})
	require.Error(t, err)
}

func TestApprovalEngine_Create_RejectsRequesterOnlyApproverSet(t *testing.T) {
	svc, _ := newApprovalService(t)
	// Quorum 1, but the only pinned approver is the requester and AllowSelf is
	// off, so no eligible decider exists — len()==1 would have passed, distinct-
	// eligible count is 0. Must be rejected as a permanently-stuck gate.
	_, err := svc.CreateApprovalRequest(context.Background(), &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		Policy: business.ApprovalPolicy{ApproverSet: []string{"user-1"}},
	})
	require.Error(t, err)
}

func TestApprovalEngine_Create_RejectsDuplicateApproverSet(t *testing.T) {
	svc, _ := newApprovalService(t)
	// len(["a","a"])==2 satisfies a naive size check, but only one DISTINCT
	// decider exists and can vote once, so quorum 2 is unreachable.
	_, err := svc.CreateApprovalRequest(context.Background(), &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		Quorum: 2,
		Policy: business.ApprovalPolicy{ApproverSet: []string{"approver-1", "approver-1"}},
	})
	require.Error(t, err)
}

func TestApprovalEngine_Create_AllowsRequesterInSetWhenAllowSelf(t *testing.T) {
	svc, _ := newApprovalService(t)
	// With AllowSelf the requester IS an eligible decider, so a requester-only
	// set of size 1 satisfies quorum 1 and the request is both creatable and
	// approvable by the requester.
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		Policy: business.ApprovalPolicy{ApproverSet: []string{"user-1"}, AllowSelf: true},
	})
	out, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "user-1", Decision: business.DecisionApprove,
	})
	require.NoError(t, err)
	require.True(t, out.Approved)
}

func TestApprovalEngine_Decide_AllowsBeforeExpiry(t *testing.T) {
	svc, _ := newApprovalService(t)
	future := time.Now().Add(time.Hour)
	// ExpiresAt is set but in the future: the decision window is open, so Decide
	// must proceed. Covers the near side of the boundary that RejectsExpired does
	// not — a flipped predicate that rejected not-yet-expired requests would fail
	// here. The one-hour margin keeps it non-flaky against the wall clock.
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
		ExpiresAt: &future,
	})

	out, err := svc.Decide(context.Background(), "org-a", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.NoError(t, err)
	require.True(t, out.Approved)
}

func TestApprovalEngine_Decide_RejectsEmptyOrg(t *testing.T) {
	svc, _ := newApprovalService(t)
	id := mustCreate(t, svc, &business.CreateApprovalRequestInput{
		OrgID: "org-a", Resource: "role", Action: "grant", RequestedBy: "user-1",
	})

	_, err := svc.Decide(context.Background(), "", id, business.DecideInput{
		Decider: "approver-1", Decision: business.DecisionApprove,
	})
	require.Error(t, err)
}
