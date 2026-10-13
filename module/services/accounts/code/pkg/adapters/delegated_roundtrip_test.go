package adapters

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

// A callback may read the original owner's retained record. It must preserve
// that read scope through the outbound call; a return binding cannot restore it.
func TestDelegatedOperationRoundTripPreservesOnlyInstalledReadAuthority(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	read := []business.ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read"}}}
	registry := business.ModulePrincipalRegistry{
		business.ModulePrincipalID("example"): {Prefix: "example", Tenant: readOrg, OperationAudiences: map[string]business.ModuleOperationAudience{
			"dispatch": {Audience: "example-worker", InvokeScopes: []business.ModuleOperationScope{
				{ResourceKind: "jobs", Actions: []string{"execute", "read"}},
				{ResourceKind: "records", Actions: []string{"read"}},
			}, LookupScopes: read},
		}},
		business.ModulePrincipalID("example-worker"): {Prefix: "example-worker", Tenant: readOrg, OperationAudiences: map[string]business.ModuleOperationAudience{
			"proof": {Audience: "example", InvokeScopes: read, LookupScopes: read},
		}},
	}
	service.SetModuleCapabilities(nil, nil, registry)
	exchange := func(prefix, binding, parent string, lookup bool) (*gen.IssuedWorkContext, error) {
		module := mintModuleIdentity(t, business.ModulePrincipalID(prefix), readOrg)
		req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: binding, ParentWorkContextToken: parent, Lookup: lookup})
		req.Header().Set(workContextHeader, module)
		req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
		out, err := client.ExchangeDelegatedOperationAudience(context.Background(), req)
		if err != nil {
			return nil, err
		}
		return out.Msg, nil
	}
	parent, _ := mintTestCapability(t, workContextSingleton, testCapability{
		Audience: "example", Tenant: readOrg, Owner: readOwner,
		Task: "019f6bf7-1111-7111-8111-111111111111", Session: "019f6bf7-2222-7222-8222-222222222222",
		Scopes: []*basev0.WorkScopeV1{
			{ResourceKind: "jobs", Actions: []string{"execute", "read"}},
			{ResourceKind: "records", Actions: []string{"read"}},
		},
	})
	for _, lookup := range []bool{false, true} {
		outbound, err := exchange("example", "dispatch", parent, lookup)
		require.NoError(t, err)
		returned, err := exchange("example-worker", "proof", outbound.Token, true)
		require.NoError(t, err)
		presented, err := workContextSingleton.verifyFor(context.Background(), returned.Token, "example")
		require.NoError(t, err)
		verified := presented.Context()
		require.Equal(t, readOwner, verified.OwnerPrincipalId)
		require.Equal(t, readOrg, verified.TenantId)
		require.Equal(t, "019f6bf7-1111-7111-8111-111111111111", verified.TaskId)
		// A module re-issuing a person's capability holds no session of that
		// person's to bind, so each exchange opens a fresh one: a session a
		// capability carries is its minter's verified session or new, never
		// copied from another capability.
		require.NotEqual(t, "019f6bf7-2222-7222-8222-222222222222", verified.SessionId)
		require.NotEmpty(t, verified.SessionId)
		require.Len(t, verified.AuthorityScopes, 1)
		require.Equal(t, "records", verified.AuthorityScopes[0].ResourceKind)
		require.Equal(t, []string{"read"}, verified.AuthorityScopes[0].Actions)
	}
	missingRead := mint("example-worker", "jobs", "read")
	out, err := exchange("example-worker", "proof", missingRead, true)
	require.Error(t, err)
	require.Nil(t, out)
	outbound, err := exchange("example", "dispatch", parent, false)
	require.NoError(t, err)
	facts.facts.OrganizationRevision++
	out, err = exchange("example-worker", "proof", outbound.Token, true)
	require.Error(t, err)
	require.Nil(t, out)
}
