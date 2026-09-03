package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestWorkContextAuthorityUsesCurrentRLSBoundFactsAndExactScopes(t *testing.T) {
	ownerID := seedUser(t)
	orgID := seedOrg(t, ownerID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, ownerID, "owner",
	))
	agent := seedAgentPrincipal(t, orgID, "test.codefly.dev/work-context:0.1.0")

	teamID := business.NewIDString()
	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateTeam(ctx, &gen.Team{
			Id:    teamID,
			OrgId: orgID,
			Name:  "Work Context Team",
			Slug:  "work-context-team-" + teamID,
			Path:  "work-context-team-" + teamID,
		}); err != nil {
			return err
		}
		if err := testStore.AddTeamMember(ctx, teamID, ownerID, "member"); err != nil {
			return err
		}
		if err := testStore.CreateRole(ctx, &gen.Role{
			Id:          roleID,
			Name:        "evidence writer " + roleID,
			Description: "test-only plugin-contributed permission",
			OrgId:       orgID,
			Permissions: []*gen.Permission{{
				Resource: "evidence",
				Action:   "append",
			}},
		}); err != nil {
			return err
		}
		return testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id:          business.NewIDString(),
			SubjectId:   agent.ID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			RoleId:      roleID,
			OrgId:       orgID,
			Scope:       "project:one",
		})
	}))

	verified := auth.WithVerifiedDatabaseIdentity(testCtx, ownerID, orgID)
	facts, err := testStore.ResolveWorkContextAuthority(
		verified,
		orgID,
		ownerID,
		agent.ID,
		[]business.WorkContextPermission{{
			ResourceKind: "evidence",
			Action:       "append",
			ResourceID:   "project:one",
		}},
	)
	require.NoError(t, err)
	require.Equal(t, agent.ID, facts.Actor.ID)
	require.Equal(t, business.PrincipalKindAgent, facts.Actor.Kind)
	require.Equal(t, []string{teamID}, facts.AttributionTeamIDs)
	require.Positive(t, facts.OrganizationRevision)
	require.Positive(t, facts.PrincipalRevision)
	firstRevision := facts.EffectiveRevision()

	_, err = testStore.ResolveWorkContextAuthority(
		verified,
		orgID,
		ownerID,
		agent.ID,
		[]business.WorkContextPermission{{
			ResourceKind: "evidence",
			Action:       "append",
		}},
	)
	require.Error(t, err, "a resource-scoped grant must not widen into wildcard authority")
	var permissionErr *business.StoreError
	require.ErrorAs(t, err, &permissionErr)
	require.Equal(t, business.ErrTypePermission, permissionErr.StoreErrorType)

	_, err = testStore.ResolveWorkContextAuthority(
		verified,
		orgID,
		ownerID,
		agent.ID,
		[]business.WorkContextPermission{{
			ResourceKind: "evidence",
			Action:       "append",
			ResourceID:   "project:two",
		}},
	)
	require.Error(t, err, "a scoped grant must not authorize another resource")

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.AddTeamMember(ctx, teamID, ownerID, "admin")
	}))
	afterMutation, err := testStore.ResolveWorkContextAuthority(
		verified,
		orgID,
		ownerID,
		agent.ID,
		[]business.WorkContextPermission{{
			ResourceKind: "evidence",
			Action:       "append",
			ResourceID:   "project:one",
		}},
	)
	require.NoError(t, err)
	require.Greater(t, afterMutation.EffectiveRevision(), firstRevision,
		"authorization mutation must advance the signed effective revision")
}

