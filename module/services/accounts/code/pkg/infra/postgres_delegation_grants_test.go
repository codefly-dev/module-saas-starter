package infra_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// =====================================================================
// delegation_grants integration tests
// =====================================================================
//
// Real Postgres only — never mock infra. delegation_grants is
// RLS-protected (org-scoped), so every store call runs as the grant's
// org via As(Identity{OrgID}); fixture inserts use the same.

// seedActorPrincipal inserts an agent principal we can use as
// actor on a delegation grant. Returns the principal id.
func seedActorPrincipal(t *testing.T, orgID string) string {
	t.Helper()
	p := seedAgentPrincipal(t, orgID, "ci.test/actor:0.1.0")
	return p.ID
}

// seedGrantorPrincipal inserts a service principal acting as the
// pre-authorizing grantor for pattern grants.
func seedGrantorPrincipal(t *testing.T, orgID string) string {
	t.Helper()
	p := seedAgentPrincipal(t, orgID, "ci.test/grantor:0.1.0")
	return p.ID
}

// newRequestInput builds a minimal valid RequestDelegationInput.
func newRequestInput(orgID, actorID, action, resource string) *business.RequestDelegationInput {
	return &business.RequestDelegationInput{
		OrgID:            orgID,
		ActorPrincipalID: actorID,
		Action:           action,
		Resource:         resource,
		Justification:    "ci test",
		RiskLevel:        business.RiskLevelLow,
		Timeout:          5 * time.Minute,
	}
}

