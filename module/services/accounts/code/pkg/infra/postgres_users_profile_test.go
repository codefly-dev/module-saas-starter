package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// TestUpdateUserProfileMerge covers the race-free self-service write path: a
// "profile_merge" update patches individual keys and leaves untouched keys
// (including a concurrent writer's) intact, while an empty value clears its key.
func TestUpdateUserProfileMerge(t *testing.T) {
	userID := seedUser(t)

	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		// Writer A sets two fields.
		_, err := testStore.UpdateUser(ctx, userID, map[string]any{
			"profile_merge": map[string]string{"display_name": "Alice", "bio": "hello"},
		})
		require.NoError(t, err)

		// Writer B patches a different field. A field-by-field merge must not
		// drop A's keys — the whole point of the fix.
		user, err := testStore.UpdateUser(ctx, userID, map[string]any{
			"profile_merge": map[string]string{"location": "NYC"},
		})
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"display_name": "Alice",
			"bio":          "hello",
			"location":     "NYC",
		}, user.Profile)

		// An empty value clears its key and leaves the rest untouched.
		user, err = testStore.UpdateUser(ctx, userID, map[string]any{
			"profile_merge": map[string]string{"bio": "", "display_name": "Alice B."},
		})
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"display_name": "Alice B.",
			"location":     "NYC",
		}, user.Profile)

		return nil
	}))
}

// TestUpdateUserProfileReplacePreservesGDPRSemantics guards the invariant GDPR
// anonymization relies on: the "profile" key still replaces the entire map, so
// scrubbing wipes every prior field rather than merging over them.
func TestUpdateUserProfileReplacePreservesGDPRSemantics(t *testing.T) {
	userID := seedUser(t)

	require.NoError(t, testStore.As(business.Identity{UserID: userID}).Within(testCtx, func(ctx context.Context) error {
		_, err := testStore.UpdateUser(ctx, userID, map[string]any{
			"profile_merge": map[string]string{
				"display_name": "Alice",
				"bio":          "hello",
				"location":     "NYC",
			},
		})
		require.NoError(t, err)

		user, err := testStore.UpdateUser(ctx, userID, map[string]any{
			"profile": map[string]string{"display_name": "Deleted User #abcd1234"},
		})
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"display_name": "Deleted User #abcd1234",
		}, user.Profile)

		return nil
	}))
}
