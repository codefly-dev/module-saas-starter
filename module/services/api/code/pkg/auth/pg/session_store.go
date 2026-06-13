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

	"api/pkg/auth"
	"api/pkg/business"
)

// RLSWrapper is the subset of *infra.PostgresStore that
// SessionStore needs to wrap each method in the right RLS scope.
// Defining this interface here (rather than importing infra)
// keeps pkg/auth/pg decoupled from the storage layer's concrete
// type — useful for fakes in tests and for the future "different
// auth backend" path.
type RLSWrapper interface {
	WithUserTx(ctx context.Context, userID string, fn func(context.Context) error) error
	WithBypass(ctx context.Context, fn func(context.Context) error) error
}

// SessionStore is a Postgres-backed auth.SessionStore over the existing
// sessions table (migrations/6_create_sessions.up.sql).
//
// The table stores refresh_token_hash as hex-encoded TEXT; this store
// converts []byte ↔ hex at the boundary so the rest of pkg/auth can
// stay binary-clean.
//
// sessions is RLS-protected (Phase 2H, migration 35). Each method
// wraps in the appropriate scope:
//
//   - Insert / RevokeByUserID — caller has the user-id, use
//     WithUserTx so the policy lets the tenant-scoped row through.
//   - FindByRefreshHash / RevokeFamily — caller has only the token
//     hash or family-id (cross-user lookups by design: the refresh
//     flow is "I have a token, who owns it?"), use WithBypass.
type SessionStore struct {
	rls RLSWrapper
}

// NewSessionStore wires a SessionStore to the RLS-aware wrapper.
// In production the wrapper is *infra.PostgresStore (which carries
// the connection pool with the BeforeAcquire SET ROLE hook); in
// tests it can be the same store or a thin pool-only adapter.
func NewSessionStore(rls RLSWrapper) *SessionStore {
	return &SessionStore{rls: rls}
}

// txFromCtx pulls the pgx.Tx that WithUserTx / WithBypass put on
// ctx under the literal "tx" key. Same convention as PostgresStore's
// getQueryExecutor in pkg/infra. Returns nil if the tx isn't there
// — should never happen inside a wrap callback, but a nil-deref
// would be a clearer failure than a silent un-RLS'd query.
func txFromCtx(ctx context.Context) pgx.Tx {
	tx, _ := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with PostgresStore.getQueryExecutor
	return tx
}

// Insert persists a new sessions row with the Identity state snapshotted
// onto the row (org_id, org_role, platform_role). The snapshot is valid
// for the access token lifetime (15 min) — refresh rotation re-resolves
// against current state at the IdentityResolver layer.
//
// Wrapped in WithUserTx so the row's user_id matches app.current_user_id
// — the WITH CHECK on sessions's RLS policy passes.
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

	return s.rls.WithUserTx(ctx, rec.UserID.String(), func(ctx context.Context) error {
		tx := txFromCtx(ctx)
		_, err := tx.Exec(ctx, `
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
	})
}

// FindByRefreshHash looks up a session row by hash. Returns
// auth.ErrRefreshRevoked on not-found to avoid oracle attacks.
//
// The row is returned regardless of revoked state; the caller
// (ed25519minter) decides whether reuse detection applies.
//
// Cross-user lookup: the refresh flow doesn't know the user id yet
// — that's what we're resolving. WithBypass elevates so the
// SELECT can reach any user's row. Counts toward the
// infra.BypassCounters audit trail (Phase 4).
func (s *SessionStore) FindByRefreshHash(ctx context.Context, hash []byte) (*auth.SessionRecord, error) {
	hashHex := hex.EncodeToString(hash)

	var rec auth.SessionRecord
	var revokedAt *time.Time
	var revokedReason *string
	var orgID *uuid.UUID

	wrapErr := s.rls.WithBypass(ctx, func(ctx context.Context) error {
		tx := txFromCtx(ctx)
		err := tx.QueryRow(ctx, `
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
		return err
	})
	if wrapErr != nil {
		if errors.Is(wrapErr, pgx.ErrNoRows) {
			return nil, auth.ErrRefreshRevoked
		}
		return nil, wrapErr
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
//
// Cross-user by potential: family_id is unique enough in practice
// that "revoke this family" only touches rows for one user, but
// the operation is keyed by family_id, not user_id. WithBypass so
// the UPDATE can find the rows regardless of who owns them.
// Audited via the bypass counter.
func (s *SessionStore) RevokeFamily(ctx context.Context, familyID uuid.UUID, reason string) error {
	return s.rls.WithBypass(ctx, func(ctx context.Context) error {
		tx := txFromCtx(ctx)
		_, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET revoked_at = NOW(), revoked_reason = $2
			 WHERE family_id = $1 AND revoked_at IS NULL`,
			familyID, reason,
		)
		return err
	})
}

// RevokeByUserID marks ALL sessions for a user as revoked. This is the
// nuclear option used when a refresh token replay attack is detected —
// every session for the user is killed, forcing re-authentication on all
// devices.
//
// Caller has the user-id; use WithUserTx so the UPDATE is scoped
// rather than spanning all users.
func (s *SessionStore) RevokeByUserID(ctx context.Context, userID uuid.UUID, reason string) error {
	return s.rls.WithUserTx(ctx, userID.String(), func(ctx context.Context) error {
		tx := txFromCtx(ctx)
		_, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET revoked_at = NOW(), revoked_reason = $2
			 WHERE user_id = $1 AND revoked_at IS NULL`,
			userID, reason,
		)
		return err
	})
}

// Compile-time assertion.
var _ auth.SessionStore = (*SessionStore)(nil)
