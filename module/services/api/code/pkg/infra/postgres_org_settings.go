package infra

import (
	"context"

	"github.com/jackc/pgx/v5"

	"api/pkg/business"
)

func (s *PostgresStore) GetOrgSettings(ctx context.Context, orgID string) (*business.OrgSettings, error) {
	q := s.getQueryExecutor(ctx)
	var settings business.OrgSettings
	err := q.QueryRow(ctx, `
		SELECT org_id, logo_url, primary_color, custom_domain, favicon_url, created_at, updated_at
		FROM org_settings
		WHERE org_id = $1`, orgID).Scan(
		&settings.OrgID,
		&settings.LogoURL,
		&settings.PrimaryColor,
		&settings.CustomDomain,
		&settings.FaviconURL,
		&settings.CreatedAt,
		&settings.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &settings, nil
}

func (s *PostgresStore) UpsertOrgSettings(ctx context.Context, settings *business.OrgSettings) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO org_settings (org_id, logo_url, primary_color, custom_domain, favicon_url)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id) DO UPDATE SET
			logo_url = EXCLUDED.logo_url,
			primary_color = EXCLUDED.primary_color,
			custom_domain = EXCLUDED.custom_domain,
			favicon_url = EXCLUDED.favicon_url,
			updated_at = CURRENT_TIMESTAMP`,
		settings.OrgID, settings.LogoURL, settings.PrimaryColor, settings.CustomDomain, settings.FaviconURL)
	return err
}
