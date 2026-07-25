package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// TestUpdateUserProfileMerge verifies the profile_merge path: non-empty keys are
// set, blank keys are removed, and untouched keys are preserved — so a caller can
// send only the fields it changed without a read-modify-write of the whole map.
func TestUpdateUserProfileMerge(t *testing.T) {
	id := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{UserID: id}).Within(testCtx, func(ctx context.Context) error {
		if _, err := testStore.UpdateUser(ctx, id, map[string]any{
			"profile_merge": map[string]string{"name": "Ada", "title": "Engineer", "phone": "555"},
		}); err != nil {
			return err
		}

		// Update name, clear title (blank), add bio, leave phone untouched.
		user, err := testStore.UpdateUser(ctx, id, map[string]any{
			"profile_merge": map[string]string{"name": "Ada Lovelace", "title": "", "bio": "hello"},
		})
		if err != nil {
			return err
		}

		require.Equal(t, map[string]string{
			"name":  "Ada Lovelace",
			"phone": "555",
			"bio":   "hello",
		}, user.Profile)
		return nil
	}))
}
