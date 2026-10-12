package adapters

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
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
		module, _, err := workContextSingleton.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID(prefix), Tenant: readOrg})
		if err != nil {
			return nil, err
		}
		req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: binding, ParentWorkContextToken: parent, Lookup: lookup})
		req.Header().Set(workcontext.WorkContextHeaderName, module.Encoded())
		req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
		out, err := client.ExchangeDelegatedOperationAudience(context.Background(), req)
		if err != nil {
			return nil, err
		}
		return out.Msg, nil
	}
	parentToken, _, err := workContextSingleton.signer.StartTask(workcontext.StartTaskInput{
		Audience: "example", TenantID: readOrg, OwnerPrincipalID: readOwner,
		TaskID: "019f6bf7-1111-7111-8111-111111111111", SessionID: "019f6bf7-2222-7222-8222-222222222222",
		AuthorizationRevision: facts.facts.EffectiveRevision(), ReplayPolicy: workcontext.WorkContextReplayIdempotent,
		AuthorityScopes: []*basev0.WorkScopeV1{
			{ResourceKind: "jobs", Actions: []string{"execute", "read"}},
			{ResourceKind: "records", Actions: []string{"read"}},
		},
	})
	require.NoError(t, err)
	parent := parentToken.Encoded()
	for _, lookup := range []bool{false, true} {
		outbound, err := exchange("example", "dispatch", parent, lookup)
		require.NoError(t, err)
		returned, err := exchange("example-worker", "proof", outbound.Token, true)
		require.NoError(t, err)
		token, err := workcontext.ParseWorkContextToken(returned.Token)
		require.NoError(t, err)
		verified, err := workContextSingleton.verifier.Verify(token, workcontext.WorkContextExpectations{Issuer: "accounts.test", Audience: "example"})
		require.NoError(t, err)
		require.Equal(t, readOwner, verified.OwnerPrincipalId)
		require.Equal(t, readOrg, verified.TenantId)
		require.Equal(t, "019f6bf7-1111-7111-8111-111111111111", verified.TaskId)
		require.Equal(t, "019f6bf7-2222-7222-8222-222222222222", verified.SessionId)
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
