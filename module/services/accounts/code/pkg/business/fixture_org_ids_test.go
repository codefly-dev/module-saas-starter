package business_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/fixtures"
)

// A module principal grant (MODULE_PRINCIPALS) names its tenant by organization
// id, so a fixture organization has to keep its uuid across reseeds the way a
// fixture user keeps its principal — otherwise the committed grant matches no
// tenant after the next fresh seed.
func TestFixtureSeedHonoursDeclaredOrganizationID(t *testing.T) {
	clearData(t)
	ctx := testCtx
	fixturePath := filepath.Join(t.TempDir(), "stable-org-ids.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)

	const pinnedID = "00000000-0000-7000-8000-00000000f101"

	writeFixture := func(pinned string) {
		t.Helper()
		contents := fmt.Sprintf(`users:
  - email: owner@fixture.test
    provider: email
    provider_id: fixture-org-owner
organizations:
  - id: %s
    name: Pinned Org
    owner: owner@fixture.test
  - name: Unpinned Org
    owner: owner@fixture.test
`, pinned)
		require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))
	}

	writeFixture(pinnedID)
	require.NoError(t, fixtures.Seed(ctx, testService, "stable-org-ids"))
	owner := seededUUIDFor(t, ctx, "fixture-org-owner")
	require.Equal(t, pinnedID, seededOrgIDFor(t, ctx, owner, "Pinned Org"))
	unpinned := seededOrgIDFor(t, ctx, owner, "Unpinned Org")
	require.NotEmpty(t, unpinned)

	require.NoError(t, fixtures.Seed(ctx, testService, "stable-org-ids"))
	require.Equal(t, pinnedID, seededOrgIDFor(t, ctx, owner, "Pinned Org"))
	require.Equal(t, unpinned, seededOrgIDFor(t, ctx, owner, "Unpinned Org"))

	// A database seeded before the id was declared keeps its own uuid; the
	// seeder converges and reports the drift rather than refusing to boot.
	drifted := captureWoolLogs(t)
	writeFixture("00000000-0000-7000-8000-00000000f102")
	require.NoError(t, fixtures.Seed(ctx, testService, "stable-org-ids"))
	require.Equal(t, pinnedID, seededOrgIDFor(t, ctx, owner, "Pinned Org"))

	logged := drifted()
	require.Contains(t, logged, pinnedID)
	require.Contains(t, logged, "00000000-0000-7000-8000-00000000f102")
	require.Contains(t, logged, "fixture organization id drift")
}

// Two fixtures declaring the same organization id into one database: the
// second cannot be honoured, so it seeds with a fresh uuid and reports.
func TestFixtureSeedFallsBackWhenDeclaredOrganizationIDIsTaken(t *testing.T) {
	clearData(t)
	ctx := testCtx
	fixturePath := filepath.Join(t.TempDir(), "conflicting-org-ids.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)

	const sharedID = "00000000-0000-7000-8000-00000000f103"
	writeOrg := func(name string) {
		t.Helper()
		contents := fmt.Sprintf(`users:
  - email: owner@fixture.test
    provider: email
    provider_id: fixture-org-owner
organizations:
  - id: %s
    name: %s
    owner: owner@fixture.test
`, sharedID, name)
		require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))
	}

	writeOrg("First Org")
	require.NoError(t, fixtures.Seed(ctx, testService, "conflicting-org-ids"))
	owner := seededUUIDFor(t, ctx, "fixture-org-owner")
	require.Equal(t, sharedID, seededOrgIDFor(t, ctx, owner, "First Org"))

	reported := captureWoolLogs(t)
	writeOrg("Second Org")
	require.NoError(t, fixtures.Seed(ctx, testService, "conflicting-org-ids"))

	second := seededOrgIDFor(t, ctx, owner, "Second Org")
	require.NotEqual(t, sharedID, second)
	require.NotEmpty(t, second)

	logged := reported()
	require.Contains(t, logged, sharedID)
	require.Contains(t, logged, "already holds")
}

// seededOrgIDFor returns the id of the owner's organization with this name.
func seededOrgIDFor(t *testing.T, ctx context.Context, ownerID, name string) string {
	t.Helper()
	id := ""
	require.NoError(t, testService.Store().WithControlPlane(ctx, func(ctx context.Context) error {
		orgs, err := testService.Store().ListOrganizationsForUser(ctx, ownerID)
		if err != nil {
			return err
		}
		for _, org := range orgs {
			if org.Name == name {
				id = org.Id
			}
		}
		return nil
	}))
	require.NotEmpty(t, id, "organization %q was not seeded for %s", name, ownerID)
	return id
}
