//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/stretchr/testify/require"
)

// A suspended user's API keys and the authority core's reading of them stop
// answering, and answer again when the account is active again. Before this
// test's change neither read looked at the user's status, so suspension ended
// the user's sessions and nothing else.
func TestASuspendedUsersKeysAndMembershipAnswerNothing(t *testing.T) {
	personID := seedUser(t)
	orgID := seedOrg(t, personID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, personID, "member"))
	hash := "suspended-key-" + business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateAPIKey(ctx, &gen.APIKey{Id: business.NewIDString(), OrganizationId: orgID, UserId: personID, Name: "k", Prefix: "cfly_sk_live_"}, hash)
	}))
	authenticated := func() bool {
		got, err := testStore.GetAPIKeyAuthentication(testCtx, hash)
		require.NoError(t, err)
		return got != nil && got.Claims.Member
	}
	isMember := func() bool {
		var found bool
		require.NoError(t, testStore.AuthorityCore().Read(testCtx, func(tx authority.Tx) error {
			_, err := tx.Member(orgID, personID)
			found = err == nil
			return nil
		}))
		return found
	}

	require.True(t, authenticated(), "an active user's key authenticates")
	require.True(t, isMember(), "an active user is a member to the core")

	setUserStatus(t, personID, "suspended")
	require.False(t, authenticated(), "a suspended user's key does not")
	require.False(t, isMember(), "a suspended user is nobody's member to the core: no role, no delegation answers for them")

	setUserStatus(t, personID, "active")
	require.True(t, authenticated())
	require.True(t, isMember())
}
