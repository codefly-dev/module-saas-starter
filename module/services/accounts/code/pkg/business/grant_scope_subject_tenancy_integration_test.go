//go:build !pure

package business_test

import (
	"context"
	"testing"

	"accounts/pkg/adapters"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A scope grant names a subject of the organization it is written in. A subject
// of another organization is refused, whichever door the grant comes through.
func TestGrantScopeRefusesASubjectOfAnotherOrganization(t *testing.T) {
	clearData(t)
	adapters.WithService(testService)
	admin, org := mustUserAndOrg(t, testCtx, "admin@tenancy.test", "tenancy-admin", "Tenancy A")
	otherAdmin, otherOrg := mustUserAndOrg(t, testCtx, "admin@tenancy-b.test", "tenancy-b-admin", "Tenancy B")
	otherTeam, err := testService.CreateTeam(testCtx, otherAdmin, &gen.CreateTeamRequest{OrgId: otherOrg, Name: "elsewhere"})
	require.NoError(t, err)
	role := createRole(t, org, "reader", "doc", "read")
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.RegisterScopeNode(ctx, &gen.ScopeNode{
			Id: business.NewIDString(), OrgId: org, ScopePath: "tenancy_root", Kind: "collection", Label: "Root",
		})
	}))

	_, err = (&adapters.PermServer{}).GrantScope(storyCallerContext(testCtx, admin, org), &gen.GrantScopeRequest{
		OrgId: org, SubjectId: otherTeam.Team.Id, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM,
		ScopePath: "tenancy_root", RoleId: role,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "a team of another organization is refused: %v", err)

	_, err = (&adapters.PermServer{}).GrantScope(storyCallerContext(testCtx, admin, org), &gen.GrantScopeRequest{
		OrgId: org, SubjectId: otherAdmin, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath: "tenancy_root", RoleId: role,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "a principal of another organization is refused: %v", err)
}
