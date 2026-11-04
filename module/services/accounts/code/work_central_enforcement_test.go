package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// clearCentralEnforcementEnvironment empties every carrier workspaceEnv reads
// RBAC_CENTRAL_ENFORCEMENT from. Both have to be blanked: `codefly ci run`
// injects the security group as CODEFLY__WORKSPACE_CONFIGURATION__SECURITY__*
// while a bare `go test` carries only the process environment, so a case that
// clears one of them asserts the default on one runner and an ambient value on
// the other.
func clearCentralEnforcementEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("RBAC_CENTRAL_ENFORCEMENT", "")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__SECURITY__RBAC_CENTRAL_ENFORCEMENT", "")
	t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__SECURITY__RBAC_CENTRAL_ENFORCEMENT", "")
}

// TestUnsetCentralEnforcementEnforces is the default this issue flipped: a
// deployment that configures nothing hard-denies at admission. It is asserted on
// the configuration reader rather than on the interceptor variable because that
// reader is the only place the default lives.
func TestUnsetCentralEnforcementEnforces(t *testing.T) {
	clearCentralEnforcementEnvironment(t)
	mode, err := configuredCentralEnforcement()
	require.NoError(t, err)
	require.Equal(t, centralEnforcementEnforce, mode)
	require.True(t, mode.Enforcing())
}

func TestExplicitCentralEnforcementValues(t *testing.T) {
	for _, testCase := range []struct {
		value string
		want  centralEnforcementMode
	}{
		{value: "enforce", want: centralEnforcementEnforce},
		{value: "ENFORCE", want: centralEnforcementEnforce},
		{value: " enforce ", want: centralEnforcementEnforce},
		{value: "shadow", want: centralEnforcementShadow},
		{value: "Shadow", want: centralEnforcementShadow},
	} {
		t.Run(testCase.value, func(t *testing.T) {
			clearCentralEnforcementEnvironment(t)
			t.Setenv("RBAC_CENTRAL_ENFORCEMENT", testCase.value)
			mode, err := configuredCentralEnforcement()
			require.NoError(t, err)
			require.Equal(t, testCase.want, mode)
		})
	}
}

// A typo must fail the boot rather than resolve to either mode. The returned
// mode is the enforcing one so a caller that ignored the error cannot end up
// shadowed, but the error is what the boot acts on.
func TestUnknownCentralEnforcementFailsBoot(t *testing.T) {
	for _, value := range []string{"off", "true", "classify", "enforced", "no"} {
		t.Run(value, func(t *testing.T) {
			clearCentralEnforcementEnvironment(t)
			t.Setenv("RBAC_CENTRAL_ENFORCEMENT", value)
			mode, err := configuredCentralEnforcement()
			require.Error(t, err)
			require.Contains(t, err.Error(), "RBAC_CENTRAL_ENFORCEMENT")
			require.True(t, mode.Enforcing())
		})
	}
}
