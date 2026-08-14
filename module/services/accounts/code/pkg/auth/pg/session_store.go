// Package pgauth provides Postgres implementations of the auth.SessionStore
// and auth.IdentityResolver interfaces.
//
// These back the JWTMinter in production; tests use the in-memory fakes in
// pkg/auth's _test files.
package pgauth

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"accounts/pkg/auth"
	"accounts/pkg/business"
)

// RLSWrapper is the subset of *infra.PostgresStore that
// SessionStore needs to wrap each method in the right RLS scope.
// Defining this interface here (rather than importing infra)
// keeps pkg/auth/pg decoupled from the storage layer's concrete
// type — useful for fakes in tests and for the future "different
// auth backend" path.
type RLSWrapper interface {
	WithUserTx(ctx context.Context, userID string, fn func(context.Context) error) error
	WithControlPlane(ctx context.Context, fn func(context.Context) error) error
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
//   - Insert — caller has the user-id, so WithUserTx lets the scoped row
//     through.
//   - FindByRefreshHash / RotateRefresh / RevokeFamily — caller has only a
//     token hash or family-id, so the named control-plane capability performs
//     the deliberately cross-user lookup.
type SessionStore struct {
	rls       RLSWrapper
	policy    auth.SessionPolicy
	configErr error
}

// NewSessionStore wires a SessionStore to the RLS-aware wrapper.
// In production the wrapper is *infra.PostgresStore (which carries
// the connection pool with the BeforeAcquire SET ROLE hook); in
// tests it can be the same store or a thin pool-only adapter.
func NewSessionStore(rls RLSWrapper, configured ...auth.SessionPolicy) *SessionStore {
	policy := auth.DefaultSessionPolicy()
	if len(configured) > 0 {
		policy = configured[0].WithDefaults()
	}
	return &SessionStore{rls: rls, policy: policy, configErr: policy.Validate()}
}

// txFromCtx pulls the pgx.Tx that WithUserTx / WithControlPlane put on
// ctx under the literal "tx" key. Same convention as PostgresStore's
// getQueryExecutor in pkg/infra. Returns nil if the tx isn't there
// — should never happen inside a wrap callback, but a nil-deref
// would be a clearer failure than a silent un-RLS'd query.
func txFromCtx(ctx context.Context) pgx.Tx {
	tx, _ := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with PostgresStore.getQueryExecutor
	return tx
}

// Insert persists a new sessions row with the Identity state snapshotted
// onto the row (org_id, org_role, platform_role). The snapshot is valid for
// the access-token lifetime; RotateRefresh re-resolves current authorization
// before minting the next access token.
//
// Wrapped in WithUserTx so the row's user_id matches app.current_user_id
// — the WITH CHECK on sessions's RLS policy passes.
func (s *SessionStore) Insert(ctx context.Context, rec *auth.SessionRecord) error {
	if s.configErr != nil {
		return fmt.Errorf("pgauth: invalid session policy: %w", s.configErr)
	}
	if err := prepareSessionRecord(rec); err != nil {
		return err
	}

	insert := func(ctx context.Context, tx pgx.Tx) error {
		if err := s.admitDeviceSession(ctx, tx, rec.UserID); err != nil {
			return err
		}
		return insertSession(ctx, tx, rec)
	}

	// CompleteMFAChallenge owns a wider transaction that locks and consumes
	// the one-use challenge. Reuse it so session issuance and consumption
	// commit atomically. Normal Mint calls still enter a user-scoped tx.
	if tx := txFromCtx(ctx); tx != nil && auth.HasAtomicSessionTransaction(ctx) {
		return insert(ctx, tx)
	}
	return s.rls.WithUserTx(ctx, rec.UserID.String(), func(ctx context.Context) error {
		return insert(ctx, txFromCtx(ctx))
	})
}

// admitDeviceSession serializes initial device-session creation on the user
// row, retires sessions that are already beyond absolute/idle policy, and
// evicts the oldest active device families to make room for the new one.
func (s *SessionStore) admitDeviceSession(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT status::text FROM users WHERE uuid = $1 FOR UPDATE`, userID).Scan(&status); err != nil {
		return err
	}
	if status != "active" {
		return auth.ErrAccountInactive
	}

	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		   SET revoked_at = CURRENT_TIMESTAMP,
		       revoked_reason = CASE
		           WHEN expires_at <= CURRENT_TIMESTAMP THEN $2
		           ELSE $3
		       END
		 WHERE user_id = $1
		   AND revoked_at IS NULL
		   AND (
		       expires_at <= CURRENT_TIMESTAMP
		       OR idle_expires_at <= CURRENT_TIMESTAMP
		   )`,
		userID,
		auth.RefreshRejectionAbsoluteLifetime,
		auth.RefreshRejectionIdleTimeout,
	); err != nil {
		return err
	}

	_, err := tx.Exec(ctx, `
		WITH active_devices AS (
			SELECT family_id,
			       ROW_NUMBER() OVER (
			           ORDER BY MAX(last_active_at) DESC, family_id DESC
			       ) AS recency
			FROM sessions
			WHERE user_id = $1 AND revoked_at IS NULL
			GROUP BY family_id
		), evicted AS (
			SELECT family_id
			FROM active_devices
			WHERE recency >= $2
		)
		UPDATE sessions
		   SET revoked_at = CURRENT_TIMESTAMP,
		       revoked_reason = 'device_limit_exceeded'
		 WHERE user_id = $1
		   AND revoked_at IS NULL
		   AND family_id IN (SELECT family_id FROM evicted)`,
		userID, s.policy.MaxActiveDevices,
	)
	return err
}

