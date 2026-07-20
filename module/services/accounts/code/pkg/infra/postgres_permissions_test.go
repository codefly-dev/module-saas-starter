package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

func TestResolveIdentityWithoutOrganizationDoesNotBindEmptyUUID(t *testing.T) {
	userID := seedUser(t)
	providerID := "no-org-" + business.NewIDString()
	identity := &gen.UserIdentity{
		Uuid:          business.NewIDString(),
		UserUuid:      userID,
		Provider:      "email",
		ProviderId:    providerID,
		ProviderEmail: providerID + "@test.local",
		EmailVerified: true,
	}
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		return testStore.AddIdentity(ctx, identity)
	}))

	resolved, err := testStore.ResolveIdentity(testCtx, identity.Provider, identity.ProviderId)
	require.NoError(t, err)
	require.True(t, resolved.Found)
	require.Equal(t, userID, resolved.UserID)
	require.Empty(t, resolved.OrgID)
	require.Empty(t, resolved.OrgRole)
	require.Empty(t, resolved.Roles)
}
