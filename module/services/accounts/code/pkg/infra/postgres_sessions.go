package infra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

func (s *PostgresStore) CreateSession(ctx context.Context, session *business.Session) error {
	q := s.getQueryExecutor(ctx)
	if session.LastActiveAt.IsZero() {
		session.LastActiveAt = time.Now()
	}
	if session.IdleExpiresAt.IsZero() {
		session.IdleExpiresAt = session.LastActiveAt.Add(24 * time.Hour)
		if session.IdleExpiresAt.After(session.ExpiresAt) {
			session.IdleExpiresAt = session.ExpiresAt
		}
	}

	deviceInfo, err := json.Marshal(session.DeviceInfo)
	if err != nil {
		deviceInfo = []byte("{}")
	}

	_, err = q.Exec(ctx, `
		INSERT INTO sessions (
			id, user_id, refresh_token_hash, family_id, device_info, ip_address,
			last_active_at, idle_expires_at, expires_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		session.ID, session.UserID, session.RefreshTokenHash, session.FamilyID,
		deviceInfo, session.IPAddress, session.LastActiveAt, session.IdleExpiresAt, session.ExpiresAt)
	return err
}

func (s *PostgresStore) GetSessionByRefreshTokenHash(ctx context.Context, hash string) (*business.Session, error) {
	q := s.getQueryExecutor(ctx)

	row := q.QueryRow(ctx, `
		SELECT id, user_id, refresh_token_hash, family_id, device_info, COALESCE(ip_address, ''),
		       created_at, last_active_at, idle_expires_at, expires_at, revoked_at, revoked_reason
		FROM sessions WHERE refresh_token_hash = $1`, hash)

	var session business.Session
	var deviceInfoJSON []byte
	var revokedAt *time.Time
	var revokedReason *string

	err := row.Scan(&session.ID, &session.UserID, &session.RefreshTokenHash,
		&session.FamilyID, &deviceInfoJSON, &session.IPAddress,
		&session.CreatedAt, &session.LastActiveAt, &session.IdleExpiresAt, &session.ExpiresAt,
		&revokedAt, &revokedReason)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if revokedAt != nil {
		session.RevokedAt = revokedAt
	}
	if revokedReason != nil {
		session.RevokedReason = *revokedReason
	}

	var deviceInfo map[string]string
	if err := json.Unmarshal(deviceInfoJSON, &deviceInfo); err == nil {
		session.DeviceInfo = deviceInfo
	}

	return &session, nil
}

// CloseImpersonationSession revokes an impersonation window and reports when it
// opened, in one statement so the revocation and the elapsed time a caller
// records with it cannot disagree.
//
// The predicate is what makes it safe to call more than once and safe to call
// with any session id: `acting_as_user_id IS NOT NULL` refuses to revoke an
// ordinary login even if one were named, and `revoked_at IS NULL` means a
// second call matches nothing instead of moving the close time or emitting a
// duplicate record. No row matching is a normal outcome, not an error.
func (s *PostgresStore) CloseImpersonationSession(ctx context.Context, sessionID, reason string) (time.Time, bool, error) {
	q := s.getQueryExecutor(ctx)

	var createdAt time.Time
	err := q.QueryRow(ctx, `
		UPDATE sessions
		   SET revoked_at = NOW(), revoked_reason = $2
		 WHERE id = $1
		   AND acting_as_user_id IS NOT NULL
		   AND revoked_at IS NULL
		RETURNING created_at`, sessionID, reason).Scan(&createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	return createdAt, true, nil
}

func (s *PostgresStore) RevokeSession(ctx context.Context, deviceSessionID string, reason string) ([]string, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx,
		`UPDATE sessions SET revoked_at = NOW(), revoked_reason = $2 WHERE family_id = $1 AND revoked_at IS NULL RETURNING id`,
		deviceSessionID, reason)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PostgresStore) RevokeSessionFamily(ctx context.Context, familyID string, reason string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx,
		`UPDATE sessions SET revoked_at = NOW(), revoked_reason = $2 WHERE family_id = $1 AND revoked_at IS NULL`,
		familyID, reason)
	return err
}

func (s *PostgresStore) RevokeAllUserSessions(ctx context.Context, userID string, reason string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx,
		`UPDATE sessions SET revoked_at = NOW(), revoked_reason = $2 WHERE user_id = $1 AND revoked_at IS NULL`,
		userID, reason)
	return err
}

func (s *PostgresStore) UpdateSessionActivity(ctx context.Context, sessionID string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx,
		`UPDATE sessions SET last_active_at = NOW() WHERE id = $1`,
		sessionID)
	return err
}
