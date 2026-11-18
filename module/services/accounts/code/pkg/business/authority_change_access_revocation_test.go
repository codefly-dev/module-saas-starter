//go:build !pure

package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/redisstate"
)

// Every path below changes what a person may do, and the database answers each
// by revoking that person's session rows from a trigger. Revoked rows end
// refreshing; the access tokens already issued are admitted from the
// revocation list alone, and the trigger writes nothing there. So until a
// marker is written the removed member, the demoted administrator and the
// thief replaying a refresh token all keep a working access token until it
// expires. One test per path; each fails without its marker.

// A removed member's token must stop working when the removal commits, and
// nobody else's.
func TestRemovingAMemberRevokesTheirOutstandingAccessTokens(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	member := mustMember(t, owner, org, "member@example.com", "example-member")
	ownerAccess := loginAndRefresh(t, owner)
	memberAccess := loginAndRefresh(t, member)
	requireAccessLive(t, memberAccess)

	require.NoError(t, testService.RemoveOrgMember(testCtx, owner,
		&gen.RemoveOrgMemberRequest{OrgId: org, UserId: member}))

	requireAccessRevoked(t, memberAccess)
	requireAccessLive(t, ownerAccess)
}

func TestLeavingAnOrganizationRevokesTheLeaversOutstandingAccessTokens(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	member := mustMember(t, owner, org, "member@example.com", "example-member")
	ownerAccess := loginAndRefresh(t, owner)
	memberAccess := loginAndRefresh(t, member)

	require.NoError(t, testService.LeaveOrganization(testCtx, member,
		&gen.LeaveOrganizationRequest{OrgId: org}))

	requireAccessRevoked(t, memberAccess)
	requireAccessLive(t, ownerAccess)
}

// Deleting an organization removes every membership in it, so every former
// member's token dies with it.
func TestDeletingAnOrganizationRevokesEveryMembersOutstandingAccessTokens(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	member := mustMember(t, owner, org, "member@example.com", "example-member")
	bystander := mustUser(t, "bystander@example.com", "example-bystander")
	ownerAccess := loginAndRefresh(t, owner)
	memberAccess := loginAndRefresh(t, member)
	bystanderAccess := loginAndRefresh(t, bystander)

	require.NoError(t, testService.DeleteOrganization(testCtx, owner,
		&gen.DeleteOrganizationRequest{OrgId: org, ConfirmSlug: "example-owner-org"}))

	requireAccessRevoked(t, ownerAccess)
	requireAccessRevoked(t, memberAccess)
	requireAccessLive(t, bystanderAccess)
}

func TestGrantingAPlatformRoleRevokesTheGranteesOutstandingAccessTokens(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	adminID, _ := mustUserAndOrg(t, testCtx, "admin@example.com", "example-admin", "Acme")
	grantPlatformRole(t, adminID, "super_admin", adminID)
	target := mustUser(t, "target@example.com", "example-target")
	adminAccess := loginAndRefresh(t, adminID)
	targetAccess := loginAndRefresh(t, target)

	require.NoError(t, testService.GrantPlatformRole(testCtx, adminID,
		&gen.GrantPlatformRoleRequest{UserId: target, PlatformRole: "support"}))
	t.Cleanup(func() { _ = controlPlaneRevokePlatformRole(testCtx, target) })

	requireAccessRevoked(t, targetAccess)
	requireAccessLive(t, adminAccess)
}

// The widest authority the host grants: a token that still states a platform
// role after the role was taken away is the one that matters most.
func TestRevokingAPlatformRoleRevokesTheHoldersOutstandingAccessTokens(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	adminID, _ := mustUserAndOrg(t, testCtx, "admin@example.com", "example-admin", "Acme")
	grantPlatformRole(t, adminID, "super_admin", adminID)
	target := mustUser(t, "target@example.com", "example-target")
	grantPlatformRole(t, target, "support", adminID)
	adminAccess := loginAndRefresh(t, adminID)
	targetAccess := loginAndRefresh(t, target)

	require.NoError(t, testService.RevokePlatformRole(testCtx, adminID,
		&gen.RevokePlatformRoleRequest{UserId: target}))

	requireAccessRevoked(t, targetAccess)
	requireAccessLive(t, adminAccess)
}

