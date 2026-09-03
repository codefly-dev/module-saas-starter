package infra_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// ============================================================================
// Principal tests — integration against real Postgres.
//
// These tests rely on TestMain in postgres_webhooks_test.go to set up
// testStore + testPool + testCtx. NEVER mock per saas-starter rule.
// ============================================================================

// seedAgentPrincipal inserts a fresh agent row and returns it. Used by
// tests that need an existing agent to read / revoke / list against.
func seedAgentPrincipal(t *testing.T, orgID, agentIdentifier string) *business.Principal {
	t.Helper()
	p := &business.Principal{
		ID:              business.NewIDString(),
		Kind:            business.PrincipalKindAgent,
		DisplayName:     "test " + agentIdentifier,
		OrgID:           orgID,
		AgentIdentifier: agentIdentifier,
		CreatedAt:       time.Now().UTC(),
	}
	require.NoError(t, p.Validate())
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).CreateAgentPrincipal(testCtx, p))
	return p
}

// seedHumanPrincipal inserts a principal row matching an existing
// user.uuid. The caller must have already created the user (via
// seedUser); this just adds the principals row.
func seedHumanPrincipal(t *testing.T, userID, displayName string) {
	t.Helper()
	// No domain method for human-principal creation (humans are backfilled at
	// registration), so this fixture inserts under the explicit System identity
	// — the audited bypass — which satisfies the principals WITH CHECK.
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx,
			`INSERT INTO principals (id, kind, display_name, org_id, agent_identifier, created_at)
			 VALUES ($1, 'human', $2, NULL, NULL, CURRENT_TIMESTAMP)
			 ON CONFLICT (id) DO NOTHING`,
			userID, displayName)
		return err
	}))
}

// ----------------------------------------------------------------------------
// GetPrincipal
// ----------------------------------------------------------------------------

func TestPrincipal_GetPrincipal_NotFound(t *testing.T) {
	_, err := testStore.As(business.System()).GetPrincipal(testCtx, business.NewIDString())
	require.Error(t, err)
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeNotFound, se.StoreErrorType)
}

func TestPrincipal_GetPrincipal_Agent(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "test.codefly.dev/get-agent:0.1.0")

	got, err := testStore.As(business.Identity{OrgID: orgID}).GetPrincipal(testCtx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, agent.ID, got.ID)
	require.Equal(t, business.PrincipalKindAgent, got.Kind)
	require.Equal(t, agent.AgentIdentifier, got.AgentIdentifier)
	require.Equal(t, orgID, got.OrgID)
	require.False(t, got.IsRevoked())
}

func TestPrincipal_GetPrincipal_Human_NoOrgScope(t *testing.T) {
	userID := seedUser(t)
	seedHumanPrincipal(t, userID, "antoine@test.local")

	got, err := testStore.As(business.Identity{UserID: userID}).GetPrincipal(testCtx, userID)
	require.NoError(t, err)
	require.Equal(t, business.PrincipalKindHuman, got.Kind)
	require.Empty(t, got.OrgID,
		"humans are cross-org; OrgID must be empty per principals_org_scope CHECK")
	require.Empty(t, got.AgentIdentifier)
}

// ----------------------------------------------------------------------------
// GetAgentPrincipal
// ----------------------------------------------------------------------------

func TestPrincipal_GetAgentPrincipal_Found(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	want := seedAgentPrincipal(t, orgID, "test.codefly.dev/auto-merge:0.1.0")

	got, err := testStore.As(business.Identity{OrgID: orgID}).GetAgentPrincipal(testCtx, orgID, want.AgentIdentifier)
	require.NoError(t, err)
	require.Equal(t, want.ID, got.ID)
}

