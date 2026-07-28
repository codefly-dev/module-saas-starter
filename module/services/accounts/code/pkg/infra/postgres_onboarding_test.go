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

func TestUpsertOnboardingStep_CreateAndUpdate(t *testing.T) {
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
		require.NoError(t, upsertOnboardingStep(ctx, userID, orgID, step))

		steps, err := onboardingSteps(ctx, userID, orgID)
		require.NoError(t, err)
		require.Len(t, steps, 1)
		require.Equal(t, "pending", steps[0].Status)
		require.Nil(t, steps[0].CompletedAt)

		step.Status = "completed"
		step.CompletedAt = &now
		step.CompletionMethod = "detected"
		require.NoError(t, upsertOnboardingStep(ctx, userID, orgID, step))

		steps, err = onboardingSteps(ctx, userID, orgID)
		require.NoError(t, err)
		require.Len(t, steps, 1)
		require.Equal(t, "completed", steps[0].Status)
		require.NotNil(t, steps[0].CompletedAt)
	})
}

func TestUpsertOnboardingStep_IsOrganizationScopedAndIdempotent(t *testing.T) {
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
			require.NoError(t, upsertOnboardingStep(ctx, userID, orgID, step))
			require.NoError(t, upsertOnboardingStep(ctx, userID, orgID, step))
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

func upsertOnboardingStep(
	ctx context.Context,
	userID, orgID string,
	step *business.OnboardingStep,
) error {
	return testStore.UpsertOnboardingStep(
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
