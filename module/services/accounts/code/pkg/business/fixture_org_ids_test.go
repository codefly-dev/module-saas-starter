//go:build !pure

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
}

// An organization that declares no id logs the reason it will not be quotable,
// so a consumer fixture (DEV_FIXTURE_PATH) — which no test gates — is told why
// its MODULE_PRINCIPALS grant has no stable tenant to name.
func TestFixtureSeedWarnsWhenOrganizationDeclaresNoID(t *testing.T) {
	clearData(t)
	ctx := testCtx
	fixturePath := filepath.Join(t.TempDir(), "unpinned-org.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)

	contents := `users:
  - email: owner@fixture.test
    provider: email
    provider_id: fixture-org-owner
organizations:
  - name: Unpinned Org
    owner: owner@fixture.test
`
	require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))

	logged := captureWoolLogs(t)
	require.NoError(t, fixtures.Seed(ctx, testService, "unpinned-org"))
	require.Contains(t, logged(), "declares no id")
}

// A database that already holds this organization under a different uuid cannot
// adopt the declared one: a seed cannot rewrite a primary key that memberships,
// teams and tenant rows reference. Continuing would be the dangerous outcome,
// not the safe one — nothing downstream checks a tenant exists
// (ParseModulePrincipalRegistry validates the uuid's form only), so a grant
// naming the declared id would mint capabilities bound to an organization that
// is not there, silently. The seed must fail instead.
func TestFixtureSeedRefusesWhenDeclaredOrganizationIDDrifts(t *testing.T) {
	clearData(t)
	ctx := testCtx
	fixturePath := filepath.Join(t.TempDir(), "drifting-org-ids.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)

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
`, pinned)
		require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))
	}

	const seededID = "00000000-0000-7000-8000-00000000f201"
	writeFixture(seededID)
	require.NoError(t, fixtures.Seed(ctx, testService, "drifting-org-ids"))
	owner := seededUUIDFor(t, ctx, "fixture-org-owner")
	require.Equal(t, seededID, seededOrgIDFor(t, ctx, owner, "Pinned Org"))

	const redeclaredID = "00000000-0000-7000-8000-00000000f202"
	writeFixture(redeclaredID)
	err := fixtures.Seed(ctx, testService, "drifting-org-ids")
	require.Error(t, err, "a declared organization id that the database cannot adopt must fail the seed, not boot with a tenant nothing can resolve")
	require.Contains(t, err.Error(), redeclaredID)
	require.Contains(t, err.Error(), seededID)

	// The stored organization is untouched: refusing is not a partial write.
	require.Equal(t, seededID, seededOrgIDFor(t, ctx, owner, "Pinned Org"))
}

// A refusal must land before the seed writes anything. seedUsers commits each
// user in its own transaction and deliberately skips personal-org creation
// (otherwise fixture users land in two orgs and ensureOrg picks the wrong one),
// so a refusal raised once organizations are being seeded would leave the new
// fixture users belonging to no organization at all.
func TestFixtureSeedRefusesBeforeWritingAnyUser(t *testing.T) {
	clearData(t)
	ctx := testCtx
	fixturePath := filepath.Join(t.TempDir(), "preflight-org-ids.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)

	writeFixture := func(pinned, extraUser string) {
		t.Helper()
		contents := fmt.Sprintf(`users:
  - email: owner@fixture.test
    provider: email
    provider_id: fixture-org-owner
%sorganizations:
  - id: %s
    name: Pinned Org
    owner: owner@fixture.test
`, extraUser, pinned)
		require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))
	}

	writeFixture("00000000-0000-7000-8000-00000000f301", "")
	require.NoError(t, fixtures.Seed(ctx, testService, "preflight-org-ids"))

	// Redeclare the organization's id AND introduce a new user in the same
	// edit. The seed must refuse, and the new user must not have been created.
	writeFixture("00000000-0000-7000-8000-00000000f302", `  - email: newcomer@fixture.test
    provider: email
    provider_id: fixture-org-newcomer
`)
	require.Error(t, fixtures.Seed(ctx, testService, "preflight-org-ids"))

	var newcomer *gen.User
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		newcomer, err = testStore.GetUserByIdentity(ctx, &gen.UserIdentity{
			Provider: "email", ProviderId: "fixture-org-newcomer",
		})
		return err
	}))
	require.Nil(t, newcomer,
		"the seed refused, so it must not have written a user first — a user seeded here would belong to no organization")
}

// Another organization already holds the declared id. The seeder must refuse
// rather than mint a fresh uuid: for a fixture's own organization the holder is
// the same organization under an owner who is no longer a member, so seeding a
// second one of that name collides on the unique organization slug — and where
// it would not collide (the holder was renamed), a grant naming the declared id
// resolves to the renamed organization instead of this one.
func TestFixtureSeedRefusesWhenDeclaredOrganizationIDIsTaken(t *testing.T) {
	clearData(t)
	ctx := testCtx
	fixturePath := filepath.Join(t.TempDir(), "conflicting-org-ids.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)

	const sharedID = "00000000-0000-7000-8000-00000000f103"
	writeOrg := func(ownerProviderID, name string) {
		t.Helper()
		contents := fmt.Sprintf(`users:
  - email: %s@fixture.test
    provider: email
    provider_id: %s
organizations:
  - id: %s
    name: %s
    owner: %s@fixture.test
`, ownerProviderID, ownerProviderID, sharedID, name, ownerProviderID)
		require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))
	}

	writeOrg("fixture-org-owner", "First Org")
	require.NoError(t, fixtures.Seed(ctx, testService, "conflicting-org-ids"))
	owner := seededUUIDFor(t, ctx, "fixture-org-owner")
	require.Equal(t, sharedID, seededOrgIDFor(t, ctx, owner, "First Org"))

	// A different owner, so the membership-scoped name lookup cannot find the
	// holder — this is the path that used to "fall back" to a fresh uuid.
	writeOrg("fixture-org-other", "Second Org")
	err := fixtures.Seed(ctx, testService, "conflicting-org-ids")
	require.Error(t, err, "a declared organization id another organization holds must fail the seed")
	require.Contains(t, err.Error(), sharedID)

	// The same shape with the SAME name is the realistic one for a fixture's
	// own organization, and is where a fallback to a fresh uuid could never
	// have worked: idx_organizations_slug is UNIQUE on LOWER(slug) across the
	// whole table, so the insert would have failed with an opaque duplicate-key
	// error. The seeder must report the declared id, not the slug collision.
	writeOrg("fixture-org-other", "First Org")
	err = fixtures.Seed(ctx, testService, "conflicting-org-ids")
	require.Error(t, err, "a same-named organization cannot fall back to a fresh uuid: the slug is globally unique")
	require.Contains(t, err.Error(), sharedID)
	require.NotContains(t, err.Error(), "idx_organizations_slug",
		"the seeder should refuse on the declared id, not let an opaque unique-violation surface")
}

// CreateFixtureOrganization takes a primary key as an argument and sits in the
// exported API next to CreateOrganization, so it validates the id itself rather
// than trusting the seeder to have done it. A tenant id that reached the
// database malformed is not recoverable by anything downstream.
func TestCreateFixtureOrganizationRejectsUnusableIDs(t *testing.T) {
	clearData(t)
	ctx := testCtx

	registered, err := testService.RegisterUser(ctx, &gen.RegisterUserRequest{
		PrimaryEmail: "fixture-org-guard@test.com",
		Identity: &gen.UserIdentity{
			Provider:   "email",
			ProviderId: "fixture-org-guard",
		},
	})
	require.NoError(t, err)
	owner := registered.GetUser().GetUuid()

	for name, id := range map[string]string{
		"malformed":    "acme",
		"too short":    "00000000-0000-7000-8000-0000000000b",
		"nil sentinel": "00000000-0000-0000-0000-000000000000",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := testService.CreateFixtureOrganization(ctx, owner,
				&gen.CreateOrganizationRequest{Name: "Guard " + name}, id)
			require.Error(t, err, "CreateFixtureOrganization accepted an id that cannot name a tenant")
		})
	}

	// uuid.Parse also accepts the urn, braced and unhyphenated spellings of a
	// real uuid. Those are not unusable — they denote the same id — so the
	// guard canonicalizes rather than rejects, and what reaches the database
	// is the dashed form a MODULE_PRINCIPALS grant would quote. (The fixture
	// loader is stricter and rejects them outright, because there the spelling
	// is something a human wrote and should fix.)
	for name, spelling := range map[string]struct{ given, want string }{
		"urn spelling": {
			given: "urn:uuid:00000000-0000-7000-8000-0000000000d1",
			want:  "00000000-0000-7000-8000-0000000000d1",
		},
		"unhyphenated spelling": {
			given: "000000000000700080000000000000d2",
			want:  "00000000-0000-7000-8000-0000000000d2",
		},
		"uppercase spelling": {
			given: "00000000-0000-7000-8000-0000000000D3",
			want:  "00000000-0000-7000-8000-0000000000d3",
		},
	} {
		t.Run(name, func(t *testing.T) {
			created, err := testService.CreateFixtureOrganization(ctx, owner,
				&gen.CreateOrganizationRequest{Name: "Canonical " + name}, spelling.given)
			require.NoError(t, err)
			require.Equal(t, spelling.want, created.GetOrganization().GetId(),
				"a declared id must reach the database in the canonical dashed form")
		})
	}
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
