package business

import (
	"slices"
	"testing"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

func enforcementManifest() *solutionhost.ModuleManifest {
	return &solutionhost.ModuleManifest{
		Binding: "host.local.documents", Alias: "documents", Generation: 2,
		Authority: &solutionhost.Authority{
			Principal: "documents",
			Bindings: []solutionhost.AuthorityBinding{
				{ID: "model", Audience: "model", Operations: map[string][]string{
					"invoke": {"modelservice.profiles:invoke", "modelservice.profiles:read"}, "headless": {"modelservice.profiles:invoke"}}},
				{ID: "runtime-admission", Audience: "runtime", Operations: map[string][]string{
					"invoke": {"runtime.tasks:start", "runtime.tasks:read", "documents.ingest:write"}, "lookup": {"runtime.tasks:read"}}},
			},
			Queues: []string{"datasource"}, ResourceKinds: []string{"documents", "documents.ingest", "documents.collection"},
		},
	}
}

// The envelope on this host knows the module but predates its manifest: it
// allows one kind and one binding of the two asked for.
func enforcementStaleEntry() ModuleEnvelopePrincipal {
	return ModuleEnvelopePrincipal{Grant: ModulePrincipalGrant{
		Tenant: "00000000-0000-7000-8000-0000000000a1", Queues: []string{"datasource"}, Resources: []string{"documents"},
		OperationAudiences: map[string]ModuleOperationAudience{
			"model": {Audience: "model", InvokeScopes: []ModuleOperationScope{{ResourceKind: "modelservice.profiles", Actions: []string{"invoke", "read"}}}},
		},
	}}
}

func TestEnforceWithholdsWhatTheEnvelopeDoesNotAllow(t *testing.T) {
	grant, omitted, err := deriveUnderEnforcement(ModuleAuthorityEnforce, "documents", enforcementStaleEntry(), true,
		enforcementManifest(), []ModuleWorkload{testWorkload}, nil)
	require.NoError(t, err)
	require.Len(t, omitted, 1, "the binding the envelope does not list is withheld")
	require.Equal(t, []string{"documents"}, grant.Resources)
	require.NotContains(t, grant.OperationAudiences, "runtime-admission")

	_, _, err = deriveUnderEnforcement(ModuleAuthorityEnforce, "documents", ModuleEnvelopePrincipal{}, false,
		enforcementManifest(), []ModuleWorkload{testWorkload}, nil)
	require.Error(t, err, "a principal the envelope does not know is refused")
}

func TestWarnGrantsWhatTheManifestAsksAndKeepsTheTenant(t *testing.T) {
	grant, omitted, err := deriveUnderEnforcement(ModuleAuthorityWarn, "documents", enforcementStaleEntry(), true,
		enforcementManifest(), []ModuleWorkload{testWorkload}, nil)
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.Equal(t, "00000000-0000-7000-8000-0000000000a1", grant.Tenant, "the tenancy is the envelope's, never the manifest's")
	require.Equal(t, []string{"documents", "documents.collection", "documents.ingest"}, grant.Resources)
	admission := grant.OperationAudiences["runtime-admission"]
	require.Equal(t, "runtime", admission.Audience)
	require.Equal(t, []ModuleOperationScope{
		{ResourceKind: "documents.ingest", Actions: []string{"write"}},
		{ResourceKind: "runtime.tasks", Actions: []string{"read", "start"}},
	}, admission.InvokeScopes)
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "runtime.tasks", Actions: []string{"read"}}}, admission.LookupScopes)
	require.Nil(t, admission.SourceDelegationScopes, "the envelope chose no delegating binding and the manifest has two, so none delegates")

	chosen := enforcementStaleEntry()
	chosen.Grant.OperationAudiences["runtime-admission"] = ModuleOperationAudience{Audience: "runtime",
		InvokeScopes:           []ModuleOperationScope{{ResourceKind: "runtime.tasks", Actions: []string{"start"}}},
		SourceDelegationScopes: []ModuleOperationScope{{ResourceKind: "runtime.tasks", Actions: []string{"start"}}}}
	grant, omitted, err = deriveUnderEnforcement(ModuleAuthorityWarn, "documents", chosen, true,
		enforcementManifest(), []ModuleWorkload{testWorkload}, nil)
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.Equal(t, grant.OperationAudiences["runtime-admission"].InvokeScopes, grant.OperationAudiences["runtime-admission"].SourceDelegationScopes,
		"the envelope's delegating binding keeps delegating, as wide as it asks")
	require.Nil(t, grant.OperationAudiences["model"].SourceDelegationScopes)
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "modelservice.profiles", Actions: []string{"invoke"}}}, grant.OperationAudiences["model"].HeadlessScopes)
}

