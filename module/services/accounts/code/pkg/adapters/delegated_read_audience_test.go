package adapters

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func installReadBinding() {
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{
		business.ModulePrincipalID("example"): {Prefix: "example", Tenant: readOrg, ReadAudiences: map[string]business.ModuleReadAudience{
			"proof": {Audience: "example-producer", Scopes: []business.ModuleReadScope{{ResourceKind: "results"}}},
		}},
	})
}
func readExchangeRequest(t *testing.T, parent string) *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest] {
	t.Helper()
	token, _, err := workContextSingleton.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("example"), Tenant: readOrg})
	require.NoError(t, err)
	req := connect.NewRequest(&gen.ModuleExchangeDelegatedReadAudienceRequest{BindingId: "proof", ParentWorkContextToken: parent})
	req.Header().Set(codefly.WorkContextHeaderName, token.Encoded())
	req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
	return req
}
func TestDelegatedReadExchangeOwnerAndDenials(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	installReadBinding()
	parent := mint("example", "results", "read", "unrelated:write")
	out, err := client.ExchangeDelegatedReadAudience(context.Background(), readExchangeRequest(t, parent))
	require.NoError(t, err)
	token, err := codefly.ParseWorkContextToken(out.Msg.Token)
	require.NoError(t, err)
	child, err := workContextSingleton.verifier.Verify(token, codefly.WorkContextExpectations{Issuer: "accounts.test", Audience: "example-producer"})
	require.NoError(t, err)
	require.Equal(t, readOwner, child.OwnerPrincipalId)
	require.Equal(t, readOrg, child.TenantId)
	require.Equal(t, "019f6bf7-1111-7111-8111-111111111111", child.TaskId)
	require.Equal(t, "019f6bf7-2222-7222-8222-222222222222", child.SessionId)
	require.Len(t, child.AuthorityScopes, 1)
	require.Equal(t, []string{"read"}, child.AuthorityScopes[0].Actions)
	for _, tc := range []struct {
		name   string
		mutate func(*connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest])
	}{
		{"missing perimeter", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Header().Del("x-codefly-internal-token")
		}},
		{"forged module", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Header().Set(codefly.WorkContextHeaderName, "forged")
		}},
		{"parent is not module credential", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Header().Set(codefly.WorkContextHeaderName, parent)
		}},
		{"duplicate carrier", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Header().Add(codefly.WorkContextHeaderName, r.Header().Get(codefly.WorkContextHeaderName))
		}},
		{"forged parent", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Msg.ParentWorkContextToken = "forged"
		}},
		{"foreign audience", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Msg.ParentWorkContextToken = mint("foreign", "results", "read")
		}},
		{"scope escalation", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Msg.ParentWorkContextToken = mint("example", "results", "write")
		}},
		{"unknown binding", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) { r.Msg.BindingId = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := readExchangeRequest(t, parent)
			tc.mutate(r)
			out, err := client.ExchangeDelegatedReadAudience(context.Background(), r)
			require.Error(t, err)
			require.Nil(t, out)
		})
	}
	facts.facts.OrganizationRevision++
	out, err = client.ExchangeDelegatedReadAudience(context.Background(), readExchangeRequest(t, parent))
	require.Error(t, err)
	require.Nil(t, out)
}

type readExchangeAuthority struct {
	business.WorkContextAuthorityStore
	facts *business.WorkContextAuthorityFacts
	after func()
	calls int
}

func (a *readExchangeAuthority) ResolveWorkContextAuthority(ctx context.Context, org, owner, actor string, _ []business.WorkContextPermission) (*business.WorkContextAuthorityFacts, error) {
	if err := auth.RequireVerifiedDatabaseScope(ctx, org, owner); err != nil {
		return nil, err
	}
	result := *a.facts
	if actor != "" {
		result.Actor = &business.Principal{ID: actor}
	}
	a.calls++
	if a.after != nil {
		a.after()
	}
	return &result, nil
}

type readExchangeJournal struct {
	business.ActorChainJournal
	revoked string
	ids     []string
}

