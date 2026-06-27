package infra

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// GetOrgSSO loads the SSO state from the organizations row. Returns
// (nil, nil) when no SSO columns are populated. The columns live on
// `organizations` directly (not a separate table) because each org
// has at most one SSO config — splitting would just add a join.
func (s *PostgresStore) GetOrgSSO(ctx context.Context, orgID string) (*business.OrgSSOConfig, error) {
	q := s.getQueryExecutor(ctx)
	var cfg business.OrgSSOConfig
	var provider, connID, workosOrgID, status *string
	var configuredAt *time.Time

	err := q.QueryRow(ctx, `
		SELECT id, sso_provider, sso_connection_id, sso_organization_id, sso_status, sso_configured_at
		FROM organizations WHERE id = $1`, orgID,
	).Scan(&cfg.OrgID, &provider, &connID, &workosOrgID, &status, &configuredAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if provider == nil || *provider == "" {
		// No SSO row written yet — treat as not-configured. Returning
		// nil here lets the FE branch on existence cleanly.
		return nil, nil
	}
	cfg.Provider = *provider
	if connID != nil {
		cfg.ConnectionID = *connID
	}
	if workosOrgID != nil {
		cfg.OrganizationID = *workosOrgID
	}
	if status != nil {
		cfg.Status = *status
	}
	cfg.ConfiguredAt = configuredAt
	return &cfg, nil
}

// UpsertOrgSSO writes the SSO columns onto the organizations row.
// Idempotent — re-saving the same state just refreshes
// sso_configured_at. Empty fields are written as NULL so a Disable
// can clear connection_id without forgetting organization_id.
func (s *PostgresStore) UpsertOrgSSO(ctx context.Context, cfg *business.OrgSSOConfig) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE organizations
		   SET sso_provider        = NULLIF($2, ''),
		       sso_connection_id   = NULLIF($3, ''),
		       sso_organization_id = NULLIF($4, ''),
		       sso_status          = NULLIF($5, ''),
		       sso_configured_at   = $6
		 WHERE id = $1`,
		cfg.OrgID, cfg.Provider, cfg.ConnectionID, cfg.OrganizationID, cfg.Status, cfg.ConfiguredAt,
	)
	return err
}
