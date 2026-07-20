package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func (s *PostgresStore) CreateAPIKey(ctx context.Context, key *gen.APIKey, keyHash string) error {
	q := s.getQueryExecutor(ctx)

	scopes, err := json.Marshal(key.Scopes)
	if err != nil {
		return err
	}

	var expiresAt *time.Time
	if key.ExpiresAt != nil {
		t := key.ExpiresAt.AsTime()
		expiresAt = &t
	}

	_, err = q.Exec(ctx, `
		INSERT INTO api_keys (id, organization_id, user_id, name, prefix, key_hash, scopes, environment, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		key.Id, key.OrganizationId, key.UserId, key.Name, key.Prefix,
		keyHash, scopes, apiKeyEnvToString(key.Environment), expiresAt, key.UserId)
	return err
}

// CountActiveAPIKeys is the authoritative cardinality query for the api_keys
// entitlement. Revoked and already-expired keys do not consume a slot.
func (s *PostgresStore) CountActiveAPIKeys(ctx context.Context, orgID string) (int64, error) {
	var count int64
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT COUNT(*)
		FROM api_keys
		WHERE organization_id = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)`, orgID,
	).Scan(&count)
	return count, err
}

func (s *PostgresStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (*gen.APIKey, error) {
	q := s.getQueryExecutor(ctx)

	row := q.QueryRow(ctx, `
		SELECT id, organization_id, user_id, name, prefix, scopes, environment,
		       created_at, expires_at, last_used_at, revoked_at
		FROM api_keys WHERE key_hash = $1`, keyHash)

	var key gen.APIKey
	var scopesJSON []byte
	var env string
	var createdAt time.Time
	var expiresAt, lastUsedAt, revokedAt *time.Time

	err := row.Scan(&key.Id, &key.OrganizationId, &key.UserId, &key.Name, &key.Prefix,
		&scopesJSON, &env, &createdAt, &expiresAt, &lastUsedAt, &revokedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	key.Environment = apiKeyEnvFromString(env)
	key.CreatedAt = timestamppb.New(createdAt)
	if expiresAt != nil {
		key.ExpiresAt = timestamppb.New(*expiresAt)
	}
	if lastUsedAt != nil {
		key.LastUsedAt = timestamppb.New(*lastUsedAt)
	}
	if revokedAt != nil {
		key.RevokedAt = timestamppb.New(*revokedAt)
	}

	var scopes []*gen.Permission
	if err := json.Unmarshal(scopesJSON, &scopes); err == nil {
		key.Scopes = scopes
	}

	return &key, nil
}

func (s *PostgresStore) ListAPIKeys(ctx context.Context, orgID string, pageSize int32, pageToken string) ([]*gen.APIKey, string, error) {
	q := s.getQueryExecutor(ctx)

	rows, err := q.Query(ctx, `
		SELECT id, organization_id, user_id, name, prefix, scopes, environment,
		       created_at, expires_at, last_used_at, revoked_at
		FROM api_keys
		WHERE organization_id = $1 AND revoked_at IS NULL
		ORDER BY created_at DESC
		LIMIT $2`, orgID, pageSize)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var keys []*gen.APIKey
	for rows.Next() {
		var key gen.APIKey
		var scopesJSON []byte
		var env string
		var createdAt time.Time
		var expiresAt, lastUsedAt, revokedAt *time.Time

		err := rows.Scan(&key.Id, &key.OrganizationId, &key.UserId, &key.Name, &key.Prefix,
			&scopesJSON, &env, &createdAt, &expiresAt, &lastUsedAt, &revokedAt)
		if err != nil {
			return nil, "", err
		}

		key.Environment = apiKeyEnvFromString(env)
		key.CreatedAt = timestamppb.New(createdAt)
		if expiresAt != nil {
			key.ExpiresAt = timestamppb.New(*expiresAt)
		}
		if lastUsedAt != nil {
			key.LastUsedAt = timestamppb.New(*lastUsedAt)
		}
		if revokedAt != nil {
			key.RevokedAt = timestamppb.New(*revokedAt)
		}

		var scopes []*gen.Permission
		if err := json.Unmarshal(scopesJSON, &scopes); err == nil {
			key.Scopes = scopes
		}
		keys = append(keys, &key)
	}

	return keys, "", nil
}

// RevokeAPIKey is ORG-SCOPED: the WHERE pins the key to the org the handler
// authorized, so a cross-org revoke-by-id is structurally impossible (one
// atomic statement — no lookup/TOCTOU). Zero rows = not found in that org.
func (s *PostgresStore) RevokeAPIKey(ctx context.Context, keyID string, orgID string) error {
	q := s.getQueryExecutor(ctx)
	tag, err := q.Exec(ctx,
		`UPDATE api_keys SET revoked_at = NOW() WHERE id = $1 AND organization_id = $2`,
		keyID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return business.NewStoreError(
			fmt.Errorf("api key %s not found in org %s", keyID, orgID),
			business.ErrTypeNotFound,
		)
	}
	return nil
}

func (s *PostgresStore) TouchAPIKeyUsage(ctx context.Context, keyID string, ip string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `UPDATE api_keys SET last_used_at = NOW(), last_used_ip = $2 WHERE id = $1`, keyID, ip)
	return err
}

func apiKeyEnvToString(env gen.APIKeyEnvironment) string {
	switch env {
	case gen.APIKeyEnvironment_API_KEY_ENVIRONMENT_TEST:
		return "test"
	default:
		return "live"
	}
}

func apiKeyEnvFromString(env string) gen.APIKeyEnvironment {
	switch env {
	case "test":
		return gen.APIKeyEnvironment_API_KEY_ENVIRONMENT_TEST
	default:
		return gen.APIKeyEnvironment_API_KEY_ENVIRONMENT_LIVE
	}
}