// Removing a second factor changes what the account's sessions were proven
// with; a token minted while the factor stood must not survive it.
func TestRevokingAnMFADeviceRevokesTheUsersOutstandingAccessTokens(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	user := mustUser(t, "target@example.com", "example-target")
	bystander := mustUser(t, "bystander@example.com", "example-bystander")
	secret, _, err := testService.SetupTOTP(testCtx, user)
	require.NoError(t, err)
	require.NoError(t, testService.VerifyTOTP(testCtx, user, testTOTP(t, secret, time.Now())))
	var devices []*business.MFADevice
	require.NoError(t, testStore.WithUserTx(testCtx, user, func(ctx context.Context) error {
		var err error
		devices, err = testStore.ListMFADevices(ctx, user)
		return err
	}))
	require.Len(t, devices, 1)

	// One token, not a refreshed pair: a session of an enrolled account that
	// did not present the factor is refused a refresh, which is not this test's
	// subject.
	login := mintAdminLogin(t, user)
	userAccess := issuedAccess{beforeRefresh: login.AccessToken, afterRefresh: login.AccessToken}
	bystanderAccess := loginAndRefresh(t, bystander)
	requireAccessLive(t, userAccess)

	require.NoError(t, testService.RevokeMFADevice(testCtx, user, devices[0].ID))

	requireAccessRevoked(t, userAccess)
	requireAccessLive(t, bystanderAccess)
}

// A refresh token presented twice is a stolen one: every session of the user
// is revoked. The access tokens the thief and the owner hold are the ones that
// have to stop.
func TestReplayingARefreshTokenRevokesTheUsersOutstandingAccessTokens(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	user := mustUser(t, "target@example.com", "example-target")
	bystander := mustUser(t, "bystander@example.com", "example-bystander")
	bystanderAccess := loginAndRefresh(t, bystander)

	login := mintAdminLogin(t, user)
	rotated, err := testService.JWTMinter().VerifyRefresh(testCtx, login.RefreshToken)
	require.NoError(t, err)
	otherDevice := mintAdminLogin(t, user)
	access := issuedAccess{beforeRefresh: login.AccessToken, afterRefresh: rotated.AccessToken}
	requireAccessLive(t, access)

	_, err = testService.JWTMinter().VerifyRefresh(testCtx, login.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshReuse)

	requireAccessRevoked(t, access)
	_, err = testService.JWTMinter().VerifyAccess(otherDevice.AccessToken)
	require.ErrorIs(t, err, auth.ErrTokenRevoked, "the replay revokes every session of the user, so every device's access token")
	requireAccessLive(t, bystanderAccess)
}

// A role assigned on a scope, or taken back, changes what the person's token
// states for that organization; the database revokes their sessions for it.
func TestAssigningOrRevokingAScopedRoleRevokesTheSubjectsOutstandingAccessTokens(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	member := mustMember(t, owner, org, "member@example.com", "example-member")
	created, err := testService.CreateRole(testCtx, owner, &gen.CreateRoleRequest{
		Name: "example-reader", OrgId: org,
		Permissions: []*gen.Permission{{Resource: "dashboards", Action: "read"}},
	})
	require.NoError(t, err)
	ownerAccess := loginAndRefresh(t, owner)

	assigned := loginAndRefresh(t, member)
	_, err = testService.AssignRole(testCtx, &gen.AssignRoleRequest{
		SubjectId: member, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		RoleId: created.Role.Id, OrgId: org, Scope: "example/collection",
	})
	require.NoError(t, err)
	requireAccessRevoked(t, assigned)

	revoked := loginAndRefresh(t, member)
	require.NoError(t, testService.RevokeRole(testCtx, owner, &gen.RevokeRoleRequest{
		SubjectId: member, RoleId: created.Role.Id, OrgId: org, Scope: "example/collection",
	}))
	requireAccessRevoked(t, revoked)

	requireAccessLive(t, ownerAccess)
}
