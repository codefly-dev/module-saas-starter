package infra_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// mutationRejected reports whether err is the append-only trigger's rejection or
// a role privilege denial — the two layers that keep approval_decisions immutable.
func mutationRejected(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "append-only") || strings.Contains(msg, "permission denied")
}

// These integration tests prove the saas/approvals/v1 schema (migration 101):
// tenant RLS isolation, the append-only trigger on approval_decisions, the
// UNIQUE (request_id, decider) double-vote guard, and distinct-approve quorum
// counting — all against a live Postgres, using the shared package testStore.

func seedApprovalRequest(t *testing.T, orgID string, quorum int) string {
	t.Helper()
	id := business.NewIDString()
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		return testStore.InsertApprovalRequest(ctx, &business.ApprovalRequest{
			ID:          id,
			OrgID:       orgID,
			Resource:    "entitlement_override",
			Action:      "grant",
			RequestedBy: "user-requester",
			Quorum:      quorum,
			State:       business.ApprovalPending,
		})
	}))
	return id
}

func TestApprovals_RLS_CrossTenantBlocked(t *testing.T) {
	owner := seedUser(t)
	orgA := seedOrg(t, owner)
	orgB := seedOrg(t, owner)

	reqID := seedApprovalRequest(t, orgA, 1)

	// Acting as orgB, try to read orgA's row (even naming orgA as the filter):
	// RLS keys on app.current_org_id = orgB, so the row is invisible → NotFound.
	var miss *business.ApprovalRequest
	err := testStore.As(business.Identity{OrgID: orgB}).Within(testCtx, func(ctx context.Context) error {
		var e error
		miss, e = testStore.GetApprovalRequest(ctx, reqID, orgA)
		return e
	})
	require.Error(t, err)
	require.Equal(t, business.ErrTypeNotFound, storeErrTypeInfra(t, err))
	require.Nil(t, miss)

	// Control: acting as orgA, the same row is visible.
	var hit *business.ApprovalRequest
	require.NoError(t, testStore.As(business.Identity{OrgID: orgA}).Within(testCtx, func(ctx context.Context) error {
		var e error
		hit, e = testStore.GetApprovalRequest(ctx, reqID, orgA)
		return e
	}))
	require.NotNil(t, hit)
	require.Equal(t, business.ApprovalPending, hit.State)
}

func TestApprovals_Decisions_AppendOnly(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	reqID := seedApprovalRequest(t, orgID, 1)

	decID := business.NewIDString()
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		return testStore.InsertDecision(ctx, &business.ApprovalDecision{
			ID: decID, RequestID: reqID, OrgID: orgID, Decider: "approver-1", Decision: business.DecisionApprove,
		})
	}))

	// A decision is immutable on the tenant/control paths, enforced at two
	// layers: the roles hold no UPDATE/DELETE grant (permission denied), and the
	// immutable trigger rejects the verb (append-only) if a privileged path ever
	// reaches it. Either error proves the row cannot be mutated.
	updErr := testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx)
		_, e := tx.Exec(ctx, "UPDATE approval_decisions SET reason = 'tamper' WHERE id = $1", decID)
		return e
	})
	require.Error(t, updErr)
	require.True(t, mutationRejected(updErr), "expected append-only/permission-denied, got: %v", updErr)

	delErr := testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx)
		_, e := tx.Exec(ctx, "DELETE FROM approval_decisions WHERE id = $1", decID)
		return e
	})
	require.Error(t, delErr)
	require.True(t, mutationRejected(delErr), "expected append-only/permission-denied, got: %v", delErr)
}

func TestApprovals_DoubleVote_UniqueViolation(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	reqID := seedApprovalRequest(t, orgID, 2)

	sc := testStore.As(business.Identity{OrgID: orgID})
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		return testStore.InsertDecision(ctx, &business.ApprovalDecision{
			ID: business.NewIDString(), RequestID: reqID, OrgID: orgID, Decider: "approver-1", Decision: business.DecisionApprove,
		})
	}))

	// Same decider again → UNIQUE (request_id, decider) → ErrTypeConflict.
	err := sc.Within(testCtx, func(ctx context.Context) error {
		return testStore.InsertDecision(ctx, &business.ApprovalDecision{
			ID: business.NewIDString(), RequestID: reqID, OrgID: orgID, Decider: "approver-1", Decision: business.DecisionApprove,
		})
	})
	require.Error(t, err)
	require.Equal(t, business.ErrTypeConflict, storeErrTypeInfra(t, err))
}

