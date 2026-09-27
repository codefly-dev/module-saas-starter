//go:build !pure

package infra_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"accounts/pkg/adapters"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The delegation, not a module's declared tenancy, authorizes the organization.
// These tests run the real mint and the real ExchangeDelegatedOperationAudience
// on the migrated schema, with every module bound to another organization and
// none holding cross_tenant.

const delegationExchangeInternalToken = "source-delegation-exchange-test"

type delegationSigning struct {
	signer   *workcontext.WorkContextSigner
	verifier *workcontext.WorkContextVerifier
}

// installDelegationAuthority points the adapters at w's service and at a fresh
// Work Context key, and returns a signer and verifier on the same key so a test
// can forge a parent the host itself would accept the signature of.
func installDelegationAuthority(t *testing.T, w *delegationWorld) delegationSigning {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	previous := *adapters.WorkContextSingleton()
	t.Cleanup(func() {
		*adapters.WorkContextSingleton() = previous
		adapters.WithService(nil)
		adapters.SetInternalToken("")
	})
	adapters.WorkContextSingleton().Configure(adapters.WorkContextAuthorityConfiguration{
		Issuer: "accounts.test", KeyID: "delegation-test", PrivateKey: private, Authority: testStore,
	})
	adapters.WithService(w.svc)
	adapters.SetInternalToken(delegationExchangeInternalToken)
	signer, err := workcontext.NewWorkContextSigner(workcontext.WorkContextSignerOptions{
		Issuer: "accounts.test", KeyID: "delegation-test", PrivateKey: private,
	})
	require.NoError(t, err)
	verifier, err := workcontext.NewWorkContextVerifier(workcontext.WorkContextVerifierOptions{
		PublicKeys: map[string]ed25519.PublicKey{"delegation-test": public},
	})
	require.NoError(t, err)
	return delegationSigning{signer: signer, verifier: verifier}
}

func (d delegationSigning) verify(t *testing.T, token string) *basev0.WorkContextV1 {
	t.Helper()
	parsed, err := workcontext.ParseWorkContextToken(token)
	require.NoError(t, err)
	verified, err := d.verifier.Verify(parsed, workcontext.WorkContextExpectations{Issuer: "accounts.test"})
	require.NoError(t, err)
	return verified
}

func internalContext() context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-codefly-internal-token", delegationExchangeInternalToken))
}

// mintSource runs the real RPC as the docstore module.
func mintSource(sourceID string) (*gen.ModuleMintSourceOperationContextResponse, error) {
	return adapters.ModuleCapabilitiesSingleton().MintSourceOperationContext(context.Background(),
		&gen.ModuleMintSourceOperationContextRequest{
			Prefix: delegationModule, Secret: delegationModuleSecret,
			Delegation: &gen.ModuleMintSourceOperationContextRequest_SourceId{SourceId: sourceID},
		})
}

// exchangeAsRuntime presents parent to ExchangeDelegatedOperationAudience as the
// runtime module, holding a module identity bound to its declared tenant.
func exchangeAsRuntime(t *testing.T, runtimeTenant, parent string) (*gen.IssuedWorkContext, error) {
	t.Helper()
	identity, _, err := adapters.WorkContextSingleton().StartModuleTask(business.ModuleWorkContextAuthority{
		PrincipalID: business.ModulePrincipalID(runtimeModule), Tenant: runtimeTenant,
	})
	require.NoError(t, err)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		workcontext.WorkContextHeaderName, identity.Encoded(),
		"x-codefly-internal-token", delegationExchangeInternalToken,
	))
	return adapters.ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(ctx,
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "ingest", ParentWorkContextToken: parent})
}

func revisionRequest(signed *basev0.WorkContextV1) *gen.CheckAuthorizationRevisionRequest {
	scopes := func(in []*basev0.WorkScopeV1) []*gen.WorkContextScope {
		out := make([]*gen.WorkContextScope, 0, len(in))
		for _, scope := range in {
			out = append(out, &gen.WorkContextScope{ResourceKind: scope.GetResourceKind(), Actions: scope.GetActions(), ResourceIds: scope.GetResourceIds()})
		}
		return out
	}
	subjects := []*gen.WorkContextRevisionSubject{{PrincipalId: signed.GetOwnerPrincipalId(), Scopes: scopes(signed.GetAuthorityScopes())}}
	for _, actor := range signed.GetActorChain() {
		subjects = append(subjects, &gen.WorkContextRevisionSubject{PrincipalId: actor.GetPrincipalId(), Scopes: scopes(actor.GetGrantedScopes())})
	}
	return &gen.CheckAuthorizationRevisionRequest{
		OrgId: signed.GetTenantId(), OwnerPrincipalId: signed.GetOwnerPrincipalId(),
		AuthorizationRevision: signed.GetAuthorizationRevision(), Subjects: subjects,
	}
}

func checkRevision(signed *basev0.WorkContextV1) error {
	_, err := adapters.WorkContextSingleton().CheckAuthorizationRevision(internalContext(), revisionRequest(signed))
	return err
}

