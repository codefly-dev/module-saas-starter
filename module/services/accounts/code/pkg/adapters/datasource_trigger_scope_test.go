package adapters

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
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
