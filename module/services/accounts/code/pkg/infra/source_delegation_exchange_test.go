//go:build !pure

package infra_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

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

// ---------------------------------------------------------------------------
// Reference-backed admission: the arm that carries work longer than any
// capability. The caller presents an identifier and holds nothing.
// ---------------------------------------------------------------------------

// exchangeByReference presents a delegation reference — and no parent — as the
// runtime module, which is the audience the delegation's binding names.
func exchangeByReference(t *testing.T, runtimeTenant, delegationID, binding string, lookup bool) (*gen.IssuedWorkContext, error) {
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
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{
			BindingId: binding, Lookup: lookup,
			DelegationId: delegationID,
		})
}

func scopeActions(scopes []*basev0.WorkScopeV1) map[string][]string {
	out := make(map[string][]string, len(scopes))
	for _, scope := range scopes {
		out[scope.GetResourceKind()] = scope.GetActions()
	}
	return out
}

// The heart of #948. A task that will run for an hour is admitted holding only
// a reference; every call mints a fresh short capability against it; and the
// capability a reference produces is the one the revision check already
// confirms, so nothing downstream can tell the two arms apart.
//
// The renewal claim is the one worth reading closely: the second exchange is
// made with no capability in hand at all, not even an expired one. There is
// therefore no window in which a worker must still hold a valid token in order
// to obtain the next — which is exactly what made an hour impossible before.
func TestSourceDelegation_ReferenceBackedExchangeOutlivesEveryCapability(t *testing.T) {
	w := newDelegationWorld(t)
	signing := installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)

	issued, err := exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", false)
	require.NoError(t, err)
	child := signing.verify(t, issued.GetToken())

	require.Equal(t, "docstore-ingest", child.GetAudience(), "the audience is the caller's own installed binding")
	require.Equal(t, w.org, child.GetTenantId(), "the delegation admits its organization, not the caller's tenancy")
	require.Equal(t, w.admin, child.GetOwnerPrincipalId(), "the work is owned by the person who connected the source")
	require.Len(t, child.GetActorChain(), 1)
	require.Equal(t, business.ModulePrincipalID(delegationModule), child.GetActorChain()[0].GetPrincipalId(),
		"the actor is the module the person delegated to, not the module that presented the reference")
	require.Equal(t, delegation.ID, child.GetActorChain()[0].GetDelegationId())
	require.Equal(t, map[string][]string{"collections": {"read", "write"}}, scopeActions(child.GetActorChain()[0].GetGrantedScopes()))

	// Nothing long-lived exists anywhere: this is the whole point of holding a
	// reference rather than a token.
	lifetime := time.Duration(child.GetExpiresAtUnix()-child.GetIssuedAtUnix()) * time.Second
	require.Equal(t, business.SourceOperationContextTTL, lifetime)
	require.LessOrEqual(t, lifetime, workcontext.WorkContextMaxTTL)

	require.NoError(t, checkRevision(child), "a reference-backed capability is confirmed by the same revision check")

	// Renewal, in full: exchanged again against the same reference, holding
	// nothing. Submit-mode work renews exactly this way.
	renewed, err := exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", false)
	require.NoError(t, err)
	second := signing.verify(t, renewed.GetToken())
	require.NotEqual(t, child.GetTaskId(), second.GetTaskId(), "each call is its own task, never a renewed one")
	require.Equal(t, child.GetAuthorizationRevision(), second.GetAuthorizationRevision())
	require.NoError(t, checkRevision(second))

	// Each mint is recorded against the delegation, not just the first.
	require.Len(t, w.audit.of(business.EventSourceDelegationUsed, sourceID), 2)
}

// A revoke in the middle of a run refuses the next call. This is the fence the
// design promises — a live re-check before every mint, not an atomic
// cross-service one — so an already-issued capability is not recalled, it
// simply stops being confirmed and no further one is issued.
func TestSourceDelegation_RevokeBetweenTwoCallsRefusesTheNext(t *testing.T) {
	w := newDelegationWorld(t)
	signing := installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)

	first, err := exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", false)
	require.NoError(t, err)
	inFlight := signing.verify(t, first.GetToken())
	require.NoError(t, checkRevision(inFlight))

	_, err = w.svc.RevokeSourceDelegation(testCtx, w.other, w.org, delegation.ID)
	require.NoError(t, err)

	_, err = exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", false)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Equal(t, codes.PermissionDenied, status.Code(checkRevision(inFlight)),
		"the capability already in flight stops being confirmed at its next hop")

	// The run closes partial rather than continuing: no further capability is
	// obtainable by any arm, reference or parent token.
	_, err = mintSource(sourceID)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// The audit's one untraced case (handbook#152 §4.3): the person whose
