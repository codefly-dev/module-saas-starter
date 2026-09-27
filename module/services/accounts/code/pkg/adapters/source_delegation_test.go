package adapters

import (
	"context"
	"sync"
	"testing"
	"time"

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

// delegationPrincipals declares a cross-tenant module whose "source-sync"
// binding accepts source delegations, and a second module without one.
const delegationPrincipals = `{"docstore":{"tenant":"` + moduleWorkContextTenant + `","cross_tenant":true,"operation_audiences":{` +
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
