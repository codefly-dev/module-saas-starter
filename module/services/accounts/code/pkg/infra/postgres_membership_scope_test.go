package infra_test

import (
	"strings"
	"testing"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

func TestMembershipReadUsesVerifiedServicePostgresScope(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	seedOrgMember(t, orgID, userID)
	ctx := auth.WithVerifiedDatabaseIdentity(testCtx, userID, orgID)

	membership, err := testStore.GetOrgMembership(ctx, orgID, userID)
	require.NoError(t, err)
	require.NotNil(t, membership)
	require.Equal(t, orgID, membership.OrgId)
	require.Equal(t, userID, membership.UserId)

	membership, err = testStore.GetOrgMembership(ctx, strings.ToUpper(orgID), strings.ToUpper(userID))
	require.NoError(t, err)
	require.NotNil(t, membership, "equivalent UUID spellings must resolve to the same verified scope")

	_, err = testStore.GetOrgMembership(ctx, "019f6bf7-5b4b-74e5-8c17-092259bb1661", userID)
	require.ErrorIs(t, err, auth.ErrVerifiedDatabaseScopeMismatch,
		"caller-selected tenant must not override the verified database tenant")

	_, err = testStore.GetOrgMembership(testCtx, orgID, userID)
	require.ErrorIs(t, err, auth.ErrVerifiedDatabaseIdentityRequired,
		"transport presentation context without verified database identity must fail closed")
}
