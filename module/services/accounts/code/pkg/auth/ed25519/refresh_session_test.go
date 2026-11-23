package ed25519minter_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
)

// A sign-out that presents only its refresh credential has to name the session
// to end. The token names it, and reading that consumes nothing: the token is
// neither rotated nor recorded as reused, so it still refreshes afterwards.
func TestARefreshTokenNamesItsSessionWithoutBeingConsumed(t *testing.T) {
	ctx := context.Background()
	m, _ := newMinter(t)
	identity := newIdentity()

	pair, err := m.Mint(ctx, identity)
	require.NoError(t, err)

	named, err := m.RefreshSession(ctx, pair.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, sessionIDOf(t, pair.AccessToken), named.ID.String(),
		"the token names the session its access token carries")
	require.Equal(t, identity.UserID, named.UserID)
	require.Equal(t, identity.OrgID, named.OrgID)

	again, err := m.RefreshSession(ctx, pair.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, named, again)
	_, err = m.VerifyRefresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "naming the session must not consume the token")
}

// A token that was never a session's names none, with the answer an unknown
// token gets everywhere else.
func TestAnUnknownRefreshTokenNamesNoSession(t *testing.T) {
	m, _ := newMinter(t)

	for _, token := range []string{"", "never-issued"} {
		named, err := m.RefreshSession(context.Background(), token)
		require.ErrorIs(t, err, auth.ErrRefreshRevoked)
		require.Equal(t, uuid.Nil, named.ID)
	}
}
