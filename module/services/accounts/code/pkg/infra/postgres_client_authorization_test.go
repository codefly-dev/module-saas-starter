//go:build !pure

package infra_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// seedClientAuthorizationCode writes a code row directly, so the sweep can be
// exercised without standing up a whole sign-in. session_id is a real session
// because the row carries a foreign key to it.
func seedClientAuthorizationCode(t *testing.T, userID string, expiresAt time.Time) string {
	t.Helper()
	id := business.NewIDString()
	sessionID := business.NewIDString()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		if _, err := tx.Exec(ctx, `
			INSERT INTO sessions (id, user_id, family_id, refresh_token_hash, expires_at, idle_expires_at)
			VALUES ($1, $2, $3, $4, $5, $5)`,
			sessionID, userID, business.NewIDString(), business.NewIDString(),
			time.Now().Add(time.Hour)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO client_authorization_codes (
				id, code_hash, client_id, redirect_uri, code_challenge,
				user_id, session_id, expires_at
			) VALUES ($1, $2, 'example-addin', 'https://localhost:3000/auth/callback', $3, $4, $5, $6)`,
			id, codeHash(id), business.NewIDString(),
			userID, sessionID, expiresAt)
		return err
	}))
	return id
}

// codeHash renders a value of the exact width the code_hash CHECK requires.
func codeHash(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(digest[:])
}

func clientAuthorizationCodeExists(t *testing.T, id string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM client_authorization_codes WHERE id = $1)`, id).Scan(&exists)
	}))
	return exists
}

// Nothing else deletes an authorization code: it is dead a minute after issue,
// redeemed or not, so without this sweep the table grows by a row for every
// client sign-in forever, each one pinning a sessions row through its foreign
// key. The suite's database is shared, so this asserts on its own rows.
func TestPostgresDeleteExpiredClientAuthorizationCodesSweepsOnlyLapsedRows(t *testing.T) {
	user := seedUser(t)
	lapsed := seedClientAuthorizationCode(t, user, time.Now().Add(-48*time.Hour))
	live := seedClientAuthorizationCode(t, user, time.Now().Add(time.Hour))

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, _, codes, err := testStore.DeleteExpiredAuthenticationCeremonies(ctx, time.Now().Add(-24*time.Hour))
		require.Positive(t, codes)
		return err
	}))

	require.False(t, clientAuthorizationCodeExists(t, lapsed))
	require.True(t, clientAuthorizationCodeExists(t, live))
}
