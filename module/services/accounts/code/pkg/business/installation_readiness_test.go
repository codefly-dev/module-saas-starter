package business

import (
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

const readinessPrefix = "documents"

// readinessTestFacts is a host that applied one manifest for `documents`,
// asking two operation bindings, and a registry derived from an envelope that
// allowed only the first. This is the composition every real cell takes and the
// one no test fed before: the units are each green about their own contract
// while the composition is false-green.
func readinessTestFacts(derived map[string]ModuleOperationAudience, sources []*DatasourceSource,
	delegated map[string]struct{},
) *readinessFacts {
	facts := &readinessFacts{
		askedByBinding:     map[string]ModuleAuthorityRequest{},
		bindingByPrincipal: map[string]string{},
		registry:           ModulePrincipalRegistry{},
		sources:            sources,
		delegated:          delegated,
	}
	if delegated == nil {
		facts.delegated = map[string]struct{}{}
	}
	facts.withAppliedManifests([]*SolutionHostBindingRecord{})
	facts.askedByBinding["acme.documents"] = ModuleAuthorityRequest{
		Principal: readinessPrefix, Binding: "acme.documents", Alias: readinessPrefix, Generation: 7,
		Authority: &solutionhost.Authority{
			Principal: readinessPrefix,
			Bindings: []solutionhost.AuthorityBinding{
				{ID: "model", Audience: "model", Operations: map[string][]string{"invoke": {"evidence:read"}}},
				{ID: "runtime-admission", Audience: "runtime", Operations: map[string][]string{"invoke": {"evidence:read"}}},
			},
		},
	}
	facts.bindingByPrincipal[readinessPrefix] = "acme.documents"
	if derived != nil {
		facts.registry[ModulePrincipalID(readinessPrefix)] = ModulePrincipalGrant{
			Prefix: readinessPrefix, OperationAudiences: derived,
		}
	}
	return facts
}

func activeInstallation() *gen.Installation {
	return &gen.Installation{
		Id: "11111111-1111-1111-1111-111111111111", TargetId: "target-1",
		Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE,
	}
}

func documentsTarget() *AvailableSolutionTarget {
	return &AvailableSolutionTarget{
		Kind: string(SolutionDeclaredKindModule), TargetID: "target-1",
		BindingID: "acme.documents", RouteAlias: readinessPrefix,
	}
}

// A healthy record over a registry that derived every asked binding is ready.
func TestReadinessIsReadyWhenEveryAskedBindingDerived(t *testing.T) {
	facts := readinessTestFacts(map[string]ModuleOperationAudience{
		"model":             {Audience: "model", InvokeScopes: []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}},
		"runtime-admission": {Audience: "runtime", InvokeScopes: []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}},
	}, nil, nil)

	readiness, reached := facts.readinessFor(activeInstallation(),
		gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, documentsTarget(),
		map[string]*gen.Installation{
			"model":   {Id: "33333333-3333-3333-3333-333333333333"},
			"runtime": {Id: "22222222-2222-2222-2222-222222222222"},
		})

	require.True(t, readiness.Ready)
	require.Empty(t, readiness.Unmet)
	// Both bindings invoke on a person's session, so both are modules a grant
	// has to reach — and both are installed here, so both are offers.
	require.Len(t, reached, 2)
}

// THE BUG. The record is HEALTHY and a declared binding derived nothing, which
// is exactly what a validation cell reported for ten installations.
func TestHealthyRecordIsNotReadyWhenAnAskedBindingDerivedNothing(t *testing.T) {
	facts := readinessTestFacts(map[string]ModuleOperationAudience{
		"model": {Audience: "model", InvokeScopes: []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}},
	}, nil, nil)

	readiness, _ := facts.readinessFor(activeInstallation(),
		gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, documentsTarget(), map[string]*gen.Installation{})

	require.False(t, readiness.Ready, "a healthy record whose binding derived nothing is not ready")
	omitted := conditionsOfKind(readiness.Unmet, gen.ReadinessConditionKind_READINESS_CONDITION_KIND_OPERATION_BINDING_OMITTED)
	require.Len(t, omitted, 1)
	require.Equal(t, "runtime-admission", omitted[0].Subject, "the condition names the binding")
	require.Contains(t, omitted[0].Detail, "module envelope", "it names where it is fixed")
	require.Equal(t, gen.ReadinessClearedBy_READINESS_CLEARED_BY_PLATFORM_OPERATOR, omitted[0].ClearedBy)
	require.Equal(t, gen.ReadinessAction_READINESS_ACTION_NONE_AT_THIS_SURFACE, omitted[0].Action,
		"an envelope is not cleared from this surface, and offering an action that cannot work is the lie")
}

// A registry with no entry for the principal denies every exchange, so one
// condition says that rather than one per binding.
func TestReadinessReportsAnOmittedPrincipalOnceRatherThanPerBinding(t *testing.T) {
	facts := readinessTestFacts(nil, nil, nil)

	readiness, reached := facts.readinessFor(activeInstallation(),
		gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, documentsTarget(), map[string]*gen.Installation{})

	require.False(t, readiness.Ready)
	require.Len(t, readiness.Unmet, 1)
	require.Equal(t, gen.ReadinessConditionKind_READINESS_CONDITION_KIND_MODULE_PRINCIPAL_OMITTED, readiness.Unmet[0].Kind)
	require.Empty(t, reached, "nothing is reached by a principal the registry does not hold")
}

