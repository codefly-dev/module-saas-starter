package business

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// HOST-INST-003: a job cannot borrow another installation's boundary, an
// undelivered action, or authority outside its agent's ceiling/standing role.
func TestInstallationStepScopesAreRootBounded(t *testing.T) {
	a := &InstallationStepAuthority{RootID: "own-root", Facts: &InstallationAuthorityFacts{Actor: &Principal{AllowedScopes: []string{"records"}}},
		StandingPermissions: []WorkContextPermission{{ResourceKind: "records", Action: "read"}},
		Scope: InstallationScope{OperationAudiences: map[string]ModuleOperationAudience{
			"example": {InvokeScopes: []ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read", "write"}}, {ResourceKind: "other", Actions: []string{"read"}}}},
			"foreign": {InvokeScopes: []ModuleOperationScope{{ResourceKind: "records", Actions: []string{"delete"}, ResourceIDs: []string{"other-root"}}}},
		}},
	}
	scopes := installationStepScopes(a)
	require.Len(t, scopes, 1)
	require.Equal(t, []string{"read"}, scopes[0].Actions)
	require.Equal(t, []string{"own-root"}, scopes[0].ResourceIds)
	a.StandingPermissions = nil
	require.Empty(t, installationStepScopes(a))
}