// A module bound to another tenant, with no cross_tenant, mints for a
// delegation in this organization; the runtime module, bound to another tenant
// too, exchanges the delegation-bearing parent; both are confirmed by the
// revision check; and a revocation ends all of it.
func TestSourceDelegation_FixedTenantModulesReachTheOrgThroughTheDelegation(t *testing.T) {
	w := newDelegationWorld(t)
	signing := installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)

	// Neither module holds cross_tenant; both are bound to otherOrg.
	for _, prefix := range []string{delegationModule, runtimeModule} {
		grant := w.svc.ModulePrincipals()[business.ModulePrincipalID(prefix)]
		require.False(t, grant.CrossTenant)
		require.Equal(t, w.otherOrg, grant.Tenant)
	}

	minted, err := mintSource(sourceID)
	require.NoError(t, err)
	require.Equal(t, w.org, minted.GetTenant())
	parent := signing.verify(t, minted.GetToken())
	require.Equal(t, w.org, parent.GetTenantId())
	require.Equal(t, w.admin, parent.GetOwnerPrincipalId())
	require.Equal(t, runtimeModule, parent.GetAudience())
	require.Equal(t, delegation.ID, parent.GetActorChain()[0].GetDelegationId())
	require.NoError(t, checkRevision(parent))

	issued, err := exchangeAsRuntime(t, w.otherOrg, minted.GetToken())
	require.NoError(t, err)
	child := signing.verify(t, issued.GetToken())
	require.Equal(t, "docstore-ingest", child.GetAudience())
	require.Equal(t, w.org, child.GetTenantId())
	require.Equal(t, w.admin, child.GetOwnerPrincipalId())
	require.Len(t, child.GetActorChain(), 1)
	require.Equal(t, business.ModulePrincipalID(delegationModule), child.GetActorChain()[0].GetPrincipalId())
	require.Equal(t, delegation.ID, child.GetActorChain()[0].GetDelegationId())
	require.NoError(t, checkRevision(child), "the revision check covers an exchanged context")

	_, err = w.svc.RevokeSourceDelegation(testCtx, w.other, w.org, delegation.ID)
	require.NoError(t, err)

	_, err = exchangeAsRuntime(t, w.otherOrg, minted.GetToken())
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Equal(t, codes.PermissionDenied, status.Code(checkRevision(parent)))
	require.Equal(t, codes.PermissionDenied, status.Code(checkRevision(child)))
	_, err = mintSource(sourceID)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// A delegation id in the token is only a pointer: a forged or mismatched hop
// admits nothing, and a parent with no delegation hop keeps the caller's own
// tenant check.
func TestSourceDelegation_ExchangeRefusesParentsTheDelegationDoesNotAuthorize(t *testing.T) {
	w := newDelegationWorld(t)
	signing := installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)
	minted, err := mintSource(sourceID)
	require.NoError(t, err)
	genuine := signing.verify(t, minted.GetToken())

	scopes := []*basev0.WorkScopeV1{{ResourceKind: "collections", Actions: []string{"read", "write"}}}
	forge := func(owner, delegationID string, revision uint64, actors bool) string {
		input := workcontext.StartTaskInput{
			Audience: runtimeModule, TenantID: w.org, OwnerPrincipalID: owner,
			TaskID: business.NewIDString(), SessionID: business.NewIDString(),
			AuthorizationRevision: revision, ReplayPolicy: workcontext.WorkContextReplayIdempotent,
			AuthorityScopes: scopes,
		}
		if actors {
			input.ActorChain = []*basev0.WorkActorV1{{
				PrincipalId: business.ModulePrincipalID(delegationModule), PrincipalKind: business.PrincipalKindService,
				DelegationId: delegationID, GrantedScopes: scopes,
			}}
		}
		token, _, err := signing.signer.StartTask(input)
		require.NoError(t, err)
		return token.Encoded()
	}

	for name, parent := range map[string]string{
		"unknown delegation id":            forge(w.admin, business.NewIDString(), genuine.GetAuthorizationRevision(), true),
		"another person's delegation":      forge(w.other, delegation.ID, genuine.GetAuthorizationRevision(), true),
		"a revision the host never sealed": forge(w.admin, delegation.ID, genuine.GetAuthorizationRevision()+1, true),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := exchangeAsRuntime(t, w.otherOrg, parent)
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		})
	}

	// No delegation hop: today's behaviour, unchanged. The runtime module is
	// bound to another tenant and holds no cross_tenant, so the parent's tenant
	// is not the caller's to reach. (On a harness without the scoped database
	// roles the refusal comes from the parent re-check that precedes the tenant
	// check; the pure adapter test pins the tenant check itself.)
	_, err = exchangeAsRuntime(t, w.otherOrg, forge(w.admin, "", genuine.GetAuthorizationRevision(), false))
	require.Error(t, err)
	require.NotEqual(t, codes.OK, status.Code(err))
}

// cross_tenant alone never makes a mint succeed: without a delegation from the
// organization there is nothing to mint from, however widely the module is
// declared.
func TestSourceDelegation_CrossTenantAloneMintsNothing(t *testing.T) {
	w := newDelegationWorld(t)
	undelegating := (&delegationWorld{audit: w.audit}).service(t, business.ModulePrincipalRegistry{})
	source, err := undelegating.AddSource(testCtx, w.admin, business.AddSourceInput{
		OrgID: w.org, Provider: business.DatasourceProviderGitHub, CollectionLabel: "plain-" + business.NewIDString()[:8],
		Credential: "pat-initial", Repo: "acme/plain", Branch: "main",
	})
	require.NoError(t, err)

	wide := w.service(t, delegationRegistryWith(t, w.otherOrg, true))
	for _, prefix := range []string{delegationModule, runtimeModule} {
		require.True(t, wide.ModulePrincipals()[business.ModulePrincipalID(prefix)].CrossTenant)
	}
	_, err = wide.AuthorizeSourceOperationContext(testCtx, delegationModule, delegationModuleSecret, business.SourceDelegationRef{SourceID: source.ID})
	require.ErrorIs(t, err, business.ErrSourceDelegationMissing)
	_, err = wide.AuthorizeSourceOperationContext(testCtx, delegationModule, delegationModuleSecret, business.SourceDelegationRef{DelegationID: business.NewIDString()})
	require.ErrorIs(t, err, business.ErrSourceDelegationInvalid)
}