func prepareSessionRecord(rec *auth.SessionRecord) error {
	if rec == nil {
		return errors.New("pgauth: session record is required")
	}
	if rec.UserID == uuid.Nil {
		return errors.New("pgauth: session record missing user id")
	}
	if rec.ID == uuid.Nil {
		rec.ID = business.NewID()
	}
	if rec.FamilyID == uuid.Nil {
		return errors.New("pgauth: session record missing family id")
	}
	if rec.IssuedAt.IsZero() {
		rec.IssuedAt = time.Now()
	}
	if rec.LastActiveAt.IsZero() {
		rec.LastActiveAt = rec.IssuedAt
	}
	if rec.IdleExpiresAt.IsZero() {
		return errors.New("pgauth: session record missing idle expiry")
	}
	if rec.AssuranceLevel == "" {
		rec.AssuranceLevel = auth.AssuranceLevelAAL1
	}
	if len(rec.RefreshHash) == 0 {
		return errors.New("pgauth: session record missing refresh hash")
	}
	if rec.ExpiresAt.IsZero() {
		return errors.New("pgauth: session record missing expiry")
	}
	return nil
}

func insertSession(ctx context.Context, tx pgx.Tx, rec *auth.SessionRecord) error {
	hashHex := hex.EncodeToString(rec.RefreshHash)
	deviceInfo, err := json.Marshal(rec.DeviceInfo)
	if err != nil {
		return fmt.Errorf("pgauth: encode session device info: %w", err)
	}
	if rec.DeviceInfo == nil {
		deviceInfo = []byte("{}")
	}

	var orgIDArg any
	if rec.OrgID != uuid.Nil {
		orgIDArg = rec.OrgID
	}
	var authenticatedAtArg any
	if !rec.AuthenticatedAt.IsZero() {
		authenticatedAtArg = rec.AuthenticatedAt
	}
	var mfaVerifiedAtArg any
	if !rec.MFAVerifiedAt.IsZero() {
		mfaVerifiedAtArg = rec.MFAVerifiedAt
	}
	authenticationMethods := rec.AuthenticationMethods
	if authenticationMethods == nil {
		authenticationMethods = []string{}
	}

	_, err = tx.Exec(ctx, `
			INSERT INTO sessions (
				id, user_id, refresh_token_hash, family_id, device_info, ip_address,
				created_at, last_active_at, idle_expires_at, expires_at,
				org_id, org_role, platform_role, mfa_satisfied,
				authentication_methods, auth_time, assurance_level, mfa_verified_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
				$14, $15, $16, $17, $18
			)`,
		rec.ID, rec.UserID, hashHex, rec.FamilyID, deviceInfo, nilIfEmpty(rec.IPAddress),
		rec.IssuedAt, rec.LastActiveAt, rec.IdleExpiresAt, rec.ExpiresAt,
		orgIDArg, rec.OrgRole, rec.PlatformRole, rec.MFASatisfied,
		authenticationMethods, authenticatedAtArg, rec.AssuranceLevel, mfaVerifiedAtArg,
	)
	return err
}