func (j *readExchangeJournal) AnyActorChainHopRevoked(_ context.Context, _ string, ids []string) (bool, error) {
	j.ids = append([]string(nil), ids...)
	for _, id := range ids {
		if id == j.revoked {
			return true, nil
		}
	}
	return false, nil
}
func TestDelegatedReadExchangePreservesChainAndRechecksAuthority(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	installReadBinding()
	authority := &readExchangeAuthority{facts: facts.facts}
	workContextSingleton.authority = authority
	journal := &readExchangeJournal{}
	workContextSingleton.journal = journal
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "results", Actions: []string{"read"}}}
	actors := []*basev0.WorkActorV1{{PrincipalId: "actor-1", PrincipalKind: "service", DelegationId: "hop-1", GrantedScopes: scopes}, {PrincipalId: "actor-2", PrincipalKind: "service", DelegationId: "hop-2", GrantedScopes: scopes}}
	token, _, err := workContextSingleton.signer.StartTask(codefly.StartTaskInput{Audience: "example", TenantID: readOrg, OwnerPrincipalID: readOwner, TaskID: "task", SessionID: "session", AuthorizationRevision: facts.facts.EffectiveRevision(), AuthorityScopes: scopes, ActorChain: actors})
	require.NoError(t, err)
	invoke := func() (*connect.Response[gen.IssuedWorkContext], error) {
		return client.ExchangeDelegatedReadAudience(context.Background(), readExchangeRequest(t, token.Encoded()))
	}
	out, err := invoke()
	require.NoError(t, err)
	childToken, err := codefly.ParseWorkContextToken(out.Msg.Token)
	require.NoError(t, err)
	child, err := workContextSingleton.verifier.Verify(childToken, codefly.WorkContextExpectations{Audience: "example-producer"})
	require.NoError(t, err)
	require.Len(t, child.ActorChain, 2)
	for i, actor := range actors {
		require.True(t, proto.Equal(actor, child.ActorChain[i]))
	}
	require.Equal(t, []string{"hop-1", "hop-2"}, journal.ids)
	for _, hop := range journal.ids {
		journal.revoked = hop
		out, err = invoke()
		require.Error(t, err)
		require.Nil(t, out)
	}
	journal.revoked = ""
	authority.after = func() { journal.revoked = "hop-1" }
	out, err = invoke()
	require.Error(t, err)
	require.Nil(t, out)
	authority.after = nil
	journal.revoked = ""
	workContextSingleton.journal = nil
	out, err = invoke()
	require.Error(t, err)
	require.Nil(t, out)
}

func TestDelegatedReadExchangeModuleAndParentBinding(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	installReadBinding()
	for _, tc := range []struct {
		name, owner, tenant, audience, module, moduleTenant string
		kind, refusal                                       string
		ids                                                 []string
	}{
		{name: "foreign module", owner: readOwner, tenant: readOrg, audience: "example", module: "foreign", moduleTenant: readOrg},
		{name: "foreign module tenant", owner: readOwner, tenant: readOrg, audience: "example", module: "example", moduleTenant: "019f6bf7-0000-7000-8000-000000000001"},
		{name: "foreign parent tenant", owner: readOwner, tenant: "019f6bf7-0000-7000-8000-000000000001", audience: "example", module: "example", moduleTenant: readOrg},
		{name: "narrow resource cannot become kind wide", owner: readOwner, tenant: readOrg, audience: "example", module: "example", moduleTenant: readOrg, ids: []string{"one-result"}},
		// The third widening axis. The binding is deployment-owned policy, so a
		// kind the parent never carried must be refused as it is for actions and
		// resource ids — asserted on the widening refusal itself, since every
		// other gate here would also produce a bare error.
		{name: "binding cannot name a kind the parent lacks", owner: readOwner, tenant: readOrg, audience: "example", module: "example", moduleTenant: readOrg,
			kind: "ledger", refusal: "widens authority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := tc.kind
			if kind == "" {
				kind = "results"
			}
			parent, _, err := workContextSingleton.signer.StartTask(codefly.StartTaskInput{Audience: tc.audience, TenantID: tc.tenant, OwnerPrincipalID: tc.owner, TaskID: "task", SessionID: "session", AuthorizationRevision: facts.facts.EffectiveRevision(), AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: kind, Actions: []string{"read"}, ResourceIds: tc.ids}}})
			require.NoError(t, err)
			module, _, err := workContextSingleton.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID(tc.module), Tenant: tc.moduleTenant})
			require.NoError(t, err)
			req := readExchangeRequest(t, parent.Encoded())
			req.Header().Set(codefly.WorkContextHeaderName, module.Encoded())
			out, err := client.ExchangeDelegatedReadAudience(context.Background(), req)
			require.Error(t, err)
			require.Nil(t, out)
			if tc.refusal != "" {
				require.ErrorContains(t, err, tc.refusal)
			}
		})
	}
}
func TestDelegatedReadExchangeRechecksInstalledBinding(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	installReadBinding()
	parent := mint("example", "results", "read")
	authority := &readExchangeAuthority{facts: facts.facts, after: func() { service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{}) }}
	workContextSingleton.authority = authority
	out, err := client.ExchangeDelegatedReadAudience(context.Background(), readExchangeRequest(t, parent))
	require.Error(t, err)
	require.Nil(t, out)
}