func TestPrincipal_GetAgentPrincipal_DifferentOrg_NotFound(t *testing.T) {
	owner := seedUser(t)
	orgA := seedOrg(t, owner)
	orgB := seedOrg(t, owner)
	seedAgentPrincipal(t, orgA, "test.codefly.dev/iso-agent:0.1.0")

	_, err := testStore.As(business.Identity{OrgID: orgB}).GetAgentPrincipal(testCtx, orgB, "test.codefly.dev/iso-agent:0.1.0")
	require.Error(t, err, "agents are org-scoped; lookup in a different org must miss")
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeNotFound, se.StoreErrorType)
}

func TestPrincipal_GetAgentPrincipal_Revoked_NotReturned(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "test.codefly.dev/revoked-agent:0.1.0")

	require.NoError(t, testStore.As(business.System()).RevokePrincipal(testCtx, agent.ID, "test revocation"))

	_, err := testStore.As(business.Identity{OrgID: orgID}).GetAgentPrincipal(testCtx, orgID, agent.AgentIdentifier)
	require.Error(t, err,
		"revoked agents must not surface to GetAgentPrincipal — slot is free for re-install")
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeNotFound, se.StoreErrorType)
}

// ----------------------------------------------------------------------------
// CreateAgentPrincipal
// ----------------------------------------------------------------------------

func TestPrincipal_CreateAgentPrincipal_RoundTrips(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	p := &business.Principal{
		ID:              business.NewIDString(),
		Kind:            business.PrincipalKindAgent,
		DisplayName:     "Round Trip Bot",
		OrgID:           orgID,
		AgentIdentifier: "test.codefly.dev/roundtrip:0.1.0",
		CreatedAt:       time.Now().UTC(),
	}
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).CreateAgentPrincipal(testCtx, p))

	got, err := testStore.As(business.Identity{OrgID: orgID}).GetPrincipal(testCtx, p.ID)
	require.NoError(t, err)
	require.Equal(t, p.AgentIdentifier, got.AgentIdentifier)
	require.Equal(t, p.DisplayName, got.DisplayName)
}

func TestPrincipal_CreateAgentPrincipal_DuplicateInSameOrg_Conflict(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	first := seedAgentPrincipal(t, orgID, "test.codefly.dev/dup:0.1.0")
	dup := &business.Principal{
		ID:              business.NewIDString(), // different ID
		Kind:            business.PrincipalKindAgent,
		DisplayName:     "duplicate attempt",
		OrgID:           orgID,
		AgentIdentifier: first.AgentIdentifier, // SAME identifier in SAME org
		CreatedAt:       time.Now().UTC(),
	}
	err := testStore.As(business.Identity{OrgID: dup.OrgID}).CreateAgentPrincipal(testCtx, dup)
	require.Error(t, err, "duplicate agent_identifier in same org must be rejected")
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeConflict, se.StoreErrorType)
}

func TestPrincipal_CreateAgentPrincipal_SameIdentifierAcrossOrgs_OK(t *testing.T) {
	owner := seedUser(t)
	orgA := seedOrg(t, owner)
	orgB := seedOrg(t, owner)

	seedAgentPrincipal(t, orgA, "test.codefly.dev/cross:0.1.0")
	pB := &business.Principal{
		ID:              business.NewIDString(),
		Kind:            business.PrincipalKindAgent,
		DisplayName:     "cross-org install",
		OrgID:           orgB,
		AgentIdentifier: "test.codefly.dev/cross:0.1.0",
		CreatedAt:       time.Now().UTC(),
	}
	require.NoError(t, testStore.As(business.Identity{OrgID: pB.OrgID}).CreateAgentPrincipal(testCtx, pB),
		"same agent identifier in different orgs must be allowed — independent installations")
}

func TestPrincipal_CreateAgentPrincipal_RejectsNonAgentKind(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	p := &business.Principal{
		ID:              business.NewIDString(),
		Kind:            business.PrincipalKindService, // wrong kind
		DisplayName:     "should fail",
		OrgID:           orgID,
		AgentIdentifier: "test.codefly.dev/wrong-kind:0.1.0",
		CreatedAt:       time.Now().UTC(),
	}
	err := testStore.As(business.System()).CreateAgentPrincipal(testCtx, p)
	require.Error(t, err, "CreateAgentPrincipal must reject non-agent kinds")
}

