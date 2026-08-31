package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"accounts/pkg/business"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var _ business.MFALoginStore = (*PostgresStore)(nil)

func (s *PostgresStore) CreateMFALoginTransaction(ctx context.Context, tx *business.MFALoginTransaction) error {
	q := s.getQueryExecutor(ctx)
	var orgID any
	if tx.OrgID != "" {
		orgID = tx.OrgID
	}
	var authenticatedAt any
	if !tx.AuthenticatedAt.IsZero() {
		authenticatedAt = tx.AuthenticatedAt
	}
	authenticationMethods := tx.AuthenticationMethods
	if authenticationMethods == nil {
		authenticationMethods = []string{}
	}
	deviceInfo, err := json.Marshal(tx.DeviceInfo)
	if err != nil {
		return fmt.Errorf("encode MFA login device info: %w", err)
	}
	if tx.DeviceInfo == nil {
		deviceInfo = []byte("{}")
	}
	_, err = q.Exec(ctx, `
		INSERT INTO mfa_login_transactions (
			id, token_hash, user_id, org_id, org_role, platform_role,
			session_id, device_info, ip_address,
			authentication_methods, auth_time, expires_at, created_at,
			email, display_name
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		tx.ID, tx.TokenHash, tx.UserID, orgID, tx.OrgRole, tx.PlatformRole,
		tx.SessionID, deviceInfo, nilIfEmpty(tx.IPAddress),
		authenticationMethods, authenticatedAt, tx.ExpiresAt, tx.CreatedAt,
		nilIfEmpty(tx.Email), nilIfEmpty(tx.DisplayName),
	)
	return err
}

// GetActiveMFALoginTransaction resolves the opaque handoff for ceremony begin.
// It uses the same exact SHA-256 predicate as consume and returns no detail for
// expired, consumed, or locked rows.
func (s *PostgresStore) GetActiveMFALoginTransaction(ctx context.Context, tokenHash string, now time.Time) (*business.MFALoginTransaction, error) {
	var found *business.MFALoginTransaction
	err := s.WithControlPlane(ctx, func(txCtx context.Context) error {
		q := s.getQueryExecutor(txCtx)
		var tx business.MFALoginTransaction
		var orgID *uuid.UUID
		var authenticatedAt *time.Time
		var deviceInfo []byte
		var ipAddress *string
		var email *string
		var displayName *string
		err := q.QueryRow(txCtx, `
			SELECT id, token_hash, user_id, org_id, org_role, platform_role,
			       session_id, device_info, ip_address, authentication_methods, auth_time,
			       expires_at, consumed_at, failed_attempts, max_attempts,
			       locked_until, created_at, email, display_name
			FROM mfa_login_transactions
			WHERE token_hash = $1`, tokenHash).Scan(
			&tx.ID, &tx.TokenHash, &tx.UserID, &orgID, &tx.OrgRole, &tx.PlatformRole,
			&tx.SessionID, &deviceInfo, &ipAddress, &tx.AuthenticationMethods, &authenticatedAt,
			&tx.ExpiresAt, &tx.ConsumedAt, &tx.FailedAttempts, &tx.MaxAttempts,
			&tx.LockedUntil, &tx.CreatedAt, &email, &displayName,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return business.ErrMFAChallengeRejected
			}
			return err
		}
		if tx.ConsumedAt != nil || !now.Before(tx.ExpiresAt) ||
			tx.FailedAttempts >= tx.MaxAttempts ||
			(tx.LockedUntil != nil && now.Before(*tx.LockedUntil)) {
			return business.ErrMFAChallengeRejected
		}
		if orgID != nil {
			tx.OrgID = orgID.String()
		}
		if authenticatedAt != nil {
			tx.AuthenticatedAt = *authenticatedAt
		}
		if err := json.Unmarshal(deviceInfo, &tx.DeviceInfo); err != nil {
			return fmt.Errorf("decode MFA login device info: %w", err)
		}
		if ipAddress != nil {
			tx.IPAddress = *ipAddress
		}
		if email != nil {
			tx.Email = *email
		}
		if displayName != nil {
			tx.DisplayName = *displayName
		}
		found = &tx
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// ConsumeMFALoginTransaction uses a bypass transaction because the caller has
// only the random token, not a trusted user id. The exact hash predicate and
// row lock constrain the bypass; issue receives the same tx context so the
// session insert and consumed_at update are one commit.
func (s *PostgresStore) ConsumeMFALoginTransaction(
	ctx context.Context,
	tokenHash string,
	now time.Time,
	issue func(context.Context, *business.MFALoginTransaction) error,
) error {
	rejected := false
	err := s.WithControlPlane(ctx, func(txCtx context.Context) error {
		q := s.getQueryExecutor(txCtx)
		var tx business.MFALoginTransaction
		var orgID *uuid.UUID
		var authenticatedAt *time.Time
		var deviceInfo []byte
		var ipAddress *string
		var email *string
		var displayName *string
		err := q.QueryRow(txCtx, `
			SELECT id, token_hash, user_id, org_id, org_role, platform_role,
			       session_id, device_info, ip_address, authentication_methods, auth_time,
			       expires_at, consumed_at, failed_attempts, max_attempts,
			       locked_until, created_at, email, display_name
			FROM mfa_login_transactions
			WHERE token_hash = $1
			FOR UPDATE`, tokenHash).Scan(
			&tx.ID, &tx.TokenHash, &tx.UserID, &orgID, &tx.OrgRole, &tx.PlatformRole,
			&tx.SessionID, &deviceInfo, &ipAddress, &tx.AuthenticationMethods, &authenticatedAt,
			&tx.ExpiresAt, &tx.ConsumedAt, &tx.FailedAttempts, &tx.MaxAttempts,
			&tx.LockedUntil, &tx.CreatedAt, &email, &displayName,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return business.ErrMFAChallengeRejected
			}
			return err
		}
		if orgID != nil {
			tx.OrgID = orgID.String()
		}
		if authenticatedAt != nil {
			tx.AuthenticatedAt = *authenticatedAt
		}
		if err := json.Unmarshal(deviceInfo, &tx.DeviceInfo); err != nil {
			return fmt.Errorf("decode MFA login device info: %w", err)
		}
		if ipAddress != nil {
			tx.IPAddress = *ipAddress
		}
		if email != nil {
			tx.Email = *email
		}
		if displayName != nil {
			tx.DisplayName = *displayName
		}
		if tx.ConsumedAt != nil || !now.Before(tx.ExpiresAt) ||
			tx.FailedAttempts >= tx.MaxAttempts ||
			(tx.LockedUntil != nil && now.Before(*tx.LockedUntil)) {
			return business.ErrMFAChallengeRejected
		}
		if err := issue(txCtx, &tx); err != nil {
			if errors.Is(err, business.ErrMFAChallengeRejected) {
				// Commit the failed-attempt increment even though the public
				// result is a rejection. Returning the sentinel from this
				// callback would roll the transaction back and make brute-force
				// counters ineffective across requests/replicas.
				rejected = true
				return q.QueryRow(txCtx, `
					UPDATE mfa_login_transactions
					SET failed_attempts = failed_attempts + 1,
					    locked_until = CASE
					        WHEN failed_attempts + 1 >= max_attempts THEN expires_at
					        ELSE locked_until
					    END
					WHERE id = $1 AND consumed_at IS NULL
					RETURNING failed_attempts, locked_until`, tx.ID).Scan(
					&tx.FailedAttempts, &tx.LockedUntil,
				)
			}
			return err
		}
		tag, err := q.Exec(txCtx, `
			UPDATE mfa_login_transactions
			SET consumed_at = $2
			WHERE id = $1 AND consumed_at IS NULL`, tx.ID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return business.ErrMFAChallengeRejected
		}
		return nil
	})
	if err != nil {
		return err
	}
	if rejected {
		return business.ErrMFAChallengeRejected
	}
	return nil
}
