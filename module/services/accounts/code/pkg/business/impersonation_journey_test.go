package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// impersonationFixture is the shape every case here needs: a support admin who
// belongs to no organization of the target's, and a target organization with an
// owner and an ordinary member.
type impersonationFixture struct {
	supportID string
	ownerID   string
	memberID  string
	orgID     string
}

// grantPlatformRole grants and, on cleanup, revokes. platform_admins.granted_by
// references users, and ClearAll does not clear that table, so a grant left
// behind aborts the next test's cleanup transaction wholesale.
func grantPlatformRole(t *testing.T, userID, role, grantedBy string) {
	t.Helper()
	require.NoError(t, testStore.GrantPlatformRole(testCtx, userID, role, grantedBy))
	t.Cleanup(func() { _ = testStore.RevokePlatformRole(testCtx, userID) })
}

func seedImpersonationFixture(t *testing.T, name string) impersonationFixture {
	t.Helper()
	ctx := testCtx

	supportID, _ := mustUserAndOrg(t,
		ctx, "support-"+name+"@example.com", "support-"+name, "Support Org "+name)
	grantPlatformRole(t, supportID, "support", supportID)

	// Registration also creates each user a "Personal" organization, and the
	// impersonation session takes the target's first organization by name, so the
	// target organization here is named to sort ahead of it.
	ownerID, orgID := mustUserAndOrg(t,
		ctx, "owner-"+name+"@example.com", "owner-"+name, "Acme Org "+name)

	memberResp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "member-" + name + "@example.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "member-" + name, ProviderEmail: "member-" + name + "@example.com",
		},
	})
	require.NoError(t, err)
	require.NoError(t, testService.AddOrgMember(ctx, ownerID, &gen.AddOrgMemberRequest{
		OrgId:  orgID,
		UserId: memberResp.User.Uuid,
		Role:   gen.OrgRole_ORG_ROLE_MEMBER,
	}))

	return impersonationFixture{
		supportID: supportID,
		ownerID:   ownerID,
		memberID:  memberResp.User.Uuid,
		orgID:     orgID,
	}
}

