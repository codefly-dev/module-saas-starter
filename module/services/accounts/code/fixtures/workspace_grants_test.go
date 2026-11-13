package fixtures

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/resources"
	codefly "github.com/codefly-dev/sdk-go"
	"gopkg.in/yaml.v3"
)

func grantsReader(content string, err error) func(string) (json.RawMessage, error) {
	return func(name string) (json.RawMessage, error) {
		if name != WorkspaceGrantsDocument {
			return nil, codefly.ErrConfigurationDocumentMissing
		}
		return json.RawMessage(content), err
	}
}

func baseFixture() *fixtureFile {
	return &fixtureFile{
		Users:         []fixtureUser{{Email: "remi@acme.test", Provider: "email", ProviderID: "fixture-remi"}},
		Organizations: []fixtureOrg{{Name: "Acme Corp", Owner: "remi@acme.test"}},
	}
}

// A composing workspace states the roles its people hold at the modules it
// composes; they join the selected fixture's roles and assignments.
func TestWorkspaceGrantsJoinTheFixture(t *testing.T) {
	grants, err := readWorkspaceGrants(grantsReader(`{"roles":[{"org":"Acme Corp","name":"Notes editor","permissions":[{"resource":"documents","action":"read"}]}],"assignments":[{"org":"Acme Corp","role":"Notes editor","email":"remi@acme.test"}]}`, nil))
	if err != nil {
		t.Fatal(err)
	}
	f := baseFixture()
	if err := withWorkspaceGrants(f, grants); err != nil {
		t.Fatal(err)
	}
	if len(f.Roles) != 1 || f.Roles[0].Permissions[0].Resource != "documents" || len(f.Assignments) != 1 || f.Assignments[0].Email != "remi@acme.test" {
		t.Fatalf("grants did not join the fixture: roles=%+v assignments=%+v", f.Roles, f.Assignments)
	}
}

// No document is no grants: every composition that states none seeds the
// module's fixture unchanged.
func TestNoWorkspaceGrantsLeavesTheFixture(t *testing.T) {
	grants, err := readWorkspaceGrants(grantsReader("", codefly.ErrConfigurationDocumentMissing))
	if err != nil || grants != nil {
		t.Fatalf("grants=%+v err=%v", grants, err)
	}
	f := baseFixture()
	if err := withWorkspaceGrants(f, grants); err != nil || len(f.Roles) != 0 {
		t.Fatalf("roles=%+v err=%v", f.Roles, err)
	}
}

