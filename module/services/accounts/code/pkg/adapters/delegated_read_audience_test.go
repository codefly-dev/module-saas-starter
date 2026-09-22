package adapters

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	workcontext "github.com/codefly-dev/sdk-go/workcontext"
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

func installedOperationRegistry(includeGenerate bool) business.ModulePrincipalRegistry {
	operations := map[string]business.ModuleOperationAudience{
		"record": {
			Audience:     "example-receipts",
			InvokeScopes: []business.ModuleOperationScope{{ResourceKind: "receipts", Actions: []string{"append", "read"}}},
			LookupScopes: []business.ModuleOperationScope{{ResourceKind: "receipts", Actions: []string{"read"}}},
		},
	}
	if includeGenerate {
		operations["generate"] = business.ModuleOperationAudience{
			Audience:     "example-producer",
			InvokeScopes: []business.ModuleOperationScope{{ResourceKind: "results", Actions: []string{"read", "write"}, ResourceIDs: []string{"result-1"}}},
			LookupScopes: []business.ModuleOperationScope{{ResourceKind: "results", Actions: []string{"read"}, ResourceIDs: []string{"result-1"}}},
		}
	}
	return business.ModulePrincipalRegistry{
		business.ModulePrincipalID("example"): {Prefix: "example", Tenant: readOrg, OperationAudiences: operations},
	}
}
func readExchangeRequest(t *testing.T, parent string) *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest] {
	t.Helper()
	token, _, err := workContextSingleton.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("example"), Tenant: readOrg})
	require.NoError(t, err)
	req := connect.NewRequest(&gen.ModuleExchangeDelegatedReadAudienceRequest{BindingId: "proof", ParentWorkContextToken: parent})
	req.Header().Set(workcontext.WorkContextHeaderName, token.Encoded())
	req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
	return req
}
func TestDelegatedReadExchangeOwnerAndDenials(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	installReadBinding()
	parent := mint("example", "results", "read", "unrelated:write")
	out, err := client.ExchangeDelegatedReadAudience(context.Background(), readExchangeRequest(t, parent))
	require.NoError(t, err)
	token, err := workcontext.ParseWorkContextToken(out.Msg.Token)
	require.NoError(t, err)
	child, err := workContextSingleton.verifier.Verify(token, workcontext.WorkContextExpectations{Issuer: "accounts.test", Audience: "example-producer"})
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
			r.Header().Set(workcontext.WorkContextHeaderName, "forged")
		}},
		{"parent is not module credential", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Header().Set(workcontext.WorkContextHeaderName, parent)
		}},
		{"duplicate carrier", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Header().Add(workcontext.WorkContextHeaderName, r.Header().Get(workcontext.WorkContextHeaderName))
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

func TestDelegatedOperationExchangeUsesInstalledScopesAndIsolatesRevocation(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	service.SetModuleCapabilities(nil, nil, installedOperationRegistry(true))
	parentToken, _, err := workContextSingleton.signer.StartTask(workcontext.StartTaskInput{
		Audience: "example", TenantID: readOrg, OwnerPrincipalID: readOwner,
		TaskID: "019f6bf7-1111-7111-8111-111111111111", SessionID: "019f6bf7-2222-7222-8222-222222222222",
		AuthorizationRevision: facts.facts.EffectiveRevision(), ReplayPolicy: workcontext.WorkContextReplayIdempotent,
		AuthorityScopes: []*basev0.WorkScopeV1{
			{ResourceKind: "receipts", Actions: []string{"append", "read"}},
			{ResourceKind: "results", Actions: []string{"read", "write"}, ResourceIds: []string{"result-1"}},
		},
	})
	require.NoError(t, err)
	parent := parentToken.Encoded()
	exchange := func(binding string, lookup bool) (*basev0.WorkContextV1, error) {
		module, _, err := workContextSingleton.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("example"), Tenant: readOrg})
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
		token, err := workcontext.ParseWorkContextToken(out.Msg.Token)
		if err != nil {
			return nil, err
		}
		return workContextSingleton.verifier.Verify(token, workcontext.WorkContextExpectations{Issuer: "accounts.test"})
	}

	invoked, err := exchange("generate", false)
	require.NoError(t, err)
	require.Equal(t, "example-producer", invoked.Audience)
	require.NoError(t, workcontext.RequireWorkContextScope(invoked, workcontext.WorkContextScopeRequirement{ResourceKind: "results", ResourceID: "result-1", Action: "write", RequireExplicitResource: true}))

	lookup, err := exchange("generate", true)
	require.NoError(t, err)
	require.Error(t, workcontext.RequireWorkContextScope(lookup, workcontext.WorkContextScopeRequirement{ResourceKind: "results", ResourceID: "result-1", Action: "write", RequireExplicitResource: true}))
	require.NoError(t, workcontext.RequireWorkContextScope(lookup, workcontext.WorkContextScopeRequirement{ResourceKind: "results", ResourceID: "result-1", Action: "read", RequireExplicitResource: true}))

	service.SetModuleCapabilities(nil, nil, installedOperationRegistry(false))
	_, err = exchange("generate", false)
	require.Error(t, err)
	recorded, err := exchange("record", false)
	require.NoError(t, err)
	require.Equal(t, "example-receipts", recorded.Audience)
	require.NoError(t, workcontext.RequireWorkContextScope(recorded, workcontext.WorkContextScopeRequirement{ResourceKind: "receipts", ResourceID: "any", Action: "append"}))
}