// The token a support admin gets back names them as the actor and the target as
// the effective subject, carries the target's organization context, and carries
// no platform role for either party.
func TestImpersonationTokenSplitsActorFromEffectiveSubject(t *testing.T) {
	clearData(t)
	fixture := seedImpersonationFixture(t, "split")

	issued, err := testService.ImpersonateUser(testCtx, fixture.supportID,
		&gen.ImpersonateUserRequest{UserId: fixture.memberID})
	require.NoError(t, err)

	identity, err := testService.JWTMinter().VerifyAccess(issued.AccessToken)
	require.NoError(t, err)
	require.Equal(t, fixture.supportID, identity.UserID.String())
	require.Equal(t, fixture.memberID, identity.ActingAsUserID.String())
	require.Equal(t, fixture.orgID, identity.OrgID.String(),
		"session org must be the target's organization")
	require.Empty(t, identity.PlatformRole, "an impersonation token must carry no platform role")

	projected := auth.RequestIdentityOf(identity)
	require.True(t, projected.Impersonated())
	require.Equal(t, fixture.supportID, projected.RealActorID())
	require.Equal(t, fixture.memberID, projected.EffectiveSubjectID())

	// The issuance record itself names both parties: the admin as the actor and
	// the target as the resource acted on.
	// audit_events is append-only, so ClearAll leaves earlier runs' rows in
	// place; scope the read to this run's actor.
	entries, _, _, err := testService.QueryAuditLog(testCtx, business.AuditQuery{
		ActorID:   fixture.supportID,
		EventType: string(business.EventPlatformImpersonated),
		PageSize:  10,
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, fixture.supportID, entries[0].ActorID)
	require.Equal(t, fixture.memberID, entries[0].ResourceID)
}

// The full journey: an action performed while impersonating is attributed to the
// effective subject and, in the same row, to the admin behind it. Before this
// contract existed the emitter looked for impersonation in headers nothing set,
// so the row was indistinguishable from the target acting alone.
func TestImpersonatedActionRecordsBothIdentitiesInAudit(t *testing.T) {
	clearData(t)
	fixture := seedImpersonationFixture(t, "audit")

	issued, err := testService.ImpersonateUser(testCtx, fixture.supportID,
		&gen.ImpersonateUserRequest{UserId: fixture.ownerID})
	require.NoError(t, err)
	identity, err := testService.JWTMinter().VerifyAccess(issued.AccessToken)
	require.NoError(t, err)

	impersonated := auth.WithVerifiedRequestIdentity(testCtx, auth.RequestIdentityOf(identity))
	_, err = testService.CreateTeam(impersonated, fixture.ownerID, &gen.CreateTeamRequest{
		OrgId: fixture.orgID,
		Name:  "Support Investigation",
	})
	require.NoError(t, err)

	entries, _, _, err := testService.QueryAuditLog(testCtx, business.AuditQuery{
		OrgID:     fixture.orgID,
		EventType: string(business.EventTeamCreated),
		PageSize:  10,
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, fixture.ownerID, entries[0].ActorID, "the action ran as the effective subject")
	require.True(t, entries[0].IsImpersonated)
	require.Equal(t, fixture.supportID, entries[0].ImpersonatedBy, "the admin behind it stays attributable")

	// The same action on an ordinary session records no impersonator, so the
	// discriminator means what it says.
	_, err = testService.CreateTeam(testCtx, fixture.ownerID, &gen.CreateTeamRequest{
		OrgId: fixture.orgID,
		Name:  "Ordinary Team",
	})
	require.NoError(t, err)
	entries, _, _, err = testService.QueryAuditLog(testCtx, business.AuditQuery{
		OrgID:      fixture.orgID,
		EventType:  string(business.EventTeamCreated),
		ResourceID: "",
		PageSize:   10,
	})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var ordinary *business.AuditEntry
	for i := range entries {
		if !entries[i].IsImpersonated {
			ordinary = &entries[i]
		}
	}
	require.NotNil(t, ordinary, "the ordinary action must be recorded as not impersonated")
	require.Empty(t, ordinary.ImpersonatedBy)
}

// Impersonation does not compose: a session already acting as someone else holds
// no platform authority, so it cannot mint a further impersonation token — not
// even when the subject it is acting as is a platform administrator.
func TestImpersonatedSessionCannotImpersonateAgain(t *testing.T) {
	clearData(t)
	fixture := seedImpersonationFixture(t, "nested")
	grantPlatformRole(t, fixture.ownerID, "super_admin", fixture.supportID)

	issued, err := testService.ImpersonateUser(testCtx, fixture.supportID,
		&gen.ImpersonateUserRequest{UserId: fixture.ownerID})
	require.NoError(t, err)
	identity, err := testService.JWTMinter().VerifyAccess(issued.AccessToken)
	require.NoError(t, err)

	impersonated := auth.WithVerifiedRequestIdentity(testCtx, auth.RequestIdentityOf(identity))
	_, err = testService.ImpersonateUser(impersonated, fixture.ownerID,
		&gen.ImpersonateUserRequest{UserId: fixture.memberID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "permission denied")
}

// A support session cannot be opened on an account that is no longer usable, so
// an impersonated session can never outlive the target's own lifecycle.
func TestImpersonationRefusesInactiveTarget(t *testing.T) {
	clearData(t)
	fixture := seedImpersonationFixture(t, "lifecycle")

	superID, _ := mustUserAndOrg(t, testCtx, "super-lifecycle@example.com", "super-lifecycle", "Super Org")
	grantPlatformRole(t, superID, "super_admin", superID)
	require.NoError(t, testService.SuspendUser(testCtx, superID, &gen.SuspendUserRequest{
		UserId: fixture.memberID,
		Reason: "test",
	}))

	_, err := testService.ImpersonateUser(testCtx, fixture.supportID,
		&gen.ImpersonateUserRequest{UserId: fixture.memberID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not active")
}

// A target who belongs to several organizations always yields the same session
// organization — the first by name — rather than whichever row the planner
// happened to return first.
func TestImpersonationTargetOrgSelectionIsDeterministic(t *testing.T) {
	clearData(t)
	fixture := seedImpersonationFixture(t, "orgsel")

	// "Acme Affiliates" sorts ahead of both the fixture's "Acme Org orgsel" and
	// the "Personal" organization registration created.
	first, err := testService.CreateOrganization(testCtx, fixture.memberID, &gen.CreateOrganizationRequest{
		Name: "Acme Affiliates", Slug: "acme-affiliates-orgsel",
	})
	require.NoError(t, err)

	for range 3 {
		issued, err := testService.ImpersonateUser(testCtx, fixture.supportID,
			&gen.ImpersonateUserRequest{UserId: fixture.memberID})
		require.NoError(t, err)
		identity, err := testService.JWTMinter().VerifyAccess(issued.AccessToken)
		require.NoError(t, err)
		require.Equal(t, first.Organization.Id, identity.OrgID.String())
	}
}

// The audit row's impersonation identity survives a round trip through the
// store, which is where the two ids had nowhere to live before migration 123.
func TestAuditStorePersistsImpersonationIdentity(t *testing.T) {
	clearData(t)
	fixture := seedImpersonationFixture(t, "persist")

	entry := business.AuditEntry{
		ActorID:        fixture.ownerID,
		ActorType:      "user",
		EventType:      business.EventTeamCreated,
		Resource:       "team",
		OrgID:          fixture.orgID,
		IsImpersonated: true,
		ImpersonatedBy: fixture.supportID,
	}
	require.NoError(t, testStore.WithOrgTx(testCtx, fixture.orgID, func(ctx context.Context) error {
		return testStore.InsertAuditEvent(ctx, entry)
	}))

	entries, _, _, err := testService.QueryAuditLog(testCtx, business.AuditQuery{
		OrgID:     fixture.orgID,
		EventType: string(business.EventTeamCreated),
		PageSize:  10,
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.True(t, entries[0].IsImpersonated)
	require.Equal(t, fixture.supportID, entries[0].ImpersonatedBy)
}
