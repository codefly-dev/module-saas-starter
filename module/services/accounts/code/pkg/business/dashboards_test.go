package business_test

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

func TestDashboards_CRUDRoundtrip(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, org := mustUserAndOrg(t, ctx, "crud-dash@rls-test.com", "crud-dash", "Acme CRUD")

	created, err := testService.CreateDashboard(ctx, org, owner, "", "revenue", dashboardSpec)
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, org, created.OrgID)
	require.Equal(t, owner, created.OwnerID)
	require.Equal(t, business.DashboardVisibilityPrivate, created.Visibility)

	got, err := testService.GetDashboard(ctx, org, owner, false, created.ID)
	require.NoError(t, err)
	require.Equal(t, "revenue", got.Name)
	require.JSONEq(t, string(dashboardSpec), string(got.Spec))

	name := "revenue-v2"
	newSpec := []byte(`{"version":1,"metrics":[{"id":"m1"}]}`)
	updated, err := testService.UpdateDashboard(ctx, org, owner, false, created.ID, &name, newSpec)
	require.NoError(t, err)
	require.Equal(t, "revenue-v2", updated.Name)
	require.JSONEq(t, string(newSpec), string(updated.Spec))

	list, _, err := testService.ListDashboards(ctx, org, owner, business.DashboardListMine, 0, "")
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, testService.DeleteDashboard(ctx, org, owner, false, created.ID))
	_, err = testService.GetDashboard(ctx, org, owner, false, created.ID)
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestDashboards_CreateHonorsClientID(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, org := mustUserAndOrg(t, ctx, "ensure-dash@rls-test.com", "ensure-dash", "Acme Ensure")

	id := business.NewIDString()
	created, err := testService.CreateDashboard(ctx, org, owner, id, "pinned", dashboardSpec)
	require.NoError(t, err)
	require.Equal(t, id, created.ID)
}

