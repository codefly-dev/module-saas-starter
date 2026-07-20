package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SessionRecord is the internal shape of a refresh-token session.
// Mirrors the columns of the `sessions` table.
type SessionRecord struct {
	ID                    uuid.UUID
	UserID                uuid.UUID
	OrgID                 uuid.UUID
	OrgRole               string
	PlatformRole          string
	MFASatisfied          bool
	AuthenticationMethods []string
	AuthenticatedAt       time.Time
	AssuranceLevel        string
	MFAVerifiedAt         time.Time
	DeviceInfo            map[string]string
	IPAddress             string
	FamilyID              uuid.UUID
	RefreshHash           []byte    // SHA-256 of the opaque refresh token
	IssuedAt              time.Time // fixed family start, persisted as created_at
	LastActiveAt          time.Time
	IdleExpiresAt         time.Time
	ExpiresAt             time.Time
	RevokedAt             *time.Time
	RevokedReason         string
}

// RefreshAuthorization is the current authorization state resolved while the
// presented refresh-token row is locked. It deliberately excludes the
// authentication evidence persisted on SessionRecord: refresh may project
// current authorization, but it must never manufacture a newer or stronger
// authentication ceremony.
type RefreshAuthorization struct {
	OrgID        uuid.UUID
	OrgRole      string
	PlatformRole string
	MFAEnrolled  bool
}

// SessionStore is the storage dependency of Ed25519Minter. Production uses a
// Postgres implementation against the sessions table; tests use an in-memory
// fake. The interface stays here in pkg/auth so concrete stores can be swapped
// without touching the minter.
//
// All methods must be safe for concurrent use.
type SessionStore interface {
	// Insert persists the first session in a new family. Refresh successors
	// are inserted only through RotateRefresh.
	Insert(ctx context.Context, rec *SessionRecord) error

	// FindByRefreshHash looks up an active or revoked session by its
	// refresh token hash. Must use a constant-time DB query or an indexed
	// lookup — the caller does the crypto/subtle comparison separately if
	// needed. Returns ErrRefreshRevoked for not-found so the caller can't
	// distinguish "never existed" from "revoked".
	FindByRefreshHash(ctx context.Context, hash []byte) (*SessionRecord, error)

	// RotateRefresh atomically resolves current authorization, consumes the
	// session identified by hash, and inserts its successor. The store must lock
	// the presented row, require an active user and a current selected-org
	// membership, and resolve current org role, platform role, and verified MFA
	// enrollment before it calls replacement. Family revocation and successor
	// insertion happen in that same transaction. Concurrent reuse of an
	// already-consumed token must revoke every active session for that user,
	// commit that revocation, and return ErrRefreshReuse. Unknown tokens return
	// ErrRefreshRevoked.
	//
	// replacement performs cryptographic token construction from the locked
	// session snapshot and freshly resolved authorization. Ordinary callback
	// errors abort without consuming the token. An error made with
	// RejectRefresh commits terminal family revocation before returning
	// ErrRefreshRevoked.
	RotateRefresh(
		ctx context.Context,
		hash []byte,
		replacement func(current *SessionRecord, authorization RefreshAuthorization) (*SessionRecord, error),
	) error

	// ExchangeOrganization locks the current active session row, verifies the
	// same user still belongs to targetOrgID, resolves the current tenant role
	// and global authorization, and invokes issue before persisting the new
	// selected organization on that row. It does not rotate the refresh token,
	// change the device family, or advance idle expiry. Stale session ids return
	// ErrSessionUnavailable; a non-membership returns
	// ErrOrganizationAccessDenied without mutating the session.
	ExchangeOrganization(
		ctx context.Context,
		userID uuid.UUID,
		sessionID uuid.UUID,
		targetOrgID uuid.UUID,
		issue func(current *SessionRecord, authorization RefreshAuthorization) error,
	) error

	// RevokeFamily marks every session sharing a family_id as revoked.
	// Used by /auth/logout.
	RevokeFamily(ctx context.Context, familyID uuid.UUID, reason string) error
}
