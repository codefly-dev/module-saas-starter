package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestRLS_APIKeys_CrossTenantBlocked — proves Phase 2A RLS on
// api_keys (organization_id column). org A's session sees only its
// own keys; B's keys are physically invisible.
//
// Note: api_keys uses organization_id, not org_id. The policy in
// migration 28 uses the right column name.
func TestRLS_APIKeys_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	// Two orgs, each with one user (the user is the key owner —
	// api_keys.user_id FKs to users).
	respA, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "alice-ak@rls-test.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "alice-ak-rls", ProviderEmail: "alice-ak@rls-test.com",
		},
	})
	require.NoError(t, err)
	resA, err := testService.ResolveIdentity(ctx, &gen.ResolveIdentityRequest{Provider: "email", ProviderId: "alice-ak-rls"})
	require.NoError(t, err)
	orgA := resA.OrgId

	respB, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "bob-ak@rls-test.com",
		Identity: &gen.UserIdentity{
			Provider: "email", ProviderId: "bob-ak-rls", ProviderEmail: "bob-ak@rls-test.com",
		},
	})
	require.NoError(t, err)
	resB, err := testService.ResolveIdentity(ctx, &gen.ResolveIdentityRequest{Provider: "email", ProviderId: "bob-ak-rls"})
	require.NoError(t, err)
	orgB := resB.OrgId

	// Each user creates a key in their own org.
	_, err = testService.CreateAPIKey(ctx, respA.User.Uuid, &gen.CreateAPIKeyRequest{
		OrganizationId: orgA, Name: "alice-key",
	})
	require.NoError(t, err)
	_, err = testService.CreateAPIKey(ctx, respB.User.Uuid, &gen.CreateAPIKeyRequest{
		OrganizationId: orgB, Name: "bob-key",
	})
	require.NoError(t, err)

	// As A: see only A's key.
	listA, err := testService.ListAPIKeys(ctx, &gen.ListAPIKeysRequest{
		OrganizationId: orgA, PageSize: 100,
	})
	require.NoError(t, err)
	require.Len(t, listA.Keys, 1)
	require.Equal(t, "alice-key", listA.Keys[0].Name)

	// As B: see only B's key.
	listB, err := testService.ListAPIKeys(ctx, &gen.ListAPIKeysRequest{
		OrganizationId: orgB, PageSize: 100,
	})
	require.NoError(t, err)
	require.Len(t, listB.Keys, 1)
	require.Equal(t, "bob-key", listB.Keys[0].Name)

	// Cross-tenant probe: from inside A's tx, query B's keys.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, _, err := testStore.ListAPIKeys(ctx, orgB, 100, "")
		require.NoError(t, err)
		require.Len(t, stolen, 0, "RLS must hide B's api_keys from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows.
	noWrap, _, err := testStore.ListAPIKeys(context.Background(), orgA, 100, "")
	require.NoError(t, err)
	require.Len(t, noWrap, 0,
		"un-wrapped ListAPIKeys must return ZERO rows (RLS fail-closed)")
}
