package adapters

import (
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A module that declares its own installation role is installed with no
// caller role: the host selects a named one, and "none" grants none. A role
// that is given must still be a role id.
func TestInstallSolutionRequestRoleIsOptionalButWellFormed(t *testing.T) {
	request := func(role string) *gen.InstallSolutionRequest {
		return &gen.InstallSolutionRequest{OrgId: uuid.NewString(), AgentIdentifier: "publisher/name:1.0.0", TargetId: uuid.NewString(), RoleId: role}
	}
	require.NoError(t, Validate(request("")), "a declared installation role leaves the caller's role out")
	require.NoError(t, Validate(request(uuid.NewString())))
	require.Error(t, Validate(request("viewer")), "a given role is a role id")
}
