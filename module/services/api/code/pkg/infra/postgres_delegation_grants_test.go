package infra_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"api/pkg/business"
)

// =====================================================================
// delegation_grants integration tests
// =====================================================================
//
// Real Postgres only — never mock infra. Schema migration 38
// is applied by the store agent's migration runner before
// TestMain returns.
//
// **What's covered:**
//   - Idempotent Insert (ON CONFLICT returns existing id)
//   - Decide atomic transition (concurrent approvers cannot stomp)
//   - SetMintedToken updates without changing status
//   - ListPending honors risk ordering
//   - Subscribe snapshot for already-decided rows
//   - M8 MatchPattern: no match, exact, glob
//   - M8 IncrementPatternUse: success, exhaustion, revoked
//   - M8 InsertAutoApproved: status=approved + via_pattern caveat

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

// seedPatternGrant inserts an active pattern grant directly via
// SQL. The business layer doesn't yet expose a public CreatePattern
// method (that's a separate UI flow); the integration tests poke
// the row directly so they can exercise the M8 read path.
func seedPatternGrant(t *testing.T, orgID, actorID, grantorID, actionPattern, resourcePattern string, maxUses *int) string {
	t.Helper()
	ctx := testCtx
	id := business.NewIDString()

	// expires_at far in the future so MatchPattern's
	// expires_at > NOW() filter doesn't trip.
	_, err := testPool.Exec(ctx, `
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
	require.NoError(t, err)
	return id
}

// =====================================================================
// Insert + Get (M7)
// =====================================================================

func TestDelegation_Insert_IdempotentByHash(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)

	in := newRequestInput(orgID, actor, "github.merge_pr", "repo:foo")
	hash := business.ComputeRequestHash(in)
	expires := time.Now().UTC().Add(5 * time.Minute)

	id1, err := testStore.Insert(testCtx, in, hash, expires)
	require.NoError(t, err)
	require.NotEmpty(t, id1)

	id2, err := testStore.Insert(testCtx, in, hash, expires)
	require.NoError(t, err)
	require.Equal(t, id1, id2, "same hash must return existing row")

	// Different hash → new row.
	in2 := newRequestInput(orgID, actor, "github.merge_pr", "repo:bar")
	id3, err := testStore.Insert(testCtx, in2, business.ComputeRequestHash(in2), expires)
	require.NoError(t, err)
	require.NotEqual(t, id1, id3)
}

func TestDelegation_Get_NotFound(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	_, err := testStore.Get(testCtx, business.NewIDString(), orgID)
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

	in := newRequestInput(orgID, actor, "infra.exec_sql", "db:prod")
	expires := time.Now().UTC().Add(5 * time.Minute)
	id, err := testStore.Insert(testCtx, in, business.ComputeRequestHash(in), expires)
	require.NoError(t, err)

	got, err := testStore.Decide(testCtx, id, orgID, grantor, business.GrantStatusApproved, "looks good")
	require.NoError(t, err)
	require.Equal(t, business.GrantStatusApproved, got.Status)
	require.Equal(t, grantor, got.GrantorPrincipalID)
	require.NotNil(t, got.DecidedAt)

	// Second Decide on the same row must fail (already decided).
	_, err = testStore.Decide(testCtx, id, orgID, grantor, business.GrantStatusDenied, "changed my mind")
	require.Error(t, err, "concurrent re-decision must be rejected")

	// Fresh row, deny path.
	in2 := newRequestInput(orgID, actor, "infra.drop_table", "db:prod")
	id2, err := testStore.Insert(testCtx, in2, business.ComputeRequestHash(in2), expires)
	require.NoError(t, err)
	denied, err := testStore.Decide(testCtx, id2, orgID, grantor, business.GrantStatusDenied, "too risky")
	require.NoError(t, err)
	require.Equal(t, business.GrantStatusDenied, denied.Status)
	require.Equal(t, "too risky", denied.DecisionReason)
}

func TestDelegation_SetMintedToken(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)

	in := newRequestInput(orgID, actor, "github.merge_pr", "repo:foo")
	id, err := testStore.Insert(testCtx, in, business.ComputeRequestHash(in), time.Now().Add(5*time.Minute))
	require.NoError(t, err)
	_, err = testStore.Decide(testCtx, id, orgID, grantor, business.GrantStatusApproved, "")
	require.NoError(t, err)

	require.NoError(t, testStore.SetMintedToken(testCtx, id, orgID, "tok-"+id))

	got, err := testStore.Get(testCtx, id, orgID)
	require.NoError(t, err)
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

	insertWithRisk := func(action string, risk business.RiskLevel) {
		in := newRequestInput(orgID, actor, action, "")
		in.RiskLevel = risk
		_, err := testStore.Insert(testCtx, in, business.ComputeRequestHash(in), time.Now().Add(5*time.Minute))
		require.NoError(t, err)
	}
	insertWithRisk("low.action", business.RiskLevelLow)
	insertWithRisk("medium.action", business.RiskLevelMedium)
	insertWithRisk("critical.action", business.RiskLevelCritical)
	insertWithRisk("high.action", business.RiskLevelHigh)

	got, _, err := testStore.ListPending(testCtx, orgID, 10, "")
	require.NoError(t, err)
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

	in := newRequestInput(orgID, actor, "github.merge_pr", "repo:foo")
	id, err := testStore.Insert(testCtx, in, business.ComputeRequestHash(in), time.Now().Add(5*time.Minute))
	require.NoError(t, err)
	_, err = testStore.Decide(testCtx, id, orgID, grantor, business.GrantStatusApproved, "")
	require.NoError(t, err)

	// Subscribe AFTER decision — Subscribe's snapshot path should
	// emit one terminal event then close.
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
	// Channel should close after terminal.
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

	got, err := testStore.MatchPattern(testCtx, orgID, actor, "github.merge_pr", "repo:foo")
	require.NoError(t, err)
	require.Nil(t, got, "no active patterns → nil match")
}

func TestDelegation_MatchPattern_ExactAndGlob(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)

	// Pattern: action="github.merge_pr" (exact), resource="repo:codefly/*"
	patID := seedPatternGrant(t, orgID, actor, grantor, "github.merge_pr", "repo:codefly/*", nil)

	got, err := testStore.MatchPattern(testCtx, orgID, actor, "github.merge_pr", "repo:codefly/codefly.dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, patID, got.ID)

	// Resource outside glob → no match.
	miss, err := testStore.MatchPattern(testCtx, orgID, actor, "github.merge_pr", "repo:other/api")
	require.NoError(t, err)
	require.Nil(t, miss)

	// Action mismatch → no match.
	miss2, err := testStore.MatchPattern(testCtx, orgID, actor, "github.delete_repo", "repo:codefly/codefly.dev")
	require.NoError(t, err)
	require.Nil(t, miss2)
}

func TestDelegation_MatchPattern_OnlyMatchesActorOrg(t *testing.T) {
	owner := seedUser(t)
	orgA := seedOrg(t, owner)
	orgB := seedOrg(t, owner)
	actorA := seedActorPrincipal(t, orgA)
	actorB := seedActorPrincipal(t, orgB)
	grantorA := seedGrantorPrincipal(t, orgA)

	seedPatternGrant(t, orgA, actorA, grantorA, "*", "*", nil)

	// Different org — no match.
	missOrg, err := testStore.MatchPattern(testCtx, orgB, actorA, "x.y", "")
	require.NoError(t, err)
	require.Nil(t, missOrg)

	// Same org, different actor — no match.
	missActor, err := testStore.MatchPattern(testCtx, orgA, actorB, "x.y", "")
	require.NoError(t, err)
	require.Nil(t, missActor)

	// Same actor + org — match.
	hit, err := testStore.MatchPattern(testCtx, orgA, actorA, "x.y", "")
	require.NoError(t, err)
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

	max := 10
	patID := seedPatternGrant(t, orgID, actor, grantor, "*", "*", &max)

	g, err := testStore.IncrementPatternUse(testCtx, patID, orgID)
	require.NoError(t, err)
	require.Equal(t, 1, g.UseCount)

	g, err = testStore.IncrementPatternUse(testCtx, patID, orgID)
	require.NoError(t, err)
	require.Equal(t, 2, g.UseCount)
}

func TestDelegation_IncrementPatternUse_Exhausted(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	actor := seedActorPrincipal(t, orgID)
	grantor := seedGrantorPrincipal(t, orgID)

	max := 1
	patID := seedPatternGrant(t, orgID, actor, grantor, "*", "*", &max)

	_, err := testStore.IncrementPatternUse(testCtx, patID, orgID)
	require.NoError(t, err)

	// Second call must fail with conflict — max_uses exhausted.
	_, err = testStore.IncrementPatternUse(testCtx, patID, orgID)
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

	patID := seedPatternGrant(t, orgID, actor, grantor, "*", "*", nil)

	// Flip to cancelled (manual revoke).
	_, err := testPool.Exec(testCtx,
		`UPDATE delegation_grants SET status='cancelled' WHERE id=$1`, patID)
	require.NoError(t, err)

	_, err = testStore.IncrementPatternUse(testCtx, patID, orgID)
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

	// Build the matching pattern row to pass as via.
	patID := seedPatternGrant(t, orgID, actor, grantor, "*", "*", nil)
	pattern, err := testStore.Get(testCtx, patID, orgID)
	require.NoError(t, err)

	in := newRequestInput(orgID, actor, "github.merge_pr", "repo:foo")
	hash := business.ComputeRequestHash(in)
	id, err := testStore.InsertAutoApproved(testCtx, in, hash, time.Now().Add(5*time.Minute), pattern)
	require.NoError(t, err)
	require.NotEmpty(t, id)

	got, err := testStore.Get(testCtx, id, orgID)
	require.NoError(t, err)
	require.Equal(t, business.GrantStatusApproved, got.Status, "auto-approved row must land approved")
	require.Equal(t, grantor, got.GrantorPrincipalID, "grantor copied from pattern")
	require.NotNil(t, got.DecidedAt)

	// via_pattern caveat is in request_context.
	require.NotNil(t, got.RequestContext)
	require.Equal(t, patID, got.RequestContext["via_pattern"])

	// Idempotency: same hash returns same id.
	id2, err := testStore.InsertAutoApproved(testCtx, in, hash, time.Now().Add(5*time.Minute), pattern)
	require.NoError(t, err)
	require.Equal(t, id, id2)
}
