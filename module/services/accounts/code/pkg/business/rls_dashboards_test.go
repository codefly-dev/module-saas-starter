package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

var dashboardSpec = []byte(`{"version":1,"metrics":[]}`)

// TestRLS_Dashboards_CrossTenantBlocked — direct-org-id table. Two orgs each
// own a dashboard; a read from inside org A's tx must never see B's rows, and
// an un-wrapped read (no org context) must return zero rows (fail-closed).
func TestRLS_Dashboards_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	ownerA, orgA := mustUserAndOrg(t, ctx, "alice-dash@rls-test.com", "alice-dash-rls", "Acme Dash A")
	ownerB, orgB := mustUserAndOrg(t, ctx, "bob-dash@rls-test.com", "bob-dash-rls", "Acme Dash B")

	_, err := testService.CreateDashboard(ctx, orgA, ownerA, "", "board-a", dashboardSpec)
	require.NoError(t, err)
	_, err = testService.CreateDashboard(ctx, orgB, ownerB, "", "board-b", dashboardSpec)
	require.NoError(t, err)

	listA, err := testService.ListDashboards(ctx, orgA, ownerA, business.DashboardListAll)
	require.NoError(t, err)
	require.Len(t, listA, 1)
	require.Equal(t, "board-a", listA[0].Name)

	// Probe: from A's tx, ask the Store directly for B's org. RLS hides them.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.ListDashboards(ctx, orgB, ownerB, business.DashboardListAll)
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's dashboards from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, err := testStore.ListDashboards(context.Background(), orgA, ownerA, business.DashboardListAll)
	require.NoError(t, err)
	require.Len(t, noWrap, 0, "un-wrapped ListDashboards must return ZERO rows (RLS fail-closed)")
}
