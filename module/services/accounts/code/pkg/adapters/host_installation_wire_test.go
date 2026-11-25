package adapters

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev1 "github.com/codefly-dev/core/generated/go/codefly/base/v1"
	"github.com/stretchr/testify/require"
)

// An installation behind a widening redelivery lists the roles its consent
// approved, never the roles the delivered declaration added, and says an
// upgrade waits as the core answers it; the core refuses those added roles
// until it is upgraded.
func TestAnInstallationListsTheRolesItsConsentApproved(t *testing.T) {
	read := []*basev1.WorkScopeV1{{ResourceKind: "example.page", Actions: []string{"read"}}}
	approved := domain.Module{Name: "example", Kind: domain.KindSolution, Generation: 1, Roles: map[string][]*basev1.WorkScopeV1{domain.AdminRole: read, domain.UserRole: read}}
	delivered := approved
	delivered.Generation = 2
	delivered.Roles = map[string][]*basev1.WorkScopeV1{domain.AdminRole: read, domain.UserRole: read, "author": read}
	inst := domain.Installation{ID: "installation", Organization: "org", Target: "example", Status: domain.Active, Revision: 3,
		Consent: domain.ConsentReceipt{OperationID: "operation", Revision: 3, Generation: 1, Digest: "sha256:approved"}}

	wire := installationWire(inst, approved, true)
	require.Equal(t, []string{"admin", "user"}, wire.GetRoles())
	require.True(t, wire.GetUpgradeAvailable())
	require.Equal(t, uint64(1), wire.GetApprovedGeneration())

	inst.Consent.Generation = 2
	wire = installationWire(inst, delivered, false)
	require.Equal(t, []string{"admin", "author", "user"}, wire.GetRoles())
	require.False(t, wire.GetUpgradeAvailable())
}
