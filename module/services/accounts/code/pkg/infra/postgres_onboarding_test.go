package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// ============================================================================
// Onboarding tests — onboarding_progress is RLS-protected (user-scoped), so
// each test runs its store ops as the owning user via As(Identity{UserID}).
// ============================================================================

func TestGetOnboardingProgress_Empty(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		steps, err := testStore.GetOnboardingProgress(ctx, userID)
		require.NoError(t, err)
		require.Empty(t, steps, "new user should have no onboarding steps")
		return nil
	}))
}

func TestUpsertOnboardingStep_CreateAndUpdate(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		// Create a new step with "pending" status.
		require.NoError(t, testStore.UpsertOnboardingStep(ctx, userID, "setup_profile", "pending"))

		steps, err := testStore.GetOnboardingProgress(ctx, userID)
		require.NoError(t, err)
		require.Len(t, steps, 1)
		require.Equal(t, "setup_profile", steps[0].StepName)
		require.Equal(t, "pending", steps[0].Status)
		require.Nil(t, steps[0].CompletedAt, "pending step should not have completed_at")

		// Update the same step to "completed".
		require.NoError(t, testStore.UpsertOnboardingStep(ctx, userID, "setup_profile", "completed"))

		steps, err = testStore.GetOnboardingProgress(ctx, userID)
		require.NoError(t, err)
		require.Len(t, steps, 1, "upsert should not create a duplicate row")
		require.Equal(t, "completed", steps[0].Status)
		require.NotNil(t, steps[0].CompletedAt, "completed step should have completed_at")
		return nil
	}))
}

func TestUpsertOnboardingStep_Idempotent(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		// Insert the same step twice with the same status.
		require.NoError(t, testStore.UpsertOnboardingStep(ctx, userID, "invite_team", "skipped"))
		require.NoError(t, testStore.UpsertOnboardingStep(ctx, userID, "invite_team", "skipped"))

		steps, err := testStore.GetOnboardingProgress(ctx, userID)
		require.NoError(t, err)
		require.Len(t, steps, 1, "idempotent upsert should produce exactly one row")
		require.Equal(t, "skipped", steps[0].Status)
		require.NotNil(t, steps[0].CompletedAt, "skipped step should have completed_at")
		return nil
	}))
}

func TestUpsertOnboardingStep_MultipleSteps(t *testing.T) {
	userID := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		require.NoError(t, testStore.UpsertOnboardingStep(ctx, userID, "step_a", "completed"))
		require.NoError(t, testStore.UpsertOnboardingStep(ctx, userID, "step_b", "pending"))
		require.NoError(t, testStore.UpsertOnboardingStep(ctx, userID, "step_c", "skipped"))

		steps, err := testStore.GetOnboardingProgress(ctx, userID)
		require.NoError(t, err)
		require.Len(t, steps, 3)
		return nil
	}))
}