// A source with no active delegation to a module that accepts them: the
// condition names the source and the person who clears it, which is the reader.
func TestReadinessNamesASourceWithNoActiveDelegation(t *testing.T) {
	derived := map[string]ModuleOperationAudience{
		"model": {Audience: "model", InvokeScopes: []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}},
		"runtime-admission": {
			Audience:               "runtime",
			InvokeScopes:           []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
			SourceDelegationScopes: []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}},
		},
	}
	sources := []*DatasourceSource{
		{ID: "source-connected", Repo: "acme/connected", Provider: "github"},
		{ID: "source-bare", Repo: "acme/bare", Provider: "github"},
	}
	facts := readinessTestFacts(derived, sources, map[string]struct{}{
		"source-connected\x00" + readinessPrefix: {},
	})
	facts.delegationTargets = facts.registry.SourceDelegationTargets()
	require.Len(t, facts.delegationTargets, 1, "the fixture's module declares one delegation binding")

	readiness, _ := facts.readinessFor(activeInstallation(),
		gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, documentsTarget(), map[string]*gen.Installation{})

	undelegated := conditionsOfKind(readiness.Unmet, gen.ReadinessConditionKind_READINESS_CONDITION_KIND_SOURCE_NOT_DELEGATED)
	require.Len(t, undelegated, 1, "only the source with no active delegation is reported")
	require.Equal(t, "acme/bare", undelegated[0].Subject)
	require.Equal(t, "source-bare", undelegated[0].SourceID)
	require.Equal(t, gen.ReadinessClearedBy_READINESS_CLEARED_BY_VIEWER, undelegated[0].ClearedBy)
	require.Equal(t, gen.ReadinessAction_READINESS_ACTION_RECONNECT_SOURCE, undelegated[0].Action)
}

// A module this target reaches on a person's session, installed here, is an
// offer rather than a condition: that is the grant a solution's own grant step
// can make in the same act.
func TestReachedModuleResolvesToThisOrganizationsInstallation(t *testing.T) {
	facts := readinessTestFacts(map[string]ModuleOperationAudience{
		"model":             {Audience: "model", InvokeScopes: []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}},
		"runtime-admission": {Audience: "runtime", InvokeScopes: []ModuleOperationScope{{ResourceKind: "evidence", Actions: []string{"read"}}}},
	}, nil, nil)
	runtimeInstallation := &gen.Installation{Id: "22222222-2222-2222-2222-222222222222"}

	readiness, reached := facts.readinessFor(activeInstallation(),
		gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, documentsTarget(),
		map[string]*gen.Installation{"runtime": runtimeInstallation})

	require.Len(t, reached, 2)
	byAudience := map[string]ReachedModule{}
	for _, entry := range reached {
		byAudience[entry.Audience] = entry
	}
	require.Equal(t, runtimeInstallation.GetId(), byAudience["runtime"].InstallationID)
	require.Empty(t, byAudience["model"].InstallationID, "a module this organisation has not installed resolves to none")
	notInstalled := conditionsOfKind(readiness.Unmet,
		gen.ReadinessConditionKind_READINESS_CONDITION_KIND_REACHED_MODULE_NOT_INSTALLED)
	require.Len(t, notInstalled, 1)
	require.Equal(t, "model", notInstalled[0].Subject)
}

// The install step, and a record that is not healthy, are conditions in the
// same list as the module's — one list a reader works through, not two.
func TestInstallStepAndRecordHealthAreConditions(t *testing.T) {
	facts := readinessTestFacts(nil, nil, nil)

	pending := activeInstallation()
	pending.Status = gen.InstallationStatus_INSTALLATION_STATUS_PENDING
	readiness, reached := facts.readinessFor(pending,
		gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, documentsTarget(), nil)
	require.False(t, readiness.Ready)
	require.Len(t, readiness.Unmet, 1)
	require.Equal(t, gen.ReadinessConditionKind_READINESS_CONDITION_KIND_INSTALL_STEP_PENDING, readiness.Unmet[0].Kind)
	require.Equal(t, gen.ReadinessClearedBy_READINESS_CLEARED_BY_NOBODY, readiness.Unmet[0].ClearedBy)
	require.Empty(t, reached, "an installation that is not active reaches nothing yet")

	failed := activeInstallation()
	failed.Status = gen.InstallationStatus_INSTALLATION_STATUS_FAILED
	failed.FailureReason = "the solution refused: no database"
	readiness, _ = facts.readinessFor(failed,
		gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, documentsTarget(), nil)
	require.Len(t, readiness.Unmet, 1)
	require.Equal(t, "the solution refused: no database", readiness.Unmet[0].Detail,
		"the solution's own refusal is the condition's words")
	require.Equal(t, gen.ReadinessAction_READINESS_ACTION_RETRY_INSTALL_STEP, readiness.Unmet[0].Action)

	readiness, _ = facts.readinessFor(activeInstallation(),
		gen.InstallationHealth_INSTALLATION_HEALTH_NO_ELIGIBLE_OWNER, nil, nil)
	require.Len(t, readiness.Unmet, 1)
	require.Equal(t, gen.ReadinessConditionKind_READINESS_CONDITION_KIND_RECORD_UNHEALTHY, readiness.Unmet[0].Kind)
	require.Equal(t, gen.ReadinessAction_READINESS_ACTION_TRANSFER_OWNERSHIP, readiness.Unmet[0].Action)
}

func conditionsOfKind(conditions []ReadinessCondition, kind gen.ReadinessConditionKind) []ReadinessCondition {
	var out []ReadinessCondition
	for _, condition := range conditions {
		if condition.Kind == kind {
			out = append(out, condition)
		}
	}
	return out
}
