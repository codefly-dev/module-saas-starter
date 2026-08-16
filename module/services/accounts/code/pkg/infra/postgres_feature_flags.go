package infra

import (
	"context"

	"accounts/pkg/business"
)

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
