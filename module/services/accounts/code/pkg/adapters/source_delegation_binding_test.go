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
const delegationBindingPrincipals = `{"docstore":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + delegationOrg + `","operation_audiences":{` +
	`"source-sync":{"audience":"ingestservice",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
	`"source_delegation_scopes":[{"resource_kind":"collections","actions":["write"]}]}}},` +
	`"ingestservice":{"workload":{"service_account":"ingest","namespace":"acme-prod","container":"app"},"tenant":"` + delegationOrg + `","operation_audiences":{` +
	`"ingest":{"audience":"reports",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}]}}},` +
	`"reports":{"workload":{"service_account":"reports","namespace":"acme-prod","container":"app"},"tenant":"` + delegationOrg + `","operation_audiences":{` +
	`"ingest":{"audience":"ingestservice",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}]}}}}`

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