func TestDashboards_ValidationRejectsEmptyNameAndSpec(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, org := mustUserAndOrg(t, ctx, "valid-dash@rls-test.com", "valid-dash", "Acme Valid")

	_, err := testService.CreateDashboard(ctx, org, owner, "", "   ", dashboardSpec)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = testService.CreateDashboard(ctx, org, owner, "", "ok", []byte(`[]`))
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = testService.CreateDashboard(ctx, org, owner, "", "ok", []byte(`{}`))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// A private board is invisible to a non-owner until it is shared to the org,
// and hidden again once unshared.
func TestDashboards_ShareControlsOrgVisibility(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, org := mustUserAndOrg(t, ctx, "share-dash@rls-test.com", "share-dash", "Acme Share")
	stranger := business.NewIDString()

	board, err := testService.CreateDashboard(ctx, org, owner, "", "ops", dashboardSpec)
	require.NoError(t, err)

	_, err = testService.GetDashboard(ctx, org, stranger, false, board.ID)
	require.Equal(t, codes.NotFound, status.Code(err), "private board is hidden from a non-owner member")

	shared, err := testService.ShareDashboard(ctx, org, owner, board.ID, business.DashboardVisibilityOrg)
	require.NoError(t, err)
	require.Equal(t, business.DashboardVisibilityOrg, shared.Visibility)

	got, err := testService.GetDashboard(ctx, org, stranger, false, board.ID)
	require.NoError(t, err, "an org-shared board is readable by any member")
	require.Equal(t, board.ID, got.ID)

	orgShared, _, err := testService.ListDashboards(ctx, org, stranger, business.DashboardListOrgShared, 0, "")
	require.NoError(t, err)
	require.Len(t, orgShared, 1)

	_, err = testService.ShareDashboard(ctx, org, owner, board.ID, business.DashboardVisibilityPrivate)
	require.NoError(t, err)
	_, err = testService.GetDashboard(ctx, org, stranger, false, board.ID)
	require.Equal(t, codes.NotFound, status.Code(err), "un-sharing hides the board again")
}

// Only the owner or an org admin may edit or delete; a plain member may not.
func TestDashboards_OnlyOwnerOrAdminEdits(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, org := mustUserAndOrg(t, ctx, "edit-dash@rls-test.com", "edit-dash", "Acme Edit")
	stranger := business.NewIDString()

	board, err := testService.CreateDashboard(ctx, org, owner, "", "budget", dashboardSpec)
	require.NoError(t, err)

	name := "hijacked"
	_, err = testService.UpdateDashboard(ctx, org, stranger, false, board.ID, &name, nil)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	err = testService.DeleteDashboard(ctx, org, stranger, false, board.ID)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// An org admin may edit a board they do not own.
	adminName := "governed"
	_, err = testService.UpdateDashboard(ctx, org, stranger, true, board.ID, &adminName, nil)
	require.NoError(t, err)

	// The owner may edit their own board.
	ownerName := "budget-v2"
	_, err = testService.UpdateDashboard(ctx, org, owner, false, board.ID, &ownerName, nil)
	require.NoError(t, err)
}

// A member sees only their own boards under MINE and every org-shared board
// under ORG_SHARED, whoever owns it.
func TestDashboards_ListScopes(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, org := mustUserAndOrg(t, ctx, "scope-dash@rls-test.com", "scope-dash", "Acme Scope")
	stranger := business.NewIDString()

	private, err := testService.CreateDashboard(ctx, org, owner, "", "mine-private", dashboardSpec)
	require.NoError(t, err)
	shared, err := testService.CreateDashboard(ctx, org, owner, "", "mine-shared", dashboardSpec)
	require.NoError(t, err)
	_, err = testService.ShareDashboard(ctx, org, owner, shared.ID, business.DashboardVisibilityOrg)
	require.NoError(t, err)

	mine, _, err := testService.ListDashboards(ctx, org, owner, business.DashboardListMine, 0, "")
	require.NoError(t, err)
	require.Len(t, mine, 2)

	ownerAll, _, err := testService.ListDashboards(ctx, org, owner, business.DashboardListAll, 0, "")
	require.NoError(t, err)
	require.Len(t, ownerAll, 2)

	strangerMine, _, err := testService.ListDashboards(ctx, org, stranger, business.DashboardListMine, 0, "")
	require.NoError(t, err)
	require.Len(t, strangerMine, 0)

	strangerAll, _, err := testService.ListDashboards(ctx, org, stranger, business.DashboardListAll, 0, "")
	require.NoError(t, err)
	require.Len(t, strangerAll, 1)
	require.Equal(t, shared.ID, strangerAll[0].ID)
	require.NotEqual(t, private.ID, strangerAll[0].ID)
}

// A page is always bounded and hands back a token that walks the rest of the
// collection exactly once, so a large collection can never force one unbounded
// read.
func TestDashboards_ListIsPagedAndBounded(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, org := mustUserAndOrg(t, ctx, "page-dash@rls-test.com", "page-dash", "Acme Page")

	const total = 5
	created := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		board, err := testService.CreateDashboard(ctx, org, owner, "", fmt.Sprintf("board-%d", i), dashboardSpec)
		require.NoError(t, err)
		created[board.ID] = true
	}

	seen := make(map[string]bool, total)
	token := ""
	pages := 0
	for {
		page, next, err := testService.ListDashboards(ctx, org, owner, business.DashboardListMine, 2, token)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page), 2, "page must never exceed the requested size")
		for _, record := range page {
			require.False(t, seen[record.ID], "a record must appear on exactly one page")
			seen[record.ID] = true
		}
		pages++
		require.LessOrEqual(t, pages, total+1, "pagination must terminate")
		if next == "" {
			break
		}
		token = next
	}
	require.Equal(t, created, seen, "paging must cover the whole collection")
	require.Equal(t, 3, pages, "5 records at page size 2 is three pages (2 + 2 + 1)")
}

func TestDashboards_ListRejectsMalformedPageToken(t *testing.T) {
	clearData(t)
	ctx := testCtx
	owner, org := mustUserAndOrg(t, ctx, "token-dash@rls-test.com", "token-dash", "Acme Token")

	_, _, err := testService.ListDashboards(ctx, org, owner, business.DashboardListMine, 10, "not-a-valid-token")
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// A well-formed base64 token whose id segment is not a UUID must also be
	// rejected up front — otherwise it reaches the query as a uuid-typed param
	// and fails with a Postgres cast error surfaced as Internal.
	crafted := base64.RawURLEncoding.EncodeToString(
		[]byte("2026-01-01T00:00:00Z\x1fnot-a-uuid"),
	)
	_, _, err = testService.ListDashboards(ctx, org, owner, business.DashboardListMine, 10, crafted)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
