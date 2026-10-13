package adapters

import (
	"context"
	"net/http"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/business/moduleregistrytest"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

// A capability scoped to one source may reconcile THAT source and no other
// (issue #1112, M1).
//
// This is the refusal #1090 shipped without. Both reconcile RPCs are
// sourceOperationProcedure, so a delegated child authenticates as the
// delegating person — and a reference is only ever recorded under someone who
// holds the connect role, which is an organization administrator. Tenant,
// membership and role are therefore satisfied for every source in the
// organization, and nothing else looked at the capability's own scopes. A
// module holding a child for source A could reconcile source B, C and every
// other source in that organization.
//
// The discriminator is deliberately not "an error came back": with no receipt
// store configured, a request that CLEARS the scope gate fails afterwards with
// Unavailable, and one the gate refuses never reaches it. So the two codes
// prove the gate fired on the source id rather than on anything else about the
// request.
func TestReconcileSourceRefusesACapabilityScopedToAnotherSource(t *testing.T) {
	const (
		org     = "11111111-1111-4111-8111-111111111111"
		actor   = "22222222-2222-4222-8222-222222222222"
		scoped  = "44444444-4444-4444-8444-444444444444"
		another = "55555555-5555-4555-8555-555555555555"
	)
	// An administrator, because that is who a reference borrows authority from:
	// the role is satisfied for every source in the organization, which is
	// precisely why the scope and not the role has to bind the source.
	store := &sourceWireStore{
		role:   gen.OrgRole_ORG_ROLE_ADMIN,
		source: &business.DatasourceSource{ID: scoped, OrgID: org, BoundaryNodeID: "boundary"},
	}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetAuditEmitter(&sourceWireAudit{})
	previous, cache := service, orgMembershipCache
	service, orgMembershipCache = svc, nil
	t.Cleanup(func() { service, orgMembershipCache = previous, cache })

	server := newCoreTestServer(t, &workContextAuthorityFake{}, newSealAnswersFake(1))
	_, claims := mintTestCapability(t, server, testCapability{
		Audience: business.SourceOperationAudience, Tenant: org, Owner: actor, Task: "occurrence",
		Scopes: []*basev0.WorkScopeV1{{
			ResourceKind: "datasource.sources",
			Actions:      []string{"invoke", "read"},
			ResourceIds:  []string{scoped},
		}},
	})
	ctx := stampVerifiedIdentity(t.Context(), actor, org, auth.Assurance{})
	ctx = context.WithValue(ctx, sourceOperationContextKey{}, claims)
	handler := &datasourceConnectHandler{svc: svc}

	_, err = handler.ReconcileSource(ctx, connect.NewRequest(&gen.ReconcileSourceRequest{
		OrgId: org, SourceId: another, EffectId: "an occurrence of its own",
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"a capability scoped to one source reconciled another")

	// The same call for the source the capability names is NOT refused by the
	// scope gate — it gets as far as the receipt store this fixture has none of.
	_, err = handler.ReconcileSource(ctx, connect.NewRequest(&gen.ReconcileSourceRequest{
		OrgId: org, SourceId: scoped, EffectId: "an occurrence of its own",
	}))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err),
		"the scope gate refused the source the capability actually names")

	// And an unconstrained kind is not an installation-selected source slot:
	// "every datasource.sources" is the shape that would make the check above
	// pass for any source at all.
	claims.AuthorityScopes[0].ResourceIds = nil
	_, err = handler.ReconcileSource(ctx, connect.NewRequest(&gen.ReconcileSourceRequest{
		OrgId: org, SourceId: scoped, EffectId: "an occurrence of its own",
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"an unconstrained datasource.sources scope reconciled a source")
}

// The lookup half takes the same gate, under the read action: recovering an
// occurrence's receipt is strictly less than producing its effect, and a
// capability scoped to another source may do neither.
func TestLookupReconcileSourceTakesTheSameScopeGate(t *testing.T) {
	const (
		org     = "11111111-1111-4111-8111-111111111111"
		actor   = "22222222-2222-4222-8222-222222222222"
		scoped  = "44444444-4444-4444-8444-444444444444"
		another = "55555555-5555-4555-8555-555555555555"
		effect  = "an occurrence of its own"
	)
	// The attempt row is what names the source a lookup is about, so the
	// refusal has to come from the ATTEMPT's source and not from the request.
	store := &sourceWireStore{
		role:    gen.OrgRole_ORG_ROLE_ADMIN,
		source:  &business.DatasourceSource{ID: another, OrgID: org, BoundaryNodeID: "boundary"},
		attempt: &business.SourceOperationAttempt{OrgID: org, ActorID: actor, SourceID: another, Operation: "_reconcile", EffectID: effect},
	}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetAuditEmitter(&sourceWireAudit{})
	previous, cache := service, orgMembershipCache
	service, orgMembershipCache = svc, nil
	t.Cleanup(func() { service, orgMembershipCache = previous, cache })

	server := newCoreTestServer(t, &workContextAuthorityFake{}, newSealAnswersFake(1))
	_, claims := mintTestCapability(t, server, testCapability{
		Audience: business.SourceOperationAudience, Tenant: org, Owner: actor, Task: "occurrence",
		Scopes: []*basev0.WorkScopeV1{{
			ResourceKind: "datasource.sources",
			Actions:      []string{"invoke", "read"},
			ResourceIds:  []string{scoped},
		}},
	})
	ctx := stampVerifiedIdentity(t.Context(), actor, org, auth.Assurance{})
	ctx = context.WithValue(ctx, sourceOperationContextKey{}, claims)

	_, err = (&datasourceConnectHandler{svc: svc}).LookupReconcileSource(ctx,
		connect.NewRequest(&gen.LookupReconcileSourceRequest{EffectId: effect}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"a capability scoped to one source read back another source's receipt")
}

// A child minted from ONE source's delegation may not act on another source the
// same binding happens to be scoped to (N2 on PR #1115).
//
// This is the hole a scope check alone cannot close, and the test is written
// end to end rather than by stuffing a context value, because the value it
// depends on is set by production code: a hand-set key would pass for a check
// nothing populates.
//
// The shape is a composition's to declare and this is a legitimate one: one
// binding, `datasource.sources` scoped to TWO sources, because a module that
// syncs several sources declares them all. The delegation, though, is recorded
// per source. So the mint — which seals the binding's declared scopes verbatim
// — hands back a capability whose scopes name both sources while the authority
// inside it came from exactly one person's delegation of exactly one of them.
//
// Before this fix the second source was reconcilable under the first source's
// delegator, and revoking the second source's delegation did not stop it: the
// mint never read it.
func TestAChildFromOneSourcesDelegationIsRefusedOnAnother(t *testing.T) {
	const (
		borrowed = "88888888-8888-4888-8888-888888888888" // the delegation's own source
		other    = "99999999-9999-4999-8999-999999999999" // another source the binding is scoped to
	)
	// audience saas-datasource, because that is the audience the source
	// operation surface verifies a presented capability for.
	principals := `{"docstore":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},` +
		`"tenant":"` + moduleWorkContextTenant + `","cross_tenant":true,"operation_audiences":{` +
		`"source-sync":{"audience":"` + business.SourceOperationAudience + `",` +
		`"invoke_scopes":[{"resource_kind":"datasource.sources","actions":["invoke","read"],"resource_ids":["` + borrowed + `","` + other + `"]}],` +
		`"source_delegation_scopes":[{"resource_kind":"datasource.sources","actions":["invoke","read"],"resource_ids":["` + borrowed + `","` + other + `"]}]}}}}`

	store := installDelegationServiceWith(t, principals, borrowed)

	minted, err := mintSourceContext("docstore", moduleToken(t, "docstore"),
		business.SourceDelegationRef{SourceID: borrowed})
	require.NoError(t, err)

	// The capability really is scoped to both sources — if it were not, the
	// refusal below would prove nothing, because the scope gate would catch it.
	signed := verifySourceContext2(t, minted.GetToken())
	require.Len(t, signed.GetAuthorityScopes(), 1)
	require.ElementsMatch(t, []string{borrowed, other}, signed.GetAuthorityScopes()[0].GetResourceIds(),
		"the mint is meant to seal the binding's declared scopes verbatim; this test is about what that does not say")

	headers := http.Header{}
	headers.Set(workContextHeader, minted.GetToken())
	authed, err := authenticateSourceOperationContext(t.Context(), headers)
	require.NoError(t, err, "the capability must authenticate; the refusal under test is about the source, not the token")

	// The source the authority was borrowed from is admitted...
	require.NoError(t, requireSourceOperationScope(authed, delegationOrg, borrowed, "invoke"))
	// ...and the other source the same scope names is not.
	err = requireSourceOperationScope(authed, delegationOrg, other, "invoke")
	require.Error(t, err, "a child from one source's delegation acted on another source")
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Contains(t, err.Error(), "does not cover this source")

	// Revoking the OTHER source's delegation is not what stops it, and that is
	// the point: the mint never read that delegation, so there was nothing to
	// revoke. The refusal comes from the delegation the capability actually
	// carries.
	require.Nil(t, store.activeOn(other), "fixture: the other source holds no delegation at all")
}

// installDelegationServiceWith is installSourceDelegationService with the
// principals and the delegated source given, so a test can declare the binding
// shape it is about. It returns the store so a test can assert which sources
// actually hold a delegation.
func installDelegationServiceWith(t *testing.T, principals, sourceID string) *sourceDelegationMemoryStore {
	t.Helper()
	previous := service
	t.Cleanup(func() { service = previous })
	registry, err := moduleregistrytest.Parse(principals)
	require.NoError(t, err)
	binding := registry[business.ModulePrincipalID("docstore")].OperationAudiences["source-sync"]
	store := &sourceDelegationMemoryStore{
		delegations: map[string]*business.SourceDelegation{delegationID: {
			ID: delegationID, OrgID: delegationOrg, SourceID: sourceID, PrincipalID: delegationPerson,
			ModulePrefix: "docstore", BindingID: "source-sync",
			BindingDigest: business.SourceDelegationBindingDigest(binding), CreatedAt: time.Now(),
		}},
		facts: business.SourceDelegationFacts{
			SourceExists: true, MemberRole: "admin", UserStatus: "active",
			OrganizationRevision: 40, PrincipalRevision: 42,
		},
	}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, registry)
	bindModuleExecution(t, svc, registry)
	svc.SetAuditEmitter(&validatingAudit{})
	WithService(svc)
	installModuleWorkContextAuthority(t)
	workContextSingleton.seals.(*sealAnswersFake).revisionFrom = func() uint64 { return store.facts.EffectiveRevision() }
	return store
}

// activeOn reports the active delegation of one source, or nil.
func (s *sourceDelegationMemoryStore) activeOn(sourceID string) *business.SourceDelegation {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.delegations {
		if d.SourceID == sourceID && d.Active() {
			return d
		}
	}
	return nil
}

// verifySourceContext2 verifies a capability for the SOURCE OPERATION audience,
// which is the audience the source surface presents one to.
func verifySourceContext2(t *testing.T, token string) *basev0.WorkContextV1 {
	t.Helper()
	verified, err := WorkContextSingleton().verifyFor(t.Context(), token, business.SourceOperationAudience)
	require.NoError(t, err)
	return verified.Context()
}