func TestWorkContextAuthorityRejectsCallerSelectedTenantAndRevokedActor(t *testing.T) {
	ownerID := seedUser(t)
	orgA := seedOrg(t, ownerID)
	orgB := seedOrg(t, ownerID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgA}).AddOrgMember(
		testCtx, ownerID, "owner",
	))
	require.NoError(t, testStore.As(business.Identity{OrgID: orgB}).AddOrgMember(
		testCtx, ownerID, "owner",
	))
	agent := seedAgentPrincipal(t, orgA, "test.codefly.dev/revoked-work-context:0.1.0")

	verifiedA := auth.WithVerifiedDatabaseIdentity(testCtx, ownerID, orgA)
	_, err := testStore.ResolveWorkContextAuthority(
		verifiedA, orgB, ownerID, "", nil,
	)
	require.ErrorIs(t, err, auth.ErrVerifiedDatabaseScopeMismatch,
		"request artifact must not override the verified service-postgres tenant")

	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction key
		_, updateErr := tx.Exec(ctx, `
			UPDATE principals
			   SET revoked_at = CURRENT_TIMESTAMP,
			       revoked_reason = 'test revocation'
			 WHERE id = $1`,
			agent.ID,
		)
		return updateErr
	}))
	_, err = testStore.ResolveWorkContextAuthority(
		verifiedA, orgA, ownerID, agent.ID, nil,
	)
	require.Error(t, err)
	var notFound *business.StoreError
	require.ErrorAs(t, err, &notFound)
	require.Equal(t, business.ErrTypeNotFound, notFound.StoreErrorType)
}

// TestWorkContextAuthorityResolvesOnlyRegisteredAgentActor pins the #419
// owner-binding invariant: the delegated actor a mint/exchange resolves must be
// a *registered* agent principal in the owner's org. An id that was never
// registered through CreateAgentPrincipal, and a registered principal of the
// wrong kind (a service principal derived from an api_key), both fail closed —
// so registration through PrincipalService is the single source of truth the
// owner-binding checks against.
func TestWorkContextAuthorityResolvesOnlyRegisteredAgentActor(t *testing.T) {
	ownerID := seedUser(t)
	orgID := seedOrg(t, ownerID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, ownerID, "owner",
	))

	verified := auth.WithVerifiedDatabaseIdentity(testCtx, ownerID, orgID)

	// An id that never went through CreateAgentPrincipal is not an actor.
	_, err := testStore.ResolveWorkContextAuthority(
		verified, orgID, ownerID, business.NewIDString(), nil,
	)
	require.Error(t, err, "an unregistered principal id must not resolve as an actor")
	var unregistered *business.StoreError
	require.ErrorAs(t, err, &unregistered)
	require.Equal(t, business.ErrTypeNotFound, unregistered.StoreErrorType)

	// A registered principal of kind=service (the api_key-derived kind) is a
	// real row in the owner's org, but it is not a delegated-execution actor.
	serviceID := business.NewIDString()
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction key
		_, insertErr := tx.Exec(ctx, `
			INSERT INTO principals (id, kind, display_name, org_id, agent_identifier, created_at)
			VALUES ($1, 'service', 'test service principal', $2, NULL, CURRENT_TIMESTAMP)`,
			serviceID, orgID,
		)
		return insertErr
	}))
	_, err = testStore.ResolveWorkContextAuthority(
		verified, orgID, ownerID, serviceID, nil,
	)
	require.Error(t, err, "a service principal must not resolve as a Work Context actor")
	var wrongKind *business.StoreError
	require.ErrorAs(t, err, &wrongKind)
	require.Equal(t, business.ErrTypeNotFound, wrongKind.StoreErrorType)

	// The registered agent principal in the same org does resolve.
	agent := seedAgentPrincipal(t, orgID, "test.codefly.dev/registered-actor:0.1.0")
	facts, err := testStore.ResolveWorkContextAuthority(
		verified, orgID, ownerID, agent.ID, nil,
	)
	require.NoError(t, err)
	require.Equal(t, agent.ID, facts.Actor.ID)
	require.Equal(t, business.PrincipalKindAgent, facts.Actor.Kind)
}