func TestApprovals_Quorum_DistinctApproversReachApproved(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	reqID := seedApprovalRequest(t, orgID, 2)

	sc := testStore.As(business.Identity{OrgID: orgID})

	// One approve → count 1, below quorum.
	var count int
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		if e := testStore.InsertDecision(ctx, &business.ApprovalDecision{
			ID: business.NewIDString(), RequestID: reqID, OrgID: orgID, Decider: "approver-1", Decision: business.DecisionApprove,
		}); e != nil {
			return e
		}
		var e error
		count, e = testStore.CountApprovals(ctx, reqID, orgID)
		return e
	}))
	require.Equal(t, 1, count)

	// Second distinct approver → count 2 = quorum; transition to approved.
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		if e := testStore.InsertDecision(ctx, &business.ApprovalDecision{
			ID: business.NewIDString(), RequestID: reqID, OrgID: orgID, Decider: "approver-2", Decision: business.DecisionApprove,
		}); e != nil {
			return e
		}
		n, e := testStore.CountApprovals(ctx, reqID, orgID)
		if e != nil {
			return e
		}
		count = n
		return testStore.UpdateApprovalState(ctx, reqID, orgID, business.ApprovalApproved, "", true)
	}))
	require.Equal(t, 2, count)

	var got *business.ApprovalRequest
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		var e error
		got, e = testStore.GetApprovalRequest(ctx, reqID, orgID)
		return e
	}))
	require.Equal(t, business.ApprovalApproved, got.State)
	require.NotNil(t, got.DecidedAt)
}

// TestApprovals_ConcurrentDecide_QuorumExactlyOnce proves the FOR UPDATE lock in
// the engine's Decide serializes concurrent approvers on the same request: two
// distinct approvers deciding at once on a quorum-2 request must both succeed,
// exactly one of them reaching quorum and flipping the row to approved. Without
// the head-row lock, both could read count=1 and neither would transition (or
// both would), so this is the test that would catch a regression there.
func TestApprovals_ConcurrentDecide_QuorumExactlyOnce(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	reqID := seedApprovalRequest(t, orgID, 2)

	svc, err := business.NewService(testStore)
	require.NoError(t, err)

	deciders := []string{"approver-1", "approver-2"}
	outcomes := make([]business.DecideOutcome, len(deciders))
	errs := make([]error, len(deciders))
	var wg sync.WaitGroup
	for i, d := range deciders {
		wg.Add(1)
		go func(i int, decider string) {
			defer wg.Done()
			outcomes[i], errs[i] = svc.Decide(testCtx, orgID, reqID, business.DecideInput{
				Decider: decider, Decision: business.DecisionApprove,
			})
		}(i, d)
	}
	wg.Wait()

	for i := range deciders {
		require.NoError(t, errs[i])
	}
	approved := 0
	for _, o := range outcomes {
		if o.Approved {
			approved++
		}
	}
	require.Equal(t, 1, approved, "exactly one concurrent decision must reach quorum and flip to approved")

	var got *business.ApprovalRequest
	var count int
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		var e error
		if got, e = testStore.GetApprovalRequest(ctx, reqID, orgID); e != nil {
			return e
		}
		count, e = testStore.CountApprovals(ctx, reqID, orgID)
		return e
	}))
	require.Equal(t, business.ApprovalApproved, got.State)
	require.Equal(t, 2, count)
}

// storeErrTypeInfra extracts the StoreErrorType from a *business.StoreError.
func storeErrTypeInfra(t *testing.T, err error) business.StoreErrorType {
	t.Helper()
	var se *business.StoreError
	require.True(t, errors.As(err, &se), "expected *business.StoreError, got %v", err)
	return se.StoreErrorType
}
