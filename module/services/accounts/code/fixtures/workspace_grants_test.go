package fixtures

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	codefly "github.com/codefly-dev/sdk-go"
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

	grants, err := readWorkspaceGrants(grantsReader(`installations:
  - {org: Acme Corp, target: notes, role: viewer, owner: remi@acme.test}
`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := withWorkspaceGrants(f, grants); err != nil {
		t.Fatal(err)
	}
	if len(f.Installations) != 1 || f.Installations[0].Target != "notes" || f.Installations[0].Owner != "remi@acme.test" {
		t.Fatalf("installations did not join the fixture: %+v", f.Installations)
	}

	for document, want := range map[string]string{
		`installations: [{org: Acme Corp, target: notes, role: viewer, owner: omar@acme.test}]`: "only an administrator installs",
		`installations: [{org: Globex, target: notes, role: viewer, owner: remi@acme.test}]`:    "only an administrator installs",
		`installations: [{org: Acme Corp, target: notes, owner: remi@acme.test}]`:               "org, target, role and owner are required",
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
