// Package pgauth provides Postgres implementations of the auth.SessionStore
// and auth.IdentityResolver interfaces.
//
// These back the JWTMinter in production; tests use the in-memory fakes in
// pkg/auth's _test files.
package pgauth

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"api/pkg/auth"
	"api/pkg/business"
)

// SessionStore is a Postgres-backed auth.SessionStore over the existing
// sessions table (migrations/6_create_sessions.up.sql).
//
// The table stores refresh_token_hash as hex-encoded TEXT; this store
// converts []byte ↔ hex at the boundary so the rest of pkg/auth can
// stay binary-clean.
type SessionStore struct {
	pool *pgxpool.Pool
}

func NewSessionStore(pool *pgxpool.Pool) *SessionStore {
	return &SessionStore{pool: pool}
}

// Insert persists a new sessions row with the Identity state snapshotted
// onto the row (org_id, org_role, platform_role). The snapshot is valid
// for the access token lifetime (15 min) — refresh rotation re-resolves
// against current state at the IdentityResolver layer.
func (s *SessionStore) Insert(ctx context.Context, rec *auth.SessionRecord) error {
	if rec.ID == uuid.Nil {
		rec.ID = business.NewID()
	}
	if rec.FamilyID == uuid.Nil {
		return errors.New("pgauth: session record missing family id")
	}
	if rec.IssuedAt.IsZero() {
		rec.IssuedAt = time.Now()
	}
	hashHex := hex.EncodeToString(rec.RefreshHash)

	var orgIDArg any
	if rec.OrgID != uuid.Nil {
		orgIDArg = rec.OrgID
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (
			id, user_id, refresh_token_hash, family_id, device_info, ip_address,
			created_at, last_active_at, expires_at,
			org_id, org_role, platform_role
		) VALUES ($1, $2, $3, $4, '{}'::jsonb, NULL, $5, $5, $6, $7, $8, $9)`,
		rec.ID, rec.UserID, hashHex, rec.FamilyID,
		rec.IssuedAt, rec.ExpiresAt,
		orgIDArg, rec.OrgRole, rec.PlatformRole,
	)
	return err
}

// FindByRefreshHash looks up a session row by hash. Returns
// auth.ErrRefreshRevoked on not-found to avoid oracle attacks.
//
// The row is returned regardless of revoked state; the caller
// (ed25519minter) decides whether reuse detection applies.
func (s *SessionStore) FindByRefreshHash(ctx context.Context, hash []byte) (*auth.SessionRecord, error) {
	hashHex := hex.EncodeToString(hash)

	var rec auth.SessionRecord
	var revokedAt *time.Time
	var revokedReason *string
	var orgID *uuid.UUID

	err := s.pool.QueryRow(ctx, `
		SELECT
			id, user_id, family_id,
			created_at, expires_at, revoked_at, revoked_reason,
			org_id, org_role, platform_role
		FROM sessions
		WHERE refresh_token_hash = $1
		LIMIT 1`, hashHex).Scan(
		&rec.ID, &rec.UserID, &rec.FamilyID,
		&rec.IssuedAt, &rec.ExpiresAt, &revokedAt, &revokedReason,
		&orgID, &rec.OrgRole, &rec.PlatformRole,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrRefreshRevoked
		}
		return nil, err
	}

	rec.RefreshHash = hash
	rec.RevokedAt = revokedAt
	if revokedReason != nil {
		rec.RevokedReason = *revokedReason
	}
	if orgID != nil {
		rec.OrgID = *orgID
	}
	return &rec, nil
}

// RevokeFamily marks all sessions sharing a family_id as revoked.
func (s *SessionStore) RevokeFamily(ctx context.Context, familyID uuid.UUID, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions
		   SET revoked_at = NOW(), revoked_reason = $2
		 WHERE family_id = $1 AND revoked_at IS NULL`,
		familyID, reason,
	)
	return err
}

// RevokeByUserID marks ALL sessions for a user as revoked. This is the
// nuclear option used when a refresh token replay attack is detected —
// every session for the user is killed, forcing re-authentication on all
// devices.
func (s *SessionStore) RevokeByUserID(ctx context.Context, userID uuid.UUID, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions
		   SET revoked_at = NOW(), revoked_reason = $2
		 WHERE user_id = $1 AND revoked_at IS NULL`,
		userID, reason,
	)
	return err
}

// Compile-time assertion.
var _ auth.SessionStore = (*SessionStore)(nil)
