package business

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/stretchr/testify/require"
)

// A role this host derived for a solution before every host decided through
// its authority core is still read back by the solution's role name; any
// other role is read by its own.
func TestAStoredSolutionRoleIsReadByItsSolutionsRoleName(t *testing.T) {
	require.Equal(t, domain.UserRole, SolutionRoleOf(solutionRoleNamePrefix+"org=org;solution=target;role="+domain.UserRole))
	require.Equal(t, "Module viewer", SolutionRoleOf("Module viewer"))
}
