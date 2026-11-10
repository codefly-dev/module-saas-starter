package ed25519minter_test

import (
	"context"
	"testing"

	"accounts/pkg/auth"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A role view's session is access-only and is its own family, keyed by the id
// the authority recorded the view under; it carries no platform role; and no
// client's session, which would hold every role its person does, is minted from it.
func TestARoleViewSessionIsAccessOnlyAndMintsNoClient(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	view := uuid.Must(uuid.NewV7())
	pair, err := m.MintRoleView(ctx, host.UserID, host.ID, view)
	require.NoError(t, err)
	require.Empty(t, pair.RefreshToken, "a role view is never refreshed")
	identity, err := m.VerifyAccess(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, view, identity.SessionID, "the session the view is keyed by is the token's")
	require.Equal(t, host.UserID, identity.UserID, "the subject is still the person")
	require.Equal(t, host.OrgID, identity.OrgID)
	require.Empty(t, identity.PlatformRole)

	row := store.records[len(store.records)-1]
	require.True(t, row.AccessOnly)
	require.Empty(t, row.RefreshHash)
	require.Equal(t, view, row.FamilyID)
	require.NotEqual(t, host.FamilyID, row.FamilyID, "ending the view does not end the session it came from")

	_, err = m.MintForClient(ctx, row.UserID, row.ID, "example-addin", "")
	require.ErrorIs(t, err, auth.ErrSessionUnavailable, "a client's session holds every role; a role view narrows")

	// A viewing session opens a narrower view, as the core allows.
	_, err = m.MintRoleView(ctx, row.UserID, row.ID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	_, err = m.MintRoleView(ctx, host.UserID, host.ID, uuid.Nil)
	require.Error(t, err)
	_, err = m.MintRoleView(ctx, uuid.Must(uuid.NewV7()), host.ID, uuid.Must(uuid.NewV7()))
	require.ErrorIs(t, err, auth.ErrSessionUnavailable, "a session id may not cross principals")
}