// TestRevokingAgentPrincipalStalesOutstandingWorkContext pins the #440
// lifecycle deliverable: disabling a delegated actor through the RevokePrincipal
// business path takes immediate effect on Work Contexts already signed against
// it. The revoked actor no longer resolves, so the consumer-side revision check
// for an outstanding context reports it stale rather than honoring the old
// signature.
func TestRevokingAgentPrincipalStalesOutstandingWorkContext(t *testing.T) {
	ownerID := seedUser(t)
	orgID := seedOrg(t, ownerID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, ownerID, "owner",
	))
	agent := seedAgentPrincipal(t, orgID, "test.codefly.dev/revoke-stales:0.1.0")

	verified := auth.WithVerifiedDatabaseIdentity(testCtx, ownerID, orgID)
	facts, err := testStore.ResolveWorkContextAuthority(verified, orgID, ownerID, agent.ID, nil)
	require.NoError(t, err)
	revision := facts.EffectiveRevision()
	subjects := []business.WorkContextRevisionSubject{{PrincipalID: agent.ID}}

	require.NoError(t, testStore.CheckWorkContextAuthorizationRevision(
		verified, orgID, ownerID, revision, subjects,
	), "the context is valid while the actor is a live registered agent")

	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	require.NoError(t, svc.RevokePrincipal(testCtx, agent.ID, "delegation disabled"))

	err = testStore.CheckWorkContextAuthorizationRevision(
		verified, orgID, ownerID, revision, subjects,
	)
	require.ErrorIs(t, err, business.ErrWorkContextAuthorizationStale,
		"revoking the actor must immediately stale an outstanding Work Context")
}

func TestWorkContextConsumerAuthorityUsesCurrentRevisionAndExactEvidenceRead(t *testing.T) {
	ownerID := seedUser(t)
	readerID := seedUser(t)
	orgID := seedOrg(t, ownerID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, ownerID, "owner",
	))
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, readerID, "member",
	))

	ownerContext := auth.WithVerifiedDatabaseIdentity(testCtx, ownerID, orgID)
	facts, err := testStore.ResolveWorkContextAuthority(
		ownerContext,
		orgID,
		ownerID,
		"",
		[]business.WorkContextPermission{{
			ResourceKind: "evidence",
			Action:       "append",
			ResourceID:   "task:one",
		}},
	)
	require.NoError(t, err)
	revision := facts.EffectiveRevision()
	revisionSubjects := []business.WorkContextRevisionSubject{{
		PrincipalID: ownerID,
		Permissions: []business.WorkContextPermission{{
			ResourceKind: "evidence",
			Action:       "append",
			ResourceID:   "task:one",
		}},
	}}
	require.NoError(t, testStore.CheckWorkContextAuthorizationRevision(
		ownerContext,
		orgID,
		ownerID,
		revision,
		revisionSubjects,
	))

	teamID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateTeam(ctx, &gen.Team{
			Id:    teamID,
			OrgId: orgID,
			Name:  "Revision bump team",
			Slug:  "revision-bump-" + teamID,
			Path:  "revision-bump-" + teamID,
		}); err != nil {
			return err
		}
		return testStore.AddTeamMember(ctx, teamID, ownerID, "member")
	}))
	err = testStore.CheckWorkContextAuthorizationRevision(
		ownerContext,
		orgID,
		ownerID,
		revision,
		revisionSubjects,
	)
	require.ErrorIs(t, err, business.ErrWorkContextAuthorizationStale)

	readerContext := auth.WithVerifiedDatabaseIdentity(testCtx, readerID, orgID)
	require.NoError(t, testStore.AuthorizeEvidenceRead(
		readerContext, orgID, readerID, readerID, "task:self", "",
	), "a current member may read their own exact Evidence resource")
	err = testStore.AuthorizeEvidenceRead(
		readerContext, orgID, readerID, ownerID, "task:one", "",
	)
	require.ErrorIs(t, err, business.ErrEvidenceReadDenied)

	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateRole(ctx, &gen.Role{
			Id:          roleID,
			Name:        "Evidence reader " + roleID,
			Description: "test exact Evidence read authority",
			OrgId:       orgID,
			Permissions: []*gen.Permission{{Resource: "evidence", Action: "read"}},
		}); err != nil {
			return err
		}
		return testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id:          business.NewIDString(),
			SubjectId:   readerID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			RoleId:      roleID,
			OrgId:       orgID,
			Scope:       "task:one",
		})
	}))
	require.NoError(t, testStore.AuthorizeEvidenceRead(
		readerContext, orgID, readerID, ownerID, "task:one", "session:one",
	))
	err = testStore.AuthorizeEvidenceRead(
		readerContext, orgID, readerID, ownerID, "task:two", "",
	)
	require.ErrorIs(t, err, business.ErrEvidenceReadDenied)
	err = testStore.AuthorizeEvidenceRead(
		readerContext, orgID, readerID, "", "", "",
	)
	require.ErrorIs(t, err, business.ErrEvidenceReadDenied,
		"a Task-scoped grant must not widen into tenant-directory access")

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id:          business.NewIDString(),
			SubjectId:   readerID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			RoleId:      roleID,
			OrgId:       orgID,
		})
	}))
	require.NoError(t, testStore.AuthorizeEvidenceRead(
		readerContext, orgID, readerID, "", "", "",
	), "an unscoped Evidence reader may use the tenant directory")
}

