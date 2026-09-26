//go:build !pure

package infra_test

import (
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/stretchr/testify/require"
)

// A contributed permission declared for every member (members: true) is held
// by a plain member of the organization as a person, in both the mint and the
// revision recheck, and by nobody else: not an outsider, not a delegated agent
// actor, and not for any other permission.
func TestWorkContextMemberPermissionHeldByMembersOnly(t *testing.T) {
	restore := infra.UseMemberPermissions(func(resource, action string) bool {
		return resource == "example.jobs" && action == "list"
	})
	t.Cleanup(restore)

	ownerID := seedUser(t)
	orgID := seedOrg(t, ownerID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, ownerID, "owner"))
	memberID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, memberID, "member"))
	outsiderID := seedUser(t)
	outsiderOrg := seedOrg(t, outsiderID)
	require.NoError(t, testStore.As(business.Identity{OrgID: outsiderOrg}).AddOrgMember(testCtx, outsiderID, "owner"))
	agent := seedAgentPrincipal(t, orgID, "test.codefly.dev/member-permission:0.1.0")

	list := []business.WorkContextPermission{{ResourceKind: "example.jobs", Action: "list"}}
	inspect := []business.WorkContextPermission{{ResourceKind: "example.jobs", Action: "inspect"}}
	isPermissionDenied := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var storeErr *business.StoreError
		require.ErrorAs(t, err, &storeErr)
		require.Equal(t, business.ErrTypePermission, storeErr.StoreErrorType)
	}

	member := auth.WithVerifiedDatabaseIdentity(testCtx, memberID, orgID)
	facts, err := testStore.ResolveWorkContextAuthority(member, orgID, memberID, "", list)
	require.NoError(t, err, "a plain member holds a member permission")
	require.NoError(t, testStore.CheckWorkContextAuthorizationRevision(member, orgID, memberID, facts.EffectiveRevision(),
		[]business.WorkContextRevisionSubject{{PrincipalID: memberID, Permissions: list}}),
		"the revision recheck honours the same grant")

	scoped := []business.WorkContextPermission{{ResourceKind: "example.jobs", Action: "list", ResourceID: "one"}}
	_, err = testStore.ResolveWorkContextAuthority(member, orgID, memberID, "", scoped)
	require.NoError(t, err, "a member permission is organization-wide, like a NULL-scope assignment")

	_, err = testStore.ResolveWorkContextAuthority(member, orgID, memberID, "", inspect)
	isPermissionDenied(t, err)

	owner := auth.WithVerifiedDatabaseIdentity(testCtx, ownerID, orgID)
	_, err = testStore.ResolveWorkContextAuthority(owner, orgID, ownerID, agent.ID, list)
	isPermissionDenied(t, err) // the agent actor is checked without membership and holds no grant

	outsider := auth.WithVerifiedDatabaseIdentity(testCtx, outsiderID, orgID)
	_, err = testStore.ResolveWorkContextAuthority(outsider, orgID, outsiderID, "", list)
	require.Error(t, err, "a non-member never holds a member permission")

	restore()
	_, err = testStore.ResolveWorkContextAuthority(member, orgID, memberID, "", list)
	isPermissionDenied(t, err) // without the declaration a plain member holds nothing
}
