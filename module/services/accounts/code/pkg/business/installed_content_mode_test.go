package business

import (
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"testing"
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
	for _, mode := range []ModuleAuthorityEnforcement{ModuleAuthorityEnforce, ModuleAuthorityWarn} {
		grant, omitted, err := deriveUnderEnforcement(mode, "reader", held, true, manifest, []ModuleWorkload{testWorkload}, nil)
		require.NoError(t, err)
		require.Empty(t, omitted)
		require.Equal(t, DelegationModeContentRead, grant.OperationAudiences["evidence"].DelegationMode)
		require.Empty(t, grant.OperationAudiences["evidence"].SourceDelegationScopes)
	}
}