// FindByRefreshHash looks up a session row by hash. Returns
// auth.ErrRefreshRevoked on not-found to avoid oracle attacks.
//
// The row is returned regardless of revoked state; the caller
// (ed25519minter) decides whether reuse detection applies.
//
// Cross-user lookup: the refresh flow doesn't know the user id yet
// — that's what we're resolving. WithControlPlane elevates so the
// SELECT can reach any user's row. Counts toward the
// infra.ControlPlaneCounters audit trail (Phase 4).
func (s *SessionStore) FindByRefreshHash(ctx context.Context, hash []byte) (*auth.SessionRecord, error) {
	hashHex := hex.EncodeToString(hash)
	var rec *auth.SessionRecord

	wrapErr := s.rls.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := txFromCtx(ctx)
		var err error
		rec, err = scanSession(tx.QueryRow(ctx, `
			SELECT
				id, user_id, family_id,
				created_at, last_active_at, idle_expires_at, expires_at, revoked_at, revoked_reason,
				device_info, ip_address,
				org_id, org_role, platform_role, mfa_satisfied,
				authentication_methods, auth_time, assurance_level, mfa_verified_at
			FROM sessions
			WHERE refresh_token_hash = $1
			LIMIT 1`, hashHex), hash)
		return err
	})
	if wrapErr != nil {
		if errors.Is(wrapErr, pgx.ErrNoRows) {
			return nil, auth.ErrRefreshRevoked
		}
		return nil, wrapErr
	}

	return rec, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(row rowScanner, hash []byte) (*auth.SessionRecord, error) {
	var rec auth.SessionRecord
	var revokedAt *time.Time
	var revokedReason *string
	var orgID *uuid.UUID
	var authenticatedAt *time.Time
	var mfaVerifiedAt *time.Time
	var deviceInfo []byte
	var ipAddress *string
	if err := row.Scan(
		&rec.ID, &rec.UserID, &rec.FamilyID,
		&rec.IssuedAt, &rec.LastActiveAt, &rec.IdleExpiresAt, &rec.ExpiresAt, &revokedAt, &revokedReason,
		&deviceInfo, &ipAddress,
		&orgID, &rec.OrgRole, &rec.PlatformRole, &rec.MFASatisfied,
		&rec.AuthenticationMethods, &authenticatedAt, &rec.AssuranceLevel, &mfaVerifiedAt,
	); err != nil {
		return nil, err
	}
	rec.RefreshHash = append([]byte(nil), hash...)
	rec.RevokedAt = revokedAt
	if revokedReason != nil {
		rec.RevokedReason = *revokedReason
	}
	if orgID != nil {
		rec.OrgID = *orgID
	}
	if authenticatedAt != nil {
		rec.AuthenticatedAt = *authenticatedAt
	}
	if mfaVerifiedAt != nil {
		rec.MFAVerifiedAt = *mfaVerifiedAt
	}
	if len(deviceInfo) > 0 {
		if err := json.Unmarshal(deviceInfo, &rec.DeviceInfo); err != nil {
			return nil, fmt.Errorf("pgauth: decode session device info: %w", err)
		}
	}
	if ipAddress != nil {
		rec.IPAddress = *ipAddress
	}
	return &rec, nil
}

