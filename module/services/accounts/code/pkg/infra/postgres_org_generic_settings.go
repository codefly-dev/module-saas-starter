package infra

import (
	"context"

	"github.com/jackc/pgx/v5"

	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/orgsettings"
)

// GetOrgGenericSettings returns the typed protobuf document for an org's
// generic settings. A missing row resolves to an empty document. Raw ProtoJSON
// never escapes this adapter.
func (s *PostgresStore) GetOrgGenericSettings(
	ctx context.Context,
	orgID string,
) (*gen.OrganizationSettings, error) {
	q := s.getQueryExecutor(ctx)
	var encoded []byte
	err := q.QueryRow(
		ctx,
		`SELECT settings FROM org_generic_settings WHERE org_id = $1`,
		orgID,
	).Scan(&encoded)
	if err == pgx.ErrNoRows {
		return &gen.OrganizationSettings{}, nil
	}
	if err != nil {
		return nil, err
	}
	return orgsettings.JSON.Unmarshal(encoded)
}

// UpdateOrgGenericSettings atomically deep-merges a canonical ProtoJSON patch
// into the org row (creating it on first write). PostgreSQL preserves nested
// siblings and fields unknown to this binary via the shared, schema-agnostic
// settings_jsonb_* functions (migration 80); explicit optional zero values
// still overwrite. reset paths are pruned before the merge is applied.
func (s *PostgresStore) UpdateOrgGenericSettings(
	ctx context.Context,
	orgID string,
	patch *gen.OrganizationSettings,
	resetPaths []string,
) error {
	if patch == nil {
		patch = &gen.OrganizationSettings{}
	}
	if resetPaths == nil {
		resetPaths = []string{}
	}
	q := s.getQueryExecutor(ctx)
	encoded, err := orgsettings.JSON.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO org_generic_settings (org_id, settings)
		VALUES ($1, public.settings_jsonb_deep_merge(
		           public.settings_jsonb_delete_paths('{}'::jsonb, $3::text[]),
		           $2::jsonb
		       ))
		ON CONFLICT (org_id) DO UPDATE
		   SET settings = public.settings_jsonb_deep_merge(
		           public.settings_jsonb_delete_paths(org_generic_settings.settings, $3::text[]),
		           $2::jsonb
		       ),
		       updated_at = NOW()`,
		orgID,
		encoded,
		resetPaths,
	)
	return err
}
