package business

import (
	"testing"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

func TestInstalledContentReadModeIsExplicitAndRestricted(t *testing.T) {
	read := []ModuleOperationScope{{ResourceKind: "documents", Actions: []string{"read"}}}
	valid := ModuleOperationAudience{Audience: "documents", InvokeScopes: read, DelegationMode: DelegationModeContentRead}
	require.NoError(t, validateOperationAudiences("reader", map[string]ModuleOperationAudience{"evidence": valid}))
	lookup := valid
	lookup.LookupScopes = read
	require.NoError(t, validateOperationAudiences("reader", map[string]ModuleOperationAudience{"evidence": lookup}))
	for name, change := range map[string]func(*ModuleOperationAudience){
		"unknown": func(b *ModuleOperationAudience) { b.DelegationMode = "future" },
		"write": func(b *ModuleOperationAudience) {
			b.InvokeScopes = []ModuleOperationScope{{ResourceKind: "documents", Actions: []string{"write"}}}
		},
		"headless":     func(b *ModuleOperationAudience) { b.HeadlessScopes = read },
		"source":       func(b *ModuleOperationAudience) { b.SourceDelegationScopes = read },
		"empty lookup": func(b *ModuleOperationAudience) { b.LookupScopes = []ModuleOperationScope{} },
		"wide lookup": func(b *ModuleOperationAudience) {
			b.LookupScopes = []ModuleOperationScope{{ResourceKind: "other", Actions: []string{"read"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := valid
			change(&b)
			require.Error(t, validateOperationAudiences("reader", map[string]ModuleOperationAudience{"evidence": b}))
		})
	}
	manifest := &solutionhost.ModuleManifest{Binding: "host.local.reader", Alias: "reader", Generation: 2, Authority: &solutionhost.Authority{Principal: "reader", Bindings: []solutionhost.AuthorityBinding{{ID: "evidence", Audience: "documents", Operations: map[string][]string{"invoke": {"documents:read"}}}}}}
	held := ModuleEnvelopePrincipal{Grant: ModulePrincipalGrant{Tenant: "00000000-0000-7000-8000-0000000000a1", OperationAudiences: map[string]ModuleOperationAudience{"evidence": valid}}}
	grant, omitted, err := deriveModulePrincipalGrant("reader", held, manifest, []ModuleWorkload{testWorkload})
	require.NoError(t, err)
	require.Empty(t, omitted)
	require.Equal(t, DelegationModeContentRead, grant.OperationAudiences["evidence"].DelegationMode)
	require.Empty(t, grant.OperationAudiences["evidence"].SourceDelegationScopes)

}

func TestInstalledModuleActorModeShape(t *testing.T) {
	invoke := []ModuleOperationScope{{ResourceKind: "tickets", Actions: []string{"read", "write"}}}
	read := []ModuleOperationScope{{ResourceKind: "tickets", Actions: []string{"read"}}}
	for _, audience := range []string{"compute", SolutionAudiencePrefix + "example-tools"} {
		valid := ModuleOperationAudience{Audience: audience, InvokeScopes: invoke, LookupScopes: read, DelegationMode: DelegationModeModuleActor}
		require.NoError(t, validateOperationAudiences("reader", map[string]ModuleOperationAudience{"act": valid}))
		for name, change := range map[string]func(*ModuleOperationAudience){
			"no invoke": func(b *ModuleOperationAudience) { b.InvokeScopes, b.LookupScopes, b.HeadlessScopes = nil, nil, read },
			"headless":  func(b *ModuleOperationAudience) { b.HeadlessScopes = read },
			"source":    func(b *ModuleOperationAudience) { b.SourceDelegationScopes = invoke },
		} {
			t.Run(audience+"/"+name, func(t *testing.T) {
				b := valid
				change(&b)
				require.Error(t, validateOperationAudiences("reader", map[string]ModuleOperationAudience{"act": b}))
			})
		}
	}
	require.True(t, NamesInstalledBinding(installedModuleActorPrefix+"x"))
	require.True(t, NamesInstalledBinding(installedContentReadPrefix+"x"))
	require.False(t, NamesInstalledContentRead(installedModuleActorPrefix+"x"))
	require.False(t, NamesInstalledBinding("act"))
}
