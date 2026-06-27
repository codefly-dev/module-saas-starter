package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SessionRecord is the internal shape of a refresh-token session.
// Mirrors the columns of the `sessions` table.
type SessionRecord struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	OrgID         uuid.UUID
	OrgRole       string
	PlatformRole  string
	FamilyID      uuid.UUID
	RefreshHash   []byte // SHA-256 of the opaque refresh token
	IssuedAt      time.Time
	ExpiresAt     time.Time
	RevokedAt     *time.Time
	RevokedReason string
}

// SessionStore is the storage dependency of Ed25519Minter. Production uses a
// Postgres implementation against the sessions table; tests use an in-memory
// fake. The interface stays here in pkg/auth so concrete stores can be swapped
// without touching the minter.
//
// All methods must be safe for concurrent use.
type SessionStore interface {
	// Insert persists a new session row. Called during Mint and during
	// refresh rotation (with a new id but the same family_id).
	Insert(ctx context.Context, rec *SessionRecord) error

	// FindByRefreshHash looks up an active or revoked session by its
	// refresh token hash. Must use a constant-time DB query or an indexed
	// lookup — the caller does the crypto/subtle comparison separately if
	// needed. Returns ErrRefreshRevoked for not-found so the caller can't
	// distinguish "never existed" from "revoked".
	FindByRefreshHash(ctx context.Context, hash []byte) (*SessionRecord, error)

	// RevokeFamily marks every session sharing a family_id as revoked.
	// Used by /auth/logout and by refresh-reuse detection (OWASP rotation).
	RevokeFamily(ctx context.Context, familyID uuid.UUID, reason string) error

	// RevokeByUserID marks ALL sessions for a user as revoked. Called on
	// refresh token replay attacks to invalidate every active session,
	// forcing the user to re-authenticate everywhere.
	RevokeByUserID(ctx context.Context, userID uuid.UUID, reason string) error
}
