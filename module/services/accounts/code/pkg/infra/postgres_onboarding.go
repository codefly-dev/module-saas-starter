package infra

import (
	"context"
	"time"

	"accounts/pkg/business"
)

// Table name is `onboarding_progress` (see migration 15). The code used
// to reference `onboarding_steps` which doesn't exist — that broke all
// GetOnboardingProgress / UpsertOnboardingStep tests with "relation does
// not exist".

func (s *PostgresStore) GetOnboardingProgress(ctx context.Context, userID string) ([]*business.OnboardingStep, error) {
	q := s.getQueryExecutor(ctx)
	rows, err := q.Query(ctx, `
		SELECT step_name, status, completed_at
		FROM onboarding_progress
		WHERE user_id = $1
		ORDER BY step_name ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var steps []*business.OnboardingStep
	for rows.Next() {
		var step business.OnboardingStep
		var completedAt *time.Time

		err := rows.Scan(&step.StepName, &step.Status, &completedAt)
		if err != nil {
			return nil, err
		}
		step.CompletedAt = completedAt
		steps = append(steps, &step)
	}
	return steps, nil
}

func (s *PostgresStore) UpsertOnboardingStep(ctx context.Context, userID string, stepName string, status string) error {
	q := s.getQueryExecutor(ctx)

	var completedAt *time.Time
	if status == "completed" || status == "skipped" {
		now := time.Now()
		completedAt = &now
	}

	_, err := q.Exec(ctx, `
		INSERT INTO onboarding_progress (id, user_id, step_name, status, completed_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, step_name) DO UPDATE
		SET status = EXCLUDED.status, completed_at = EXCLUDED.completed_at`,
		business.NewIDString(), userID, stepName, status, completedAt)
	return err
}
