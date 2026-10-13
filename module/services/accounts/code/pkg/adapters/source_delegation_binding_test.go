package adapters

import (
	"context"
	"testing"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	"accounts/pkg/business/moduleregistrytest"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// A capability minted from a source delegation exercises the delegation as its
// core OPERATION BINDING, and its actor hop names the module that USES it — the
// one whose running build core attests. These tests drive the reference arm
// (long-running work presenting a delegation id), and hold the binding to the
// live delegation and to the modules it entitles.
//
// docstore holds the delegation; its source-sync binding names ingestservice as
// the module it calls, so ingestservice is the one module besides docstore the
// delegation entitles. reports is declared, installed and running, and entitled
// to nothing here.
//
// ingestservice declares two bindings onto reports, and the difference is the
// point: `sync-write` stays inside what the person delegated (write), while
// `ingest` declares read as well. The delegation grants write only.
const delegationBindingPrincipals = `{"docstore":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + delegationOrg + `","operation_audiences":{` +
	`"source-sync":{"audience":"ingestservice",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
	`"source_delegation_scopes":[{"resource_kind":"collections","actions":["write"]}]}}},` +
	`"ingestservice":{"workload":{"service_account":"ingest","namespace":"acme-prod","container":"app"},"tenant":"` + delegationOrg + `","operation_audiences":{` +
	`"ingest":{"audience":"reports",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}]},` +
	`"sync-write":{"audience":"reports",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["write"]}]},` +
	`"identity-surface":{"audience":"module-capabilities",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["write"]}]}}},` +
	`"reports":{"workload":{"service_account":"reports","namespace":"acme-prod","container":"app"},"tenant":"` + delegationOrg + `","operation_audiences":{` +
	`"ingest":{"audience":"ingestservice",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}]},` +
	`"relay":{"audience":"ingestservice",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["write"]}]}}}}`

// installDelegationBindingService is installSourceDelegationService over the
// registry above.
func installDelegationBindingService(t *testing.T) *sourceDelegationMemoryStore {
	t.Helper()
	previous := service
	t.Cleanup(func() { service = previous })
	registry, err := moduleregistrytest.Parse(delegationBindingPrincipals)
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
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, registry)
	bindModuleExecution(t, svc, registry)
	svc.SetAuditEmitter(&validatingAudit{})
	WithService(svc)
	installModuleWorkContextAuthority(t)
	workContextSingleton.seals.(*sealAnswersFake).revisionFrom = func() uint64 { return store.facts.EffectiveRevision() }
	return store
}

// identityOf is prefix's own module capability, minted through the real RPC with
// its pod's projected token, in the delegation's organization.
func identityOf(t *testing.T, prefix string) string {
	t.Helper()
	resp, err := mintModuleWorkContext(t, prefix, moduleToken(t, prefix))
	require.NoError(t, err)
	return resp.GetToken()
}

// presentedAs is an internal call carrying prefix's identity, as a module calls
// the capability surface.
func presentedAs(t *testing.T, identity string) context.Context {
	t.Helper()
	SetInternalToken("delegation-binding-test")
	t.Cleanup(func() { SetInternalToken("") })
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		workContextHeader, identity, "x-codefly-internal-token", "delegation-binding-test",
	))
}

func exchangeByDelegation(ctx context.Context, bindingID string) (*gen.IssuedWorkContext, error) {
	return ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(ctx,
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: bindingID, DelegationId: delegationID})
}

func TestTheReferenceArmMintsWithTheUsingModulesHopAndTheDelegationsBinding(t *testing.T) {
	installDelegationBindingService(t)
	ctx := presentedAs(t, identityOf(t, "ingestservice"))

	issued, err := exchangeByDelegation(ctx, "ingest")
	require.NoError(t, err)

	verified, err := workContextSingleton.verifyFor(context.Background(), issued.GetToken(), "reports")
	require.NoError(t, err, "a capability minted from a delegation verifies for its audience against the live binding")
	claims := verified.Context()
	require.Equal(t, delegationPerson, claims.GetOwnerPrincipalId())
	require.Len(t, claims.GetActorChain(), 1)
	hop := claims.GetActorChain()[0]
	user := business.ModulePrincipalID("ingestservice")
	require.Equal(t, user, hop.GetPrincipalId(), "the hop names the module that uses the capability, not the delegating one")
	require.Equal(t, delegationID, hop.GetDelegationId())
	require.Equal(t, executiontest.Digest("ingestservice"), hop.GetImageDigest(), "core attests the using module's own build")
	require.Equal(t, business.SourceDelegationBindingID(delegationID, "ingestservice"), claims.GetOperationBinding().GetBindingId())
	require.Equal(t, "installation-current", claims.GetSeal().GetInstallationId(), "sealed to the using module's installation")
	require.Equal(t, uint64(42), claims.GetAuthorizationRevision(), "the person's ordinary revision")
	require.Equal(t, []*basev0.WorkScopeV1{{ResourceKind: "collections", Actions: []string{"write"}}}, hop.GetGrantedScopes())
}

// The binding is read live on every verification: revoking the delegation
// refuses every capability minted from it, at once, wherever it is presented.
func TestARevokedDelegationIsRefusedByTheLiveBindingRead(t *testing.T) {
	store := installDelegationBindingService(t)
	issued, err := exchangeByDelegation(presentedAs(t, identityOf(t, "ingestservice")), "ingest")
	require.NoError(t, err)

	store.revoke(t)

	_, err = workContextSingleton.verifyFor(context.Background(), issued.GetToken(), "reports")
	require.ErrorIs(t, err, workcontext.ErrRevoked, "a withdrawn binding refuses the capability sealed to it")
}

// A delegation superseded by a change to the binding it was recorded against
// no longer supports a binding at all: the live read refuses it outright.
func TestASupersededDelegationIsRefusedByTheLiveBindingRead(t *testing.T) {
	installDelegationBindingService(t)
	issued, err := exchangeByDelegation(presentedAs(t, identityOf(t, "ingestservice")), "ingest")
	require.NoError(t, err)

	registry, err := moduleregistrytest.Parse(delegationBindingPrincipals)
	require.NoError(t, err)
	docstore := registry[business.ModulePrincipalID("docstore")]
	changed := docstore.OperationAudiences["source-sync"]
	changed.SourceDelegationScopes = []business.ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"read", "write"}}}
	docstore.OperationAudiences["source-sync"] = changed
	registry[business.ModulePrincipalID("docstore")] = docstore
	service.SetModuleCapabilities(nil, nil, registry)

	_, err = workContextSingleton.verifyFor(context.Background(), issued.GetToken(), "reports")
	require.Error(t, err)
	require.ErrorIs(t, err, business.ErrSourceDelegationContextStale)
}

// A module the delegation does not entitle cannot use it: not by presenting the
// reference, not through a binding id naming it, and not by presenting a
// capability minted from the delegation for another module.
func TestADifferentModuleCannotUseTheDelegation(t *testing.T) {
	installDelegationBindingService(t)

	t.Run("presenting the reference", func(t *testing.T) {
		_, err := exchangeByDelegation(presentedAs(t, identityOf(t, "reports")), "ingest")
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("a binding id naming it", func(t *testing.T) {
		_, err := service.WorkContextOperationBinding(context.Background(), delegationOrg,
			business.SourceDelegationBindingID(delegationID, "reports"))
		require.ErrorIs(t, err, business.ErrNoOperationBinding)
	})

	t.Run("presenting a capability minted for another module", func(t *testing.T) {
		minted, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{DelegationID: delegationID})
		require.NoError(t, err)
		_, err = ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(presentedAs(t, identityOf(t, "reports")),
			&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "ingest", ParentWorkContextToken: minted.GetToken()})
		require.Error(t, err)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})
}

// The plain mint is the delegating module using its own delegation: its hop,
// its build, the binding naming it.
func TestThePlainMintExercisesTheDelegationAsTheDelegatingModule(t *testing.T) {
	installDelegationBindingService(t)
	minted, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{DelegationID: delegationID})
	require.NoError(t, err)

	verified, err := workContextSingleton.verifyFor(context.Background(), minted.GetToken(), "ingestservice")
	require.NoError(t, err)
	claims := verified.Context()
	require.Equal(t, business.ModulePrincipalID("docstore"), claims.GetActorChain()[0].GetPrincipalId())
	require.Equal(t, executiontest.Digest("docstore"), claims.GetActorChain()[0].GetImageDigest())
	require.Equal(t, business.SourceDelegationBindingID(delegationID, "docstore"), claims.GetOperationBinding().GetBindingId())
}

// The TOKEN arm: the entitled module presents the capability it was handed and
// gets that capability back, re-pointed at its own binding's audience and
// narrowed to that binding — descending from the session it was handed.
//
// It appends no hop, so the chain still names the DELEGATING module and the
// binding it exercises stays docstore's. That is what makes the result provable
// as "the capability I was handed, narrowed": a callee holding both can compare
// them field for field. The caller's own entitlement is not carried in the
// capability, it is enforced before signing — its installed binding is resolved
// against this delegation in its own organization, re-resolved after signing,
// and the exchange is recorded against it.
func TestTheTokenArmReissuesThePresentedParentAndLinksItsSession(t *testing.T) {
	installDelegationBindingService(t)
	minted, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{DelegationID: delegationID})
	require.NoError(t, err)
	parent, err := workContextSingleton.verifyFor(context.Background(), minted.GetToken(), "ingestservice")
	require.NoError(t, err)

	issued, err := ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(
		presentedAs(t, identityOf(t, "ingestservice")),
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "sync-write", ParentWorkContextToken: minted.GetToken()})
	require.NoError(t, err)

	verified, err := workContextSingleton.verifyFor(context.Background(), issued.GetToken(), "reports")
	require.NoError(t, err)
	child := verified.Context()

	require.Equal(t, parent.Context().GetSessionId(), child.GetParentSessionId(),
		"the child names the session it descends from")
	require.NotEqual(t, parent.Context().GetSessionId(), child.GetSessionId())
	require.Equal(t, parent.Context().GetSessionId(), issued.GetParentSessionId())

	require.Len(t, child.GetActorChain(), 1, "an exchange appends no hop")
	hop := child.GetActorChain()[0]
	require.Equal(t, business.ModulePrincipalID("docstore"), hop.GetPrincipalId())
	require.Equal(t, executiontest.Digest("docstore"), hop.GetImageDigest(),
		"the hop still attests the build the delegating module runs; nothing was re-attested")
	require.Equal(t, business.SourceDelegationBindingID(delegationID, "docstore"),
		child.GetOperationBinding().GetBindingId(), "a binding is held by a principal and does not travel")
	require.Equal(t, parent.Context().GetSeal().GetInstallationId(), child.GetSeal().GetInstallationId(),
		"sealed to the installation that granted the binding, which the audience does not move")
	narrowed := []*basev0.WorkScopeV1{{ResourceKind: "collections", Actions: []string{"write"}}}
	// Narrowed to the caller's installed binding.
	requireScopes(t, narrowed, hop.GetGrantedScopes())
	requireSameCapability(t, reissuedFrom(parent.Context(), child, narrowed), child)
}

// A binding declaring more than the person delegated through this parent is
// refused rather than signed. The re-mint this replaced attenuated against the
// OWNER's authority scopes, so a parent whose hop had been narrowed could be
// exchanged back up to the installed binding's full set.
func TestTheTokenArmRefusesABindingWiderThanThePresentedParent(t *testing.T) {
	installDelegationBindingService(t)
	minted, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{DelegationID: delegationID})
	require.NoError(t, err)
	parent, err := workContextSingleton.verifyFor(context.Background(), minted.GetToken(), "ingestservice")
	require.NoError(t, err)
	// The person delegated write, so read is the widening.
	requireScopes(t, []*basev0.WorkScopeV1{{ResourceKind: "collections", Actions: []string{"write"}}},
		parent.EffectiveScopes())

	_, err = ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(
		presentedAs(t, identityOf(t, "ingestservice")),
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "ingest", ParentWorkContextToken: minted.GetToken()})
	require.Error(t, err)
	require.Contains(t, status.Convert(err).Message(), "widens authority")
}

// A THIRD module cannot pass the delegation on, even holding a child the
// entitled module legitimately handed it.
//
// Exactly two modules may use a delegation: the one the person granted to, and
// the audience its binding names (work_context_seal.go). The mint enforced that
// as a side effect — it named SourceDelegationBindingID(delegation, caller) as
// the capability's binding and core resolved it live, and that read refuses an
// id naming any other module. A derivation carries the PARENT's binding
// instead, so the read no longer happens on its own and the rule has to be
// asked for.
//
// Without it the exchange is transitive: ingestservice re-issues for audience
// reports, reports presents that child, and every remaining gate passes —
// `parent_audience == grant.Prefix` is now "reports", the delegation still
// confirms against docstore's hop, and reports' own binding attenuates the
// parent. The person's delegated authority would reach a module they never
// delegated to, for an audience of that module's choosing, with docstore still
// named as the actor.
func TestAThirdModuleCannotPassTheDelegationOn(t *testing.T) {
	installDelegationBindingService(t)
	minted, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{DelegationID: delegationID})
	require.NoError(t, err)

	// The legitimate hop: the audience module the delegation entitles.
	handed, err := ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(
		presentedAs(t, identityOf(t, "ingestservice")),
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "sync-write", ParentWorkContextToken: minted.GetToken()})
	require.NoError(t, err)
	_, err = workContextSingleton.verifyFor(context.Background(), handed.GetToken(), "reports")
	require.NoError(t, err, "reports is entitled to CONSUME it; the question is whether it may pass it on")

	// `relay` stays inside what the person delegated, so a refusal here is
	// about who is asking and not about the reach.
	_, err = ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(
		presentedAs(t, identityOf(t, "reports")),
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "relay", ParentWorkContextToken: handed.GetToken()})
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
}

// An installed binding cannot name this host's module capability surface.
//
// A capability addressed to `module-capabilities` is read by every peer as a
// module identity with no person present, which is why no mint takes that
// audience from anywhere a caller reaches. Nothing in
// validateOperationAudiences rejects the spelling — the registry's own comment
// says the guard is `requireMintableAudience` at signing, whatever a binding
// declares — so the derivation has to run it, exactly as the mint did. Without
// it, a binding naming the surface yields a capability that reads as the
// DELEGATING module's own identity.
func TestAnInstalledBindingCannotNameTheModuleIdentitySurface(t *testing.T) {
	installDelegationBindingService(t)
	minted, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{DelegationID: delegationID})
	require.NoError(t, err)

	_, err = ModuleCapabilitiesSingleton().ExchangeDelegatedOperationAudience(
		presentedAs(t, identityOf(t, "ingestservice")),
		&gen.ModuleExchangeDelegatedOperationAudienceRequest{BindingId: "identity-surface", ParentWorkContextToken: minted.GetToken()})
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), ModuleWorkContextAudience)
}