// delegation admits the work loses their membership while the task is running.
// The next call refuses, the delegation is revoked with the reason, and the
// capability in flight stops confirming.
func TestSourceDelegation_MembershipLapsingMidRunRefusesTheNextCall(t *testing.T) {
	w := newDelegationWorld(t)
	signing := installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)

	first, err := exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", false)
	require.NoError(t, err)
	inFlight := signing.verify(t, first.GetToken())

	// w.other keeps the organization administered, so what lapses is this
	// person's authority alone, not the organization's.
	execControlPlane(t, `DELETE FROM organization_members WHERE org_id = $1 AND user_id = $2`, w.org, w.admin)

	_, err = exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", false)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Equal(t, business.SourceDelegationMemberRemoved, w.only(t, w.org, sourceID, delegationModule).RevokedReason,
		"the reference arm revokes what no longer holds, with the reason, exactly as a mint does")
	require.Equal(t, codes.PermissionDenied, status.Code(checkRevision(inFlight)))
}

// A reference is an identifier, not a bearer capability. What stops it being
// one is the audience the delegating binding declares: only the module a person
// authorized calling may present it, so learning an id is worth nothing.
func TestSourceDelegation_ReferenceIsNotABearerCapability(t *testing.T) {
	w := newDelegationWorld(t)
	installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	toDocstore := w.only(t, w.org, sourceID, delegationModule)
	toReports := w.only(t, w.org, sourceID, otherModule)

	for name, presented := range map[string]struct {
		delegationID string
		binding      string
	}{
		// reports' binding is addressed to "reportservice"; the runtime is not
		// the audience of that grant, however genuine the id it holds.
		"another module's delegation": {toReports.ID, "ingest"},
		"an id that names nothing":    {business.NewIDString(), "ingest"},
		// The runtime declares "ingest" and nothing else.
		"a binding the caller does not declare": {toDocstore.ID, "source-sync"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := exchangeByReference(t, w.otherOrg, presented.delegationID, presented.binding, false)
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		})
	}

	// The grantee itself is not the audience either: docstore holds the grant,
	// and the grant authorizes calling the runtime — not calling docstore.
	identity, _, err := adapters.WorkContextSingleton().StartModuleTask(business.ModuleWorkContextAuthority{
		PrincipalID: business.ModulePrincipalID(delegationModule), Tenant: w.otherOrg,
	})
	require.NoError(t, err)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		workcontext.WorkContextHeaderName, identity.Encoded(),
		"x-codefly-internal-token", delegationExchangeInternalToken,
	))
	_, err = adapters.ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(ctx,
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{
			BindingId:    delegationBinding,
			DelegationId: toDocstore.ID,
		})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
}

// Recovering an outcome never carries more authority than producing it did: a
// receipt lookup is the caller's read-only subset, intersected with what the
// person delegated.
func TestSourceDelegation_ReferenceExchangeNarrowsAReceiptLookup(t *testing.T) {
	w := newDelegationWorld(t)
	signing := installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)

	issued, err := exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", true)
	require.NoError(t, err)
	child := signing.verify(t, issued.GetToken())
	require.Equal(t, map[string][]string{"collections": {"read"}}, scopeActions(child.GetActorChain()[0].GetGrantedScopes()),
		"write is dropped: a lookup may not produce the effect it recovers")
	require.NoError(t, checkRevision(child))
}

