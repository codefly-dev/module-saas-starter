package fixtures

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	"bytes"
	"context"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedRejectsUnsafeFixtureName(t *testing.T) {
	err := Seed(context.Background(), nil, "../outside")
	if err == nil {
		t.Fatal("Seed() accepted a path-traversing fixture name")
	}
}

func TestSelectedNameUsesCodeflySDKAcrossAvailableFixtures(t *testing.T) {
	t.Setenv("CODEFLY__FIXTURE", "dev-admin")
	name, err := SelectedName()
	if err != nil {
		t.Fatal(err)
	}
	if name != "dev-admin" {
		t.Fatalf("SelectedName() = %q, want dev-admin", name)
	}
}

func TestSelectedNameRejectsUnknownCodeflyFixture(t *testing.T) {
	t.Setenv("CODEFLY__FIXTURE", "not-installed")
	if _, err := SelectedName(); err == nil {
		t.Fatal("SelectedName() accepted a fixture without a module YAML definition")
	}
}

func TestFixtureNamePatternAcceptsProductFixtureNames(t *testing.T) {
	for _, name := range []string{"simple", "dev-admin", "codefly_local-1"} {
		if !fixtureNamePattern.MatchString(name) {
			t.Fatalf("fixtureNamePattern rejected %q", name)
		}
	}
}

func TestLoadFixtureRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.yaml")
	if err := os.WriteFile(path, []byte("users: []\nraw_environment: SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFixtureFile(path); err == nil || !strings.Contains(err.Error(), "raw_environment") {
		t.Fatalf("loadFixtureFile() error = %v, want unknown field rejection", err)
	}
}

func TestLoadFixtureAcceptsDevelopmentAssuranceField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.yaml")
	contents := "users:\n  - email: owner@example.com\n    provider: email\n    provider_id: owner\n    mfa_verified: true\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture, err := loadFixtureFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.Users) != 1 || !fixture.Users[0].MFAVerified {
		t.Fatalf("loadFixtureFile() users = %+v, want one MFA-verified user", fixture.Users)
	}
}

func TestLoadFixtureCanonicalizesDeclaredUserID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.yaml")
	contents := "users:\n  - id: 0000000A-0000-7000-8000-0000000000A1\n    email: owner@example.com\n    provider: email\n    provider_id: owner\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture, err := loadFixtureFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fixture.Users[0].ID; got != "0000000a-0000-7000-8000-0000000000a1" {
		t.Fatalf("loadFixtureFile() user id = %q, want the canonical uuid rendering", got)
	}
}

func TestValidateFixtureRejectsUnusableUserIDs(t *testing.T) {
	tests := map[string][]fixtureUser{
		"malformed": {
			{ID: "dev-admin", Email: "owner@example.com", Provider: "email", ProviderID: "owner"},
		},
		"collision": {
			{ID: "00000000-0000-7000-8000-0000000000a1", Email: "owner@example.com", Provider: "email", ProviderID: "owner"},
			{ID: "00000000-0000-7000-8000-0000000000A1", Email: "member@example.com", Provider: "email", ProviderID: "member"},
		},
		"nil sentinel": {
			{ID: "00000000-0000-0000-0000-000000000000", Email: "owner@example.com", Provider: "email", ProviderID: "owner"},
		},
		// Spellings uuid.Parse accepts but the frontend's fixture schema does not.
		"urn spelling": {
			{ID: "urn:uuid:00000000-0000-7000-8000-0000000000a1", Email: "owner@example.com", Provider: "email", ProviderID: "owner"},
		},
		"unhyphenated spelling": {
			{ID: "000000000000700080000000000000a1", Email: "owner@example.com", Provider: "email", ProviderID: "owner"},
		},
		"braced spelling": {
			{ID: "{00000000-0000-7000-8000-0000000000a1}", Email: "owner@example.com", Provider: "email", ProviderID: "owner"},
		},
	}
	for name, users := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateFixture(&fixtureFile{Users: users}); err == nil {
				t.Fatal("validateFixture() accepted a user id that cannot name a principal")
			}
		})
	}
}