// ----------------------------------------------------------------------------
// RevokePrincipal
// ----------------------------------------------------------------------------

func TestPrincipal_RevokePrincipal_FirstCallSetsRevokedAt(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "test.codefly.dev/revoke1:0.1.0")

	require.NoError(t, testStore.As(business.System()).RevokePrincipal(testCtx, agent.ID, "policy violation"))

	got, err := testStore.As(business.Identity{OrgID: orgID}).GetPrincipal(testCtx, agent.ID)
	require.NoError(t, err)
	require.True(t, got.IsRevoked())
	require.NotNil(t, got.RevokedAt)
	require.Equal(t, "policy violation", got.RevokedReason)
}

func TestPrincipal_RevokePrincipal_Idempotent_PreservesOriginalReason(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "test.codefly.dev/revoke2:0.1.0")

	require.NoError(t, testStore.As(business.System()).RevokePrincipal(testCtx, agent.ID, "original reason"))
	require.NoError(t, testStore.As(business.System()).RevokePrincipal(testCtx, agent.ID, "second attempt"),
		"double-revoke must be a no-op success, not an error")

	got, err := testStore.As(business.Identity{OrgID: orgID}).GetPrincipal(testCtx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, "original reason", got.RevokedReason,
		"the first revocation reason wins; the audit trail must not be overwritten")
}

func TestPrincipal_RevokePrincipal_NotFound(t *testing.T) {
	err := testStore.As(business.System()).RevokePrincipal(testCtx, business.NewIDString(), "ghost")
	require.Error(t, err)
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeNotFound, se.StoreErrorType)
}

// ----------------------------------------------------------------------------
// ListPrincipals
// ----------------------------------------------------------------------------

func TestPrincipal_ListPrincipals_FilterByKind_Agent(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	seedAgentPrincipal(t, orgID, fmt.Sprintf("test.codefly.dev/list-a:%d.0.0", time.Now().UnixNano()))
	seedAgentPrincipal(t, orgID, fmt.Sprintf("test.codefly.dev/list-b:%d.0.0", time.Now().UnixNano()))
	// Also seed a human in the org — must NOT appear when filter=agent.
	humanID := seedUser(t)
	seedHumanPrincipal(t, humanID, "human-in-list-test@local")
	seedOrgMember(t, orgID, humanID)

	got, _, err := testStore.As(business.Identity{UserID: owner, OrgID: orgID, Kind: business.PrincipalKindHuman}).
		ListPrincipals(testCtx, business.PrincipalKindAgent, 50, "")
	require.NoError(t, err)
	for _, p := range got {
		require.Equal(t, business.PrincipalKindAgent, p.Kind,
			"kind=agent filter must exclude humans even if they're org members")
	}
	require.GreaterOrEqual(t, len(got), 2, "must include the two agents we seeded")
}

func TestPrincipal_ListPrincipals_AllKinds_IncludesHumansViaOrgMembership(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	seedAgentPrincipal(t, orgID, fmt.Sprintf("test.codefly.dev/all-kinds:%d.0.0", time.Now().UnixNano()))
	humanID := seedUser(t)
	seedHumanPrincipal(t, humanID, "list-all-human@local")
	seedOrgMember(t, orgID, humanID)

	got, _, err := testStore.As(business.Identity{UserID: owner, OrgID: orgID, Kind: business.PrincipalKindHuman}).
		ListPrincipals(testCtx, "", 50, "")
	require.NoError(t, err)

	kinds := map[string]int{}
	for _, p := range got {
		kinds[p.Kind]++
	}
	require.Greater(t, kinds[business.PrincipalKindAgent], 0)
	require.Greater(t, kinds[business.PrincipalKindHuman], 0,
		"empty kind filter must surface humans via organization_members join")
}

