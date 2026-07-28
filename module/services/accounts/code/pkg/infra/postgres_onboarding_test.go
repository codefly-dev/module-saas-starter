package infra_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestGetOnboardingProgress_Empty(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	withOnboardingScope(t, userID, orgID, func(ctx context.Context) {
		steps, err := testStore.GetOnboardingProgress(
			ctx, userID, orgID, business.CurrentOnboardingFlowID, business.CurrentOnboardingFlowVersion,
		)
		require.NoError(t, err)
		require.Empty(t, steps)
	})
}

func TestEnsureOnboardingStepDoesNotRegressCompletedProgress(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	withOnboardingScope(t, userID, orgID, func(ctx context.Context) {
		now := time.Now()
		step := &business.OnboardingStep{
			ID:          gen.OnboardingStepId_ONBOARDING_STEP_ID_CONFIGURE_ORGANIZATION,
			StepName:    "configure_organization",
			Status:      "pending",
			Required:    true,
			FirstSeenAt: now,
		}
		current, err := ensureOnboardingStep(ctx, userID, orgID, step)
		require.NoError(t, err)
		require.Equal(t, "pending", current.Status)

		completed := *step
		completed.Status = "completed"
		completed.CompletedAt = &now
		completed.CompletionMethod = "detected"
		current, transitioned, err := testStore.TransitionOnboardingStep(
			ctx,
			userID,
			orgID,
			business.CurrentOnboardingFlowID,
			business.CurrentOnboardingFlowVersion,
			"pending",
			&completed,
		)
		require.NoError(t, err)
		require.True(t, transitioned)
		require.Equal(t, "completed", current.Status)

		current, err = ensureOnboardingStep(ctx, userID, orgID, step)
		require.NoError(t, err)
		require.Equal(t, "completed", current.Status)
		require.NotNil(t, current.CompletedAt)

		steps, err := onboardingSteps(ctx, userID, orgID)
		require.NoError(t, err)
		require.Len(t, steps, 1)
		require.Equal(t, "completed", steps[0].Status)
		require.NotNil(t, steps[0].CompletedAt)
	})
}

func TestEnsureOnboardingStep_IsOrganizationScopedAndIdempotent(t *testing.T) {
	userID := seedUser(t)
	firstOrgID := seedOrg(t, userID)
	secondOrgID := seedOrg(t, userID)
	for _, orgID := range []string{firstOrgID, secondOrgID} {
		withOnboardingScope(t, userID, orgID, func(ctx context.Context) {
			now := time.Now()
			step := &business.OnboardingStep{
				ID:               gen.OnboardingStepId_ONBOARDING_STEP_ID_INVITE_TEAM,
				StepName:         "invite_team",
				Status:           "skipped",
				FirstSeenAt:      now,
				SkippedAt:        &now,
				CompletionMethod: "user_skip",
			}
			_, err := ensureOnboardingStep(ctx, userID, orgID, step)
			require.NoError(t, err)
			_, err = ensureOnboardingStep(ctx, userID, orgID, step)
			require.NoError(t, err)
			steps, err := onboardingSteps(ctx, userID, orgID)
			require.NoError(t, err)
			require.Len(t, steps, 1)
			require.Equal(t, "skipped", steps[0].Status)
			require.NotNil(t, steps[0].SkippedAt)
		})
	}
}

func withOnboardingScope(
	t *testing.T,
	userID, orgID string,
	fn func(context.Context),
) {
	t.Helper()
	require.NoError(t,
		testStore.As(business.Identity{UserID: userID, OrgID: orgID}).Within(
			testCtx,
			func(ctx context.Context) error {
				fn(ctx)
				return nil
			},
		),
	)
}

func ensureOnboardingStep(
	ctx context.Context,
	userID, orgID string,
	step *business.OnboardingStep,
) (*business.OnboardingStep, error) {
	return testStore.EnsureOnboardingStep(
		ctx,
		userID,
		orgID,
		business.CurrentOnboardingFlowID,
		business.CurrentOnboardingFlowVersion,
		step,
	)
}

func onboardingSteps(
	ctx context.Context,
	userID, orgID string,
) ([]*business.OnboardingStep, error) {
	return testStore.GetOnboardingProgress(
		ctx,
		userID,
		orgID,
		business.CurrentOnboardingFlowID,
		business.CurrentOnboardingFlowVersion,
	)
}