// Exactly one authority, enforced by the contract rather than by the handler.
// The two arms are separate fields because moving a published field into a
// oneof would be a breaking change, so the exclusivity a oneof would have given
// for free is a validation rule — and this is what holds it to being the
// contract. Neither is a default: a request presenting no authority is refused,
// never read as an empty parent token, and one presenting both is refused
// rather than silently preferring an arm.
func TestSourceDelegation_ExchangeRequiresExactlyOneAuthority(t *testing.T) {
	w := newDelegationWorld(t)
	installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)
	minted, err := mintSource(sourceID)
	require.NoError(t, err)

	identity, _, err := adapters.WorkContextSingleton().StartModuleTask(business.ModuleWorkContextAuthority{
		PrincipalID: business.ModulePrincipalID(runtimeModule), Tenant: w.otherOrg,
	})
	require.NoError(t, err)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		workcontext.WorkContextHeaderName, identity.Encoded(),
		"x-codefly-internal-token", delegationExchangeInternalToken,
	))
	for name, request := range map[string]*gen.ModuleExchangeDelegatedOperationAudienceRequest{
		"neither": {BindingId: "ingest"},
		"both":    {BindingId: "ingest", ParentWorkContextToken: minted.GetToken(), DelegationId: delegation.ID},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := adapters.ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(ctx, request)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		})
	}

	// And each arm alone is accepted, so what was refused is the count.
	_, err = adapters.ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(ctx,
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "ingest", ParentWorkContextToken: minted.GetToken()})
	require.NoError(t, err)
	_, err = exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", false)
	require.NoError(t, err)
}

// A delegation authorizes exactly one actor, so a parent carrying more than one
// hop is not a deeper chain of the same authority — it is a shape this
// delegation never granted, and it is refused rather than partly honoured.
// handbook#152 §4.3 left this untraced; it is traced here.
func TestSourceDelegation_MultiHopActorChainIsRefusedUnderADelegation(t *testing.T) {
	w := newDelegationWorld(t)
	signing := installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)

	minted, err := mintSource(sourceID)
	require.NoError(t, err)
	genuine := signing.verify(t, minted.GetToken())
	require.Len(t, genuine.GetActorChain(), 1, "a delegation mint seals exactly one actor")

	// The same authority, the same delegation, one hop deeper: a second module
	// appended to the chain. Every other fact is the one the host itself sealed.
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "collections", Actions: []string{"read", "write"}}}
	hop := func(prefix string) *basev0.WorkActorV1 {
		return &basev0.WorkActorV1{
			PrincipalId: business.ModulePrincipalID(prefix), PrincipalKind: business.PrincipalKindService,
			DelegationId: delegation.ID, GrantedScopes: scopes,
		}
	}
	deeper, deeperContext, err := signing.signer.StartTask(workcontext.StartTaskInput{
		Audience: runtimeModule, TenantID: w.org, OwnerPrincipalID: w.admin,
		TaskID: business.NewIDString(), SessionID: business.NewIDString(),
		AuthorizationRevision: genuine.GetAuthorizationRevision(), ReplayPolicy: workcontext.WorkContextReplayIdempotent,
		AuthorityScopes: scopes,
		ActorChain:      []*basev0.WorkActorV1{hop(delegationModule), hop(otherModule)},
	})
	require.NoError(t, err)

	_, err = exchangeAsRuntime(t, w.otherOrg, deeper.Encoded())
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)

	// And the revision check refuses it at every hop too, so a consumer that
	// somehow received one could not confirm it either.
	require.Equal(t, codes.PermissionDenied, status.Code(checkRevision(deeperContext)))

	// The genuine single-hop parent, by contrast, exchanges — so what was
	// refused is the extra hop, not the fixture.
	_, err = exchangeAsRuntime(t, w.otherOrg, minted.GetToken())
	require.NoError(t, err)
}