func TestPrincipal_ListPrincipals_RejectsBadKind(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	_, _, err := testStore.As(business.Identity{UserID: owner, OrgID: orgID, Kind: business.PrincipalKindHuman}).
		ListPrincipals(testCtx, "not-a-real-kind", 50, "")
	require.Error(t, err, "unknown kind values fail loud, not silently empty")
}

// ----------------------------------------------------------------------------
// SQL CHECK constraint coverage
// ----------------------------------------------------------------------------

func TestPrincipal_SchemaCHECK_HumanWithOrgID_Rejected(t *testing.T) {
	// principals_org_scope: humans must have org_id NULL.
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	id := business.NewIDString()
	err := testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, e := tx.Exec(ctx,
			`INSERT INTO principals (id, kind, display_name, org_id) VALUES ($1, 'human', 'bad', $2)`,
			id, orgID)
		return e
	})
	require.Error(t, err, "schema CHECK must reject human with org_id set")
}

func TestPrincipal_SchemaCHECK_AgentWithoutAgentIdentifier_Rejected(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	id := business.NewIDString()
	err := testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, e := tx.Exec(ctx,
			`INSERT INTO principals (id, kind, display_name, org_id) VALUES ($1, 'agent', 'no-id', $2)`,
			id, orgID)
		return e
	})
	require.Error(t, err, "schema CHECK must reject agent without agent_identifier")
}

func TestPrincipal_SchemaCHECK_NonAgentWithAgentIdentifier_Rejected(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	id := business.NewIDString()
	err := testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, e := tx.Exec(ctx,
			`INSERT INTO principals (id, kind, display_name, org_id, agent_identifier)
			 VALUES ($1, 'service', 'bad', $2, 'codefly.dev/x:0.0.1')`,
			id, orgID)
		return e
	})
	require.Error(t, err, "schema CHECK must reject service with agent_identifier set")
}

// ----------------------------------------------------------------------------
// Backfill verification — run once after migrations land
// ----------------------------------------------------------------------------

