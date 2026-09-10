package business_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/codefly-dev/core/wool"
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

	// A database seeded before the id was declared keeps its own uuid. The
	// seeder runs at service startup, so refusing here would stop the whole
	// graph from booting; it converges and reports the drift instead.
	drifted := captureWoolLogs(t)
	writeFixture("00000000-0000-7000-8000-00000000f002")
	require.NoError(t, fixtures.Seed(ctx, testService, "stable-ids"))
	require.Equal(t, pinnedID, seededUUID("fixture-pinned"))

	logged := drifted()
	require.Contains(t, logged, pinnedID)
	require.Contains(t, logged, "00000000-0000-7000-8000-00000000f002")
	require.Contains(t, logged, "fixture user id drift")
}

// A fixture copied from another one keeps its pinned ids, so seeding both into
// one database claims a uuid that is already taken. RegisterUser inserts the
// caller's uuid, so without its own check the collision surfaces as a raw
// users_pkey violation — the opaque failure this pinning exists to remove.
func TestFixtureSeedNamesAConflictingUserID(t *testing.T) {
	clearData(t)
	ctx := testCtx
	fixturePath := filepath.Join(t.TempDir(), "conflicting-ids.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)

	const sharedID = "00000000-0000-7000-8000-00000000f003"
	contents := fmt.Sprintf(`users:
  - id: %s
    email: first@fixture.test
    provider: email
    provider_id: fixture-first
`, sharedID)
	require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))
	require.NoError(t, fixtures.Seed(ctx, testService, "conflicting-ids"))

	contents = fmt.Sprintf(`users:
  - id: %s
    email: second@fixture.test
    provider: email
    provider_id: fixture-second
`, sharedID)
	require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))

	err := fixtures.Seed(ctx, testService, "conflicting-ids")
	require.Error(t, err)
	require.Contains(t, err.Error(), sharedID)
	require.NotContains(t, err.Error(), "users_pkey")
}

// captureWoolLogs redirects wool's fallback logger — the one Get() uses for a
// context carrying no provider, which is what the test context is — and returns
// a reader for everything logged until it is called.
func captureWoolLogs(t *testing.T) func() string {
	t.Helper()
	capture := &woolCapture{}
	wool.SetFallbackLogger(capture)
	t.Cleanup(func() { wool.SetFallbackLogger(nil) })
	return func() string {
		wool.SetFallbackLogger(nil)
		return capture.String()
	}
}

type woolCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *woolCapture) Process(msg *wool.Log) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, msg.String())
}

func (c *woolCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}
