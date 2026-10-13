package adapters

import (
	"context"
	"net/http"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
)

// Use the real mint and verifier: a hand-stamped delegation key cannot prove
// that authentication preserves the source whose authority the child carries.
func TestSourceOperationDelegatedRefusalAudited(t *testing.T) {
	const borrowed = "88888888-8888-4888-8888-888888888888"
	const other = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	principals := `{"docstore":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},` +
		`"tenant":"` + moduleWorkContextTenant + `","cross_tenant":true,"operation_audiences":{` +
		`"source-sync":{"audience":"` + business.SourceOperationAudience + `",` +
		`"invoke_scopes":[{"resource_kind":"datasource.sources","actions":["invoke","read"],"resource_ids":["` + borrowed + `","` + other + `"]}],` +
		`"source_delegation_scopes":[{"resource_kind":"datasource.sources","actions":["invoke","read"],"resource_ids":["` + borrowed + `","` + other + `"]}]}}}}`
	installDelegationServiceWith(t, principals, borrowed)
	minted, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{SourceID: borrowed})
	require.NoError(t, err)
	claims := verifySourceContext2(t, minted.GetToken())
	require.ElementsMatch(t, []string{borrowed, other}, claims.GetAuthorityScopes()[0].GetResourceIds())
	headers := http.Header{}
	headers.Set(workContextHeader, minted.GetToken())
	ctx, err := authenticateSourceOperationContext(t.Context(), headers)
	require.NoError(t, err)
	require.NoError(t, requireSourceOperationScope(ctx, delegationOrg, borrowed, "invoke"))

	for _, method := range []string{"invoke", "lookup", "prune", "lookup-prune"} {
		t.Run(method, func(t *testing.T) {
			store := &sourceRefusalStore{sourceWireStore: &sourceWireStore{role: gen.OrgRole_ORG_ROLE_ADMIN,
				attempt: &business.SourceOperationAttempt{OrgID: delegationOrg, ActorID: delegationPerson, SourceID: other, Operation: "read_item", EffectID: "refused-effect"}}}
			svc, err := business.NewService(store)
			require.NoError(t, err)
			audit := &sourceWireAudit{}
			svc.SetAuditEmitter(audit)
			previous, cache := service, orgMembershipCache
			service, orgMembershipCache = svc, nil
			t.Cleanup(func() { service, orgMembershipCache = previous, cache })
			h := &datasourceConnectHandler{svc: svc}
			switch method {
			case "invoke":
				_, err = h.InvokeSourceOperation(ctx, connect.NewRequest(&gen.InvokeSourceOperationRequest{OrgId: delegationOrg, SourceId: other, Operation: "read_item", InputJson: `{"private":"must not enter audit"}`, EffectId: "refused-effect"}))
			case "lookup":
				_, err = h.LookupInvokeSourceOperation(ctx, connect.NewRequest(&gen.LookupInvokeSourceOperationRequest{EffectId: "refused-effect"}))
			case "prune":
				_, err = h.PruneSourceOperationReceipts(ctx, connect.NewRequest(&gen.PruneSourceOperationReceiptsRequest{OrgId: delegationOrg, SourceId: other, EffectId: "refused-effect"}))
			case "lookup-prune":
				store.attempt.Operation = "_receipt_retention"
				_, err = h.LookupPruneSourceOperationReceipts(ctx, connect.NewRequest(&gen.LookupPruneSourceOperationReceiptsRequest{EffectId: "refused-effect"}))
			}
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			require.EqualError(t, err, "permission_denied: source delegation does not cover this source")
			require.Zero(t, store.sourceReads, "refusal must precede source resolution and dispatch")
			require.Zero(t, store.prunes)
			require.Len(t, audit.events, 1, "one attributed refusal must reach the owner audit spine")
			event := audit.events[0]
			require.Equal(t, business.EventDatasourceOperationInvoked, event.EventType)
			require.Equal(t, delegationOrg, event.OrgID)
			require.Equal(t, delegationPerson, event.ActorID)
			require.Equal(t, "user", event.ActorType)
			require.Equal(t, "datasource", event.Resource)
			require.Equal(t, other, event.ResourceID)
			require.Equal(t, map[string]any{"operation": "unknown", "effect": "UNKNOWN", "effect_id": "refused-effect", "outcome": "refused"}, event.Payload)
		})
	}
}

// A canceled client must not cancel the refusal's independent audit write.
type sourceRefusalAuditContext struct {
	sourceWireAudit
	err     error
	bounded bool
}

