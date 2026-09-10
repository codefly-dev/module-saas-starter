package business_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/fixtures"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestFixtureSeedHonoursDeclaredUserID(t *testing.T) {
	clearData(t)
	ctx := testCtx
	fixturePath := filepath.Join(t.TempDir(), "stable-ids.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)

	const pinnedID = "00000000-0000-7000-8000-00000000f001"

	writeFixture := func(pinned string) {
		t.Helper()
		contents := fmt.Sprintf(`users:
  - id: %s
    email: pinned@fixture.test
    provider: email
    provider_id: fixture-pinned
  - email: unpinned@fixture.test
    provider: email
    provider_id: fixture-unpinned
`, pinned)
		require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))
	}

	seededUUID := func(providerID string) string {
		t.Helper()
		var user *gen.User
		require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
			var err error
			user, err = testStore.GetUserByIdentity(ctx, &gen.UserIdentity{
				Provider: "email", ProviderId: providerID,
			})
			return err
		}))
		require.NotNil(t, user)
		return user.Uuid
	}

	writeFixture(pinnedID)
	require.NoError(t, fixtures.Seed(ctx, testService, "stable-ids"))
	require.Equal(t, pinnedID, seededUUID("fixture-pinned"))
	unpinned := seededUUID("fixture-unpinned")
	require.NotEmpty(t, unpinned)

	require.NoError(t, fixtures.Seed(ctx, testService, "stable-ids"))
	require.Equal(t, pinnedID, seededUUID("fixture-pinned"))
	require.Equal(t, unpinned, seededUUID("fixture-unpinned"))

	// A store that already holds another uuid for the identity cannot serve
	// configuration quoting the declared one, and both failures look identical
	// at runtime — so the seeder names the drift instead of converging.
	writeFixture("00000000-0000-7000-8000-00000000f002")
	err := fixtures.Seed(ctx, testService, "stable-ids")
	require.ErrorContains(t, err, pinnedID)
	require.Equal(t, pinnedID, seededUUID("fixture-pinned"))
}
