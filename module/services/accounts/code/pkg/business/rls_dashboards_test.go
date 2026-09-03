package business_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
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

	listA, _, err := testService.ListDashboards(ctx, orgA, ownerA, business.DashboardListAll, 0, "")
	require.NoError(t, err)
	require.Len(t, listA, 1)
	require.Equal(t, "board-a", listA[0].Name)

	// Probe: from A's tx, ask the Store directly for B's org. RLS hides them.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.ListDashboards(ctx, orgB, ownerB, business.DashboardListAll, 0, nil)
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's dashboards from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, err := testStore.ListDashboards(context.Background(), orgA, ownerA, business.DashboardListAll, 0, nil)
	require.NoError(t, err)
	require.Len(t, noWrap, 0, "un-wrapped ListDashboards must return ZERO rows (RLS fail-closed)")
}

// A dashboard shared to the org is an org asset and must survive erasure of the
// member who authored it: the owner_id FK detaches (SET NULL) instead of
// cascading the row away. This is the guard against silently deleting a shared
// board when its owner is offboarded.
func TestDashboards_SharedBoardSurvivesOwnerDeletion(t *testing.T) {
	clearData(t)
	ctx := testCtx

	// organizations.owner_id is ON DELETE RESTRICT, so the org owner can't be
	// erased; a separate member authors the board under test.
	_, org := mustUserAndOrg(t, ctx, "keeper@rls-test.com", "keeper-rls", "Acme Keeper")
	author := mustUser(t, ctx, "author@rls-test.com", "author-rls")

	board, err := testService.CreateDashboard(ctx, org, author, "", "org-board", dashboardSpec)
	require.NoError(t, err)
	_, err = testService.ShareDashboard(ctx, org, author, board.ID, business.DashboardVisibilityOrg)
	require.NoError(t, err)

	// Hard-delete the author's user row — what a GDPR erasure or admin purge
	// does. The in-app DeleteUser is a soft-delete that never fires the FK, and
	// the app_tenant role cannot delete users, so run it under the control-plane
	// role to exercise the constraint directly.
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `DELETE FROM users WHERE uuid = $1`, author)
		return err
	}))

	survived, err := testService.GetDashboard(ctx, org, author, false, board.ID)
	require.NoError(t, err, "the shared board must outlive its author")
	require.Equal(t, board.ID, survived.ID)
	require.Equal(t, business.DashboardVisibilityOrg, survived.Visibility)
	require.Empty(t, survived.OwnerID, "the erased owner detaches to a null owner")

	shared, _, err := testService.ListDashboards(ctx, org, author, business.DashboardListOrgShared, 0, "")
	require.NoError(t, err)
	require.Len(t, shared, 1)
	require.Equal(t, board.ID, shared[0].ID)
}

func mustUser(t *testing.T, ctx context.Context, email, providerID string) string {
	t.Helper()
	resp, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: email,
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: providerID, ProviderEmail: email,
		},
	})
	require.NoError(t, err)
	return resp.User.Uuid
}
