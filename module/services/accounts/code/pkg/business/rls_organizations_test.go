package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/gen"
)

// TestRLS_Organizations_SelfReferential — Phase 2F. The org row IS
// the tenant; the policy filters by id (not org_id) against the
// current setting. Two orgs exist; each is visible only to its own
// scope. WithBypass spans both (org switcher / platform admin).
func TestRLS_Organizations_SelfReferential(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-orgs@rls-test.com", "alice-orgs-rls", "Acme Org A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-orgs@rls-test.com", "bob-orgs-rls", "Acme Org B")

	// As A's scope: GetOrganization for A succeeds.
	gotA, err := testService.GetOrganization(ctx, &gen.GetOrganizationRequest{Id: orgA})
	require.NoError(t, err)
	require.NotNil(t, gotA)
	require.Equal(t, "Acme Org A", gotA.Name)

	// As B's scope: GetOrganization for B succeeds.
	gotB, err := testService.GetOrganization(ctx, &gen.GetOrganizationRequest{Id: orgB})
	require.NoError(t, err)
	require.NotNil(t, gotB)
	require.Equal(t, "Acme Org B", gotB.Name)

	// Probe: from A's tx, ask for B's row via Store. RLS hides it.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.GetOrganization(ctx, orgB)
		require.NoError(t, err)
		require.Nil(t, stolen, "RLS must hide org B's row from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows — even for an org you "own".
	noWrap, err := testStore.GetOrganization(context.Background(), orgA)
	require.NoError(t, err)
	require.Nil(t, noWrap, "un-wrapped GetOrganization must return nil (RLS fail-closed)")

	// WithBypass sees both.
	require.NoError(t, testStore.WithBypass(context.Background(), func(ctx context.Context) error {
		gA, err := testStore.GetOrganization(ctx, orgA)
		require.NoError(t, err)
		require.NotNil(t, gA, "WithBypass must see org A")
		gB, err := testStore.GetOrganization(ctx, orgB)
		require.NoError(t, err)
		require.NotNil(t, gB, "WithBypass must see org B")
		return nil
	}))
}

// TestRLS_Organizations_ListForUser_BypassesPolicy — the org switcher
// (Service.ListOrganizations) needs to see every org a user is a
// member of regardless of current tenant context. The Service wraps
// in WithBypass; the SQL still filters by user_id so cross-user
// leakage isn't possible.
func TestRLS_Organizations_ListForUser_BypassesPolicy(t *testing.T) {
	clearData(t)
	ctx := testCtx

	userA, _ := mustUserAndOrg(t, ctx, "alice-list@rls-test.com", "alice-list-rls", "Acme List A")

	// User A creates a second org — they own both.
	org2, err := testService.CreateOrganization(ctx, userA, &gen.CreateOrganizationRequest{
		Name: "Second Org", Slug: "alice-list-second",
	})
	require.NoError(t, err)
	require.NotNil(t, org2.Organization)

	// ListOrganizations must return both. Without bypass, the
	// self-referential policy would match at most one (whichever
	// app.current_org_id pointed at).
	resp, err := testService.ListOrganizations(ctx, userA)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(resp.Organizations), 2,
		"ListOrganizations must see both orgs A owns (got %d)", len(resp.Organizations))
}
