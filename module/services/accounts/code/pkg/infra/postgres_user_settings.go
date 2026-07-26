package infra

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/usersettings"
)

// GetUserSettings returns the typed protobuf document. Raw ProtoJSON never
// escapes this adapter.
func (s *PostgresStore) GetUserSettings(
	ctx context.Context,
	userID string,
) (*gen.UserSettings, error) {
	q := s.getQueryExecutor(ctx)
	var encoded []byte
	err := q.QueryRow(
		ctx,
		`SELECT settings FROM users WHERE uuid = $1`,
		userID,
	).Scan(&encoded)
	if err != nil {
		return nil, err
	}
	return usersettings.JSON.Unmarshal(encoded)
}

// UpdateUserSettings atomically deep-merges a canonical ProtoJSON patch.
// PostgreSQL preserves nested siblings and fields unknown to this binary,
// while explicit optional zero values still overwrite.
func (s *PostgresStore) UpdateUserSettings(
	ctx context.Context,
	userID string,
	patch *gen.UserSettings,
	resetPaths []string,
) error {
	if patch == nil {
		patch = &gen.UserSettings{}
	}
	if resetPaths == nil {
		resetPaths = []string{}
	}
	q := s.getQueryExecutor(ctx)
	encoded, err := usersettings.JSON.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		UPDATE users
		   SET settings = public.settings_jsonb_deep_merge(
		           public.settings_jsonb_delete_paths(settings, $3::text[]),
		           $2::jsonb
		       ),
		       updated_at = NOW()
		 WHERE uuid = $1`,
		userID,
		encoded,
		resetPaths,
	)
	return err
}
