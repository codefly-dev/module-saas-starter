package infra

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

func (s *PostgresStore) GetOnboardingProgress(
	ctx context.Context,
	userID, orgID, flowID string,
	flowVersion uint32,
) ([]*business.OnboardingStep, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx, `
		UPDATE onboarding_progress
		SET last_seen_at = CURRENT_TIMESTAMP
		WHERE user_id = $1 AND org_id = $2 AND flow_id = $3 AND flow_version = $4
		RETURNING step_name, status, required, first_seen_at, last_seen_at,
		          completed_at, skipped_at, completion_method, skip_reason`,
		userID, orgID, flowID, flowVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var steps []*business.OnboardingStep
	for rows.Next() {
		var step business.OnboardingStep
		if err := rows.Scan(
			&step.StepName,
			&step.Status,
			&step.Required,
			&step.FirstSeenAt,
			&step.LastSeenAt,
			&step.CompletedAt,
			&step.SkippedAt,
			&step.CompletionMethod,
			&step.SkipReason,
		); err != nil {
			return nil, err
		}
		steps = append(steps, &step)
	}
	return steps, rows.Err()
}

func (s *PostgresStore) UpsertOnboardingStep(
	ctx context.Context,
	userID, orgID, flowID string,
	flowVersion uint32,
	step *business.OnboardingStep,
) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO onboarding_progress (
			id, user_id, org_id, flow_id, flow_version, variant, step_name,
			status, required, first_seen_at, last_seen_at, completed_at,
			skipped_at, completion_method, skip_reason
		)
		VALUES (
			$1, $2, $3, $4, $5, 'default', $6, $7, $8,
			$9, CURRENT_TIMESTAMP, $10, $11, COALESCE(NULLIF($12, ''), 'migrated'), $13
		)
		ON CONFLICT (user_id, org_id, flow_id, flow_version, step_name)
			WHERE org_id IS NOT NULL
		DO UPDATE SET
			status = EXCLUDED.status,
			required = EXCLUDED.required,
			last_seen_at = CURRENT_TIMESTAMP,
			completed_at = COALESCE(EXCLUDED.completed_at, onboarding_progress.completed_at),
			skipped_at = COALESCE(EXCLUDED.skipped_at, onboarding_progress.skipped_at),
			completion_method = COALESCE(EXCLUDED.completion_method, onboarding_progress.completion_method),
			skip_reason = EXCLUDED.skip_reason`,
		business.NewIDString(),
		userID,
		orgID,
		flowID,
		flowVersion,
		step.StepName,
		step.Status,
		step.Required,
		step.FirstSeenAt,
		step.CompletedAt,
		step.SkippedAt,
		step.CompletionMethod,
		step.SkipReason,
	)
	return err
}

func (s *PostgresStore) GetOrganizationActivation(
	ctx context.Context,
	orgID, flowID string,
	flowVersion uint32,
	milestone string,
) (*time.Time, error) {
	var achievedAt time.Time
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT achieved_at
		FROM organization_activations
		WHERE org_id = $1 AND flow_id = $2 AND flow_version = $3 AND milestone = $4`,
		orgID, flowID, flowVersion, milestone).Scan(&achievedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &achievedAt, nil
}

func (s *PostgresStore) RecordOrganizationActivation(
	ctx context.Context,
	orgID, flowID string,
	flowVersion uint32,
	milestone, actorID string,
) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO organization_activations (
			org_id, flow_id, flow_version, milestone, actor_id
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id, flow_id, flow_version, milestone) DO NOTHING`,
		orgID, flowID, flowVersion, milestone, nilIfEmpty(actorID))
	return err
}
