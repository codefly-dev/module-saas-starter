package ed25519minter_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// keyedRevoker answers what was actually written, which the shared fakeRevoker
// cannot: it returns one fixed verdict for every id, so it can show THAT a
// marker was written but never that the right id was matched — and the whole
// defect here is a marker written under an id nothing carries.
type keyedRevoker struct {
	mu       sync.Mutex
	sessions map[string]time.Duration
	jtis     map[string]time.Duration
}

func newKeyedRevoker() *keyedRevoker {
	return &keyedRevoker{
		sessions: map[string]time.Duration{},
		jtis:     map[string]time.Duration{},
	}
}

func (k *keyedRevoker) Revoke(_ context.Context, jti string, ttl time.Duration) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.jtis[jti] = ttl
	return nil
}

func (k *keyedRevoker) IsRevoked(_ context.Context, jti string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.jtis[jti]
	return ok, nil
}

func (k *keyedRevoker) RevokeSession(_ context.Context, id string, ttl time.Duration) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sessions[id] = ttl
	return nil
}

func (k *keyedRevoker) IsSessionRevoked(_ context.Context, id string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.sessions[id]
	return ok, nil
}

// Signing out kills every access token in the refresh family, not only the
// rotation the caller happens to be holding.
//
// The sequence is the one that was broken: mint, refresh, then log out with the
// refreshed pair. Every refresh mints a NEW session id and keeps the family, and
// the sign-out wrote its access-revocation marker under the CURRENT session id —
// so an access token minted before that refresh carried a different `sid`,
// matched no marker, and stayed valid until its natural expiry. A token stolen
// before a refresh therefore survived the sign-out that was supposed to end it.
//
// The family id is the identifier that does not rotate, so it is what the token
// now carries and what the sign-out revokes.
func TestSigningOutRevokesTheWholeRefreshFamily(t *testing.T) {
	ctx := context.Background()
	m, _ := newMinter(t)
	m.SetRevoker(newKeyedRevoker())

	first, err := m.Mint(ctx, newIdentity())
	require.NoError(t, err)

	// Both tokens verify before anything is revoked, so the assertions below
	// cannot pass on a minter that rejects everything.
	_, err = m.VerifyAccess(first.AccessToken)
	require.NoError(t, err)

	second, err := m.VerifyRefresh(ctx, first.RefreshToken)
	require.NoError(t, err)
	_, err = m.VerifyAccess(second.AccessToken)
	require.NoError(t, err)

	// The rotation really did change the session id — otherwise this test would
	// pass for the wrong reason, on a build where `sid` happened to be stable.
	require.NotEqual(t,
		sessionIDOf(t, first.AccessToken), sessionIDOf(t, second.AccessToken),
		"a refresh must rotate the session id, or this finding cannot occur")

	require.NoError(t, m.Revoke(ctx, second.RefreshToken))

	_, err = m.VerifyAccess(second.AccessToken)
	require.Error(t, err, "the access token presented at sign-out must be refused")

	_, err = m.VerifyAccess(first.AccessToken)
	require.Error(t, err,
		"an access token minted BEFORE the last refresh must be refused too: it is "+
			"the same family, and the sign-out ended the family")
}

// The counterpart: a sign-out in one family must not revoke another's. Without
// it, stamping a shared or zero family id would pass the test above while
// signing every other person out.
func TestSigningOutLeavesAnotherFamilyAlone(t *testing.T) {
	ctx := context.Background()
	m, _ := newMinter(t)
	m.SetRevoker(newKeyedRevoker())

	mine, err := m.Mint(ctx, newIdentity())
	require.NoError(t, err)
	theirs, err := m.Mint(ctx, newIdentity())
	require.NoError(t, err)

	require.NoError(t, m.Revoke(ctx, mine.RefreshToken))

	_, err = m.VerifyAccess(mine.AccessToken)
	require.Error(t, err)
	_, err = m.VerifyAccess(theirs.AccessToken)
	require.NoError(t, err, "another family's session must survive this sign-out")
}

// sessionIDOf reads the `sid` claim off an access token's payload, so the test
// can prove the rotation it depends on actually happened.
func sessionIDOf(t *testing.T, accessToken string) string {
	t.Helper()
	var claims struct {
		SessionID string `json:"sid"`
	}
	require.NoError(t, json.Unmarshal([]byte(decodeJWTPayload(t, accessToken)), &claims))
	require.NotEmpty(t, claims.SessionID)
	return claims.SessionID
}
