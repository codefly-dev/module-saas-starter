package business

import (
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// What a grant may carry is decided before anything is written, and the two
// refusals are the whole point of deciding it here: a module nothing is
// installed at, and a declaration a role cannot express.

func TestAGrantCarriesAnInstalledReachedModulesDeclaredConsumption(t *testing.T) {
	plans, unmet := planCarriedModuleAccess([]ReachedModule{{
		Audience: "evidence", InstallationID: "install-1",
		InvokeScopes: []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
	}})
	require.Empty(t, unmet)
	require.Len(t, plans, 1)
	require.Equal(t, "evidence", plans[0].audience)
	require.Equal(t, "install-1", plans[0].installationID)
}

func TestAReachedModuleThisOrganisationHasNotInstalledCarriesNothingAndSaysSo(t *testing.T) {
	plans, unmet := planCarriedModuleAccess([]ReachedModule{{
		Audience:     "evidence",
		InvokeScopes: []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
	}})
	require.Empty(t, plans, "there is no node to grant at")
	require.Len(t, unmet, 1)
	require.Equal(t, gen.ReadinessConditionKind_READINESS_CONDITION_KIND_REACHED_MODULE_NOT_INSTALLED, unmet[0].Kind)
	require.Equal(t, "evidence", unmet[0].Subject)
}

// A role is (resource, action). A declared scope narrowed to NAMED RESOURCES
// cannot be expressed as one, and carrying it as the whole kind would hand the
// person more than the solution declared — the one thing this derivation exists
// to prevent. It refuses, and it says which part of the declaration did it.
func TestADeclarationNarrowedToNamedResourcesRefusesRatherThanWidening(t *testing.T) {
	plans, unmet := planCarriedModuleAccess([]ReachedModule{{
		Audience: "evidence", InstallationID: "install-1",
		InvokeScopes: []ModuleOperationScope{
			{ResourceKind: "evidence", Actions: []string{"read"}, ResourceIDs: []string{"a", "b"}},
		},
	}})
	require.Empty(t, plans, "nothing is carried rather than the whole kind")
	require.Len(t, unmet, 1)
	require.Equal(t, gen.ReadinessConditionKind_READINESS_CONDITION_KIND_CARRIED_MODULE_ACCESS_NOT_DERIVED, unmet[0].Kind)
	require.Contains(t, unmet[0].Detail, "2 named resources")
	require.Equal(t, gen.ReadinessClearedBy_READINESS_CLEARED_BY_PLATFORM_OPERATOR, unmet[0].ClearedBy)
}

// One narrowed scope refuses the whole audience: a person reaching that module
// through the solution would hold the unrestricted kinds and be denied the
// narrowed one, which is a partial grant nobody asked for and nothing reports.
func TestOneNamedResourceScopeRefusesTheWholeAudience(t *testing.T) {
	plans, unmet := planCarriedModuleAccess([]ReachedModule{{
		Audience: "evidence", InstallationID: "install-1",
		InvokeScopes: []ModuleOperationScope{
			{ResourceKind: "evidence", Actions: []string{"read"}},
			{ResourceKind: "note", Actions: []string{"read"}, ResourceIDs: []string{"n1"}},
		},
	}})
	require.Empty(t, plans)
	require.Len(t, unmet, 1)
	require.Contains(t, unmet[0].Detail, "note (1 named resources)")
}

func TestDeclaredPairsAreDeduplicatedAndOrdered(t *testing.T) {
	resources, actions := declaredPairs([]ModuleOperationScope{
		{ResourceKind: "note", Actions: []string{"write", "read"}},
		{ResourceKind: "evidence", Actions: []string{"read"}},
		{ResourceKind: "note", Actions: []string{"read"}},
	})
	require.Equal(t, []string{"evidence", "note", "note"}, resources)
	require.Equal(t, []string{"read", "read", "write"}, actions)
}

// The declared consumption travels on the reached module, read from the DERIVED
// registry rather than from the manifest's ask: a scope the module envelope
// withheld is not consumption, and readiness reports it as an omitted binding.
func TestAReachedModuleCarriesTheDerivedInvokeScopes(t *testing.T) {
	facts := readinessTestFacts(map[string]ModuleOperationAudience{
		"model": {Audience: "model", InvokeScopes: []ModuleOperationScope{
			{ResourceKind: "evidence", Actions: []string{"read"}},
		}},
	}, nil, nil)
	_, reached := facts.readinessFor(activeInstallation(), gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY,
		documentsTarget(), map[string]*gen.Installation{"model": {Id: "install-1"}})
	require.Len(t, reached, 1, "only the binding the envelope derived is reached")
	require.Equal(t, "model", reached[0].Audience)
	require.Equal(t, "install-1", reached[0].InstallationID)
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}, reached[0].InvokeScopes)
}

// Two bindings may redeem on the same audience. The person needs what either
// does on their session, so what the grant carries there is the union.
func TestTwoBindingsOnOneAudienceUnionTheirDeclaredConsumption(t *testing.T) {
	facts := readinessTestFacts(map[string]ModuleOperationAudience{
		"model": {Audience: "model", InvokeScopes: []ModuleOperationScope{
			{ResourceKind: "evidence", Actions: []string{"read"}},
		}},
		"model-notes": {Audience: "model", InvokeScopes: []ModuleOperationScope{
			{ResourceKind: "note", Actions: []string{"write"}},
		}},
	}, nil, nil)
	facts.askedByBinding["acme.documents"] = ModuleAuthorityRequest{
		Principal: readinessPrefix, Binding: "acme.documents",
		Authority: &solutionhost.Authority{Principal: readinessPrefix, Bindings: []solutionhost.AuthorityBinding{
			{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"evidence:read"}}},
			{ID: "model-notes", Audience: "model", Operations: map[string][]string{"invoke": {"note:write"}}},
		}},
	}
	_, reached := facts.readinessFor(activeInstallation(), gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY,
		documentsTarget(), map[string]*gen.Installation{"model": {Id: "install-1"}})
	require.Len(t, reached, 1, "one audience, however many bindings redeem on it")
	resources, actions := declaredPairs(reached[0].InvokeScopes)
	require.Equal(t, []string{"evidence", "note"}, resources)
	require.Equal(t, []string{"read", "write"}, actions)
}

func TestADerivedRolesProvenanceNamesAllThreeOfItsInputs(t *testing.T) {
	provenance := derivedRoleProvenance("install-1", "evidence", "role-7")
	require.Equal(t, "installation=install-1;audience=evidence;role=role-7", provenance)
	require.NotEqual(t, provenance, derivedRoleProvenance("install-1", "evidence", "role-8"),
		"two roles chosen for the same reach intersect differently and cannot share a derived role")
}