func TestDeprecatedUserSubjectAliasPersistsCanonicalPrincipal(t *testing.T) {
	ownerID := seedUser(t)
	orgID := seedOrg(t, ownerID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, ownerID, "owner",
	))
	roleID := business.NewIDString()
	assignmentID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateRole(ctx, &gen.Role{
			Id:          roleID,
			Name:        "legacy principal alias " + roleID,
			Description: "wire-compatibility regression",
			OrgId:       orgID,
			Permissions: []*gen.Permission{{
				Resource: "evidence",
				Action:   "append",
			}},
		}); err != nil {
			return err
		}
		return testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id: assignmentID,
			// SUBJECT_KIND_USER remains numeric alias 1 for old clients. The
			// store must never persist the obsolete user-only meaning.
			SubjectId:   ownerID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
			RoleId:      roleID,
			OrgId:       orgID,
		})
	}))

	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction key
		var subjectKind string
		if err := tx.QueryRow(ctx, `
			SELECT subject_kind
			FROM role_assignments
			WHERE id = $1`,
			assignmentID,
		).Scan(&subjectKind); err != nil {
			return err
		}
		require.Equal(t, "principal", subjectKind)
		return nil
	}))

	var assignments []*gen.RoleAssignment
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		assignments, err = testStore.ListRoleAssignments(
			ctx,
			orgID,
			ownerID,
			gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		)
		return err
	}))
	require.Len(t, assignments, 1)
	require.Equal(
		t,
		gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		assignments[0].SubjectKind,
	)
}

func TestOrganizationDeletionDoesNotResurrectAuthorizationRevisions(t *testing.T) {
	ownerID := seedUser(t)
	orgID := seedOrg(t, ownerID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(
		testCtx, ownerID, "owner",
	))
	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateRole(ctx, &gen.Role{
			Id:          roleID,
			Name:        "cascade regression " + roleID,
			Description: "authorization revisions must not outlive their organization",
			OrgId:       orgID,
			Permissions: []*gen.Permission{{Resource: "evidence", Action: "read"}},
		}); err != nil {
			return err
		}
		return testStore.AssignRole(ctx, &gen.RoleAssignment{
			Id:          business.NewIDString(),
			SubjectId:   ownerID,
			SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			RoleId:      roleID,
			OrgId:       orgID,
		})
	}))

	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction key
		if _, err := tx.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
			return err
		}
		var organizationRevisions, principalRevisions int
		if err := tx.QueryRow(ctx, `
			SELECT
				(SELECT count(*) FROM organization_authorization_revisions WHERE org_id = $1),
				(SELECT count(*) FROM principal_authorization_revisions WHERE org_id = $1)`,
			orgID,
		).Scan(&organizationRevisions, &principalRevisions); err != nil {
			return err
		}
		require.Zero(t, organizationRevisions)
		require.Zero(t, principalRevisions)
		return nil
	}))
}
