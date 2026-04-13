package infra_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ============================================================================
// Onboarding tests
// ============================================================================

func TestGetOnboardingProgress_Empty(t *testing.T) {
	userID := seedUser(t)

	steps, err := testStore.GetOnboardingProgress(testCtx, userID)
	require.NoError(t, err)
	require.Empty(t, steps, "new user should have no onboarding steps")
}

func TestUpsertOnboardingStep_CreateAndUpdate(t *testing.T) {
	userID := seedUser(t)

	// Create a new step with "pending" status.
	err := testStore.UpsertOnboardingStep(testCtx, userID, "setup_profile", "pending")
	require.NoError(t, err)

	steps, err := testStore.GetOnboardingProgress(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	require.Equal(t, "setup_profile", steps[0].StepName)
	require.Equal(t, "pending", steps[0].Status)
	require.Nil(t, steps[0].CompletedAt, "pending step should not have completed_at")

	// Update the same step to "completed".
	err = testStore.UpsertOnboardingStep(testCtx, userID, "setup_profile", "completed")
	require.NoError(t, err)

	steps, err = testStore.GetOnboardingProgress(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, steps, 1, "upsert should not create a duplicate row")
	require.Equal(t, "completed", steps[0].Status)
	require.NotNil(t, steps[0].CompletedAt, "completed step should have completed_at")
}

func TestUpsertOnboardingStep_Idempotent(t *testing.T) {
	userID := seedUser(t)

	// Insert the same step twice with the same status.
	err := testStore.UpsertOnboardingStep(testCtx, userID, "invite_team", "skipped")
	require.NoError(t, err)
	err = testStore.UpsertOnboardingStep(testCtx, userID, "invite_team", "skipped")
	require.NoError(t, err)

	steps, err := testStore.GetOnboardingProgress(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, steps, 1, "idempotent upsert should produce exactly one row")
	require.Equal(t, "skipped", steps[0].Status)
	require.NotNil(t, steps[0].CompletedAt, "skipped step should have completed_at")
}

func TestUpsertOnboardingStep_MultipleSteps(t *testing.T) {
	userID := seedUser(t)

	require.NoError(t, testStore.UpsertOnboardingStep(testCtx, userID, "step_a", "completed"))
	require.NoError(t, testStore.UpsertOnboardingStep(testCtx, userID, "step_b", "pending"))
	require.NoError(t, testStore.UpsertOnboardingStep(testCtx, userID, "step_c", "skipped"))

	steps, err := testStore.GetOnboardingProgress(testCtx, userID)
	require.NoError(t, err)
	require.Len(t, steps, 3)
}
