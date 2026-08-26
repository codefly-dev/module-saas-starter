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
			{Name: "Mind AI", Owner: "owner@example.com"},
			{Name: "mind-ai", Owner: "owner@example.com"},
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
	t.Cleanup(func() { os.Chdir(orig) })

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
	t.Cleanup(func() { os.RemoveAll(dir) })

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
	t.Cleanup(func() { os.Chdir(orig) })

	t.Setenv("CODEFLY__FIXTURE", "")
	name, err := SelectedName()
	if err != nil {
		t.Fatalf("SelectedName() with no fixture selected: %v", err)
	}
	if name != "" {
		t.Fatalf("SelectedName() = %q, want empty", name)
	}
}
