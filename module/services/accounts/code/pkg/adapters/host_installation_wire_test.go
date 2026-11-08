package adapters

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

// An installation behind a widening redelivery lists the roles its consent
// approved, never the roles the delivered declaration added, and says an
// upgrade waits; the core refuses those added roles until it is upgraded.
func TestAnInstallationListsTheRolesItsConsentApproved(t *testing.T) {
	read := []*basev0.WorkScopeV1{{ResourceKind: "example.page", Actions: []string{"read"}}}
	approved := domain.Module{Name: "example", Kind: domain.KindSolution, Generation: 1, Roles: map[string][]*basev0.WorkScopeV1{"installer": read, "viewer": read}}
	delivered := approved
	delivered.Generation = 2
	delivered.Roles = map[string][]*basev0.WorkScopeV1{"installer": read, "viewer": read, "editor": read}
	inst := domain.Installation{ID: "installation", Organization: "org", Target: "example", Status: domain.Active, Revision: 3,
		Consent: domain.ConsentReceipt{OperationID: "operation", Revision: 3, Generation: 1, Digest: "sha256:approved"}}

	wire := installationWire(inst, delivered, approved)
	require.Equal(t, []string{"installer", "viewer"}, wire.GetRoles())
	require.True(t, wire.GetUpgradeAvailable())
	require.Equal(t, uint64(1), wire.GetApprovedGeneration())

	inst.Consent.Generation = 2
	wire = installationWire(inst, delivered, delivered)
	require.Equal(t, []string{"editor", "installer", "viewer"}, wire.GetRoles())
	require.False(t, wire.GetUpgradeAvailable())
}