// Module fixtures are quoted by committed configuration — seeded grants, e2e
// specs, runbooks — so a user without a declared id silently re-mints its
// principal on every fresh seed.
func TestModuleFixtureUsersDeclareStableIDs(t *testing.T) {
	entries, err := embeddedFixtures.ReadDir("embedded")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			fixture, err := loadFixtureFile(filepath.Join("..", "..", "..", "..", "fixtures", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range fixture.Users {
				if u.ID == "" {
					t.Fatalf("fixture user %s declares no id", u.Email)
				}
			}
		})
	}
}

func TestLoadFixtureCanonicalizesDeclaredOrganizationID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.yaml")
	contents := "users:\n  - email: owner@example.com\n    provider: email\n    provider_id: owner\n" +
		"organizations:\n  - id: 0000000A-0000-7000-8000-0000000000B1\n    name: Example\n    owner: owner@example.com\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture, err := loadFixtureFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fixture.Organizations[0].ID; got != "0000000a-0000-7000-8000-0000000000b1" {
		t.Fatalf("loadFixtureFile() organization id = %q, want the canonical uuid rendering", got)
	}
}

// An organization id is sealed as the tenant of a module principal's
// capability and compared against organization ids downstream, so the same
// spellings that cannot name a user cannot name a tenant.
func TestValidateFixtureRejectsUnusableOrganizationIDs(t *testing.T) {
	owner := fixtureUser{Email: "owner@example.com", Provider: "email", ProviderID: "owner"}
	tests := map[string][]fixtureOrg{
		"malformed": {
			{ID: "acme", Name: "Acme", Owner: owner.Email},
		},
		"collision": {
			{ID: "00000000-0000-7000-8000-0000000000b1", Name: "Acme", Owner: owner.Email},
			{ID: "00000000-0000-7000-8000-0000000000B1", Name: "Globex", Owner: owner.Email},
		},
		"nil sentinel": {
			{ID: "00000000-0000-0000-0000-000000000000", Name: "Acme", Owner: owner.Email},
		},
		"urn spelling": {
			{ID: "urn:uuid:00000000-0000-7000-8000-0000000000b1", Name: "Acme", Owner: owner.Email},
		},
		"unhyphenated spelling": {
			{ID: "000000000000700080000000000000b1", Name: "Acme", Owner: owner.Email},
		},
		"braced spelling": {
			{ID: "{00000000-0000-7000-8000-0000000000b1}", Name: "Acme", Owner: owner.Email},
		},
	}
	for name, orgs := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateFixture(&fixtureFile{Users: []fixtureUser{owner}, Organizations: orgs}); err == nil {
				t.Fatal("validateFixture() accepted an organization id that cannot name a tenant")
			}
		})
	}
}

// A module principal grant (MODULE_PRINCIPALS) names its tenant by organization
// id, so a module fixture organization without a declared id leaves every
// composed grant nothing stable to quote.
func TestModuleFixtureOrganizationsDeclareStableIDs(t *testing.T) {
	entries, err := embeddedFixtures.ReadDir("embedded")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			fixture, err := loadFixtureFile(filepath.Join("..", "..", "..", "..", "fixtures", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, org := range fixture.Organizations {
				if org.ID == "" {
					t.Fatalf("fixture organization %s declares no id", org.Name)
				}
			}
		})
	}
}

func TestValidateFixtureAcceptsAgentRoleAndAssignment(t *testing.T) {
	fixture := &fixtureFile{
		Users: []fixtureUser{{
			Email:      "owner@example.com",
			Provider:   "dev",
			ProviderID: "owner",
		}},
		Organizations: []fixtureOrg{{
			Name:  "Example",
			Owner: "owner@example.com",
		}},
		Agents: []fixtureAgent{{
			Org:             "Example",
			AgentIdentifier: "example/agent:local",
			CreatedBy:       "owner@example.com",
		}},
		Roles: []fixtureRole{{
			Org:  "Example",
			Name: "executor",
			Permissions: []fixturePermission{{
				Resource: "build",
				Action:   "run",
			}},
		}},
		Assignments: []fixtureRoleAssignment{{
			Org:             "Example",
			Role:            "executor",
			AgentIdentifier: "example/agent:local",
		}},
	}

	if err := validateFixture(fixture); err != nil {
		t.Fatalf("validateFixture() rejected generic agent authority: %v", err)
	}
}