func TestWarnGrantsAnUnknownPrincipalOnlyATenantTheEnvelopeShares(t *testing.T) {
	shared := &ModuleEnvelope{Principals: map[string]ModuleEnvelopePrincipal{
		"assistant": {Grant: ModulePrincipalGrant{Tenant: "00000000-0000-7000-8000-0000000000a1"}}, "inference": {Grant: ModulePrincipalGrant{Tenant: "00000000-0000-7000-8000-0000000000a1"}},
	}}
	grant, omitted, err := deriveUnderEnforcement(ModuleAuthorityWarn, "documents", ModuleEnvelopePrincipal{}, false,
		enforcementManifest(), []ModuleWorkload{testWorkload}, shared)
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.Equal(t, "00000000-0000-7000-8000-0000000000a1", grant.Tenant)

	split := &ModuleEnvelope{Principals: map[string]ModuleEnvelopePrincipal{
		"assistant": {Grant: ModulePrincipalGrant{Tenant: "00000000-0000-7000-8000-0000000000a1"}}, "inference": {Grant: ModulePrincipalGrant{Tenant: "00000000-0000-7000-8000-0000000000b2"}},
	}}
	_, _, err = deriveUnderEnforcement(ModuleAuthorityWarn, "documents", ModuleEnvelopePrincipal{}, false,
		enforcementManifest(), []ModuleWorkload{testWorkload}, split)
	require.ErrorIs(t, err, ErrModuleAuthorityTenantUnknown, "even with enforcement off nothing invents an organization")
}

