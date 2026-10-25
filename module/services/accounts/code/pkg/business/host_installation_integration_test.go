//go:build !pure

package business_test

import (
	"context"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestHostInstallationExposesItsOwnRoleOnlyAfterAcknowledgement(t *testing.T) {
	w := newExposureWorld(t)
	target, alias := declarePresence(t, "host-exposure")
	i, err := testService.InstallSolution(testCtx, w.admin, &business.InstallSolutionParams{OrgID: w.org, AgentIdentifier: "acme.example/host-exposure:1.0.0", TargetID: target, RootScopeLabel: alias, RoleID: w.useRole})
	require.NoError(t, err)
	before, err := testService.ReadHostInstallation(testCtx, w.org, i.Id)
	require.NoError(t, err)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_PENDING, before.Installation.Status)
	_, err = testService.ExposeHostInstallation(testCtx, w.admin, w.org, i.Id, w.team)
	require.Error(t, err, "pending cannot be exposed")
	acknowledgeInstallation(t, i)
	after, err := testService.ExposeHostInstallation(testCtx, w.admin, w.org, i.Id, w.team)
	require.NoError(t, err)
	require.Equal(t, before.Revision+1, after.Revision)
	require.Equal(t, w.useRole, after.RoleID)
	require.Equal(t, []string{w.team}, after.TeamIDs)
	again, err := testService.ExposeHostInstallation(testCtx, w.admin, w.org, i.Id, w.team)
	require.NoError(t, err)
	require.Equal(t, after.Revision, again.Revision, "an unchanged grant does not change authority")
	_, err = testService.ExposeHostInstallation(testCtx, w.admin, w.otherOrg, i.Id, w.team)
	require.Error(t, err)
}

// An installation made holding a catalogue role named after one of the
// solution's roles, before solution roles were read ("viewer", which does not
// permit using a solution), is exposed to a team with the solution's own role
// of that name: the team is granted it, its members hold it, and the
// installation's standing grant is untouched.
func TestAnInstallationHoldingACatalogueViewerIsExposedWithTheSolutionsViewer(t *testing.T) {
	w := newExposureWorld(t)
	viewer := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, w.org, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{Id: viewer, Name: "viewer", OrgId: w.org, Permissions: []*gen.Permission{{Resource: "doc", Action: "read"}}})
	}))
	target, alias := declarePresence(t, "legacy-viewer")
	i, err := testService.InstallSolution(testCtx, w.admin, &business.InstallSolutionParams{OrgID: w.org, AgentIdentifier: "acme.example/legacy-viewer:1.0.0", TargetID: target, RootScopeLabel: alias, RoleID: viewer})
	require.NoError(t, err)
	acknowledgeInstallation(t, i)

	_, err = testService.ExposeHostInstallationAs(testCtx, w.admin, w.org, i.Id, w.team, nil)
	require.ErrorContains(t, err, business.ErrRoleDoesNotExposeSolution.Error(), "the catalogue viewer alone exposes nothing")

	authority := &business.SolutionAuthority{Principal: alias, Kinds: []string{"doc"}, Roles: map[string][]*basev0.WorkScopeV1{
		business.InstallerRole: {{ResourceKind: "doc", Actions: []string{"read", "write"}}},
		"viewer":               {{ResourceKind: "doc", Actions: []string{"read"}}},
	}}
	exposed, err := testService.ExposeHostInstallationAs(testCtx, w.admin, w.org, i.Id, w.team, authority)
	require.NoError(t, err)
	require.Equal(t, []string{w.team}, exposed.TeamIDs)
	require.Equal(t, "viewer", exposed.Role)
	require.Equal(t, viewer, exposed.RoleID, "the standing grant is not rewritten")
	held := authority.HeldBy(w.inTeam, []string{w.team}, business.SolutionRoleHolders{Owner: exposed.Installation.GetOwnerPrincipalId(), Role: exposed.Role, Teams: exposed.TeamIDs})
	require.Equal(t, authority.Grants("viewer"), held)

	again, err := testService.ExposeHostInstallationAs(testCtx, w.admin, w.org, i.Id, w.team, authority)
	require.NoError(t, err)
	require.Equal(t, exposed.Revision, again.Revision, "re-exposing changes nothing")
}
