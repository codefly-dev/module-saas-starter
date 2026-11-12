//go:build !pure

package pgauth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"accounts/pkg/auth"
	pgauth "accounts/pkg/auth/pg"
	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// clientSuccessor builds the record AuthorizeClientSession's callback returns:
// a new family for the named client, derived from the host session.
func clientSuccessor(current *auth.SessionRecord, clientID string) *auth.SessionRecord {
	next := *current
	next.ID = business.NewID()
	next.FamilyID = business.NewID()
	next.ClientID = clientID
	next.RefreshHash = []byte(business.NewID().String())
	next.RevokedAt = nil
	next.RevokedReason = ""
	next.LastActiveAt = time.Now()
	return &next
}

func countSessions(t *testing.T, userID uuid.UUID, clientID string) int {
	t.Helper()
	var count int
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM sessions
			WHERE user_id = $1 AND client_id = $2 AND revoked_at IS NULL`,
			userID, clientID).Scan(&count)
	}))
	return count
}

// The client's session must join the transaction that consumes the one-use
// authorization code. Opening its own would commit the session independently:
// a caller that then failed to commit would leave a live session behind a code
// that is still redeemable, and every replay would mint another one. Rolling
// the caller's transaction back is the only way to observe which it did.
func TestAuthorizeClientSessionJoinsTheCallersTransaction(t *testing.T) {
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	host := newRecord(userID)
	require.NoError(t, store.Insert(context.Background(), host))

	rolledBack := errors.New("caller rolled back")
	err := testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		if err := store.AuthorizeClientSession(
			auth.WithAtomicSessionTransaction(ctx), userID, host.ID,
			func(current *auth.SessionRecord, _ auth.RefreshAuthorization) (*auth.SessionRecord, error) {
				return clientSuccessor(current, "example-addin"), nil
			},
		); err != nil {
			return err
		}
		require.Equal(t, 1, countSessionsInTx(t, ctx, userID, "example-addin"),
			"the session must be visible inside the caller's transaction")
		return rolledBack
	})
	require.ErrorIs(t, err, rolledBack)

	require.Zero(t, countSessions(t, userID, "example-addin"),
		"the client session must not survive a rollback of the transaction that consumed the code")
}

func countSessionsInTx(t *testing.T, ctx context.Context, userID uuid.UUID, clientID string) int {
	t.Helper()
	tx := storetx.Tx(ctx)
	var count int
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT count(*) FROM sessions WHERE user_id = $1 AND client_id = $2`,
		userID, clientID).Scan(&count))
	return count
}

// A client that re-authorizes on every launch must not accumulate families, and
// must never be the reason the person's real devices are evicted.
func TestClientSessionsAreBoundedAndDoNotEvictDevices(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)

	host := newRecord(userID)
	require.NoError(t, store.Insert(ctx, host))

	// Authorize the same client far more times than the ceiling allows.
	for range auth.DefaultMaxActiveDevices + 4 {
		require.NoError(t, store.AuthorizeClientSession(ctx, userID, host.ID,
			func(current *auth.SessionRecord, _ auth.RefreshAuthorization) (*auth.SessionRecord, error) {
				return clientSuccessor(current, "example-addin"), nil
			}))
	}

	require.LessOrEqual(t, countSessions(t, userID, "example-addin"), auth.DefaultMaxActiveDevices,
		"client families must be bounded by the same ceiling devices are")

	var hostAlive bool
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		return tx.QueryRow(ctx, `
			SELECT revoked_at IS NULL FROM sessions WHERE id = $1`, host.ID).Scan(&hostAlive)
	}))
	require.True(t, hostAlive,
		"authorizing a client must never evict the browser session that authorized it")
}

// A refresh rotates the host session's row: the family's live row is a new one
// and the presented row is revoked "rotated", while the access token minted
// from it is honoured at the perimeter until it expires (rotation revokes no
// access token). A browser whose cookie another tab or document just rotated
// is in exactly that state, so an authorization keyed by that token's session
// id (a client's session, a role view, a team cast) must follow the rotation
// to the family's live row, not refuse the person as if they had signed out.
func TestAuthorizeClientSessionFollowsTheRotatedHostSession(t *testing.T) {
	ctx := context.Background()
	store := pgauth.NewSessionStore(testStore)
	userID := seedUser(t)
	host := newRecord(userID)
	require.NoError(t, store.Insert(ctx, host))

	var successor *auth.SessionRecord
	require.NoError(t, store.RotateRefresh(ctx, host.RefreshHash, func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) (*auth.SessionRecord, error) {
		successor = replacementRecord(current, authorization)
		return successor, nil
	}))

	var authorizedFrom uuid.UUID
	err := store.AuthorizeClientSession(ctx, userID, host.ID, func(current *auth.SessionRecord, _ auth.RefreshAuthorization) (*auth.SessionRecord, error) {
		authorizedFrom = current.ID
		return clientSuccessor(current, "example-addin"), nil
	})
	require.NoError(t, err, "the access token still works everywhere else; the cast must not be refused for a rotation it did not see")
	require.Equal(t, successor.ID, authorizedFrom, "the authorization is resolved from the family's live row")
	require.Equal(t, 1, countSessions(t, userID, "example-addin"))

	// A family that is gone (signed out) is still refused through its old id.
	require.NoError(t, store.RevokeFamily(ctx, host.FamilyID, "logout"))
	err = store.AuthorizeClientSession(ctx, userID, host.ID, func(current *auth.SessionRecord, _ auth.RefreshAuthorization) (*auth.SessionRecord, error) {
		return clientSuccessor(current, "example-addin"), nil
	})
	require.ErrorIs(t, err, auth.ErrSessionUnavailable)
}
