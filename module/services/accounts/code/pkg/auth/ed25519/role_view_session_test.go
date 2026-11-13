package ed25519minter_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"

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

// A shell keeps a role view (or a cast) its person is using by opening the
// same one again from the person's own session shortly before it lapses; a
// forgotten tab opens nothing and lapses. That keeps the guard only while
// every view, the first and each one opened again, stays capped at the
// impersonation lifetime however long a person's own session runs. And a
// page keeps what it was doing across the reopening only while the new view
// reads as the same viewer (issuer, person, organisation, authentication
// time, no acting) as the one it replaces.
func TestARoleViewOpenedAgainIsCappedAndReadsAsTheSameViewer(t *testing.T) {
	ctx := context.Background()
	_, priv, err := ed25519minter.GenerateKey()
	require.NoError(t, err)
	store := &memoryStore{}
	m := ed25519minter.New(ed25519minter.Config{
		Issuer: "test-issuer", Audience: "test-audience",
		AccessTokenTTL: 15 * time.Minute, ImpersonationTokenTTL: 5 * time.Minute,
	}, priv, store)
	identity := newIdentity()
	identity.AuthenticatedAt = time.Now().Add(-time.Hour).Truncate(time.Second)
	_, err = m.Mint(ctx, identity)
	require.NoError(t, err)
	host := store.records[0]

	viewer := func(token string) (map[string]any, time.Duration) {
		var claims map[string]any
		require.NoError(t, json.Unmarshal([]byte(decodeJWTPayload(t, token)), &claims))
		life := time.Duration(claims["exp"].(float64)-claims["iat"].(float64)) * time.Second
		return map[string]any{
			"iss": claims["iss"], "sub": claims["sub"], "org": claims["org"],
			"auth_time": claims["auth_time"], "acting": claims["acting"], "act": claims["act"],
		}, life
	}

	first, err := m.MintRoleView(ctx, host.UserID, host.ID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)
	again, err := m.MintRoleView(ctx, host.UserID, host.ID, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	firstViewer, firstLife := viewer(first.AccessToken)
	againViewer, againLife := viewer(again.AccessToken)
	require.Equal(t, 5*time.Minute, firstLife, "a view lives the impersonation lifetime, not the person's own")
	require.Equal(t, 5*time.Minute, againLife, "opening it again never lengthens one credential")
	require.Empty(t, again.RefreshToken, "a view opened again is still access-only")
	require.NotNil(t, firstViewer["auth_time"])
	require.Equal(t, firstViewer, againViewer, "the view opened again reads as the same viewer")
}