// seedPatternGrant inserts an active pattern grant directly. delegation_grants
// is RLS-protected (org-scoped); the insert runs as the org so WITH CHECK passes.
func seedPatternGrant(t *testing.T, orgID, actorID, grantorID, actionPattern, resourcePattern string, maxUses *int) string {
	t.Helper()
	id := business.NewIDString()
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
        INSERT INTO delegation_grants (
            id, org_id, actor_principal_id, grantor_principal_id,
            action, resource, justification, risk_level, expires_at,
            kind, status, action_pattern, resource_pattern, max_uses,
            request_hash
        )
        VALUES ($1, $2, $3, $4,
                'pattern.placeholder', NULL, 'pattern grant', 'low',
                CURRENT_TIMESTAMP + INTERVAL '1 hour',
                'pattern', 'active', $5, $6, $7, $8)
    `, id, orgID, actorID, grantorID, actionPattern, resourcePattern, maxUses, "pattern-"+id)
		return err
	}))
	return id
}

// =====================================================================
// Insert + Get (M7)
// =====================================================================

func TestDelegation_Insert_IdempotentByHash(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	sc := testStore.As(business.Identity{OrgID: orgID})

	in := newRequestInput(orgID, actor, "github.merge_pr", "repo:foo")
	hash := business.ComputeRequestHash(in)
	expires := time.Now().UTC().Add(5 * time.Minute)

	var id1, id2, id3 string
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		var e error
		if id1, e = testStore.Insert(ctx, in, hash, expires); e != nil {
			return e
		}
		if id2, e = testStore.Insert(ctx, in, hash, expires); e != nil {
			return e
		}
		in2 := newRequestInput(orgID, actor, "github.merge_pr", "repo:bar")
		id3, e = testStore.Insert(ctx, in2, business.ComputeRequestHash(in2), expires)
		return e
	}))
	require.NotEmpty(t, id1)
	require.Equal(t, id1, id2, "same hash must return existing row")
	require.NotEqual(t, id1, id3)
}

func TestDelegation_Get_NotFound(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	err := testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		_, e := testStore.Get(ctx, business.NewIDString(), orgID)
		return e
	})
	require.Error(t, err)
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeNotFound, se.StoreErrorType)
}

// =====================================================================
// Decide
// =====================================================================

func TestDelegation_Decide_ApproveAndDeny(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)
	sc := testStore.As(business.Identity{OrgID: orgID})
	expires := time.Now().UTC().Add(5 * time.Minute)

	in := newRequestInput(orgID, actor, "infra.exec_sql", "db:prod")
	var id string
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		var e error
		id, e = testStore.Insert(ctx, in, business.ComputeRequestHash(in), expires)
		return e
	}))

	var got *business.DelegationGrant
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		var e error
		got, e = testStore.Decide(ctx, id, orgID, grantor, business.GrantStatusApproved, "looks good")
		return e
	}))
	require.Equal(t, business.GrantStatusApproved, got.Status)
	require.Equal(t, grantor, got.GrantorPrincipalID)
	require.NotNil(t, got.DecidedAt)

	// Second Decide on the same row must fail (already decided).
	require.Error(t, sc.Within(testCtx, func(ctx context.Context) error {
		_, e := testStore.Decide(ctx, id, orgID, grantor, business.GrantStatusDenied, "changed my mind")
		return e
	}), "concurrent re-decision must be rejected")

	// Fresh row, deny path.
	in2 := newRequestInput(orgID, actor, "infra.drop_table", "db:prod")
	var denied *business.DelegationGrant
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		id2, e := testStore.Insert(ctx, in2, business.ComputeRequestHash(in2), expires)
		if e != nil {
			return e
		}
		denied, e = testStore.Decide(ctx, id2, orgID, grantor, business.GrantStatusDenied, "too risky")
		return e
	}))
	require.Equal(t, business.GrantStatusDenied, denied.Status)
	require.Equal(t, "too risky", denied.DecisionReason)
}

func TestDelegation_SetMintedToken(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)
	sc := testStore.As(business.Identity{OrgID: orgID})

	in := newRequestInput(orgID, actor, "github.merge_pr", "repo:foo")
	var id string
	var got *business.DelegationGrant
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		var e error
		if id, e = testStore.Insert(ctx, in, business.ComputeRequestHash(in), time.Now().Add(5*time.Minute)); e != nil {
			return e
		}
		if _, e = testStore.Decide(ctx, id, orgID, grantor, business.GrantStatusApproved, ""); e != nil {
			return e
		}
		if e = testStore.SetMintedToken(ctx, id, orgID, "tok-"+id); e != nil {
			return e
		}
		got, e = testStore.Get(ctx, id, orgID)
		return e
	}))
	require.Equal(t, "tok-"+id, got.MintedTokenID)
	require.Equal(t, business.GrantStatusApproved, got.Status, "SetMintedToken must not change status")
}

// =====================================================================
// ListPending
// =====================================================================

func TestDelegation_ListPending_RiskOrdering(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	sc := testStore.As(business.Identity{OrgID: orgID})

	var got []*business.DelegationGrant
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		insertWithRisk := func(action string, risk business.RiskLevel) error {
			in := newRequestInput(orgID, actor, action, "")
			in.RiskLevel = risk
			_, e := testStore.Insert(ctx, in, business.ComputeRequestHash(in), time.Now().Add(5*time.Minute))
			return e
		}
		for _, r := range []struct {
			a string
			l business.RiskLevel
		}{
			{"low.action", business.RiskLevelLow},
			{"medium.action", business.RiskLevelMedium},
			{"critical.action", business.RiskLevelCritical},
			{"high.action", business.RiskLevelHigh},
		} {
			if e := insertWithRisk(r.a, r.l); e != nil {
				return e
			}
		}
		var e error
		got, _, e = testStore.ListPending(ctx, orgID, 10, "")
		return e
	}))
	require.Len(t, got, 4)
	// Critical first, then high, medium, low.
	require.Equal(t, business.RiskLevelCritical, got[0].RiskLevel)
	require.Equal(t, business.RiskLevelHigh, got[1].RiskLevel)
	require.Equal(t, business.RiskLevelMedium, got[2].RiskLevel)
	require.Equal(t, business.RiskLevelLow, got[3].RiskLevel)
}

// =====================================================================
// Subscribe — snapshot path for already-decided rows
// =====================================================================

func TestDelegation_Subscribe_AlreadyDecided(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)
	sc := testStore.As(business.Identity{OrgID: orgID})

	in := newRequestInput(orgID, actor, "github.merge_pr", "repo:foo")
	var id string
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		var e error
		if id, e = testStore.Insert(ctx, in, business.ComputeRequestHash(in), time.Now().Add(5*time.Minute)); e != nil {
			return e
		}
		_, e = testStore.Decide(ctx, id, orgID, grantor, business.GrantStatusApproved, "")
		return e
	}))

	// Subscribe AFTER decision — its snapshot path emits one terminal event then
	// closes. Subscribe sets its own connection's org context internally.
	ctx, cancel := context.WithTimeout(testCtx, 3*time.Second)
	defer cancel()
	events, err := testStore.Subscribe(ctx, id, orgID)
	require.NoError(t, err)

	select {
	case ev, ok := <-events:
		require.True(t, ok, "expected a snapshot event")
		require.Equal(t, business.GrantStatusApproved, ev.Status)
	case <-ctx.Done():
		t.Fatal("snapshot event not delivered within timeout")
	}
	select {
	case _, ok := <-events:
		require.False(t, ok, "channel should close after terminal event")
	case <-ctx.Done():
		t.Fatal("channel did not close after terminal")
	}
}

// =====================================================================
// M8 — MatchPattern
// =====================================================================

func TestDelegation_MatchPattern_NoActivePattern(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)

	var got *business.DelegationGrant
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		var e error
		got, e = testStore.MatchPattern(ctx, orgID, actor, "github.merge_pr", "repo:foo")
		return e
	}))
	require.Nil(t, got, "no active patterns → nil match")
}

func TestDelegation_MatchPattern_ExactAndGlob(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)
	sc := testStore.As(business.Identity{OrgID: orgID})

	patID := seedPatternGrant(t, orgID, actor, grantor, "github.merge_pr", "repo:codefly/*", nil)

	var got, miss, miss2 *business.DelegationGrant
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		var e error
		if got, e = testStore.MatchPattern(ctx, orgID, actor, "github.merge_pr", "repo:codefly/codefly.dev"); e != nil {
			return e
		}
		if miss, e = testStore.MatchPattern(ctx, orgID, actor, "github.merge_pr", "repo:other/api"); e != nil {
			return e
		}
		miss2, e = testStore.MatchPattern(ctx, orgID, actor, "github.delete_repo", "repo:codefly/codefly.dev")
		return e
	}))
	require.NotNil(t, got)
	require.Equal(t, patID, got.ID)
	require.Nil(t, miss, "resource outside glob → no match")
	require.Nil(t, miss2, "action mismatch → no match")
}

func TestDelegation_MatchPattern_OnlyMatchesActorOrg(t *testing.T) {
	owner := seedUser(t)
	orgA := seedOrg(t, owner)
	orgB := seedOrg(t, owner)
	actorA := seedActorPrincipal(t, orgA)
	actorB := seedActorPrincipal(t, orgB)
	grantorA := seedGrantorPrincipal(t, orgA)

	seedPatternGrant(t, orgA, actorA, grantorA, "*", "*", nil)

	// Different org — no match (read as orgB).
	var missOrg *business.DelegationGrant
	require.NoError(t, testStore.As(business.Identity{OrgID: orgB}).Within(testCtx, func(ctx context.Context) error {
		var e error
		missOrg, e = testStore.MatchPattern(ctx, orgB, actorA, "x.y", "")
		return e
	}))
	require.Nil(t, missOrg)

	// Same org, different actor / same actor — read as orgA.
	var missActor, hit *business.DelegationGrant
	require.NoError(t, testStore.As(business.Identity{OrgID: orgA}).Within(testCtx, func(ctx context.Context) error {
		var e error
		if missActor, e = testStore.MatchPattern(ctx, orgA, actorB, "x.y", ""); e != nil {
			return e
		}
		hit, e = testStore.MatchPattern(ctx, orgA, actorA, "x.y", "")
		return e
	}))
	require.Nil(t, missActor)
	require.NotNil(t, hit)
}

// =====================================================================
// M8 — IncrementPatternUse
// =====================================================================

func TestDelegation_IncrementPatternUse_Success(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)
	sc := testStore.As(business.Identity{OrgID: orgID})

	max := 10
	patID := seedPatternGrant(t, orgID, actor, grantor, "*", "*", &max)

	var g1, g2 *business.DelegationGrant
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		var e error
		if g1, e = testStore.IncrementPatternUse(ctx, patID, orgID); e != nil {
			return e
		}
		g2, e = testStore.IncrementPatternUse(ctx, patID, orgID)
		return e
	}))
	require.Equal(t, 1, g1.UseCount)
	require.Equal(t, 2, g2.UseCount)
}

func TestDelegation_IncrementPatternUse_Exhausted(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)
	sc := testStore.As(business.Identity{OrgID: orgID})

	max := 1
	patID := seedPatternGrant(t, orgID, actor, grantor, "*", "*", &max)

	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		_, e := testStore.IncrementPatternUse(ctx, patID, orgID)
		return e
	}))

	// Second call must fail with conflict — max_uses exhausted.
	err := sc.Within(testCtx, func(ctx context.Context) error {
		_, e := testStore.IncrementPatternUse(ctx, patID, orgID)
		return e
	})
	require.Error(t, err)
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeConflict, se.StoreErrorType)
}

func TestDelegation_IncrementPatternUse_Revoked(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)
	sc := testStore.As(business.Identity{OrgID: orgID})

	patID := seedPatternGrant(t, orgID, actor, grantor, "*", "*", nil)

	// Flip to cancelled (manual revoke) — under org context.
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, e := tx.Exec(ctx, `UPDATE delegation_grants SET status='cancelled' WHERE id=$1`, patID)
		return e
	}))

	err := sc.Within(testCtx, func(ctx context.Context) error {
		_, e := testStore.IncrementPatternUse(ctx, patID, orgID)
		return e
	})
	require.Error(t, err)
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeConflict, se.StoreErrorType)
}

// =====================================================================
// M8 — InsertAutoApproved
// =====================================================================

func TestDelegation_InsertAutoApproved(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)
	sc := testStore.As(business.Identity{OrgID: orgID})

	patID := seedPatternGrant(t, orgID, actor, grantor, "*", "*", nil)

	in := newRequestInput(orgID, actor, "github.merge_pr", "repo:foo")
	hash := business.ComputeRequestHash(in)

	var id, id2 string
	var got *business.DelegationGrant
	require.NoError(t, sc.Within(testCtx, func(ctx context.Context) error {
		pattern, e := testStore.Get(ctx, patID, orgID)
		if e != nil {
			return e
		}
		if id, e = testStore.InsertAutoApproved(ctx, in, hash, time.Now().Add(5*time.Minute), pattern); e != nil {
			return e
		}
		if got, e = testStore.Get(ctx, id, orgID); e != nil {
			return e
		}
		// Idempotency: same hash returns same id.
		id2, e = testStore.InsertAutoApproved(ctx, in, hash, time.Now().Add(5*time.Minute), pattern)
		return e
	}))
	require.NotEmpty(t, id)
	require.Equal(t, business.GrantStatusApproved, got.Status, "auto-approved row must land approved")
	require.Equal(t, grantor, got.GrantorPrincipalID, "grantor copied from pattern")
	require.NotNil(t, got.DecidedAt)
	require.NotNil(t, got.RequestContext)
	require.Equal(t, patID, got.RequestContext["via_pattern"])
	require.Equal(t, id, id2)
}