func TestValidateFixtureRejectsIncompleteAgentAuthority(t *testing.T) {
	tests := map[string]*fixtureFile{
		"agent creator": {
			Agents: []fixtureAgent{{Org: "Example", AgentIdentifier: "example/agent:local"}},
		},
		"empty role permissions": {
			Roles: []fixtureRole{{Org: "Example", Name: "executor"}},
		},
		"incomplete assignment": {
			Assignments: []fixtureRoleAssignment{{Org: "Example", Role: "executor"}},
		},
	}
	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateFixture(fixture); err == nil {
				t.Fatal("validateFixture() accepted incomplete agent authority")
			}
		})
	}
}

func TestValidateFixtureRejectsUnsafeOrganizationSlugs(t *testing.T) {
	tests := map[string][]fixtureOrg{
		"empty": {
			{Name: "!!!", Owner: "owner@example.com"},
		},
		"collision": {
			{Name: "Example AI", Owner: "owner@example.com"},
			{Name: "example-ai", Owner: "owner@example.com"},
		},
	}
	for name, organizations := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateFixture(&fixtureFile{Organizations: organizations}); err == nil {
				t.Fatal("validateFixture() accepted unsafe organization slugs")
			}
		})
	}
}

func TestSameFixturePermissionsIsOrderIndependent(t *testing.T) {
	left := []*gen.Permission{
		{Resource: "build", Action: "read"},
		{Resource: "build", Action: "run"},
	}
	right := []*gen.Permission{
		{Resource: "build", Action: "run"},
		{Resource: "build", Action: "read"},
	}
	if !sameFixturePermissions(left, right) {
		t.Fatal("sameFixturePermissions() treated an ordering change as authority drift")
	}
	right[0].Action = "delete"
	if sameFixturePermissions(left, right) {
		t.Fatal("sameFixturePermissions() accepted different authority")
	}
}

func TestEmbeddedFixturesMatchModuleFixtures(t *testing.T) {
	entries, err := embeddedFixtures.ReadDir("embedded")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no fixtures embedded in the binary")
	}
	for _, entry := range entries {
		got, err := embeddedFixtures.ReadFile(path.Join("embedded", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "fixtures", entry.Name()))
		if err != nil {
			t.Fatalf("read module fixture %q: %v", entry.Name(), err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("embedded fixture %q drifted from module/fixtures/%s; re-copy the module fixture", entry.Name(), entry.Name())
		}
	}
}

func TestSelectedNameFallsBackToEmbeddedFixtures(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	t.Setenv("CODEFLY__FIXTURE", "dev-admin")
	name, err := SelectedName()
	if err != nil {
		t.Fatalf("SelectedName() with no on-disk fixtures dir: %v", err)
	}
	if name != "dev-admin" {
		t.Fatalf("SelectedName() = %q, want dev-admin", name)
	}

	fixturePath, err := FixturePath("dev-admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fixturePath); err != nil {
		t.Fatalf("embedded fixture was not materialized on disk: %v", err)
	}
}

func TestEmbeddedFixtureFallbackIsAnnounced(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	dir, err := writeEmbeddedFixtures()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if got := buf.String(); !strings.Contains(got, "using fixtures embedded in the binary") {
		t.Fatalf("embedded fixture fallback was served silently; expected a warning, log was: %q", got)
	}
}

func TestSelectedNameEmptyWithoutSelectionSkipsDirectory(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	t.Setenv("CODEFLY__FIXTURE", "")
	name, err := SelectedName()
	if err != nil {
		t.Fatalf("SelectedName() with no fixture selected: %v", err)
	}
	if name != "" {
		t.Fatalf("SelectedName() = %q, want empty", name)
	}
}
