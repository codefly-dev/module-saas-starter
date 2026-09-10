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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/fixtures"
	"accounts/pkg/business"
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

	writeFixture(pinnedID)
	require.NoError(t, fixtures.Seed(ctx, testService, "stable-ids"))
	require.Equal(t, pinnedID, seededUUIDFor(t, ctx, "fixture-pinned"))
	unpinned := seededUUIDFor(t, ctx, "fixture-unpinned")
	require.NotEmpty(t, unpinned)

	require.NoError(t, fixtures.Seed(ctx, testService, "stable-ids"))
	require.Equal(t, pinnedID, seededUUIDFor(t, ctx, "fixture-pinned"))
	require.Equal(t, unpinned, seededUUIDFor(t, ctx, "fixture-unpinned"))

	// A database seeded before the id was declared keeps its own uuid. The
	// seeder runs at service startup, so refusing here would stop the whole
	// graph from booting; it converges and reports the drift instead.
	drifted := captureWoolLogs(t)
	writeFixture("00000000-0000-7000-8000-00000000f002")
	require.NoError(t, fixtures.Seed(ctx, testService, "stable-ids"))
	require.Equal(t, pinnedID, seededUUIDFor(t, ctx, "fixture-pinned"))

	logged := drifted()
	require.Contains(t, logged, pinnedID)
	require.Contains(t, logged, "00000000-0000-7000-8000-00000000f002")
	require.Contains(t, logged, "fixture user id drift")
}

// A fixture copied from another one keeps its pinned ids, so seeding both into
// one database claims a uuid that is already taken. So does reseeding after a
// privacy erasure: deleting the identities leaves the users row, and with it
// the id. The seeder runs during service startup, so it seeds a fresh uuid and
// reports rather than stopping the graph from booting.
func TestFixtureSeedFallsBackWhenDeclaredIDIsTaken(t *testing.T) {
	clearData(t)
	ctx := testCtx
	fixturePath := filepath.Join(t.TempDir(), "conflicting-ids.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)

	const sharedID = "00000000-0000-7000-8000-00000000f003"
	writeUser := func(email, providerID string) {
		t.Helper()
		contents := fmt.Sprintf(`users:
  - id: %s
    email: %s
    provider: email
    provider_id: %s
`, sharedID, email, providerID)
		require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))
	}

	writeUser("first@fixture.test", "fixture-first")
	require.NoError(t, fixtures.Seed(ctx, testService, "conflicting-ids"))
	require.Equal(t, sharedID, seededUUIDFor(t, ctx, "fixture-first"))

	reported := captureWoolLogs(t)
	writeUser("second@fixture.test", "fixture-second")
	require.NoError(t, fixtures.Seed(ctx, testService, "conflicting-ids"))

	second := seededUUIDFor(t, ctx, "fixture-second")
	require.NotEqual(t, sharedID, second)
	require.NotEmpty(t, second)

	logged := reported()
	require.Contains(t, logged, sharedID)
	require.Contains(t, logged, "already holds")
}

// RegisterUser inserts a caller-supplied uuid, so it owns the guard against
// claiming one that is taken; without it the collision escapes as a raw
// users_pkey violation. The fixture seeder avoids the collision itself, so this
// covers the store contract every other caller relies on.
func TestRegisterUserRejectsATakenUserID(t *testing.T) {
	clearData(t)
	ctx := testCtx

	const takenID = "00000000-0000-7000-8000-00000000f004"
	register := func(id, email, providerID string) error {
		return testStore.RegisterUser(ctx,
			&gen.User{Uuid: id, PrimaryEmail: email, Status: gen.UserStatus_USER_STATUS_ACTIVE},
			&gen.UserIdentity{
				Uuid:     business.NewIDString(),
				UserUuid: id,
				Provider: "email", ProviderId: providerID, ProviderEmail: email,
			})
	}

	require.NoError(t, register(takenID, "holder@fixture.test", "fixture-holder"))

	err := register(takenID, "other@fixture.test", "fixture-other")
	require.Error(t, err)
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	require.Contains(t, err.Error(), takenID)
	require.NotContains(t, err.Error(), "users_pkey")
}

// seededUUIDFor returns the uuid the store holds for a fixture identity.
func seededUUIDFor(t *testing.T, ctx context.Context, providerID string) string {
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
