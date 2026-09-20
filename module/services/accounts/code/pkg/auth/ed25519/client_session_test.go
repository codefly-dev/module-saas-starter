package ed25519minter_test

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// mintHostSession puts a live host session in the store and returns it, so a
// client can be authorized from something that actually exists.
func mintHostSession(t *testing.T, m *ed25519minter.Minter, store *memoryStore) *auth.SessionRecord {
	t.Helper()
	identity := newIdentity()
	_, err := m.Mint(context.Background(), identity)
	require.NoError(t, err)
	require.Len(t, store.records, 1)
	return &store.records[0]
}

func TestMintForClientIssuesAnIndependentSessionCarryingAzp(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-addin")
	require.NoError(t, err)
	require.NotEmpty(t, pair.AccessToken)
	require.NotEmpty(t, pair.RefreshToken, "a client session is refreshable on its own")

	identity, err := m.VerifyAccess(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "example-addin", identity.ClientID)
	require.Equal(t, host.UserID, identity.UserID, "the subject is still the person")
	require.Equal(t, host.OrgID, identity.OrgID)
	require.NotEqual(t, host.ID, identity.SessionID, "the client gets its own session")

	require.Contains(t, decodeJWTPayload(t, pair.AccessToken), `"azp":"example-addin"`)

	client := store.records[len(store.records)-1]
	require.NotEqual(t, host.FamilyID, client.FamilyID,
		"revoking the client must not revoke the browser session")
	require.Equal(t, "example-addin", client.ClientID)
}

// The host's own web session carries no client, so a consumer reads an absent
// azp as "first-party web session" rather than as a missing value.
func TestHostSessionOmitsAzpEntirely(t *testing.T) {
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)
	require.Empty(t, host.ClientID)

	pair, err := m.Mint(context.Background(), newIdentity())
	require.NoError(t, err)
	require.NotContains(t, decodeJWTPayload(t, pair.AccessToken), `"azp"`)
}

func TestMintForClientRefusesWithoutAClient(t *testing.T) {
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)
	_, err := m.MintForClient(context.Background(), host.UserID, host.ID, "")
	require.Error(t, err)
}

func TestMintForClientRefusesAnUnknownOrForeignSession(t *testing.T) {
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	_, err := m.MintForClient(context.Background(), host.UserID, uuid.Must(uuid.NewV7()), "example-addin")
	require.ErrorIs(t, err, auth.ErrSessionUnavailable)

	_, err = m.MintForClient(context.Background(), uuid.Must(uuid.NewV7()), host.ID, "example-addin")
	require.ErrorIs(t, err, auth.ErrSessionUnavailable, "a session id may not cross principals")
}

// One client may not bootstrap another, and a client session may not authorize
// anything: only the host's own web session authorizes.
func TestAClientSessionCannotAuthorizeASecondClient(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	_, err := m.MintForClient(ctx, host.UserID, host.ID, "example-addin")
	require.NoError(t, err)
	client := store.records[len(store.records)-1]

	_, err = m.MintForClient(ctx, client.UserID, client.ID, "example-cli")
	require.ErrorIs(t, err, auth.ErrSessionUnavailable)
}

func TestClientRefreshRotationPreservesAzp(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-addin")
	require.NoError(t, err)

	rotated, err := m.VerifyClientRefresh(ctx, pair.RefreshToken, "example-addin")
	require.NoError(t, err)
	require.NotEqual(t, pair.RefreshToken, rotated.RefreshToken, "the refresh half rotates")

	identity, err := m.VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "example-addin", identity.ClientID)
}

// A client presenting a token that belongs to another client — or to the host's
// own web session — is refused exactly as it would be for a token that never
// existed, and the legitimate holder's session survives.
func TestClientRefreshRefusesAnotherClientsToken(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-addin")
	require.NoError(t, err)

	_, err = m.VerifyClientRefresh(ctx, pair.RefreshToken, "example-cli")
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)

	// The refusal must not have revoked the family it did not belong to.
	rotated, err := m.VerifyClientRefresh(ctx, pair.RefreshToken, "example-addin")
	require.NoError(t, err)
	require.NotEmpty(t, rotated.AccessToken)
}

func TestClientRefreshRefusesTheHostsOwnWebSession(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	mintHostSession(t, m, store)
	hostPair, err := m.Mint(ctx, newIdentity())
	require.NoError(t, err)

	_, err = m.VerifyClientRefresh(ctx, hostPair.RefreshToken, "example-addin")
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)

	_, err = m.VerifyClientRefresh(ctx, hostPair.RefreshToken, "")
	require.Error(t, err, "a client refresh must name a client")
}