// RotateRefresh is the single refresh-token consumption boundary. The
// presented row is locked before its state is inspected, so only one caller
// can construct a successor. Family revocation and successor insertion share
// the same control-plane transaction; an insertion/signing failure rolls both
// back. A caller that arrives after consumption commits user-wide replay
// revocation before ErrRefreshReuse is returned.
func (s *SessionStore) RotateRefresh(
	ctx context.Context,
	hash []byte,
	replacement func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) (*auth.SessionRecord, error),
) error {
	if len(hash) == 0 {
		return auth.ErrRefreshRevoked
	}
	if replacement == nil {
		return errors.New("pgauth: refresh replacement callback is required")
	}

	hashHex := hex.EncodeToString(hash)
	var outcome error
	err := s.rls.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := txFromCtx(ctx)
		current, err := scanSession(tx.QueryRow(ctx, `
			SELECT
				id, user_id, family_id,
				created_at, last_active_at, idle_expires_at, expires_at, revoked_at, revoked_reason,
				device_info, ip_address,
				org_id, org_role, platform_role, mfa_satisfied,
				authentication_methods, auth_time, assurance_level, mfa_verified_at
			FROM sessions
			WHERE refresh_token_hash = $1
			LIMIT 1
			FOR UPDATE`, hashHex), hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrRefreshRevoked
		}
		if err != nil {
			return err
		}

		// Authorization-source rows are not locked after the session row. The
		// database invalidation triggers serialize every relevant mutation by
		// updating affected session rows in the mutation transaction. Adding a
		// second, inverse lock order here would create a session↔user deadlock.
		status, err := loadUserStatus(ctx, tx, current.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrRefreshRevoked
		}
		if err != nil {
			return err
		}
		if status != "active" {
			if err := revokeActiveUserSessions(ctx, tx, current.UserID, auth.RefreshRejectionUserNotActive); err != nil {
				return err
			}
			outcome = auth.ErrRefreshRevoked
			return nil
		}

		if current.RevokedAt != nil {
			if current.RevokedReason != "rotated" {
				return auth.ErrRefreshRevoked
			}
			if err := revokeActiveUserSessions(ctx, tx, current.UserID, "refresh-reuse-all-sessions"); err != nil {
				return err
			}
			// Returning nil from the transaction callback is intentional: replay
			// revocation must commit before the caller receives the sentinel.
			outcome = auth.ErrRefreshReuse
			return nil
		}

		authorization, err := resolveRefreshAuthorization(ctx, tx, current)
		if errors.Is(err, errSelectedOrgMembershipMissing) {
			if err := revokeActiveFamily(ctx, tx, current.FamilyID, auth.RefreshRejectionOrganizationMembership); err != nil {
				return err
			}
			outcome = auth.ErrRefreshRevoked
			return nil
		}
		if err != nil {
			return err
		}

		next, err := replacement(current, authorization)
		if err != nil {
			if reason, terminal := auth.RefreshRejectionReason(err); terminal {
				if err := revokeActiveFamily(ctx, tx, current.FamilyID, reason); err != nil {
					return err
				}
				outcome = auth.ErrRefreshRevoked
				return nil
			}
			return err
		}
		if err := prepareSessionRecord(next); err != nil {
			return err
		}
		if err := validateRefreshReplacement(current, next, authorization); err != nil {
			return err
		}

		if err := revokeActiveFamily(ctx, tx, current.FamilyID, "rotated"); err != nil {
			return err
		}
		return insertSession(ctx, tx, next)
	})
	if err != nil {
		return err
	}
	return outcome
}

