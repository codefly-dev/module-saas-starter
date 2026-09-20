package pgauth

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/auth"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AuthorizeClientSession mints a client's own session from the host session
// that authorized it.
//
// The host session is the authority for two separate things and both are
// resolved here rather than snapshotted when the authorization code was issued:
// that the sign-in is still live (not revoked, not timed out, still an active
// user), and what the person is currently authorized to do. A code redeemed
// after a logout therefore yields nothing, and a role changed between the
// redirect and the redemption is reflected in the client's first token.
//
// The new session starts its own family with no relationship to the host
// session's: revoking the client revokes only the client, and the person
// signing out of the browser does not sign out of their add-in.
func (s *SessionStore) AuthorizeClientSession(
	ctx context.Context,
	userID uuid.UUID,
	authorizingSessionID uuid.UUID,
	issue func(current *auth.SessionRecord, authorization auth.RefreshAuthorization) (*auth.SessionRecord, error),
) error {
	if userID == uuid.Nil || authorizingSessionID == uuid.Nil {
		return auth.ErrSessionUnavailable
	}
	if issue == nil {
		return errors.New("pgauth: client authorization issue callback is required")
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
				authentication_methods, auth_time, assurance_level, mfa_verified_at,
				email, display_name, acting_as_user_id, client_id
			FROM sessions
			WHERE id = $1 AND user_id = $2
			LIMIT 1
			FOR UPDATE`, authorizingSessionID, userID), nil)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrSessionUnavailable
		}
		if err != nil {
			return err
		}
		if current.RevokedAt != nil {
			return auth.ErrSessionUnavailable
		}
		// An impersonation window may not hand a client a credential. The
		// window is access-only and capped precisely so no rotatable session
		// outlives it, and a client session minted here would do exactly that.
		if current.ActingAsUserID != uuid.Nil {
			return auth.ErrSessionUnavailable
		}
		// One client cannot authorize another, and a client session cannot
		// bootstrap a second one: only the host's own web session authorizes.
		if current.ClientID != "" {
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
			return auth.ErrSessionUnavailable
		}

		authorization, err := resolveRefreshAuthorization(ctx, tx, current)
		if errors.Is(err, errSelectedOrgMembershipMissing) {
			return auth.ErrSessionUnavailable
		}
		if err != nil {
			return err
		}

		next, err := issue(current, authorization)
		if err != nil {
			return err
		}
		if err := prepareSessionRecord(next); err != nil {
			return err
		}
		if next.UserID != current.UserID {
			return errors.New("pgauth: client session changed user id")
		}
		if next.FamilyID == current.FamilyID {
			return errors.New("pgauth: client session reused the authorizing device family")
		}
		if next.ClientID == "" {
			return errors.New("pgauth: client session names no client")
		}
		return insertSession(ctx, tx, next)
	})
	if err != nil {
		return err
	}
	return outcome
}