// TestPrincipal_Backfill_UsersHaveHumanPrincipals verifies the
// migration 37_backfill produced one principal per non-deleted user.
// A separate test asserts api_keys backfilled to service principals.
func TestPrincipal_Backfill_UsersHaveHumanPrincipals(t *testing.T) {
	// The backfill is migration 37; this test runs after migrations,
	// so users seeded BEFORE the backfill should have principal rows.
	// Users seeded after by tests don't have rows automatically (the
	// migration only ran once); this test seeds a user, then calls
	// the backfill SQL directly to verify the SHAPE of the produced row.
	userID := seedUser(t)

	// Mimic the backfill INSERT — the production migration runs once
	// at deploy. This test verifies the SQL shape produces a valid
	// row that GetPrincipal can read.
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			INSERT INTO principals (id, kind, display_name, org_id, agent_identifier, created_at)
			SELECT u.uuid, 'human', u.primary_email, NULL, NULL, u.created_at
			FROM users u
			WHERE u.uuid = $1
			ON CONFLICT (id) DO NOTHING`, userID)
		return err
	}))

	got, err := testStore.As(business.Identity{UserID: userID}).GetPrincipal(testCtx, userID)
	require.NoError(t, err)
	require.Equal(t, business.PrincipalKindHuman, got.Kind)
	require.Empty(t, got.OrgID)
}

// ----------------------------------------------------------------------------
// Concurrency
// ----------------------------------------------------------------------------

func TestPrincipal_CreateAgent_ConcurrentDuplicate_OneWins(t *testing.T) {
	// Two goroutines try to create the same agent simultaneously. The
	// UNIQUE index must serialize them: exactly one CREATE succeeds,
	// the other gets ErrTypeConflict.
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	identifier := fmt.Sprintf("test.codefly.dev/concurrent:%d.0.0", time.Now().UnixNano())

	var (
		errCh = make(chan error, 2)
	)
	for i := 0; i < 2; i++ {
		go func() {
			p := &business.Principal{
				ID:              business.NewIDString(),
				Kind:            business.PrincipalKindAgent,
				DisplayName:     "concurrent",
				OrgID:           orgID,
				AgentIdentifier: identifier,
				CreatedAt:       time.Now().UTC(),
			}
			errCh <- testStore.As(business.Identity{OrgID: p.OrgID}).CreateAgentPrincipal(context.Background(), p)
		}()
	}
	results := []error{<-errCh, <-errCh}

	successCount := 0
	conflictCount := 0
	for _, err := range results {
		if err == nil {
			successCount++
			continue
		}
		var se *business.StoreError
		require.ErrorAs(t, err, &se)
		if se.StoreErrorType == business.ErrTypeConflict {
			conflictCount++
		}
	}
	require.Equal(t, 1, successCount, "exactly one create must succeed")
	require.Equal(t, 1, conflictCount, "the other must report conflict")
}

// TestPrincipals_CreateAgent_AuditActorTypeFromCreatorKind proves the
// principal.created audit event is attributed to the creator's actual kind, not
// a hardcoded "user": an agent creating a sub-agent must be recorded as an
// "agent" actor.
func TestPrincipals_CreateAgent_AuditActorTypeFromCreatorKind(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	creator := seedAgentPrincipal(t, orgID, "publisher/creator:1.0.0")

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	rec := &recordingEmitter{}
	svc.SetAuditEmitter(rec)

	_, err = svc.CreateAgentPrincipal(testCtx, business.CreateAgentRequest{
		OrgID:           orgID,
		AgentIdentifier: "publisher/child:1.0.0",
		CreatedBy:       creator.ID,
	})
	require.NoError(t, err)

	found := false
	for _, e := range rec.entries {
		if e.EventType == business.EventPrincipalCreated {
			require.Equal(t, "agent", e.ActorType,
				"principal.created by an agent creator must be attributed to an agent actor")
			found = true
		}
	}
	require.True(t, found, "principal.created must be emitted")
}

func TestPrincipals_RevokePrincipal_EmitsAuditEvent(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "publisher/revoke-audit:1.0.0")

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	rec := &recordingEmitter{}
	svc.SetAuditEmitter(rec)

	require.NoError(t, svc.RevokePrincipal(testCtx, agent.ID, "delegation disabled"))

	found := false
	for _, e := range rec.entries {
		if e.EventType == business.EventPrincipalRevoked {
			require.Equal(t, agent.ID, e.ResourceID)
			require.Equal(t, orgID, e.OrgID)
			require.Equal(t, "delegation disabled", e.Payload["reason"])
			found = true
		}
	}
	require.True(t, found, "principal.revoked must be emitted")
}

// ----------------------------------------------------------------------------
// Ceiling + disable/enable lifecycle (issue #440)
// ----------------------------------------------------------------------------

func TestPrincipals_CreateAgent_PersistsCeiling(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	svc, err := business.NewService(testStore)
	require.NoError(t, err)

	created, err := svc.CreateAgentPrincipal(testCtx, business.CreateAgentRequest{
		OrgID:            orgID,
		AgentIdentifier:  "publisher/ceiling:1.0.0",
		CreatedBy:        owner,
		AllowedAudiences: []string{"github", "jira"},
		AllowedScopes:    []string{"repo"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"github", "jira"}, created.AllowedAudiences)
	require.Equal(t, []string{"repo"}, created.AllowedScopes)

	got, err := testStore.As(business.Identity{OrgID: orgID}).GetAgentPrincipal(testCtx, orgID, "publisher/ceiling:1.0.0")
	require.NoError(t, err)
	require.Equal(t, []string{"github", "jira"}, got.AllowedAudiences)
	require.Equal(t, []string{"repo"}, got.AllowedScopes)
}

func TestPrincipals_CreateAgent_EmptyCeilingReadsBackEmpty(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "publisher/no-ceiling:1.0.0")

	got, err := testStore.As(business.Identity{OrgID: orgID}).GetPrincipal(testCtx, agent.ID)
	require.NoError(t, err)
	require.Empty(t, got.AllowedAudiences, "an unset ceiling is the empty array (unrestricted), never an allow-nothing set")
	require.Empty(t, got.AllowedScopes)
}

func TestPrincipals_DisableEnable_LifecycleAndAudit(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "publisher/toggle:1.0.0")

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	rec := &recordingEmitter{}
	svc.SetAuditEmitter(rec)

	require.NoError(t, svc.DisableAgentPrincipal(testCtx, agent.ID, "incident-123"))

	// A disabled agent is no longer resolvable as a fresh actor. The exclusion
	// lives at the business layer (Service.GetAgentPrincipal) and the Work Context
	// authority query, not the store primitive — the store still returns disabled
	// rows so CreateAgentPrincipal's idempotency check sees the occupied slot.
	_, err = svc.GetAgentPrincipal(testCtx, orgID, agent.AgentIdentifier)
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeNotFound, se.StoreErrorType)

	require.NoError(t, svc.EnableAgentPrincipal(testCtx, agent.ID))
	got, err := svc.GetAgentPrincipal(testCtx, orgID, agent.AgentIdentifier)
	require.NoError(t, err)
	require.False(t, got.IsDisabled(), "an enabled agent resolves again")

	require.Equal(t, 1, countEvents(rec, business.EventPrincipalDisabled))
	require.Equal(t, 1, countEvents(rec, business.EventPrincipalEnabled))
}

func TestPrincipals_Disable_IdempotentNoDuplicateAudit(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "publisher/idem-disable:1.0.0")

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	rec := &recordingEmitter{}
	svc.SetAuditEmitter(rec)

	require.NoError(t, svc.DisableAgentPrincipal(testCtx, agent.ID, "first"))
	require.NoError(t, svc.DisableAgentPrincipal(testCtx, agent.ID, "second"))
	require.Equal(t, 1, countEvents(rec, business.EventPrincipalDisabled),
		"re-disabling an already-disabled agent must not emit a second audit event")

	got, err := testStore.As(business.Identity{OrgID: orgID}).GetPrincipal(testCtx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, "first", got.DisabledReason, "the original disable reason must be preserved")
}

// TestPrincipals_DisableEnable_ReportsTransition pins the store contract the
// business layer relies on to audit exactly once: Disable/Enable report whether
// this call actually flipped the row. Without a truthful transition signal the
// Service would re-derive it from a pre-UPDATE read, which double-audits when two
// first-time disables race (both read "not disabled", both emit).
func TestPrincipals_DisableEnable_ReportsTransition(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "publisher/transition:1.0.0")
	store := testStore.As(business.System())

	transitioned, err := store.DisableAgentPrincipal(testCtx, agent.ID, "first")
	require.NoError(t, err)
	require.True(t, transitioned, "the first disable of an active agent flips the row")

	transitioned, err = store.DisableAgentPrincipal(testCtx, agent.ID, "second")
	require.NoError(t, err)
	require.False(t, transitioned, "re-disabling reports no transition, so no duplicate audit event fires")

	transitioned, err = store.EnableAgentPrincipal(testCtx, agent.ID)
	require.NoError(t, err)
	require.True(t, transitioned, "enabling a disabled agent lifts the suspension")

	transitioned, err = store.EnableAgentPrincipal(testCtx, agent.ID)
	require.NoError(t, err)
	require.False(t, transitioned, "re-enabling an already-active agent reports no transition")
}

func TestPrincipals_Enable_RejectedForRevoked(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "publisher/revoked-noenable:1.0.0")

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	require.NoError(t, testStore.As(business.System()).RevokePrincipal(testCtx, agent.ID, "terminal"))

	err = svc.EnableAgentPrincipal(testCtx, agent.ID)
	require.Error(t, err, "a revoked principal is terminal and cannot be re-enabled")
}

func countEvents(rec *recordingEmitter, event business.EventType) int {
	n := 0
	for _, e := range rec.entries {
		if e.EventType == event {
			n++
		}
	}
	return n
}