func TestDelegatedOperationExchangeAuditsVerifiedAttributionAndOutcome(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	service.SetModuleCapabilities(nil, nil, installedOperationRegistry(true))
	audit := &recordingAuditEmitter{}
	service.SetAuditEmitter(audit)
	parent := mint("example", "results", "read")

	exchange := func(binding string) error {
		module, _, err := workContextSingleton.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("example"), Tenant: readOrg})
		require.NoError(t, err)
		req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: binding, ParentWorkContextToken: parent, Lookup: true})
		req.Header().Set(workcontext.WorkContextHeaderName, module.Encoded())
		req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
		_, err = client.ExchangeDelegatedOperationAudience(context.Background(), req)
		return err
	}

	require.NoError(t, exchange("generate"))
	require.Error(t, exchange("missing"))
	facts.facts.OrganizationRevision++
	require.Error(t, exchange("generate"))
	require.Len(t, audit.entries, 3)
	for i, outcome := range []string{business.DelegatedAudienceExchangeIssued, business.DelegatedAudienceExchangeRefused, business.DelegatedAudienceExchangeRefused} {
		entry := audit.entries[i]
		require.Equal(t, business.EventDelegatedAudienceExchange, entry.EventType)
		require.Equal(t, readOwner, entry.ActorID)
		require.Equal(t, readOrg, entry.OrgID)
		require.Equal(t, readOwner, entry.Payload["owner_principal_id"])
		require.Equal(t, readOwner, entry.Payload["actor_principal_id"])
		require.Equal(t, string(business.ModulePrincipalID("example")), entry.Payload["module_principal_id"])
		require.Equal(t, "operation", entry.Payload["binding_kind"])
		require.Equal(t, true, entry.Payload["lookup"])
		require.Equal(t, outcome, entry.Payload["outcome"])
	}
	require.Equal(t, "example-producer", audit.entries[0].Payload["audience"])
	require.NotContains(t, audit.entries[0].Payload, "refusal_code")
	require.Equal(t, "PermissionDenied", audit.entries[1].Payload["refusal_code"])
	require.NotContains(t, audit.entries[1].Payload, "audience")
	require.Equal(t, "FailedPrecondition", audit.entries[2].Payload["refusal_code"])
	require.NotContains(t, audit.entries[2].Payload, "audience")
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
	token, _, err := workContextSingleton.signer.StartTask(workcontext.StartTaskInput{Audience: "example", TenantID: readOrg, OwnerPrincipalID: readOwner, TaskID: "task", SessionID: "session", AuthorizationRevision: facts.facts.EffectiveRevision(), AuthorityScopes: scopes, ActorChain: actors})
	require.NoError(t, err)
	invoke := func() (*connect.Response[gen.IssuedWorkContext], error) {
		return client.ExchangeDelegatedReadAudience(context.Background(), readExchangeRequest(t, token.Encoded()))
	}
	out, err := invoke()
	require.NoError(t, err)
	childToken, err := workcontext.ParseWorkContextToken(out.Msg.Token)
	require.NoError(t, err)
	child, err := workContextSingleton.verifier.Verify(childToken, workcontext.WorkContextExpectations{Audience: "example-producer"})
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
			parent, _, err := workContextSingleton.signer.StartTask(workcontext.StartTaskInput{Audience: tc.audience, TenantID: tc.tenant, OwnerPrincipalID: tc.owner, TaskID: "task", SessionID: "session", AuthorizationRevision: facts.facts.EffectiveRevision(), AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: kind, Actions: []string{"read"}, ResourceIds: tc.ids}}})
			require.NoError(t, err)
			module, _, err := workContextSingleton.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID(tc.module), Tenant: tc.moduleTenant})
			require.NoError(t, err)
			req := readExchangeRequest(t, parent.Encoded())
			req.Header().Set(workcontext.WorkContextHeaderName, module.Encoded())
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
