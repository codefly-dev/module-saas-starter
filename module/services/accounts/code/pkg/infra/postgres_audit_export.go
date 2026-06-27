package infra

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// GetAuditExportConfig loads the per-org export config. Returns
// (nil, nil) on not-found.
func (s *PostgresStore) GetAuditExportConfig(ctx context.Context, orgID string) (*business.AuditExportConfig, error) {
	q := s.getQueryExecutor(ctx)
	var cfg business.AuditExportConfig
	var endpoint, lastError *string
	var lastExportedAt, lastErrorAt *time.Time
	err := q.QueryRow(ctx, `
		SELECT id, org_id, bucket, region, endpoint, prefix,
		       access_key_id, secret_access_key, cadence_minutes, enabled,
		       last_exported_at, last_error, last_error_at,
		       created_at, updated_at
		FROM audit_export_configs WHERE org_id = $1`, orgID,
	).Scan(
		&cfg.ID, &cfg.OrgID, &cfg.Bucket, &cfg.Region, &endpoint, &cfg.Prefix,
		&cfg.AccessKeyID, &cfg.SecretAccessKey, &cfg.CadenceMinutes, &cfg.Enabled,
		&lastExportedAt, &lastError, &lastErrorAt,
		&cfg.CreatedAt, &cfg.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if endpoint != nil {
		cfg.Endpoint = *endpoint
	}
	cfg.LastExportedAt = lastExportedAt
	if lastError != nil {
		cfg.LastError = *lastError
	}
	cfg.LastErrorAt = lastErrorAt
	return &cfg, nil
}

// UpsertAuditExportConfig inserts or replaces a per-org config.
// Conflict resolution: ON CONFLICT (org_id) DO UPDATE preserves the
// generated id but refreshes everything else, so re-saving from the
// admin UI is a single round-trip.
func (s *PostgresStore) UpsertAuditExportConfig(ctx context.Context, cfg *business.AuditExportConfig) error {
	q := s.getQueryExecutor(ctx)
	var endpoint *string
	if cfg.Endpoint != "" {
		endpoint = &cfg.Endpoint
	}
	_, err := q.Exec(ctx, `
		INSERT INTO audit_export_configs (
			id, org_id, bucket, region, endpoint, prefix,
			access_key_id, secret_access_key, cadence_minutes, enabled
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (org_id) DO UPDATE SET
			bucket            = EXCLUDED.bucket,
			region            = EXCLUDED.region,
			endpoint          = EXCLUDED.endpoint,
			prefix            = EXCLUDED.prefix,
			access_key_id     = EXCLUDED.access_key_id,
			secret_access_key = EXCLUDED.secret_access_key,
			cadence_minutes   = EXCLUDED.cadence_minutes,
			enabled           = EXCLUDED.enabled,
			updated_at        = NOW()`,
		cfg.ID, cfg.OrgID, cfg.Bucket, cfg.Region, endpoint, cfg.Prefix,
		cfg.AccessKeyID, cfg.SecretAccessKey, cfg.CadenceMinutes, cfg.Enabled,
	)
	return err
}

// DeleteAuditExportConfig removes the org's config. Idempotent.
func (s *PostgresStore) DeleteAuditExportConfig(ctx context.Context, orgID string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `DELETE FROM audit_export_configs WHERE org_id = $1`, orgID)
	return err
}

// ListDueAuditExportConfigs returns configs whose last_exported_at is
// older than (now - cadence_minutes), or have never run. enabled=false
// configs are skipped — disabled is the off switch the admin UI
// toggles without deleting (so cadence + creds are preserved on
// re-enable).
func (s *PostgresStore) ListDueAuditExportConfigs(ctx context.Context, now time.Time) ([]*business.AuditExportConfig, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT id, org_id, bucket, region, endpoint, prefix,
		       access_key_id, secret_access_key, cadence_minutes, enabled,
		       last_exported_at, last_error, last_error_at,
		       created_at, updated_at
		FROM audit_export_configs
		WHERE enabled = TRUE
		  AND (last_exported_at IS NULL
		       OR last_exported_at <= $1::timestamptz - make_interval(mins => cadence_minutes))`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*business.AuditExportConfig
	for rows.Next() {
		var cfg business.AuditExportConfig
		var endpoint, lastError *string
		var lastExportedAt, lastErrorAt *time.Time
		if err := rows.Scan(
			&cfg.ID, &cfg.OrgID, &cfg.Bucket, &cfg.Region, &endpoint, &cfg.Prefix,
			&cfg.AccessKeyID, &cfg.SecretAccessKey, &cfg.CadenceMinutes, &cfg.Enabled,
			&lastExportedAt, &lastError, &lastErrorAt,
			&cfg.CreatedAt, &cfg.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if endpoint != nil {
			cfg.Endpoint = *endpoint
		}
		cfg.LastExportedAt = lastExportedAt
		if lastError != nil {
			cfg.LastError = *lastError
		}
		cfg.LastErrorAt = lastErrorAt
		out = append(out, &cfg)
	}
	return out, nil
}

// MarkAuditExportSucceeded advances the cursor and clears any prior
// error. Called by the exporter on a clean upload (or a "no events
// to export" no-op).
func (s *PostgresStore) MarkAuditExportSucceeded(ctx context.Context, orgID string, exportedAt time.Time) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE audit_export_configs
		   SET last_exported_at = $2,
		       last_error       = NULL,
		       last_error_at    = NULL,
		       updated_at       = NOW()
		 WHERE org_id = $1`, orgID, exportedAt)
	return err
}

// RecordAuditExportError stamps the most recent failure for surfacing
// in the admin UI. Doesn't advance last_exported_at — next tick
// retries the same window.
func (s *PostgresStore) RecordAuditExportError(ctx context.Context, orgID, message string) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		UPDATE audit_export_configs
		   SET last_error    = $2,
		       last_error_at = NOW(),
		       updated_at    = NOW()
		 WHERE org_id = $1`, orgID, message)
	return err
}
