package infra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"backend/pkg/business"
	"backend/pkg/gen"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetUser returns a user by UUID.
func (s *PostgresStore) GetUser(ctx context.Context, id string) (*gen.User, error) {
	w := wool.Get(ctx).In("GetUser")
	executor := s.getQueryExecutor(ctx)

	var u gen.User
	var profile []byte
	var statusStr string
	var createdAt, updatedAt time.Time

	err := executor.QueryRow(ctx, `
		SELECT uuid, primary_email, status, profile, created_at, updated_at
		FROM users WHERE uuid = $1`, id,
	).Scan(&u.Uuid, &u.PrimaryEmail, &statusStr, &profile, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(errors.New("user not found"), business.ErrTypeNotFound)
		}
		return nil, w.Wrapf(err, "failed to get user")
	}

	u.Status = parseUserStatus(statusStr)
	u.CreatedAt = timestamppb.New(createdAt)
	u.UpdatedAt = timestamppb.New(updatedAt)
	if len(profile) > 0 {
		u.Profile = make(map[string]string)
		_ = json.Unmarshal(profile, &u.Profile)
	}
	return &u, nil
}

// GetUserByEmail returns a user by email (case-insensitive).
func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (*gen.User, error) {
	w := wool.Get(ctx).In("GetUserByEmail")
	executor := s.getQueryExecutor(ctx)

	var u gen.User
	var profile []byte
	var statusStr string
	var createdAt, updatedAt time.Time

	err := executor.QueryRow(ctx, `
		SELECT uuid, primary_email, status, profile, created_at, updated_at
		FROM users WHERE LOWER(primary_email) = LOWER($1)`, email,
	).Scan(&u.Uuid, &u.PrimaryEmail, &statusStr, &profile, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(errors.New("user not found"), business.ErrTypeNotFound)
		}
		return nil, w.Wrapf(err, "failed to get user by email")
	}

	u.Status = parseUserStatus(statusStr)
	u.CreatedAt = timestamppb.New(createdAt)
	u.UpdatedAt = timestamppb.New(updatedAt)
	if len(profile) > 0 {
		u.Profile = make(map[string]string)
		_ = json.Unmarshal(profile, &u.Profile)
	}
	return &u, nil
}

// ListUsers returns paginated users, optionally filtered by org membership and status.
func (s *PostgresStore) ListUsers(ctx context.Context, orgID string, statusFilter string, pageSize int32, pageToken string) ([]*gen.User, string, error) {
	w := wool.Get(ctx).In("ListUsers")
	executor := s.getQueryExecutor(ctx)

	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}

	query := `SELECT u.uuid, u.primary_email, u.status, u.profile, u.created_at, u.updated_at FROM users u`
	args := []any{}
	argIdx := 1

	if orgID != "" {
		query += ` JOIN organization_members om ON u.uuid = om.user_id WHERE om.org_id = $1`
		args = append(args, orgID)
		argIdx++
	} else {
		query += ` WHERE 1=1`
	}

	if statusFilter != "" {
		query += ` AND u.status = $` + string(rune('0'+argIdx))
		args = append(args, statusFilter)
		argIdx++
	}

	query += ` ORDER BY u.created_at DESC LIMIT $` + string(rune('0'+argIdx))
	args = append(args, pageSize+1)

	rows, err := executor.Query(ctx, query, args...)
	if err != nil {
		return nil, "", w.Wrapf(err, "failed to list users")
	}
	defer rows.Close()

	var users []*gen.User
	for rows.Next() {
		var u gen.User
		var profile []byte
		var statusStr string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&u.Uuid, &u.PrimaryEmail, &statusStr, &profile, &createdAt, &updatedAt); err != nil {
			return nil, "", w.Wrapf(err, "failed to scan user")
		}
		u.Status = parseUserStatus(statusStr)
		u.CreatedAt = timestamppb.New(createdAt)
		u.UpdatedAt = timestamppb.New(updatedAt)
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