// ExchangeOrganization changes only the selected-tenant projection on the
// current active session row. The access-token sid is the lookup key, while the
// independently verified user id prevents a valid session id from crossing
// principals. Refresh state and lifetime columns are deliberately untouched.
func (s *SessionStore) ExchangeOrganization(
	ctx context.Context,
	userID uuid.UUID,
	sessionID uuid.UUID,
	targetOrgID uuid.UUID,
	issue func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) error,
) error {
	if userID == uuid.Nil || sessionID == uuid.Nil || targetOrgID == uuid.Nil {
		return auth.ErrSessionUnavailable
	}
	if issue == nil {
		return errors.New("pgauth: organization exchange issue callback is required")
	}

	var outcome error
	err := s.rls.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := txFromCtx(ctx)
		current, err := scanSession(tx.QueryRow(ctx, `
			SELECT
				id, user_id, family_id,
				created_at, last_active_at, idle_expires_at, expires_at, revoked_at, revoked_reason,
				device_info, ip_address,
				org_id, org_role, platform_role, mfa_satisfied,
				authentication_methods, auth_time, assurance_level, mfa_verified_at
			FROM sessions
			WHERE id = $1 AND user_id = $2
			LIMIT 1
			FOR UPDATE`, sessionID, userID), nil)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrSessionUnavailable
		}
		if err != nil {
			return err
		}
		if current.RevokedAt != nil {
			return auth.ErrSessionUnavailable
		}

		status, err := loadUserStatus(ctx, tx, current.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrSessionUnavailable
		}
		if err != nil {
			return err
		}
		if status != "active" {
			if err := revokeActiveUserSessions(ctx, tx, current.UserID, auth.RefreshRejectionUserNotActive); err != nil {
				return err
			}
			outcome = auth.ErrSessionUnavailable
			return nil
		}

		now := time.Now()
		if !now.Before(current.ExpiresAt) || !now.Before(current.IdleExpiresAt) {
			reason := auth.RefreshRejectionIdleTimeout
			if !now.Before(current.ExpiresAt) {
				reason = auth.RefreshRejectionAbsoluteLifetime
			}
			if err := revokeActiveFamily(ctx, tx, current.FamilyID, reason); err != nil {
				return err
			}
			outcome = auth.ErrSessionUnavailable
			return nil
		}

		authorization, err := resolveOrganizationAuthorization(ctx, tx, current.UserID, targetOrgID)
		if err != nil {
			return err
		}
		if err := issue(current, authorization); err != nil {
			if reason, terminal := auth.RefreshRejectionReason(err); terminal {
				if err := revokeActiveFamily(ctx, tx, current.FamilyID, reason); err != nil {
					return err
				}
				outcome = auth.ErrSessionUnavailable
				return nil
			}
			return err
		}

		tag, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET org_id = $3,
			       org_role = $4,
			       platform_role = $5
			 WHERE id = $1
			   AND user_id = $2
			   AND revoked_at IS NULL`,
			sessionID, userID, authorization.OrgID, authorization.OrgRole, authorization.PlatformRole,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrSessionUnavailable
		}

		// Persist the choice as the user's default so the next login lands on it
		// deterministically instead of re-deriving a tenant. Same locked
		// transaction as the session update: the two never diverge.
		if _, err := tx.Exec(ctx, `
			UPDATE users SET default_org_id = $2 WHERE uuid = $1`,
			userID, authorization.OrgID,
		); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return outcome
}

var errSelectedOrgMembershipMissing = errors.New("pgauth: selected organization membership is no longer active")

func loadUserStatus(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status::text
		FROM users
		WHERE uuid = $1`, userID).Scan(&status)
	return status, err
}

func resolveRefreshAuthorization(
	ctx context.Context,
	tx pgx.Tx,
	current *auth.SessionRecord,
) (auth.RefreshAuthorization, error) {
	var authorization auth.RefreshAuthorization

	if current.OrgID != uuid.Nil {
		err := tx.QueryRow(ctx, `
			SELECT org_id, role
			FROM organization_members
			WHERE user_id = $1 AND org_id = $2`, current.UserID, current.OrgID).Scan(
			&authorization.OrgID,
			&authorization.OrgRole,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.RefreshAuthorization{}, errSelectedOrgMembershipMissing
		}
		if err != nil {
			return auth.RefreshAuthorization{}, err
		}
	} else {
		err := tx.QueryRow(ctx, `
			SELECT org_id, role
			FROM organization_members
			WHERE user_id = $1
			ORDER BY joined_at DESC
			LIMIT 1`, current.UserID).Scan(
			&authorization.OrgID,
			&authorization.OrgRole,
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return auth.RefreshAuthorization{}, err
		}
	}

	return resolveGlobalAuthorization(ctx, tx, current.UserID, authorization)
}

func resolveOrganizationAuthorization(
	ctx context.Context,
	tx pgx.Tx,
	userID uuid.UUID,
	targetOrgID uuid.UUID,
) (auth.RefreshAuthorization, error) {
	authorization := auth.RefreshAuthorization{OrgID: targetOrgID}
	err := tx.QueryRow(ctx, `
		SELECT role
		FROM organization_members
		WHERE user_id = $1 AND org_id = $2`, userID, targetOrgID).Scan(&authorization.OrgRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.RefreshAuthorization{}, auth.ErrOrganizationAccessDenied
	}
	if err != nil {
		return auth.RefreshAuthorization{}, err
	}

	return resolveGlobalAuthorization(ctx, tx, userID, authorization)
}

