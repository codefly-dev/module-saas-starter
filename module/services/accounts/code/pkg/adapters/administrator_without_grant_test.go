package adapters

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// An organization administrator who holds no grant is issued nothing at a
// module that is neither role-gated nor owner-aware. The role "admin" names who
// administers; it holds no scope. This is the journey the wildcard used to serve:
// the store answered the role as "*:*" and the legacy Work Context mint began
// with a role IN ('owner','admin') arm, so such an administrator was issued full
// scopes at every such module (on a composed platform: runtime, tracing,
// evaluation, model). `records` is that kind of audience in this composition, so a
// refusal here is not an owner-aware module refusing anyway.
//
// dev is an administrator of the organization with no role assigned. admin
// grants and withdraws the role (granting is an administrator's act); the
// subject of the journey is dev, who holds only what a grant gives them.
func testAdministratorWithoutAGrant(t *testing.T, conn grpc.ClientConnInterface, admin, dev context.Context, devID string) {
	contexts := gen.NewWorkContextServiceClient(conn)
	permissions := gen.NewPermissionServiceClient(conn)
	start := func() (*gen.IssuedWorkContext, error) {
		return contexts.StartTask(dev, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(),
			Audience: "records", AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "example-entries", Actions: []string{"read"}}}})
	}

	_, err := start()
	require.Error(t, err, "an administrator with no grant is issued nothing at a module that checks no owner")
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.NotEmpty(t, status.Convert(err).Message(), "the refusal names its reason")

	roles, err := permissions.ListRoles(admin, &gen.ListRolesRequest{OrgId: fixtureOrganization})
	require.NoError(t, err)
	var operator string
	for _, role := range roles.GetRoles() {
		if role.GetName() == "Fixture operator" {
			operator = role.GetId()
		}
	}
	require.NotEmpty(t, operator, "the fixture's role over example-entries")
	assign := &gen.AssignRoleRequest{SubjectId: devID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: operator, OrgId: fixtureOrganization}
	_, err = permissions.AssignRole(admin, assign)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = permissions.RevokeRole(admin, &gen.RevokeRoleRequest{SubjectId: devID, RoleId: operator, OrgId: fixtureOrganization})
	})

	issued, err := start()
	require.NoError(t, err, "the same administrator, holding an explicit grant, is issued the scope")
	require.Equal(t, []string{"example-entries"}, carriedKinds(t, issued))

	_, err = permissions.RevokeRole(admin, &gen.RevokeRoleRequest{SubjectId: devID, RoleId: operator, OrgId: fixtureOrganization})
	require.NoError(t, err)
	_, err = start()
	require.Equal(t, codes.PermissionDenied, status.Code(err), "revoking the grant withdraws it: %v", err)
}
