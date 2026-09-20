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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
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
	tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
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
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		return tx.QueryRow(ctx, `
			SELECT revoked_at IS NULL FROM sessions WHERE id = $1`, host.ID).Scan(&hostAlive)
	}))
	require.True(t, hostAlive,
		"authorizing a client must never evict the browser session that authorized it")
}