// UpdateUser updates specific fields on a user.
func (s *PostgresStore) UpdateUser(ctx context.Context, userID string, updates map[string]any) (*gen.User, error) {
	w := wool.Get(ctx).In("UpdateUser")
	executor := s.getQueryExecutor(ctx)

	if email, ok := updates["primary_email"]; ok {
		_, err := executor.Exec(ctx, `UPDATE users SET primary_email = $1, updated_at = CURRENT_TIMESTAMP WHERE uuid = $2`, email, userID)
		if err != nil {
			return nil, w.Wrapf(err, "failed to update email")
		}
	}

	if profile, ok := updates["profile"]; ok {
		profileJSON, _ := json.Marshal(profile)
		_, err := executor.Exec(ctx, `UPDATE users SET profile = $1, updated_at = CURRENT_TIMESTAMP WHERE uuid = $2`, profileJSON, userID)
		if err != nil {
			return nil, w.Wrapf(err, "failed to update profile")
		}
	}

	return s.GetUser(ctx, userID)
}

// DeleteUser soft-deletes a user by setting status to 'deleted'.
func (s *PostgresStore) DeleteUser(ctx context.Context, userID string) error {
	w := wool.Get(ctx).In("DeleteUser")
	executor := s.getQueryExecutor(ctx)

	tag, err := executor.Exec(ctx, `
		UPDATE users SET status = 'deleted', updated_at = CURRENT_TIMESTAMP WHERE uuid = $1`, userID)
	if err != nil {
		return w.Wrapf(err, "failed to delete user")
	}
	if tag.RowsAffected() == 0 {
		return business.NewStoreError(errors.New("user not found"), business.ErrTypeNotFound)
	}
	return nil
}

// ── Identity methods ────────────────────────────────────────────

// AddIdentity adds a new identity to an existing user.
func (s *PostgresStore) AddIdentity(ctx context.Context, identity *gen.UserIdentity) error {
	w := wool.Get(ctx).In("AddIdentity")
	executor := s.getQueryExecutor(ctx)

	_, err := executor.Exec(ctx, `
		INSERT INTO user_identities (uuid, user_uuid, provider, provider_id, provider_email, email_verified)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		identity.Uuid, identity.UserUuid, identity.Provider,
		identity.ProviderId, identity.ProviderEmail, identity.EmailVerified,
	)
	if err != nil {
		return w.Wrapf(err, "failed to add identity")
	}
	return nil
}

// FindUserByIdentity finds a user by provider identity.
func (s *PostgresStore) FindUserByIdentity(ctx context.Context, provider, providerID string) (*gen.User, error) {
	w := wool.Get(ctx).In("FindUserByIdentity")
	executor := s.getQueryExecutor(ctx)

	var userID string
	err := executor.QueryRow(ctx, `
		SELECT user_uuid FROM user_identities
		WHERE provider = $1 AND provider_id = $2`,
		provider, providerID,
	).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(errors.New("identity not found"), business.ErrTypeNotFound)
		}
		return nil, w.Wrapf(err, "failed to find user by identity")
	}

	return s.GetUser(ctx, userID)
}

// ListUserIdentities returns all identities for a user.
func (s *PostgresStore) ListUserIdentities(ctx context.Context, userID string) ([]*gen.UserIdentity, error) {
	w := wool.Get(ctx).In("ListUserIdentities")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT uuid, user_uuid, provider, provider_id, provider_email, email_verified
		FROM user_identities WHERE user_uuid = $1
		ORDER BY created_at`, userID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list identities")
	}
	defer rows.Close()

	var identities []*gen.UserIdentity
	for rows.Next() {
		var id gen.UserIdentity
		if err := rows.Scan(&id.Uuid, &id.UserUuid, &id.Provider, &id.ProviderId, &id.ProviderEmail, &id.EmailVerified); err != nil {
			return nil, w.Wrapf(err, "failed to scan identity")
		}
		identities = append(identities, &id)
	}
	return identities, nil
}
