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

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-addin", "")
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
	_, err := m.MintForClient(context.Background(), host.UserID, host.ID, "", "")
	require.Error(t, err)
}

func TestMintForClientRefusesAnUnknownOrForeignSession(t *testing.T) {
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	_, err := m.MintForClient(context.Background(), host.UserID, uuid.Must(uuid.NewV7()), "example-addin", "")
	require.ErrorIs(t, err, auth.ErrSessionUnavailable)

	_, err = m.MintForClient(context.Background(), uuid.Must(uuid.NewV7()), host.ID, "example-addin", "")
	require.ErrorIs(t, err, auth.ErrSessionUnavailable, "a session id may not cross principals")
}

// One client may not bootstrap another, and a client session may not authorize
// anything: only the host's own web session authorizes.
func TestAClientSessionCannotAuthorizeASecondClient(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	_, err := m.MintForClient(ctx, host.UserID, host.ID, "example-addin", "")
	require.NoError(t, err)
	client := store.records[len(store.records)-1]

	_, err = m.MintForClient(ctx, client.UserID, client.ID, "example-cli", "")
	require.ErrorIs(t, err, auth.ErrSessionUnavailable)
}

func TestClientRefreshRotationPreservesAzp(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-addin", "")
	require.NoError(t, err)

	rotated, err := m.VerifyClientRefresh(ctx, pair.RefreshToken, "example-addin", "")
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

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-addin", "")
	require.NoError(t, err)

	_, err = m.VerifyClientRefresh(ctx, pair.RefreshToken, "example-cli", "")
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)

	// The refusal must not have revoked the family it did not belong to.
	rotated, err := m.VerifyClientRefresh(ctx, pair.RefreshToken, "example-addin", "")
	require.NoError(t, err)
	require.NotEmpty(t, rotated.AccessToken)
}

func TestClientRefreshRefusesTheHostsOwnWebSession(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	mintHostSession(t, m, store)
	hostPair, err := m.Mint(ctx, newIdentity())
	require.NoError(t, err)

	_, err = m.VerifyClientRefresh(ctx, hostPair.RefreshToken, "example-addin", "")
	require.ErrorIs(t, err, auth.ErrRefreshRevoked)

	_, err = m.VerifyClientRefresh(ctx, hostPair.RefreshToken, "", "")
	require.Error(t, err, "a client refresh must name a client")
}

// A host session whose second factor is no longer good enough to project is a
// refusal, not an internal error, and it must not take the browser session down
// with it. Before the fix the terminal rejection escaped as a raw error from a
// public endpoint, after the one-use code had already been spent.
func TestClientAuthorizationRefusesStaleMFAEvidenceWithoutRevokingTheHost(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	// The person enrolled MFA after this session was minted: authorization now
	// reports an enrolled factor the session carries no evidence of.
	store.refreshAuthorization = &auth.RefreshAuthorization{
		OrgID:       host.OrgID,
		OrgRole:     host.OrgRole,
		MFAEnrolled: true,
	}

	_, err := m.MintForClient(ctx, host.UserID, host.ID, "example-addin", "")
	require.ErrorIs(t, err, auth.ErrSessionUnavailable)

	for i := range store.records {
		if store.records[i].ID == host.ID {
			require.Nil(t, store.records[i].RevokedAt,
				"authorizing a client must not sign the person out of their browser")
		}
	}
}

// RFC 8707: a client session minted for a resource carries that resource as a
// SECOND audience beside the host's own.
//
// Both, not one. The resource is what the gateway checks to refuse this token
// at another solution; the host audience is what every existing verifier
// validates, including this minter's own VerifyAccess, which the redemption
// path calls to build its audit row. Dropping either breaks something real.
func TestMintForClientBindsTheResourceAsASecondAudience(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)
	const resource = "https://host.example.com/solutions/example/mcp"

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-mcp", resource)
	require.NoError(t, err)

	payload := decodeJWTPayload(t, pair.AccessToken)
	require.Contains(t, payload, `"aud":["test-audience","`+resource+`"]`)

	identity, err := m.VerifyAccess(pair.AccessToken)
	require.NoError(t, err, "the host audience must still verify")
	require.Equal(t, resource, identity.Resource)

	// Persisted on the session row, which is what a rotation reads.
	client := store.records[len(store.records)-1]
	require.Equal(t, resource, client.Resource)
}

// A session that named no resource carries only the host audience, so nothing
// about the browser's own session or the first registered client changes.
func TestASessionWithNoResourceCarriesOnlyTheHostAudience(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-addin", "")
	require.NoError(t, err)
	require.Contains(t, decodeJWTPayload(t, pair.AccessToken), `"aud":["test-audience"]`)

	identity, err := m.VerifyAccess(pair.AccessToken)
	require.NoError(t, err)
	require.Empty(t, identity.Resource)
}

// The binding is fixed when the person consented to it. A rotation reissues the
// resource from the locked session row, never from the request — otherwise a
// client could move its own audience by asking at refresh time.
func TestARotationReissuesTheBoundResource(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)
	const resource = "https://host.example.com/solutions/example/mcp"

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-mcp", resource)
	require.NoError(t, err)

	rotated, err := m.VerifyClientRefresh(ctx, pair.RefreshToken, "example-mcp", "")
	require.NoError(t, err)
	require.Contains(t, decodeJWTPayload(t, rotated.AccessToken),
		`"aud":["test-audience","`+resource+`"]`)

	identity, err := m.VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, resource, identity.Resource)
}

// A client naming the wrong resource on refresh is refused AND keeps its
// token. Checking the binding after the rotation would have cost it the session
// it legitimately holds, for sending a parameter RFC 8707 only lets it repeat.
func TestARefreshNamingTheWrongResourceDoesNotConsumeTheToken(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)
	const resource = "https://host.example.com/solutions/example/mcp"

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-mcp", resource)
	require.NoError(t, err)

	_, err = m.VerifyClientRefresh(ctx, pair.RefreshToken, "example-mcp",
		"https://host.example.com/solutions/other/mcp")
	require.ErrorIs(t, err, auth.ErrRefreshResourceMismatch)

	// The token survived: the rotation never happened.
	rotated, err := m.VerifyClientRefresh(ctx, pair.RefreshToken, "example-mcp", resource)
	require.NoError(t, err)
	require.NotEmpty(t, rotated.AccessToken)
}

// Repeating the resource it was granted is accepted, which is what a standard
// OAuth client actually does.
func TestARefreshRepeatingItsOwnResourceIsAccepted(t *testing.T) {
	ctx := context.Background()
	m, store := newMinter(t)
	host := mintHostSession(t, m, store)
	const resource = "https://host.example.com/solutions/example/mcp"

	pair, err := m.MintForClient(ctx, host.UserID, host.ID, "example-mcp", resource)
	require.NoError(t, err)

	rotated, err := m.VerifyClientRefresh(ctx, pair.RefreshToken, "example-mcp", resource)
	require.NoError(t, err)
	identity, err := m.VerifyAccess(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, resource, identity.Resource)
}