// A document that is there and does not read, or states an unknown field or an
// incomplete role, is refused by name: its workspace believes its people hold it.
func TestUnreadableWorkspaceGrantsAreRefused(t *testing.T) {
	if _, err := readWorkspaceGrants(grantsReader("", errors.New("carrier unreadable"))); err == nil || !strings.Contains(err.Error(), WorkspaceGrantsDocument) {
		t.Fatalf("an unreadable document was not refused by name: %v", err)
	}
	if _, err := readWorkspaceGrants(grantsReader(`{"people":[]}`, nil)); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	grants, err := readWorkspaceGrants(grantsReader(`{"roles":[{"org":"Acme Corp","name":"Empty"}]}`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := withWorkspaceGrants(baseFixture(), grants); err == nil || !strings.Contains(err.Error(), "at least one permission") {
		t.Fatalf("a role with no permission was accepted: %v", err)
	}
}

// A composing workspace states the installations its organisations hold of
// what it composes; an owner the fixture does not make an administrator of the
// organisation, or a field left out, is refused at seed, by name.
func TestWorkspaceInstallationsJoinTheFixture(t *testing.T) {
	f := baseFixture()
	f.Users = append(f.Users, fixtureUser{Email: "omar@acme.test", Provider: "email", ProviderID: "fixture-omar"})
	f.Organizations[0].Members = []fixtureOrgMember{{Email: "omar@acme.test", Role: "member"}}

	// A row names a role where teams are to hold one; a target that holds
	// none (a module declaring `installation_role: none`, or no role at all)
	// is declared without one and installed holding none.
	grants, err := readWorkspaceGrants(grantsReader(`installations:
  - {org: Acme Corp, target: notes, role: viewer, owner: remi@acme.test}
  - {org: Acme Corp, target: records, owner: remi@acme.test}
`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := withWorkspaceGrants(f, grants); err != nil {
		t.Fatal(err)
	}
	if len(f.Installations) != 2 || f.Installations[0].Target != "notes" || f.Installations[0].Owner != "remi@acme.test" ||
		f.Installations[1].Target != "records" || f.Installations[1].Role != "" {
		t.Fatalf("installations did not join the fixture: %+v", f.Installations)
	}

	for document, want := range map[string]string{
		`installations: [{org: Acme Corp, target: notes, role: viewer, owner: omar@acme.test}]`:                            "only an administrator installs",
		`installations: [{org: Globex, target: notes, role: viewer, owner: remi@acme.test}]`:                               "only an administrator installs",
		`installations: [{org: Acme Corp, target: notes, role: viewer}]`:                                                   "org, target and owner are required",
		`installations: [{org: Acme Corp, target: notes, owner: remi@acme.test, teams: [Ops]}]`:                            "a row naming teams names a role",
		`installations: [{org: Acme Corp, target: notes, owner: remi@acme.test, members: {viewer: [stranger@acme.test]}}]`: "only a member joins its team",
		`installations: [{org: Acme Corp, target: notes, owner: remi@acme.test, members: {viewer: []}}]`:                   "members name a role and the people who take it",
	} {
		grants, err := readWorkspaceGrants(grantsReader(document, nil))
		if err != nil {
			t.Fatal(err)
		}
		f := baseFixture()
		f.Users = append(f.Users, fixtureUser{Email: "omar@acme.test", Provider: "email", ProviderID: "fixture-omar"})
		f.Organizations[0].Members = []fixtureOrgMember{{Email: "omar@acme.test", Role: "member"}}
		if err := withWorkspaceGrants(f, grants); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want a refusal naming %q, got %v", document, want, err)
		}
	}
}

// A row installs as an administrator does: the roles the installer takes, and
// by declared role the people who join that role's team, each a member of the
// organisation.
func TestWorkspaceInstallationsDeclareInstallerRolesAndMembers(t *testing.T) {
	f := baseFixture()
	f.Users = append(f.Users, fixtureUser{Email: "omar@acme.test", Provider: "email", ProviderID: "fixture-omar"})
	f.Organizations[0].Members = []fixtureOrgMember{{Email: "omar@acme.test", Role: "member"}}
	grants, err := readWorkspaceGrants(grantsReader(`installations:
  - org: Acme Corp
    target: notes
    owner: remi@acme.test
    installer_roles: [installer]
    members: {editor: [omar@acme.test], viewer: [remi@acme.test]}
`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := withWorkspaceGrants(f, grants); err != nil {
		t.Fatal(err)
	}
	row := f.Installations[0]
	if len(row.InstallerRoles) != 1 || row.InstallerRoles[0] != "installer" || len(row.Members["editor"]) != 1 || row.Members["viewer"][0] != "remi@acme.test" {
		t.Fatalf("the row did not read: %+v", row)
	}
	if _, err := readWorkspaceGrants(grantsReader(`installations: [{org: Acme Corp, target: notes, owner: remi@acme.test, member: {viewer: [omar@acme.test]}}]`, nil)); err == nil {
		t.Fatal("a misspelled field was accepted")
	}
}

// The access a row asks an install for is the admin area's: the first team
// named for the row's role (an install names one team per role; a further
// team is granted the role in Set up), and the roles the installer takes.
func TestARowAsksTheInstallForTheAdministratorsAccess(t *testing.T) {
	row := pendingInstallation{declared: fixtureInstallation{Role: "viewer", InstallerRoles: []string{"installer"}}}
	if access := installAccess(row); access.RoleTeams != nil || len(access.InstallerRoles) != 1 {
		t.Fatalf("no team named: every role goes to its conventional team, got %+v", access)
	}
	row.allTeamIDs = []string{"finance", "engineering"}
	access := installAccess(row)
	if len(access.RoleTeams) != 1 || access.RoleTeams["viewer"] != "finance" {
		t.Fatalf("the first named team holds the row's role at install, got %+v", access)
	}
}

// Exercise the real seed entry point with an unavailable service: every duplicate
// must be refused during loading/combined validation, before a store is touched.
func TestSeedRejectsDuplicateGrantIdentitiesBeforeStoreAccess(t *testing.T) {
	role := fixtureRole{Org: "Acme Corp", Name: "Reader", Permissions: []fixturePermission{{Resource: "notes", Action: "read"}}}
	changedRole := role
	changedRole.Permissions = []fixturePermission{{Resource: "notes", Action: "write"}}
	assignment := fixtureRoleAssignment{Org: "Acme Corp", Role: "Reader", Email: "remi@acme.test"}
	agentAssignment := fixtureRoleAssignment{Org: "Acme Corp", Role: "Reader", AgentIdentifier: "example-agent"}
	installation := fixtureInstallation{Org: "Acme Corp", Target: "notes", Role: "viewer", Owner: "remi@acme.test"}
	changedInstallation := installation
	changedInstallation.Role = "editor"
	teamInstallation := installation
	teamInstallation.Teams = []string{"Engineering"}
	ownerInstallation := installation
	ownerInstallation.Owner = "admin@example.com"
	cases := []struct {
		name, kind    string
		first, second workspaceGrants
	}{
		{"identical role", "role", workspaceGrants{Roles: []fixtureRole{role}}, workspaceGrants{Roles: []fixtureRole{role}}},
		{"conflicting role", "role", workspaceGrants{Roles: []fixtureRole{role}}, workspaceGrants{Roles: []fixtureRole{changedRole}}},
		{"person assignment", "assignment", workspaceGrants{Assignments: []fixtureRoleAssignment{assignment}}, workspaceGrants{Assignments: []fixtureRoleAssignment{assignment}}},
		{"agent assignment", "assignment", workspaceGrants{Assignments: []fixtureRoleAssignment{agentAssignment}}, workspaceGrants{Assignments: []fixtureRoleAssignment{agentAssignment}}},
		{"identical installation", "installation", workspaceGrants{Installations: []fixtureInstallation{installation}}, workspaceGrants{Installations: []fixtureInstallation{installation}}},
		{"conflicting installation role", "installation", workspaceGrants{Installations: []fixtureInstallation{installation}}, workspaceGrants{Installations: []fixtureInstallation{changedInstallation}}},
		{"conflicting installation teams", "installation", workspaceGrants{Installations: []fixtureInstallation{installation}}, workspaceGrants{Installations: []fixtureInstallation{teamInstallation}}},
		{"conflicting installation owner", "installation", workspaceGrants{Installations: []fixtureInstallation{installation}}, workspaceGrants{Installations: []fixtureInstallation{ownerInstallation}}},
	}
	for _, tc := range cases {
		for _, location := range []string{"canonical", "workspace", "across"} {
			t.Run(tc.name+"/"+location, func(t *testing.T) {
				f := baseFixture()
				f.Users = append(f.Users, fixtureUser{Email: "admin@example.com", Provider: "email", ProviderID: "example-admin"})
				f.Organizations[0].Members = []fixtureOrgMember{{Email: "admin@example.com", Role: "admin"}}
				var grants workspaceGrants
				switch location {
				case "canonical":
					f.Roles = append(tc.first.Roles, tc.second.Roles...)
					f.Assignments = append(tc.first.Assignments, tc.second.Assignments...)
					f.Installations = append(tc.first.Installations, tc.second.Installations...)
				case "workspace":
					grants.Roles = append(tc.first.Roles, tc.second.Roles...)
					grants.Assignments = append(tc.first.Assignments, tc.second.Assignments...)
					grants.Installations = append(tc.first.Installations, tc.second.Installations...)
				case "across":
					f.Roles, f.Assignments, f.Installations = tc.first.Roles, tc.first.Assignments, tc.first.Installations
					grants = tc.second
				}
				data, err := yaml.Marshal(f)
				if err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(t.TempDir(), "duplicates.yaml")
				if err := os.WriteFile(file, data, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("DEV_FIXTURE_PATH", file)
				t.Setenv(resources.EnvironmentPrefix, "fixture-validation")
				content, err := yaml.Marshal(grants)
				if err != nil {
					t.Fatal(err)
				}
				if err := codefly.InjectConfigurations(&basev0.Configuration{Origin: resources.ConfigurationWorkspace, Infos: []*basev0.ConfigurationInformation{{Name: WorkspaceGrantsDocument, Data: &basev0.ConfigurationData{Kind: "yaml", Content: content}}}}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := codefly.InjectConfigurations(); err != nil {
						t.Error(err)
					}
				})
				_, err = SeedWithInstallations(context.Background(), nil, "duplicates")
				if err == nil || !strings.Contains(err.Error(), tc.kind+"[1]: duplicate identity") || !strings.Contains(err.Error(), "of "+tc.kind+"[0]") {
					t.Fatalf("want deterministic %s duplicate refusal before store access, got %v", tc.kind, err)
				}
			})
		}
	}
}

func TestWorkspaceGrantsKeepDistinctLookupIdentities(t *testing.T) {
	f := baseFixture()
	f.Users = append(f.Users, fixtureUser{Email: "jane@example.com", Provider: "email", ProviderID: "example-jane"})
	f.Organizations[0].Members = []fixtureOrgMember{{Email: "jane@example.com", Role: "member"}}
	f.Organizations = append(f.Organizations, fixtureOrg{Name: "Globex", Owner: "remi@acme.test"})
	f.Agents = []fixtureAgent{{Org: "Acme Corp", AgentIdentifier: "remi@acme.test", CreatedBy: "remi@acme.test"}}
	f.Roles = []fixtureRole{{Org: "Acme Corp", Name: "Reader", Permissions: []fixturePermission{{Resource: "notes", Action: "read"}}}}
	f.Assignments = []fixtureRoleAssignment{{Org: "Acme Corp", Role: "Reader", Email: "remi@acme.test"}}
	f.Installations = []fixtureInstallation{{Org: "Acme Corp", Target: "notes", Role: "viewer", Owner: "remi@acme.test"}}
	grants := &workspaceGrants{
		Roles: []fixtureRole{
			{Org: "Globex", Name: "Reader", Permissions: []fixturePermission{{Resource: "notes", Action: "read"}}},
			{Org: "Acme Corp", Name: "reader", Permissions: []fixturePermission{{Resource: "notes", Action: "read"}}},
		},
		Assignments: []fixtureRoleAssignment{
			{Org: "Globex", Role: "Reader", Email: "remi@acme.test"},
			{Org: "Acme Corp", Role: "Reader", Email: "remi@acme.test", Scope: "collection-a"},
			{Org: "Acme Corp", Role: "Reader", Email: "jane@example.com"},
			{Org: "Acme Corp", Role: "Reader", AgentIdentifier: "remi@acme.test"},
			{Org: "Acme Corp", Role: "reader", Email: "remi@acme.test"},
		},
		Installations: []fixtureInstallation{
			{Org: "Globex", Target: "notes", Role: "viewer", Owner: "remi@acme.test"},
			{Org: "Acme Corp", Target: "other-notes", Role: "viewer", Owner: "remi@acme.test"},
		},
	}
	if err := withWorkspaceGrants(f, grants); err != nil {
		t.Fatal(err)
	}
	if len(f.Roles) != 3 || len(f.Assignments) != 6 || len(f.Installations) != 3 {
		t.Fatal("distinct keys were lost from combined fixture")
	}
}

// A row standing for every target (`target: "*"`) seeds the same people onto
// every role-gated target the composition delivers, naming none: it carries
// members only, and is expanded per target to the roles that target declares.
func TestARowMayStandForEveryTarget(t *testing.T) {
	f := baseFixture()
	f.Users = append(f.Users, fixtureUser{Email: "omar@acme.test", Provider: "email", ProviderID: "fixture-omar"})
	f.Organizations[0].Members = []fixtureOrgMember{{Email: "omar@acme.test", Role: "member"}}
	grants, err := readWorkspaceGrants(grantsReader(`installations:
  - org: Acme Corp
    target: "*"
    owner: remi@acme.test
    members: {viewer: [omar@acme.test], editor: [remi@acme.test]}
`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := withWorkspaceGrants(f, grants); err != nil {
		t.Fatal(err)
	}
	if f.Installations[0].Target != EveryTarget {
		t.Fatalf("the row did not read: %+v", f.Installations[0])
	}
	for document, want := range map[string]string{
		`installations: [{org: Acme Corp, target: "*", role: viewer, owner: remi@acme.test}]`:              "names only its members",
		`installations: [{org: Acme Corp, target: "*", owner: remi@acme.test, installer_roles: [viewer]}]`: "names only its members",
	} {
		grants, err := readWorkspaceGrants(grantsReader(document, nil))
		if err != nil {
			t.Fatal(err)
		}
		if err := withWorkspaceGrants(baseFixture(), grants); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want a refusal naming %q, got %v", document, want, err)
		}
	}

	template := pendingInstallation{
		declared: fixtureInstallation{Org: "Acme Corp", Target: EveryTarget, Owner: "remi@acme.test"},
		orgID:    "org", ownerID: "remi",
		memberIDs: map[string][]string{"viewer": {"omar"}, "editor": {"remi"}, "installer": {"remi"}},
	}
	row := everyRow(template, domain.Module{Name: "notes", Kind: domain.KindSolution, Roles: map[string][]*basev0.WorkScopeV1{"viewer": nil, "editor": nil}})
	if row.declared.Target != "notes" || row.declared.Org != "Acme Corp" || row.ownerID != "remi" {
		t.Fatalf("the row is not the template's for the target: %+v", row)
	}
	if len(row.memberIDs) != 2 || row.memberIDs["viewer"][0] != "omar" || row.memberIDs["editor"][0] != "remi" {
		t.Fatalf("members are cut to the roles the target declares: %+v", row.memberIDs)
	}
	if len(template.memberIDs) != 3 {
		t.Fatal("the template is left whole for the next target")
	}
}

// THE FIXTURE seats someone on every role a target declares: Bob on viewer,
// Carol on editor and on user (the role that uses a product without authoring
// it, which the editor holds all of: core-solutions' wiki and robin-builder),
// Dana on installer. A target declaring `user` (wiki, Robin Builder) reads
// "user: nobody" otherwise. Red before the fixture named a user member.
func TestTheOneFixtureSeatsSomeoneOnEveryStandardRole(t *testing.T) {
	data, err := embeddedFixtures.ReadFile("embedded/dev-admin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtureFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if err := validateInstallations(&f); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"viewer": {"bob@acme.com"}, "editor": {"carol@acme.com"}, "user": {"carol@acme.com"}, "installer": {"dana@example.com"}}
	for _, row := range f.Installations {
		if row.Target != EveryTarget {
			continue
		}
		for role, people := range want {
			if strings.Join(row.Members[role], ",") != strings.Join(people, ",") {
				t.Errorf("%s: members %v, want %v", role, row.Members[role], people)
			}
		}
		return
	}
	t.Fatal("dev-admin has no row standing for every target")
}
