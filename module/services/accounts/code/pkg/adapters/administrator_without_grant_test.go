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

// An organisation role decides administration only: it reaches no module, on
// this host's own Work Context mints as on the host Issue. dev administers the
// organisation and holds no role on records' installation, so the mint at
// records is refused, by the core's name for it. Assigning dev an organisation
// role over the kind records serves changes nothing: the journey the role
// catalogue used to serve (a role over a module's kinds, assigned in the
// organisation, issued at the module) is gone. A role on records' installation
// is what reaches it, and it holds what that role holds: the user role reads,
// and the organisation role's write adds nothing to it.
//
// admin assigns the organisation role (granting is an administrator's act);
// seat and unseat grant and revoke dev a role on records' installation.
func testAnOrganisationRoleReachesNoModule(t *testing.T, conn grpc.ClientConnInterface, admin context.Context, dev func() context.Context, devID string, seat, unseat func(role string)) {
	contexts := gen.NewWorkContextServiceClient(conn)
	permissions := gen.NewPermissionServiceClient(conn)
	start := func(actions ...string) (*gen.IssuedWorkContext, error) {
		return contexts.StartTask(dev(), &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(),
			Audience: "records", AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "example-entries", Actions: actions}}})
	}
	refusedWithoutARole := func(err error, why string) {
		t.Helper()
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%s: %v", why, err)
		require.Contains(t, status.Convert(err).Message(), "holds no role on records", why)
	}

	_, err := start("read")
	refusedWithoutARole(err, "an administrator holding no role on the installation is issued nothing at the module")

	roles, err := permissions.ListRoles(admin, &gen.ListRolesRequest{OrgId: fixtureOrganization})
	require.NoError(t, err)
	var operator string
	for _, role := range roles.GetRoles() {
		if role.GetName() == "Fixture operator" {
			operator = role.GetId()
		}
	}
	require.NotEmpty(t, operator, "the fixture's organisation role over example-entries")
	assign := &gen.AssignRoleRequest{SubjectId: devID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: operator, OrgId: fixtureOrganization}
	_, err = permissions.AssignRole(admin, assign)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = permissions.RevokeRole(admin, &gen.RevokeRoleRequest{SubjectId: devID, RoleId: operator, OrgId: fixtureOrganization})
	})
	_, err = start("read")
	refusedWithoutARole(err, "an organisation role over the module's kind reaches nothing there")

	seat("user")
	issued, err := start("read")
	require.NoError(t, err, "a role on the module's installation reaches it")
	require.Equal(t, []string{"example-entries"}, carriedKinds(t, issued))
	_, err = start("write")
	require.Equal(t, codes.PermissionDenied, status.Code(err), "the organisation role's write adds nothing to the installation role's read: %v", err)

	unseat("user")
	_, err = start("read")
	refusedWithoutARole(err, "revoking the installation role withdraws it, whatever the organisation role still says")
}
