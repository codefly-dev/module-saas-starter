package business

import (
	"context"
	"testing"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// With no administrative envelope the signed manifest is the ceiling, and a
// later generation that widens it does not reach an organisation that
// installed the narrower one: the installation recorded what its administrator
// consented to. A fresh installation consents to the wider generation, so the
// widening is available — to an organisation whose administrator takes it.
// Where an envelope sets the ceiling, a module installation follows it as
// before, because a platform administrator approved that ceiling.
func TestAManifestWideningWithNoEnvelopeDoesNotReachExistingInstallations(t *testing.T) {
	generationOne := testManifest(t, map[string][]string{"invoke": {"profiles:read"}})
	generationTwo := testManifest(t, map[string][]string{"invoke": {"profiles:read", "profiles:write"}})
	generationTwo.Generation = 2
	require.NoError(t, generationTwo.Validate())

	grantAt := func(t *testing.T, manifest *solutionhost.ModuleManifest) ModulePrincipalGrant {
		t.Helper()
		host := deliveredHost(t, noEnvelopeWritten{}, ceilingReleaseKey, manifest)
		require.NoError(t, host.modulePrincipals.RunOnce(context.Background()))
		grant, ok := host.service.ModulePrincipals()[ModulePrincipalID(testModulePrefix)]
		require.True(t, ok)
		return grant
	}
	one, two := grantAt(t, generationOne), grantAt(t, generationTwo)
	require.Equal(t, []string{"read", "write"}, two.OperationAudiences["run"].InvokeScopes[0].Actions, "generation 2 asks for write")

	installed := consentedInstallationScope("module", one, 0)
	require.False(t, installed.Follows, "with no envelope a module installation records its consent, it does not follow")
	held := intersectInstallationScope(two, installed)
	require.Equal(t, []string{"read"}, held.OperationAudiences["run"].InvokeScopes[0].Actions,
		"the existing installation keeps generation 1's read; write reached no organisation that did not approve it")

	fresh := consentedInstallationScope("module", two, 0)
	require.Equal(t, []string{"read", "write"}, intersectInstallationScope(two, fresh).OperationAudiences["run"].InvokeScopes[0].Actions,
		"an administrator who installs under generation 2 consents to it")

	// A narrowing still reaches the existing installation.
	narrower := one
	narrower.Queues = nil
	require.Empty(t, intersectInstallationScope(narrower, installed).Queues)

	// Under an administrative envelope the installation follows the ceiling.
	following := consentedInstallationScope("module", one, 3)
	require.True(t, following.Follows)
	require.Equal(t, []string{"read", "write"}, intersectInstallationScope(two, following).OperationAudiences["run"].InvokeScopes[0].Actions)

	// A solution's installation always records its consent.
	require.False(t, consentedInstallationScope("solution", one, 3).Follows)
}
