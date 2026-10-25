package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// ActiveTargetInstallations is the admission read of the gateway's bearer-less
// module arm: every ACTIVE installation of one target in one organization, and
// nothing else. A revoked row, another target's row and another organization's
// row of the same target are none of them.
func TestActiveTargetInstallationsAnswersEveryActiveRowOfTheTarget(t *testing.T) {
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	orgA, ownerA, roleA := entitlementOrg(t)
	orgB, ownerB, roleB := entitlementOrg(t)
	alias := uniqueAlias("ledger")
	target := declarePresence(t, "acme.test."+alias, alias)

	revoked := installSolutionTarget(t, orgA, ownerA, roleA, "ledger", target)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgA, func(ctx context.Context) error {
		_, _, err := testStore.UninstallSolution(ctx, orgA, revoked.GetId())
		return err
	}))
	current := installSolutionTarget(t, orgA, ownerA, roleA, "ledger", target)
	installSolution(t, orgA, ownerA, roleA, "audit")
	installSolutionTarget(t, orgB, ownerB, roleB, "ledger-other-org", target)

	ids, err := svc.ActiveTargetInstallations(testCtx, orgA, target)
	require.NoError(t, err)
	require.Equal(t, []string{current.GetId()}, ids)

	ids, err = svc.ActiveTargetInstallations(testCtx, orgA, business.NewIDString())
	require.NoError(t, err)
	require.Empty(t, ids, "a target the organization never installed has no rows")
}
