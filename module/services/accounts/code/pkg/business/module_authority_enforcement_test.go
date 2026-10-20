package business

import (
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
