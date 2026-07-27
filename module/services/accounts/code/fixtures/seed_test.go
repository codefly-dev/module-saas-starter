package fixtures

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
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