// A delegation may authorize producing an effect without authorizing recovering
// its receipt — the person delegated writes, and a lookup is a read. That is a
// refusal, not a capability with nothing in it: an empty scope set would be a
// token that proves authority and grants none, which is worse than no token.
func TestSourceDelegation_ReferenceExchangeRefusesALookupThePersonNeverDelegated(t *testing.T) {
	w := newDelegationWorld(t)
	// The delegating binding grants write alone, so it cannot cover the
	// runtime's read-only lookup subset. Installed before the connect, so the
	// delegation records this binding's digest and stays current.
	writeOnly, err := business.ParseModulePrincipalRegistry(`{` +
		`"` + delegationModule + `":{"tenant":"` + w.otherOrg + `","operation_audiences":{` +
		`"` + delegationBinding + `":{"audience":"` + runtimeModule + `",` +
		`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
		`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
		`"source_delegation_scopes":[{"resource_kind":"collections","actions":["write"]}]}}},` +
		`"` + runtimeModule + `":{"tenant":"` + w.otherOrg + `","operation_audiences":{` +
		`"ingest":{"audience":"docstore-ingest",` +
		`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
		`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}]}}}}`)
	require.NoError(t, err)
	w.svc = w.service(t, writeOnly)
	signing := installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)

	_, err = exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", true)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)

	// Invoking is unaffected, and narrowed to the write the person did delegate.
	issued, err := exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", false)
	require.NoError(t, err)
	child := signing.verify(t, issued.GetToken())
	require.Equal(t, map[string][]string{"collections": {"write"}}, scopeActions(child.GetActorChain()[0].GetGrantedScopes()))
}

// exchangeRecords is every delegated-audience-exchange record, unfiltered by
// module: this event names the module in its own field, not as "module".
func (a *delegationAudit) exchangeRecords() []business.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []business.AuditEntry
	for _, entry := range a.entries {
		if entry.EventType == business.EventDelegatedAudienceExchange {
			out = append(out, entry)
		}
	}
	return out
}

// Every mint is audited, and so is every refusal — including the refusals that
// happen before any person has been identified, which are the ones an authority
// surface most needs to keep. The grant reference is what makes those legible.
//
// The emitter here rejects an undeclared or invalid payload exactly as the
// durable one does, so a record that reaches this assertion is one production
// would have written.
func TestSourceDelegation_ReferenceExchangeAuditsEveryMintAndEveryRefusal(t *testing.T) {
	w := newDelegationWorld(t)
	installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)

	_, err := exchangeByReference(t, w.otherOrg, delegation.ID, "ingest", false)
	require.NoError(t, err)
	// Refused before any owner exists: the id names no delegation at all.
	unknown := business.NewIDString()
	_, err = exchangeByReference(t, w.otherOrg, unknown, "ingest", false)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)

	records := w.audit.exchangeRecords()
	require.Len(t, records, 2)

	issued := records[0]
	require.Equal(t, business.DelegatedAudienceExchangeIssued, issued.Payload["outcome"])
	require.Equal(t, delegation.ID, issued.Payload["delegation_id"], "the record names the grant the capability came from")
	require.Equal(t, w.admin, issued.Payload["owner_principal_id"])
	require.Equal(t, w.org, issued.OrgID)
	require.Equal(t, "docstore-ingest", issued.Payload["audience"])
	require.Equal(t, business.ModulePrincipalID(runtimeModule), issued.Payload["module_principal_id"],
		"the module that presented the reference, which is not the module the grant was made to")

	refused := records[1]
	require.Equal(t, business.DelegatedAudienceExchangeRefused, refused.Payload["outcome"])
	require.Equal(t, "PermissionDenied", refused.Payload["refusal_code"])
	require.Equal(t, unknown, refused.Payload["delegation_id"], "the attempt is identified by what was presented")
	require.NotContains(t, refused.Payload, "owner_principal_id",
		"no person was identified, and the record says so rather than naming one untruthfully")
}

// The parent-token arm records an owner whenever it has verified one, which is
// every case past the parent check. That invariant is what made it safe to stop
// requiring the field for the reference arm — and dropping the requirement is
// what lets the one case before the check, an unverifiable parent, be recorded
// at all. It was silently unrecorded before.
func TestSourceDelegation_ParentTokenArmRecordsAnOwnerOnceItHasVerifiedOne(t *testing.T) {
	w := newDelegationWorld(t)
	installDelegationAuthority(t, w)
	sourceID := w.connect(t, w.org, w.admin)
	minted, err := mintSource(sourceID)
	require.NoError(t, err)

	_, err = exchangeAsRuntime(t, w.otherOrg, minted.GetToken())
	require.NoError(t, err)
	_, err = exchangeAsRuntime(t, w.otherOrg, "not-a-token")
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)

	records := w.audit.exchangeRecords()
	require.Len(t, records, 2)
	for _, record := range records {
		require.NotContains(t, record.Payload, "delegation_id", "the parent-token arm presents no reference")
	}
	require.Equal(t, w.admin, records[0].Payload["owner_principal_id"])
	require.Equal(t, business.DelegatedAudienceExchangeRefused, records[1].Payload["outcome"])
	require.NotContains(t, records[1].Payload, "owner_principal_id",
		"a parent that does not verify names nobody; the refusal is kept rather than dropped for want of a name")
	require.Equal(t, business.ModulePrincipalID(runtimeModule), records[1].Payload["module_principal_id"],
		"and the module that presented it is on the record")
}
