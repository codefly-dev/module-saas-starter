package infra

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"encoding/json"

	"github.com/codefly-dev/core/wool"
)

// SearchUsers searches users by email or UUID prefix across all orgs.
func (s *PostgresStore) SearchUsers(ctx context.Context, query string, pageSize int32, pageToken string) ([]*gen.User, string, error) {
	w := wool.Get(ctx).In("SearchUsers")
	executor := s.getQueryExecutor(ctx)

	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}

	likeQuery := "%" + query + "%"

	rows, err := executor.Query(ctx, `
		SELECT uuid, primary_email, status, profile, created_at, updated_at
		FROM users
		WHERE primary_email ILIKE $1 OR uuid::text LIKE $2
		ORDER BY created_at DESC
		LIMIT $3`,
		likeQuery, likeQuery, pageSize+1,
	)
	if err != nil {
		return nil, "", w.Wrapf(err, "failed to search users")
	}
	defer rows.Close()

	var users []*gen.User
	for rows.Next() {
		var u gen.User
		var profile []byte
		var createdAt, updatedAt time.Time
		var statusStr string
		if err := rows.Scan(&u.Uuid, &u.PrimaryEmail, &statusStr, &profile, &createdAt, &updatedAt); err != nil {
			return nil, "", w.Wrapf(err, "failed to scan user")
		}
		u.Status = parseUserStatus(statusStr)
		if len(profile) > 0 {
			u.Profile = make(map[string]string)
			_ = json.Unmarshal(profile, &u.Profile)
		}
		users = append(users, &u)
	}

	nextToken := ""
	if int32(len(users)) > pageSize {
		users = users[:pageSize]
		nextToken = users[len(users)-1].Uuid
	}

	return users, nextToken, nil
}

// UpdateUserStatus changes a user's status (active, suspended, deleted).
func (s *PostgresStore) UpdateUserStatus(ctx context.Context, userID string, status string) error {
	w := wool.Get(ctx).In("UpdateUserStatus")
	executor := s.getQueryExecutor(ctx)

	tag, err := executor.Exec(ctx, `
		UPDATE users SET status = $1, updated_at = CURRENT_TIMESTAMP
		WHERE uuid = $2`,
		status, userID,
	)
	if err != nil {
		return w.Wrapf(err, "failed to update user status")
	}
	if tag.RowsAffected() == 0 {
		return business.NewStoreError(errors.New("user not found"), business.ErrTypeNotFound)
	}
	return nil
}

// ListActiveSessions returns policy-valid device families for a user.
func (s *PostgresStore) ListActiveSessions(ctx context.Context, userID string, pageSize int32) ([]*business.Session, error) {
	w := wool.Get(ctx).In("ListActiveSessions")
	executor := s.getQueryExecutor(ctx)

	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}

	rows, err := executor.Query(ctx, `
		SELECT id, user_id, refresh_token_hash, family_id, COALESCE(ip_address, ''), device_info,
		       created_at, last_active_at, idle_expires_at, expires_at
		FROM sessions
		WHERE user_id = $1
		  AND revoked_at IS NULL
		  AND expires_at > CURRENT_TIMESTAMP
		  AND idle_expires_at > CURRENT_TIMESTAMP
		ORDER BY last_active_at DESC
		LIMIT $2`,
		userID, pageSize,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list sessions")
	}
	defer rows.Close()

	var sessions []*business.Session
	for rows.Next() {
		var sess business.Session
		var deviceInfo []byte
		if err := rows.Scan(
			&sess.ID, &sess.UserID, &sess.RefreshTokenHash, &sess.FamilyID,
			&sess.IPAddress, &deviceInfo,
			&sess.CreatedAt, &sess.LastActiveAt, &sess.IdleExpiresAt, &sess.ExpiresAt,
		); err != nil {
			return nil, w.Wrapf(err, "failed to scan session")
		}
		if err := json.Unmarshal(deviceInfo, &sess.DeviceInfo); err != nil {
			return nil, w.Wrapf(err, "failed to decode session device info")
		}
		sessions = append(sessions, &sess)
	}
	return sessions, nil
}

// parseUserStatus is defined in postgres.go
