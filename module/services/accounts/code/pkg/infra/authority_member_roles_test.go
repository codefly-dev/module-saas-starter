package infra

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

func TestHostMembershipMapsOnlyAdministratorsToCoreAdmin(t *testing.T) {
	for _, role := range []string{"owner", "admin", "member", "viewer", "developer", ""} {
		person := domain.Person{ID: "person", Organization: "org", Roles: memberRoles(role)}
		want := role == "owner" || role == "admin"
		if got := decide.IsAdministrator(person); got != want {
			t.Errorf("host role %q administers=%t, want %t", role, got, want)
		}
		if want && (len(person.Roles) != 1 || person.Roles[0] != domain.AdminRole) {
			t.Errorf("host role %q leaked host vocabulary: %v", role, person.Roles)
		}
	}
}
