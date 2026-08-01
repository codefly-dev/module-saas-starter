package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

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
		SELECT uuid, primary_email, status, profile, created_at, updated_at, email_verified
		FROM users WHERE uuid = $1 AND status != 'deleted'`, id,
	).Scan(&u.Uuid, &u.PrimaryEmail, &statusStr, &profile, &createdAt, &updatedAt, &u.EmailVerified)
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
		FROM users WHERE LOWER(primary_email) = LOWER($1) AND status != 'deleted'`, email,
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

// GetOrganizationMemberPrimaryEmail resolves the primary email of a user who
// belongs to the transaction's current organization. The database function is
// the intentionally narrow co-member directory surface: app_tenant cannot read
// another user's full users row, and the function returns NULL when userID is
// not a member of app.current_org_id.
func (s *PostgresStore) GetOrganizationMemberPrimaryEmail(ctx context.Context, userID string) (string, error) {
	w := wool.Get(ctx).In("GetOrganizationMemberPrimaryEmail")
	executor := s.getQueryExecutor(ctx)

	var email *string
	if err := executor.QueryRow(ctx,
		`SELECT public.organization_member_primary_email($1)`, userID,
	).Scan(&email); err != nil {
		return "", w.Wrapf(err, "failed to get organization member email")
	}
	if email == nil {
		return "", nil
	}
	return *email, nil
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

	// Soft-delete filter: deleted users must not appear in listings.
	query += ` AND u.status != 'deleted'`

	if statusFilter != "" {
		// Previous code used `string(rune('0'+argIdx))` which silently
		// breaks for argIdx > 9 (produces e.g. "$:" instead of "$10") —
		// fmt.Sprintf is safer and the correct builder for numeric
		// placeholders regardless of size.
		query += fmt.Sprintf(` AND u.status = $%d`, argIdx)
		args = append(args, statusFilter)
		argIdx++
	}

	query += fmt.Sprintf(` ORDER BY u.created_at DESC LIMIT $%d`, argIdx)
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

	if patch, ok := updates["profile_merge"]; ok {
		if err := s.mergeUserProfile(ctx, executor, userID, patch); err != nil {
			return nil, w.Wrap(err)
		}
	}

	return s.GetUser(ctx, userID)
}

// mergeUserProfile applies a partial profile patch atomically: keys with a
// non-empty value are set, keys with an empty value are removed, and every
// other existing key is preserved. The row is locked FOR UPDATE so concurrent
// profile writers serialize on the server instead of a caller having to
// read-modify-write the whole map (which loses concurrent changes).
//
// This is deliberately separate from the "profile" replace path, which GDPR
// anonymization (business.(*Service).processDeletion) relies on to scrub PII
// by overwriting the entire map — a merge there would preserve the PII.
func (s *PostgresStore) mergeUserProfile(ctx context.Context, executor QueryExecutor, userID string, patch any) error {
	w := wool.Get(ctx).In("mergeUserProfile")
	fields, ok := patch.(map[string]string)
	if !ok {
		return w.NewError("profile_merge expects map[string]string, got %T", patch)
	}
	var raw []byte
	if err := executor.QueryRow(ctx,
		`SELECT profile FROM users WHERE uuid = $1 FOR UPDATE`, userID).Scan(&raw); err != nil {
		return w.Wrapf(err, "failed to read profile for merge")
	}
	current := map[string]string{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &current); err != nil {
			return w.Wrapf(err, "malformed profile json")
		}
	}
	for key, value := range fields {
		if value == "" {
			delete(current, key)
		} else {
			current[key] = value
		}
	}
	merged, err := json.Marshal(current)
	if err != nil {
		return w.Wrapf(err, "failed to marshal merged profile")
	}
	if _, err := executor.Exec(ctx,
		`UPDATE users SET profile = $1, updated_at = CURRENT_TIMESTAMP WHERE uuid = $2`, merged, userID); err != nil {
		return w.Wrapf(err, "failed to update profile")
	}
	return nil
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

// DeleteUserIdentities removes every authentication identity linked to a
// user. Callers must establish that user's scope (or the control-plane role);
// the user_identities RLS policy is the database-level cross-user guard.
func (s *PostgresStore) DeleteUserIdentities(ctx context.Context, userID string) error {
	w := wool.Get(ctx).In("DeleteUserIdentities")
	if _, err := s.getQueryExecutor(ctx).Exec(ctx,
		`DELETE FROM user_identities WHERE user_uuid = $1`, userID,
	); err != nil {
		return w.Wrapf(err, "failed to delete user identities")
	}
	return nil
}