func TestParseModuleAuthorityEnforcement(t *testing.T) {
	for raw, want := range map[string]ModuleAuthorityEnforcement{"": ModuleAuthorityEnforce, "enforce": ModuleAuthorityEnforce, " Warn ": ModuleAuthorityWarn} {
		got, err := ParseModuleAuthorityEnforcement(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}
	_, err := ParseModuleAuthorityEnforcement("off")
	require.Error(t, err, "a typo cannot quietly turn enforcement off")
}

// The runtime's installation bindings exist only in the envelope: a runtime
// manifest declares no bindings, because the operations it is admitted to are
// a property of each installation, not of the module. Warn widens the envelope
// to the manifest; it never shrinks the envelope to it.
func TestWarnKeepsWhatOnlyTheEnvelopeGrants(t *testing.T) {
	held := enforcementStaleEntry()
	held.Grant.Namespaces = []string{"document"}
	held.Grant.OperationAudiences["plan-sync-pages"] = ModuleOperationAudience{Audience: "documents",
		InvokeScopes: []ModuleOperationScope{{ResourceKind: "documents.ingest", Actions: []string{"read", "write"}}},
		LookupScopes: []ModuleOperationScope{{ResourceKind: "documents.ingest", Actions: []string{"read"}}}}

	// A manifest that asks for nothing at all — the runtime's shape — is
	// granted the envelope's entry whole, exactly as enforce grants it.
	bare := &solutionhost.ModuleManifest{Binding: "host.local.runtime", Alias: "runtime", Generation: 1,
		Authority: &solutionhost.Authority{Principal: "runtime"}}
	enforced, _, err := deriveUnderEnforcement(ModuleAuthorityEnforce, "runtime", held, true, bare, []ModuleWorkload{testWorkload}, nil)
	require.NoError(t, err)
	grant, omitted, err := deriveUnderEnforcement(ModuleAuthorityWarn, "runtime", held, true, bare, []ModuleWorkload{testWorkload}, nil)
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.Equal(t, held.Grant.OperationAudiences, grant.OperationAudiences, "warn never grants less than the envelope allows")
	require.Equal(t, enforced.OperationAudiences, grant.OperationAudiences, "and for a bare manifest it is exactly enforce's grant")
	require.Equal(t, []string{"datasource"}, grant.Queues)
	require.Equal(t, []string{"document"}, grant.Namespaces)

	// A manifest that asks is held to what it asks, as under enforce: the
	// envelope-only binding is not granted on top of a published contract, and
	// the manifest's own bindings are granted as asked.
	grant, omitted, err = deriveUnderEnforcement(ModuleAuthorityWarn, "documents", held, true,
		enforcementManifest(), []ModuleWorkload{testWorkload}, nil)
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.NotContains(t, grant.OperationAudiences, "plan-sync-pages")
	require.Contains(t, grant.OperationAudiences, "runtime-admission")
	require.Equal(t, []string{"documents", "documents.collection", "documents.ingest"}, grant.Resources)
}

// A manifest names kinds and actions, never resources, so the resource ids a
// binding's scopes name exist only in the envelope. Warn widens kinds and
// actions to the manifest and keeps those ids: a binding that acts as a person
// toward named sources stays usable, instead of turning kind-wide, which the
// person-delegated source path refuses.
func TestWarnKeepsTheResourcesTheEnvelopeNames(t *testing.T) {
	held := enforcementStaleEntry()
	held.Grant.OperationAudiences["source-calls"] = ModuleOperationAudience{Audience: SourceOperationAudience, DelegationMode: DelegationModeModuleActor,
		InvokeScopes: []ModuleOperationScope{{ResourceKind: SourceOperationResourceKind, Actions: []string{"invoke", "read"}, ResourceIDs: []string{"source-a"}}},
		LookupScopes: []ModuleOperationScope{{ResourceKind: SourceOperationResourceKind, Actions: []string{"read"}, ResourceIDs: []string{"source-a"}}}}
	manifest := enforcementManifest()
	manifest.Authority.Bindings = append(manifest.Authority.Bindings, solutionhost.AuthorityBinding{
		ID: "source-calls", Audience: SourceOperationAudience, Operations: map[string][]string{
			"invoke": {SourceOperationResourceKind + ":invoke", SourceOperationResourceKind + ":read"},
			"lookup": {SourceOperationResourceKind + ":read"}}})

	enforced, _, err := deriveUnderEnforcement(ModuleAuthorityEnforce, "documents", held, true, manifest, []ModuleWorkload{testWorkload}, nil)
	require.NoError(t, err)
	require.Equal(t, held.Grant.OperationAudiences["source-calls"], enforced.OperationAudiences["source-calls"], "enforce grants the envelope's narrowing")

	grant, omitted, err := deriveUnderEnforcement(ModuleAuthorityWarn, "documents", held, true, manifest, []ModuleWorkload{testWorkload}, nil)
	require.NoError(t, err)
	require.Empty(t, omitted)
	calls := grant.OperationAudiences["source-calls"]
	require.Equal(t, DelegationModeModuleActor, calls.DelegationMode)
	require.Equal(t, enforced.OperationAudiences["source-calls"], calls, "warn keeps the envelope's source, as enforce does")
	for _, scope := range append(slices.Clone(calls.InvokeScopes), calls.LookupScopes...) {
		require.True(t, sourceOperationActorScope(scope), "the person-delegated source path accepts the warn grant")
	}

	// A kind the envelope names without ids, or not at all, stays kind-wide.
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "modelservice.profiles", Actions: []string{"invoke", "read"}}}, grant.OperationAudiences["model"].InvokeScopes)
	require.Nil(t, grant.OperationAudiences["runtime-admission"].InvokeScopes[0].ResourceIDs)

	// The same binding id under another audience is another binding: it takes
	// none of the envelope's resources.
	manifest.Authority.Bindings[len(manifest.Authority.Bindings)-1].Audience = "elsewhere"
	grant, _, err = deriveUnderEnforcement(ModuleAuthorityWarn, "documents", held, true, manifest, []ModuleWorkload{testWorkload}, nil)
	require.NoError(t, err)
	require.Nil(t, grant.OperationAudiences["source-calls"].InvokeScopes[0].ResourceIDs)
}
