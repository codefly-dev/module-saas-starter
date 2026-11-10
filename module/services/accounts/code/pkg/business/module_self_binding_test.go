package business

import (
	"testing"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// selfManifest is testManifest asking the given bindings instead of `run`.
func selfManifest(t *testing.T, bindings ...solutionhost.AuthorityBinding) *solutionhost.ModuleManifest {
	t.Helper()
	manifest := testManifest(t, nil)
	manifest.Authority.Bindings = bindings
	require.NoError(t, manifest.Validate())
	return manifest
}

var (
	runBinding  = solutionhost.AuthorityBinding{ID: "run", Audience: testProducerAudience, Operations: map[string][]string{"invoke": {"profiles:read"}}}
	selfTools   = solutionhost.AuthorityBinding{ID: "self-tools", Audience: testModulePrefix, Operations: map[string][]string{"invoke": {"reports:read"}, "lookup": {"reports:read"}}}
	selfRunning = solutionhost.AuthorityBinding{ID: "self-running", Audience: testModulePrefix, Operations: map[string][]string{"headless": {"reports:read"}}}
)

// A module's own operations prepared as runnables are bindings it addresses to
// itself that act only as the person. The authority core issues through one
// within the person's capability, so the registry, the module's standing
// authority, neither grants it nor omits it: the envelope does not list it,
// and its absence there must not fail the module's derivation.
func TestASelfAddressedPersonBindingIsNeitherRegisteredNorOmitted(t *testing.T) {
	envelope := parseTestEnvelope(t)
	manifest := selfManifest(t, runBinding, selfTools)

	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix], manifest, []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, omitted, "a self-addressed person binding the envelope does not list is not an omission")
	require.Contains(t, grant.OperationAudiences, "run", "the foreign binding is granted as before")
	require.NotContains(t, grant.OperationAudiences, "self-tools", "the registry grants nothing through it: no invoke, lookup, headless or source delegation")

	registry := ModulePrincipalRegistry{ModulePrincipalID(testModulePrefix): grant}
	withheld, reasons := withheldModuleAuthority([]*solutionhost.ModuleManifest{manifest}, registry, omitted)
	require.Empty(t, withheld, "nor is it reported as authority withheld")
	require.Empty(t, reasons)

	// Without an envelope the manifest is its own ceiling: with two bindings
	// it names no source delegation, and the self binding is skipped the same way.
	grant, omitted, err = deriveModulePrincipalGrant(testModulePrefix, manifestCeiling(manifest.Authority), manifest, []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.NotContains(t, grant.OperationAudiences, "self-tools")
}

// A self-addressed binding that asks a headless scope is standing authority
// and keeps its path: omitted, by name, when the envelope does not allow it,
// and granted within the envelope when it does. A self-addressed binding the
// ceiling grants source delegation keeps its path too.
func TestASelfAddressedStandingBindingKeepsItsPath(t *testing.T) {
	envelope := parseTestEnvelope(t)
	_, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix],
		selfManifest(t, runBinding, selfRunning), []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Len(t, omitted, 1, "a self-addressed headless binding the envelope does not allow is still omitted")
	require.ErrorIs(t, omitted[0], ErrModulePrincipalBindingOmitted)
	require.ErrorContains(t, omitted[0], "self-running")

	wire := testEnvelopeDocument()
	ceiling := wire["principals"].(map[string]any)[testModulePrefix].(map[string]any)["grant"].(map[string]any)
	ceiling["operation_audiences"].(map[string]any)["self-running"] = map[string]any{
		"audience":        testModulePrefix,
		"headless_scopes": []any{map[string]any{"resource_kind": "reports", "actions": []any{"read"}}},
	}
	allowing, err := ParseModuleEnvelope(encodeEnvelope(t, wire))
	require.NoError(t, err)
	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, allowing.Principals[testModulePrefix],
		selfManifest(t, runBinding, selfRunning), []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.Equal(t, ModuleOperationAudience{Audience: testModulePrefix, HeadlessScopes: []ModuleOperationScope{{ResourceKind: "reports", Actions: []string{"read"}}}},
		grant.OperationAudiences["self-running"], "registered within the envelope, as before")

	// One self-addressed invoke binding with no envelope: the manifest's own
	// ceiling grants it source delegation, a standing grant, so it is
	// registered as before rather than skipped.
	single := selfManifest(t, selfTools)
	grant, omitted, err = deriveModulePrincipalGrant(testModulePrefix, manifestCeiling(single.Authority), single, []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.NotNil(t, grant.OperationAudiences["self-tools"].SourceDelegationScopes, "the source-delegating self binding keeps its path")
}

// A binding addressed to another module that the envelope does not list is
// omitted, by name, as before.
func TestAForeignBindingTheEnvelopeLacksIsStillOmitted(t *testing.T) {
	envelope := parseTestEnvelope(t)
	foreign := solutionhost.AuthorityBinding{ID: "other-tools", Audience: "other-module", Operations: map[string][]string{"invoke": {"things:read"}}}
	grant, omitted, err := deriveModulePrincipalGrant(testModulePrefix, envelope.Principals[testModulePrefix],
		selfManifest(t, runBinding, foreign), []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.NotContains(t, grant.OperationAudiences, "other-tools")
	require.Len(t, omitted, 1)
	require.ErrorContains(t, omitted[0], "the envelope allows no such operation binding")
}

// Only a binding addressed to the module itself that asks invoke or lookup
// and nothing else acts only as the person.
func TestPersonSelfBindingIsInvokeOrLookupToItself(t *testing.T) {
	require.True(t, personSelfBinding(testModulePrefix, selfTools))
	require.True(t, personSelfBinding(testModulePrefix, solutionhost.AuthorityBinding{Audience: testModulePrefix, Operations: map[string][]string{"lookup": {"reports:read"}}}))
	require.False(t, personSelfBinding(testModulePrefix, selfRunning), "headless is standing authority")
	require.False(t, personSelfBinding(testModulePrefix, solutionhost.AuthorityBinding{Audience: testModulePrefix,
		Operations: map[string][]string{"invoke": {"reports:read"}, "headless": {"reports:read"}}}), "invoke beside headless is standing authority")
	require.False(t, personSelfBinding(testModulePrefix, runBinding), "another module's audience")
	require.False(t, personSelfBinding(testModulePrefix, solutionhost.AuthorityBinding{Audience: testModulePrefix, Operations: map[string][]string{}}), "asks nothing")
}
