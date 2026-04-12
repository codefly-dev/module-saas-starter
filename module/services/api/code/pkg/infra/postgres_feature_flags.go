package infra

import (
	"context"

	"github.com/jackc/pgx/v5"

	"backend/pkg/business"
)

func (s *PostgresStore) GetFeatureFlag(ctx context.Context, name string) (*business.FeatureFlag, error) {
	q := s.getQueryExecutor(ctx)

	var flag business.FeatureFlag
	var targetOrgIDs []string

	err := q.QueryRow(ctx, `
		SELECT id, name, description, enabled, rollout_percent, target_org_ids
		FROM feature_flags WHERE name = $1`, name).
		Scan(&flag.ID, &flag.Name, &flag.Description, &flag.Enabled, &flag.RolloutPercent, &targetOrgIDs)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // unknown flag = not configured
		}
		return nil, err
	}
	flag.TargetOrgIDs = targetOrgIDs
	return &flag, nil
}

func (s *PostgresStore) ListFeatureFlags(ctx context.Context) ([]*business.FeatureFlag, error) {
	q := s.getQueryExecutor(ctx)

	rows, err := q.Query(ctx, `
		SELECT id, name, description, enabled, rollout_percent, target_org_ids
		FROM feature_flags ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flags []*business.FeatureFlag
	for rows.Next() {
		var flag business.FeatureFlag
		var targetOrgIDs []string
		err := rows.Scan(&flag.ID, &flag.Name, &flag.Description, &flag.Enabled, &flag.RolloutPercent, &targetOrgIDs)
		if err != nil {
			return nil, err
		}
		flag.TargetOrgIDs = targetOrgIDs
		flags = append(flags, &flag)
	}
	return flags, nil
}

func (s *PostgresStore) UpsertFeatureFlag(ctx context.Context, flag *business.FeatureFlag) error {
	q := s.getQueryExecutor(ctx)
	_, err := q.Exec(ctx, `
		INSERT INTO feature_flags (id, name, description, enabled, rollout_percent, target_org_ids)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (name) DO UPDATE SET
			description = EXCLUDED.description,
			enabled = EXCLUDED.enabled,
			rollout_percent = EXCLUDED.rollout_percent,
			target_org_ids = EXCLUDED.target_org_ids,
			updated_at = NOW()`,
		flag.ID, flag.Name, flag.Description, flag.Enabled, flag.RolloutPercent, flag.TargetOrgIDs)
	return err
}
