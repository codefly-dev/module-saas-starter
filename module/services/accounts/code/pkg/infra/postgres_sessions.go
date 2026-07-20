package infra

import (
	"context"
	"encoding/json"
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

func (s *PostgresStore) RevokeSession(ctx context.Context, deviceSessionID string, reason string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx,
		`UPDATE sessions SET revoked_at = NOW(), revoked_reason = $2 WHERE family_id = $1 AND revoked_at IS NULL`,
		deviceSessionID, reason)
	return err
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