func resolveGlobalAuthorization(
	ctx context.Context,
	tx pgx.Tx,
	userID uuid.UUID,
	authorization auth.RefreshAuthorization,
) (auth.RefreshAuthorization, error) {
	err := tx.QueryRow(ctx, `
		SELECT platform_role::text
		FROM platform_admins
		WHERE user_id = $1`, userID).Scan(&authorization.PlatformRole)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return auth.RefreshAuthorization{}, err
	}

	err = tx.QueryRow(ctx, `
		SELECT true
		FROM mfa_devices
		WHERE user_id = $1 AND verified_at IS NOT NULL
		ORDER BY created_at DESC
		LIMIT 1`, userID).Scan(&authorization.MFAEnrolled)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return auth.RefreshAuthorization{}, err
	}

	authorization.ScopedRoles, err = resolveScopedRoles(ctx, tx, userID, authorization.OrgID)
	if err != nil {
		return auth.RefreshAuthorization{}, err
	}

	return authorization, nil
}

func revokeActiveUserSessions(ctx context.Context, tx pgx.Tx, userID uuid.UUID, reason string) error {
	_, err := tx.Exec(ctx, `
		UPDATE sessions
		   SET revoked_at = NOW(), revoked_reason = $2
		 WHERE user_id = $1 AND revoked_at IS NULL`, userID, reason)
	return err
}

func revokeActiveFamily(ctx context.Context, tx pgx.Tx, familyID uuid.UUID, reason string) error {
	_, err := tx.Exec(ctx, `
		UPDATE sessions
		   SET revoked_at = NOW(), revoked_reason = $2
		 WHERE family_id = $1 AND revoked_at IS NULL`, familyID, reason)
	return err
}

func validateRefreshReplacement(
	current *auth.SessionRecord,
	next *auth.SessionRecord,
	authorization auth.RefreshAuthorization,
) error {
	if next.UserID != current.UserID {
		return errors.New("pgauth: refresh replacement changed user id")
	}
	if next.FamilyID != current.FamilyID {
		return errors.New("pgauth: refresh replacement changed family id")
	}
	if next.OrgID != authorization.OrgID ||
		next.OrgRole != authorization.OrgRole ||
		next.PlatformRole != authorization.PlatformRole {
		return errors.New("pgauth: refresh replacement did not project current authorization")
	}
	if next.ID == current.ID {
		return errors.New("pgauth: refresh replacement reused session id")
	}
	if bytes.Equal(next.RefreshHash, current.RefreshHash) {
		return errors.New("pgauth: refresh replacement reused token hash")
	}
	if next.RevokedAt != nil || next.RevokedReason != "" {
		return errors.New("pgauth: refresh replacement is already revoked")
	}
	if next.IssuedAt != current.IssuedAt || next.ExpiresAt != current.ExpiresAt {
		return errors.New("pgauth: refresh replacement changed absolute session lifetime")
	}
	if next.LastActiveAt.Before(current.LastActiveAt) {
		return errors.New("pgauth: refresh replacement moved activity backwards")
	}
	if !next.IdleExpiresAt.After(next.LastActiveAt) || next.IdleExpiresAt.After(next.ExpiresAt) {
		return errors.New("pgauth: refresh replacement has invalid idle expiry")
	}
	if !maps.Equal(next.DeviceInfo, current.DeviceInfo) || next.IPAddress != current.IPAddress {
		return errors.New("pgauth: refresh replacement changed device identity")
	}
	return nil
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// RevokeFamily marks all sessions sharing a family_id as revoked.
//
// Cross-user by potential: family_id is unique enough in practice
// that "revoke this family" only touches rows for one user, but
// the operation is keyed by family_id, not user_id. WithControlPlane so
// the UPDATE can find the rows regardless of who owns them.
// Audited via the bypass counter.
func (s *SessionStore) RevokeFamily(ctx context.Context, familyID uuid.UUID, reason string) error {
	return s.rls.WithControlPlane(ctx, func(ctx context.Context) error {
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

// Compile-time assertion.
var _ auth.SessionStore = (*SessionStore)(nil)
