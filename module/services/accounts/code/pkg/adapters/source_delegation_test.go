package adapters

import (
	"context"

	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The adapter half of the source-delegation mint, over an in-memory store: the
// signed capability's claims, the refusal codes and reasons a module reads, and
// the revision check a consumer runs at every hop. The same checks against a
// real database are in pkg/infra/source_delegation_test.go.

const (
	delegationOrg    = "66666666-6666-4666-8666-666666666666"
	delegationPerson = "77777777-7777-4777-8777-777777777777"
	delegationSource = "88888888-8888-4888-8888-888888888888"
	delegationID     = "99999999-9999-4999-8999-999999999999"
)

// delegationPrincipals declares a module bound to another tenant, without
// cross_tenant, whose "source-sync" binding accepts source delegations — the
// delegation alone admits the delegation's organization — and a second module
// without one.
const delegationPrincipals = `{"docstore":{"tenant":"` + moduleWorkContextTenant + `","operation_audiences":{` +
	`"source-sync":{"audience":"ingestservice",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
	`"source_delegation_scopes":[{"resource_kind":"collections","actions":["write"]}]}}},` +
	`"reports":{"tenant":"` + delegationOrg + `"}}`

type sourceDelegationMemoryStore struct {
	business.Store
	mu          sync.Mutex
	delegations map[string]*business.SourceDelegation
	facts       business.SourceDelegationFacts
}

func (s *sourceDelegationMemoryStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *sourceDelegationMemoryStore) GetSourceDelegation(_ context.Context, orgID, id string) (*business.SourceDelegation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.delegations[id]
	if !ok || orgID != "" && d.OrgID != orgID {
		return nil, nil
	}
	cp := *d
	return &cp, nil
}

func (s *sourceDelegationMemoryStore) ActiveSourceDelegation(_ context.Context, sourceID, prefix string) (*business.SourceDelegation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.delegations {
		if d.SourceID == sourceID && d.ModulePrefix == prefix && d.Active() {
			cp := *d
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *sourceDelegationMemoryStore) ActiveSourceDelegationsForPrincipal(_ context.Context, orgID, principalID, prefix string) ([]*business.SourceDelegation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*business.SourceDelegation
	for _, d := range s.delegations {
		if d.OrgID == orgID && d.PrincipalID == principalID && d.ModulePrefix == prefix && d.Active() {
			cp := *d
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *sourceDelegationMemoryStore) SourceDelegationFacts(context.Context, string, string, string) (*business.SourceDelegationFacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	facts := s.facts
	return &facts, nil
}

func (s *sourceDelegationMemoryStore) RevokeSourceDelegations(_ context.Context, filter business.SourceDelegationFilter, reason, by string) ([]*business.SourceDelegation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*business.SourceDelegation
	for _, d := range s.delegations {
		if d.OrgID != filter.OrgID || !d.Active() || filter.ID != "" && d.ID != filter.ID {
			continue
		}
		now := time.Now()
		d.RevokedAt, d.RevokedReason, d.RevokedBy = &now, reason, by
		cp := *d
		out = append(out, &cp)
	}
	return out, nil
}

func (s *sourceDelegationMemoryStore) revoke(t *testing.T) {
	t.Helper()
	revoked, err := s.RevokeSourceDelegations(context.Background(), business.SourceDelegationFilter{OrgID: delegationOrg, ID: delegationID}, business.SourceDelegationRevokedByAdmin, delegationPerson)
	require.NoError(t, err)
	require.Len(t, revoked, 1)
}

func installSourceDelegationService(t *testing.T) (*sourceDelegationMemoryStore, *validatingAudit) {
	t.Helper()
	previous := service
	t.Cleanup(func() { service = previous })
	registry, err := business.ParseModulePrincipalRegistry(delegationPrincipals)
	require.NoError(t, err)
	binding := registry[business.ModulePrincipalID("docstore")].OperationAudiences["source-sync"]
	store := &sourceDelegationMemoryStore{
		delegations: map[string]*business.SourceDelegation{delegationID: {
			ID: delegationID, OrgID: delegationOrg, SourceID: delegationSource, PrincipalID: delegationPerson,
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
	secrets, err := business.ParseRegistrationSecrets("docstore:" + registrationDigest("docstore-secret") +
		",reports:" + registrationDigest("reports-secret"))
	require.NoError(t, err)
	svc.SetModuleIdentitySecrets(secrets)
	svc.SetModuleAuthorityReads(currentModuleAuthority{}, nil)
	svc.SetModuleCapabilities(nil, nil, registry)
	audit := &validatingAudit{}
	svc.SetAuditEmitter(audit)
	WithService(svc)
	installModuleWorkContextAuthority(t)
	return store, audit
}

func mintSourceContext(prefix, secret string, ref business.SourceDelegationRef) (*gen.ModuleMintSourceOperationContextResponse, error) {
	req := &gen.ModuleMintSourceOperationContextRequest{Prefix: prefix, Secret: secret}
	if ref.DelegationID != "" {
		req.Delegation = &gen.ModuleMintSourceOperationContextRequest_DelegationId{DelegationId: ref.DelegationID}
	} else {
		req.Delegation = &gen.ModuleMintSourceOperationContextRequest_SourceId{SourceId: ref.SourceID}
	}
	return ModuleCapabilitiesSingleton().MintSourceOperationContext(context.Background(), req)
}

func verifySourceContext(t *testing.T, token string) *basev0.WorkContextV1 {
	t.Helper()
	parsed, err := workcontext.ParseWorkContextToken(token)
	require.NoError(t, err)
	verified, err := WorkContextSingleton().verifier.Verify(parsed, workcontext.WorkContextExpectations{
		Issuer:   WorkContextSingleton().issuer,
		Audience: "ingestservice",
	})
	require.NoError(t, err)
	return verified
}

func requireRefusal(t *testing.T, err error, code codes.Code, reason string) {
	t.Helper()
	require.Equal(t, code, status.Code(err), "%v", err)
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			require.Equal(t, reason, info.GetReason())
			require.Equal(t, SolutionRegistryErrorDomain, info.GetDomain())
			return
		}
	}
	t.Fatalf("refusal %v carries no ErrorInfo", err)
}

// The capability runs in the source's organization, is owned by the person who
// connected the source, is actored by the module principal with the delegation
// on its hop, and carries exactly the binding's delegation scopes.
func TestMintSourceOperationContextSealsTheDelegation(t *testing.T) {
	for name, ref := range map[string]business.SourceDelegationRef{
		"by delegation": {DelegationID: delegationID},
		"by source":     {SourceID: delegationSource},
	} {
		t.Run(name, func(t *testing.T) {
			_, audit := installSourceDelegationService(t)
			module := business.ModulePrincipalID("docstore")

			resp, err := mintSourceContext("docstore", "docstore-secret", ref)
			require.NoError(t, err)
			require.Equal(t, module, resp.GetPrincipalId())
			require.Equal(t, delegationPerson, resp.GetOwnerPrincipalId())
			require.Equal(t, delegationOrg, resp.GetTenant(), "the tenant is the delegation's org, never the module's declared one")
			require.Equal(t, "ingestservice", resp.GetAudience())
			require.Equal(t, "source-sync", resp.GetBinding())
			require.Equal(t, delegationID, resp.GetDelegationId())
			require.Equal(t, delegationSource, resp.GetSourceId())
			require.WithinDuration(t, time.Now().Add(business.SourceOperationContextTTL), resp.GetExpiresAt().AsTime(), 2*time.Second)

			signed := verifySourceContext(t, resp.GetToken())
			require.Equal(t, delegationOrg, signed.GetTenantId())
			require.Equal(t, delegationPerson, signed.GetOwnerPrincipalId())
			require.LessOrEqual(t, signed.GetExpiresAtUnix()-signed.GetIssuedAtUnix(), int64(business.SourceOperationContextTTL/time.Second))
			require.NotZero(t, signed.GetAuthorizationRevision())
			want := []*basev0.WorkScopeV1{{ResourceKind: "collections", Actions: []string{"write"}}}
			requireScopes(t, want, signed.GetAuthorityScopes())
			require.Len(t, signed.GetActorChain(), 1)
			actor := signed.GetActorChain()[0]
			require.Equal(t, module, actor.GetPrincipalId())
			require.Equal(t, business.PrincipalKindService, actor.GetPrincipalKind())
			require.Equal(t, delegationID, actor.GetDelegationId())
			requireScopes(t, want, actor.GetGrantedScopes())

			// Neither a module identity nor anything the capability surface accepts.
			_, err = WorkContextSingleton().VerifyModuleWorkContext(resp.GetToken())
			require.Equal(t, codes.Unauthenticated, status.Code(err))

			used := audit.of(business.EventSourceDelegationUsed)
			require.Len(t, used, 1)
			require.Equal(t, module, used[0].ActorID)
			require.Equal(t, delegationOrg, used[0].OrgID)
			require.Equal(t, delegationSource, used[0].ResourceID)
			require.Equal(t, map[string]any{
				"delegation_id": delegationID, "source_id": delegationSource, "principal_id": delegationPerson,
				"module": "docstore", "binding_id": "source-sync",
				"audience": "ingestservice", "scopes": []string{"collections:write"},
				// A plain mint is never the receipt-lookup narrowing, and the
				// trail says so rather than leaving it to be inferred.
				"lookup": false,
			}, used[0].Payload)
		})
	}
}

func TestMintSourceOperationContextRefusals(t *testing.T) {
	t.Run("unproven module", func(t *testing.T) {
		installSourceDelegationService(t)
		_, err := mintSourceContext("docstore", "wrong", business.SourceDelegationRef{DelegationID: delegationID})
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	})
	t.Run("another module's delegation", func(t *testing.T) {
		installSourceDelegationService(t)
		_, err := mintSourceContext("reports", "reports-secret", business.SourceDelegationRef{DelegationID: delegationID})
		requireRefusal(t, err, codes.PermissionDenied, SourceDelegationInvalidReason)
	})
	t.Run("unknown delegation", func(t *testing.T) {
		installSourceDelegationService(t)
		_, err := mintSourceContext("docstore", "docstore-secret", business.SourceDelegationRef{DelegationID: delegationSource})
		requireRefusal(t, err, codes.PermissionDenied, SourceDelegationInvalidReason)
	})
	t.Run("missing for a source", func(t *testing.T) {
		installSourceDelegationService(t)
		_, err := mintSourceContext("docstore", "docstore-secret", business.SourceDelegationRef{SourceID: delegationPerson})
		requireRefusal(t, err, codes.FailedPrecondition, SourceDelegationMissingReason)
	})
	t.Run("revoked", func(t *testing.T) {
		store, _ := installSourceDelegationService(t)
		store.revoke(t)
		_, err := mintSourceContext("docstore", "docstore-secret", business.SourceDelegationRef{DelegationID: delegationID})
		requireRefusal(t, err, codes.PermissionDenied, SourceDelegationRevokedReason)
		// By source, a revoked delegation leaves the source with none.
		_, err = mintSourceContext("docstore", "docstore-secret", business.SourceDelegationRef{SourceID: delegationSource})
		requireRefusal(t, err, codes.FailedPrecondition, SourceDelegationMissingReason)
	})
}

// The reason strings are a wire contract with consuming modules and the
// gateway (auth-gateway's sourceDelegationMissingReason).
func TestSourceDelegationReasonsAreTheWireContract(t *testing.T) {
	require.Equal(t, "DELEGATION_MISSING", SourceDelegationMissingReason)
	require.Equal(t, "DELEGATION_REVOKED", SourceDelegationRevokedReason)
	require.Equal(t, "DELEGATION_INVALID", SourceDelegationInvalidReason)
	require.Equal(t, "accounts.saas.codefly.dev", SolutionRegistryErrorDomain)
}

// A consumer re-checks authority at every hop: the revision check confirms a
// freshly minted context, and stops confirming it the moment the delegation is
// revoked, the person's authorization revision moves, or a scope is widened.
func TestCheckAuthorizationRevisionFollowsTheSourceDelegation(t *testing.T) {
	mint := func(t *testing.T) (*sourceDelegationMemoryStore, *gen.CheckAuthorizationRevisionRequest) {
		store, _ := installSourceDelegationService(t)
		resp, err := mintSourceContext("docstore", "docstore-secret", business.SourceDelegationRef{DelegationID: delegationID})
		require.NoError(t, err)
		request := revisionRequestFromClaims(verifySourceContext(t, resp.GetToken()))
		require.NoError(t, Validate(request))
		return store, request
	}

	t.Run("confirms a minted context", func(t *testing.T) {
		_, request := mint(t)
		_, err := WorkContextSingleton().CheckAuthorizationRevision(revisionTestContext(t), request)
		require.NoError(t, err)
	})
	t.Run("refuses after revoke", func(t *testing.T) {
		store, request := mint(t)
		store.revoke(t)
		_, err := WorkContextSingleton().CheckAuthorizationRevision(revisionTestContext(t), request)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
	t.Run("refuses once the person's revision moves", func(t *testing.T) {
		store, request := mint(t)
		store.facts.PrincipalRevision++
		_, err := WorkContextSingleton().CheckAuthorizationRevision(revisionTestContext(t), request)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
	t.Run("refuses once the person is demoted", func(t *testing.T) {
		store, request := mint(t)
		store.facts.MemberRole = "member"
		_, err := WorkContextSingleton().CheckAuthorizationRevision(revisionTestContext(t), request)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
	t.Run("refuses a widened scope", func(t *testing.T) {
		_, request := mint(t)
		request.Subjects[1].Scopes[0].Actions = []string{"read", "write"}
		_, err := WorkContextSingleton().CheckAuthorizationRevision(revisionTestContext(t), request)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
	t.Run("refuses another tenant", func(t *testing.T) {
		store, request := mint(t)
		// The module's declared tenancy is never consulted, so the refusal is
		// the absence of a delegation in that tenant.
		store.delegations[delegationID].OrgID = moduleWorkContextTenant
		request.OrgId = delegationOrg
		_, err := WorkContextSingleton().CheckAuthorizationRevision(revisionTestContext(t), request)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
}

// A stale delegation found at mint time is revoked with its reason, recorded as
// the system, and refused — and the record survives the refusal.
func TestMintSourceOperationContextRevokesWhatNoLongerHolds(t *testing.T) {
	for name, test := range map[string]struct {
		mutate func(*sourceDelegationMemoryStore)
		reason string
	}{
		"source deleted": {func(s *sourceDelegationMemoryStore) { s.facts.SourceExists = false }, business.SourceDelegationSourceDeleted},
		"member removed": {func(s *sourceDelegationMemoryStore) { s.facts.MemberRole = "" }, business.SourceDelegationMemberRemoved},
		"demoted":        {func(s *sourceDelegationMemoryStore) { s.facts.MemberRole = "member" }, business.SourceDelegationPermissionLost},
		"user deleted":   {func(s *sourceDelegationMemoryStore) { s.facts.UserStatus = "deleted" }, business.SourceDelegationUserInactive},
		"binding changed": {func(s *sourceDelegationMemoryStore) {
			s.delegations[delegationID].BindingDigest = "0" + s.delegations[delegationID].BindingDigest[1:]
		}, business.SourceDelegationBindingChanged},
	} {
		t.Run(name, func(t *testing.T) {
			store, audit := installSourceDelegationService(t)
			test.mutate(store)

			_, err := mintSourceContext("docstore", "docstore-secret", business.SourceDelegationRef{SourceID: delegationSource})
			requireRefusal(t, err, codes.PermissionDenied, SourceDelegationRevokedReason)

			require.False(t, store.delegations[delegationID].Active())
			require.Equal(t, test.reason, store.delegations[delegationID].RevokedReason)
			revoked := audit.of(business.EventSourceDelegationRevoked)
			require.Len(t, revoked, 1)
			require.Equal(t, "system", revoked[0].ActorType)
			require.Equal(t, test.reason, revoked[0].Payload["reason"])
			require.Empty(t, audit.of(business.EventSourceDelegationUsed))
		})
	}
}

// A parent with no delegation hop keeps today's tenant check exactly: a module
// bound to another tenant without cross_tenant cannot exchange a parent in this
// one, and the same module declared cross_tenant can.
func TestDelegatedOperationExchangeWithoutADelegationHopKeepsTheTenantCheck(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	parentToken, _, err := workContextSingleton.signer.StartTask(workcontext.StartTaskInput{
		Audience: "example", TenantID: readOrg, OwnerPrincipalID: readOwner,
		TaskID: "019f6bf7-1111-7111-8111-111111111111", SessionID: "019f6bf7-2222-7222-8222-222222222222",
		AuthorizationRevision: facts.facts.EffectiveRevision(), ReplayPolicy: workcontext.WorkContextReplayIdempotent,
		AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: "results", Actions: []string{"read", "write"}, ResourceIds: []string{"result-1"}}},
	})
	require.NoError(t, err)
	exchange := func(crossTenant bool) error {
		registry := installedOperationRegistry(true)
		grant := registry[business.ModulePrincipalID("example")]
		grant.Tenant, grant.CrossTenant = moduleWorkContextTenant, crossTenant
		registry[business.ModulePrincipalID("example")] = grant
		service.SetModuleAuthorityReads(currentModuleAuthority{}, nil)
		service.SetModuleCapabilities(nil, nil, registry)
		module, _, err := workContextSingleton.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: business.ModulePrincipalID("example"), Tenant: moduleWorkContextTenant})
		require.NoError(t, err)
		req := connect.NewRequest(&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "generate", ParentWorkContextToken: parentToken.Encoded()})
		req.Header().Set(workcontext.WorkContextHeaderName, module.Encoded())
		req.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
		_, err = client.ExchangeDelegatedOperationAudience(context.Background(), req)
		return err
	}
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(exchange(false)))
	require.NoError(t, exchange(true))
}
