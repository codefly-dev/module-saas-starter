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
//
// Redemption consumes a one-use code in a transaction the caller owns, so this
// reuses that transaction when the caller marks it — exactly as Insert does for
// CompleteMFAChallenge. Opening a second transaction here would commit the
// session independently of the code's consumption, and a caller that then
// failed to commit would leave a live session behind a code that is still
// redeemable.
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
	authorize := func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockAuthorizingSession(ctx, tx, authorizingSessionID, userID)
		if err != nil {
			return err
		}
		// An impersonation window may not hand a client a credential. The
		// window is access-only and capped precisely so no rotatable session
		// outlives it, and a client session minted here would do exactly that.
		if current.ActingAsUserID != uuid.Nil {
			return auth.ErrSessionUnavailable
		}
		// A role view is read here so a client's own session, which holds every role
		// its person does, is never minted from one: MintForClient refuses it.
		if err := tx.QueryRow(ctx, `SELECT role_view FROM sessions WHERE id = $1`, current.ID).Scan(&current.AccessOnly); err != nil {
			return err
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
			// A terminal rejection means the host session may no longer project
			// what it is being asked to project — a stale second factor, most
			// often. It refuses this one authorization and is deliberately NOT
			// escalated into family revocation the way a refresh rotation is:
			// the browser session is untouched and still faces that gate on its
			// own next rotation, so trying to authorize an add-in never signs
			// the person out of the tab they are sitting in.
			if _, terminal := auth.RefreshRejectionReason(err); terminal {
				return auth.ErrSessionUnavailable
			}
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
		if next.ClientID == "" && !next.AccessOnly {
			return errors.New("pgauth: client session names no client")
		}
		// Admission bounds the client's families and re-checks the account under
		// the user row's lock. Without it a client that re-authorizes on every
		// launch grows without limit, and those families then rank inside the
		// device ceiling that the person's next browser login evicts against.
		if err := s.admitSession(ctx, tx, next); err != nil {
			if errors.Is(err, auth.ErrAccountInactive) {
				return auth.ErrSessionUnavailable
			}
			return err
		}
		return insertSession(ctx, tx, next)
	}

	// Reuse the redemption's transaction when the caller owns one, so the code's
	// consumption and this session commit together or not at all.
	var err error
	if tx := txFromCtx(ctx); tx != nil && auth.HasAtomicSessionTransaction(ctx) {
		err = authorize(ctx, tx)
	} else {
		err = s.rls.WithControlPlane(ctx, func(ctx context.Context) error {
			return authorize(ctx, txFromCtx(ctx))
		})
	}
	if err != nil {
		return err
	}
	return outcome
}

// lockAuthorizingSession resolves and locks the host session an access token
// names by its sid, for the authorizations keyed by it (a client's session, a
// role view, a team cast, an organization exchange).
//
// A refresh rotates the family: the presented row is revoked "rotated" and a
// new row becomes the family's live one, while the access token minted from
// the old row stays honoured at the perimeter until it expires (rotation
// revokes no access token; VerifyAccess checks the family). A browser whose
// cookie another tab or document rotated a moment ago therefore presents a
// valid token whose sid names a rotated row, and it is the family's live row
// that answers for it: same person, same sign-in, authorization re-resolved
// from it exactly as a refresh would. Any other revocation (sign-out, reuse
// detection, an administrator's kill) leaves the family no live row, and the
// session is unavailable.
func lockAuthorizingSession(ctx context.Context, tx pgx.Tx, sessionID, userID uuid.UUID) (*auth.SessionRecord, error) {
	const columns = `
			id, user_id, family_id,
			created_at, last_active_at, idle_expires_at, expires_at, revoked_at, revoked_reason,
			device_info, ip_address,
			org_id, org_role, platform_role, mfa_satisfied,
			authentication_methods, auth_time, assurance_level, mfa_verified_at,
			email, display_name, acting_as_user_id, client_id, resource`
	current, err := scanSession(tx.QueryRow(ctx, `
		SELECT `+columns+`
		FROM sessions
		WHERE id = $1 AND user_id = $2
		LIMIT 1
		FOR UPDATE`, sessionID, userID), nil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, auth.ErrSessionUnavailable
	}
	if err != nil {
		return nil, err
	}
	if current.RevokedAt == nil {
		return current, nil
	}
	if current.RevokedReason != "rotated" {
		return nil, auth.ErrSessionUnavailable
	}
	live, err := scanSession(tx.QueryRow(ctx, `
		SELECT `+columns+`
		FROM sessions
		WHERE family_id = $1 AND user_id = $2 AND revoked_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1
		FOR UPDATE`, current.FamilyID, userID), nil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, auth.ErrSessionUnavailable
	}
	if err != nil {
		return nil, err
	}
	return live, nil
}
