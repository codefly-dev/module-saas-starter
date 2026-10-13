package adapters

import (
	"context"
	"slices"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

func installReadBinding() {
	service.SetModuleAuthorityReads(currentModuleAuthority{service: service}, nil)
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
	token := mintModuleIdentity(t, business.ModulePrincipalID("example"), readOrg)
	req := connect.NewRequest(&gen.ModuleExchangeDelegatedReadAudienceRequest{BindingId: "proof", ParentWorkContextToken: parent})
	req.Header().Set(workContextHeader, token)
	req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
	return req
}
func TestDelegatedReadExchangeOwnerAndDenials(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	installReadBinding()
	parent := mint("example", "results", "read", "unrelated:write")
	out, err := client.ExchangeDelegatedReadAudience(context.Background(), readExchangeRequest(t, parent))
	require.NoError(t, err)
	verified, err := workContextSingleton.verifyFor(context.Background(), out.Msg.Token, "example-producer")
	require.NoError(t, err)
	child := verified.Context()
	require.Equal(t, readOwner, child.OwnerPrincipalId)
	require.Equal(t, readOrg, child.TenantId)
	require.Equal(t, "019f6bf7-1111-7111-8111-111111111111", child.TaskId)
	// Re-issued by a module, so in a fresh session: never the parent's copied.
	require.NotEqual(t, "019f6bf7-2222-7222-8222-222222222222", child.SessionId)
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
			r.Header().Set(workContextHeader, "forged")
		}},
		{"parent is not module credential", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Header().Set(workContextHeader, parent)
		}},
		{"duplicate carrier", func(r *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) {
			r.Header().Add(workContextHeader, r.Header().Get(workContextHeader))
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
	_, _, client, _ := sourceReadFixture(t)
	service.SetModuleAuthorityReads(currentModuleAuthority{service: service}, nil)
	service.SetModuleCapabilities(nil, nil, installedOperationRegistry(true))
	parent, _ := mintTestCapability(t, workContextSingleton, testCapability{
		Audience: "example", Tenant: readOrg, Owner: readOwner,
		Task: "019f6bf7-1111-7111-8111-111111111111", Session: "019f6bf7-2222-7222-8222-222222222222",
		Scopes: []*basev0.WorkScopeV1{
			{ResourceKind: "receipts", Actions: []string{"append", "read"}},
			{ResourceKind: "results", Actions: []string{"read", "write"}, ResourceIds: []string{"result-1"}},
		},
	})
	exchange := func(binding string, lookup bool) (*basev0.WorkContextV1, error) {
		module := mintModuleIdentity(t, business.ModulePrincipalID("example"), readOrg)
		req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: binding, ParentWorkContextToken: parent, Lookup: lookup})
		req.Header().Set(workContextHeader, module)
		req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
		out, err := client.ExchangeDelegatedOperationAudience(context.Background(), req)
		if err != nil {
			return nil, err
		}
		verified, err := workContextSingleton.verifyIssued(context.Background(), out.Msg.Token)
		if err != nil {
			return nil, err
		}
		return verified.Context(), nil
	}

	invoked, err := exchange("generate", false)
	require.NoError(t, err)
	require.Equal(t, "example-producer", invoked.Audience)
	require.True(t, grantsExplicitly(invoked, "results", "write", "result-1"))

	lookup, err := exchange("generate", true)
	require.NoError(t, err)
	require.False(t, grantsExplicitly(lookup, "results", "write", "result-1"))
	require.True(t, grantsExplicitly(lookup, "results", "read", "result-1"))

	service.SetModuleAuthorityReads(currentModuleAuthority{service: service}, nil)
	service.SetModuleCapabilities(nil, nil, installedOperationRegistry(false))
	_, err = exchange("generate", false)
	require.Error(t, err)
	recorded, err := exchange("record", false)
	require.NoError(t, err)
	require.Equal(t, "example-receipts", recorded.Audience)
	require.True(t, workContextGrants(recorded, "receipts", "append", "any"))
}

func TestDelegatedOperationExchangeAuditsVerifiedAttributionAndOutcome(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	service.SetModuleAuthorityReads(currentModuleAuthority{service: service}, nil)
	service.SetModuleCapabilities(nil, nil, installedOperationRegistry(true))
	audit := &recordingAuditEmitter{}
	service.SetAuditEmitter(audit)
	parent := mint("example", "results", "read")

	exchange := func(binding string) error {
		module := mintModuleIdentity(t, business.ModulePrincipalID("example"), readOrg)
		req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: binding, ParentWorkContextToken: parent, Lookup: true})
		req.Header().Set(workContextHeader, module)
		req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
		_, err := client.ExchangeDelegatedOperationAudience(context.Background(), req)
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
	actors := []delegationHop{agentHop("actor-1", "hop-1", scopes...), agentHop("actor-2", "hop-2", scopes...)}
	token, _ := mintTestCapability(t, workContextSingleton, testCapability{Audience: "example", Tenant: readOrg, Owner: readOwner, Task: "task", Session: "session", Scopes: scopes, Actors: actors})
	invoke := func() (*connect.Response[gen.IssuedWorkContext], error) {
		return client.ExchangeDelegatedReadAudience(context.Background(), readExchangeRequest(t, token))
	}
	out, err := invoke()
	require.NoError(t, err)
	verified, err := workContextSingleton.verifyFor(context.Background(), out.Msg.Token, "example-producer")
	require.NoError(t, err)
	child := verified.Context()
	require.Len(t, child.ActorChain, 2)
	// The chain is re-derived hop by hop, so each hop is the same delegation —
	// principal, kind, delegation id — carrying its live epoch.
	for i, actor := range actors {
		require.Equal(t, actor.PrincipalID, child.ActorChain[i].GetPrincipalId())
		require.Equal(t, actor.PrincipalKind, child.ActorChain[i].GetPrincipalKind())
		require.Equal(t, actor.DelegationID, child.ActorChain[i].GetDelegationId())
		require.NotZero(t, child.ActorChain[i].GetPrincipalEpoch())
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
	_, _, client, _ := sourceReadFixture(t)
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
			parent, _ := mintTestCapability(t, workContextSingleton, testCapability{Audience: tc.audience, Tenant: tc.tenant, Owner: tc.owner, Task: "task", Session: "session",
				Scopes: []*basev0.WorkScopeV1{{ResourceKind: kind, Actions: []string{"read"}, ResourceIds: tc.ids}}})
			module := mintModuleIdentity(t, business.ModulePrincipalID(tc.module), tc.moduleTenant)
			req := readExchangeRequest(t, parent)
			req.Header().Set(workContextHeader, module)
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

// grantsExplicitly reports whether claims' effective authority names resourceID
// explicitly for action — a wildcard over the kind does not count.
func grantsExplicitly(claims *basev0.WorkContextV1, resourceKind, action, resourceID string) bool {
	effective := claims.GetAuthorityScopes()
	if chain := claims.GetActorChain(); len(chain) > 0 {
		effective = chain[len(chain)-1].GetGrantedScopes()
	}
	for _, scope := range effective {
		if scope.GetResourceKind() == resourceKind && slices.Contains(scope.GetActions(), action) &&
			slices.Contains(scope.GetResourceIds(), resourceID) {
			return true
		}
	}
	return false
}
