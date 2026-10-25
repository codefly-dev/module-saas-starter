//go:build !pure

package business_test

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
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