func (a *sourceRefusalAuditContext) Emit(ctx context.Context, event business.AuditEntry) {
	a.err = ctx.Err()
	_, a.bounded = ctx.Deadline()
	a.sourceWireAudit.Emit(ctx, event)
}

// Any source resolution would enter the engine's authority/dispatch path.
type sourceRefusalStore struct {
	*sourceWireStore
	sourceReads int
}

func (s *sourceRefusalStore) GetDatasourceSource(ctx context.Context, org, source string) (*business.DatasourceSource, error) {
	s.sourceReads++
	return s.sourceWireStore.GetDatasourceSource(ctx, org, source)
}

func TestSourceOperationScopeRefusalAuditSurvivesCancellation(t *testing.T) {
	const org = "11111111-1111-4111-8111-111111111111"
	const actor = "22222222-2222-4222-8222-222222222222"
	const source = "33333333-3333-4333-8333-333333333333"
	for _, tc := range []struct {
		name         string
		actions, ids []string
		message      string
	}{
		{"wrong source", []string{"invoke"}, []string{"another"}, "source scope required"},
		{"read only", []string{"read"}, []string{source}, "source scope required"},
		{"unconstrained", []string{"invoke"}, nil, "explicit source scope required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &sourceRefusalStore{sourceWireStore: &sourceWireStore{}}
			svc, err := business.NewService(store)
			require.NoError(t, err)
			audit := &sourceRefusalAuditContext{}
			svc.SetAuditEmitter(audit)
			previous, cache := service, orgMembershipCache
			service, orgMembershipCache = svc, nil
			t.Cleanup(func() { service, orgMembershipCache = previous, cache })
			ctx := stampVerifiedIdentity(t.Context(), actor, org, auth.Assurance{})
			ctx = context.WithValue(ctx, sourceOperationContextKey{}, &basev0.WorkContextV1{TenantId: org, OwnerPrincipalId: actor, AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: "datasource.sources", Actions: tc.actions, ResourceIds: tc.ids}}})
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			_, err = (&datasourceConnectHandler{svc: svc}).InvokeSourceOperation(ctx, connect.NewRequest(&gen.InvokeSourceOperationRequest{OrgId: org, SourceId: source, Operation: "read_item", InputJson: "{}", EffectId: "refused-effect"}))
			require.EqualError(t, err, "permission_denied: "+tc.message)
			require.Zero(t, store.sourceReads)
			require.Len(t, audit.events, 1)
			require.Equal(t, "refused", audit.events[0].Payload["outcome"])
			require.NoError(t, audit.err, "request cancellation must not cancel audit")
			require.True(t, audit.bounded, "audit must retain a bounded write deadline")
		})
	}
}

func TestSourceOperationLookupUnownedEffectNeverAudits(t *testing.T) {
	const org = "11111111-1111-4111-8111-111111111111"
	const actor = "22222222-2222-4222-8222-222222222222"
	for _, owner := range []string{"", "another-member"} {
		t.Run("owner="+owner, func(t *testing.T) {
			store := &sourceRefusalStore{sourceWireStore: &sourceWireStore{}}
			if owner != "" {
				store.attempt = &business.SourceOperationAttempt{OrgID: org, ActorID: owner, SourceID: "source", EffectID: "effect"}
			}
			svc, err := business.NewService(store)
			require.NoError(t, err)
			audit := &sourceWireAudit{}
			svc.SetAuditEmitter(audit)
			previous, cache := service, orgMembershipCache
			service, orgMembershipCache = svc, nil
			t.Cleanup(func() { service, orgMembershipCache = previous, cache })
			ctx := stampVerifiedIdentity(t.Context(), actor, org, auth.Assurance{})
			h := &datasourceConnectHandler{svc: svc}
			invoke, err := h.LookupInvokeSourceOperation(ctx, connect.NewRequest(&gen.LookupInvokeSourceOperationRequest{EffectId: "effect"}))
			require.Error(t, err)
			require.Nil(t, invoke)
			prune, err := h.LookupPruneSourceOperationReceipts(ctx, connect.NewRequest(&gen.LookupPruneSourceOperationReceiptsRequest{EffectId: "effect"}))
			require.Error(t, err)
			require.Nil(t, prune)
			require.Zero(t, store.sourceReads)
			require.Empty(t, audit.events, "an unknown or foreign actor's effect must not select audit content")
		})
	}
}
