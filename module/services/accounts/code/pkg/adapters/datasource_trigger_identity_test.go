package adapters

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/business/moduleregistrytest"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The host side of a declared trigger's identity, exercised at the route an
// executing module actually calls.
//
// The two arms are not alternatives of equal standing and these tests are
// written to keep that distinction visible: the reference arm is re-derived
// from current facts on every occurrence and a revocation lands on the next
// one; the installation-principal arm is an ordinary short-lived capability
// the installation's own declaration bounds. A test that only proved "a
// capability came back" would pass for both and distinguish neither.

// executionPrincipals declares the shape a composition needs for BOTH arms on
// one module: the audience an executing module admits under, delegation scopes
// for the reference arm, and headless scopes for the installation-principal
// arm. Nothing in this host names a particular module — the prefix and the
// audience are a composition's to declare, and this is one declaration of them.
const executionPrincipals = `{"runtime":{"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + moduleWorkContextTenant + `","cross_tenant":true,"operation_audiences":{` +
	`"occurrences":{"audience":"runtime",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
	`"headless_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
	`"source_delegation_scopes":[{"resource_kind":"collections","actions":["write"]}]}}}}`

// An occurrence runs under a child minted from the reference, and the next
// occurrence after a revocation is refused.
//
// The mint route is the one an executing module calls for the reference arm,
// and the capability it answers with is what makes an occurrence's run able to
// outlive it: owned by the person who delegated the source, actored by exactly
// one module service principal whose hop carries the delegation id. That shape
// is the discriminator a consuming module reads as a grant reference, so it is
// asserted rather than assumed.
func TestTriggerOccurrenceMintsFromTheReferenceAndIsRefusedAfterRevocation(t *testing.T) {
	store, audit := installSourceDelegationService(t)

	// Occurrence one: the trigger names the reference, which is the source id,
	// and the host resolves the live delegation from it.
	first, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{SourceID: delegationSource})
	require.NoError(t, err)
	require.Equal(t, delegationPerson, first.GetOwnerPrincipalId(), "an occurrence borrows the delegating person's authority, never the module's own")
	require.Equal(t, delegationOrg, first.GetTenant())

	signed := verifySourceContext(t, first.GetToken())
	require.Len(t, signed.GetActorChain(), 1, "a grant reference grants to one actor; a deeper chain is a shape the delegation never granted")
	actor := signed.GetActorChain()[0]
	require.Equal(t, business.ModulePrincipalID("docstore"), actor.GetPrincipalId())
	require.Equal(t, business.PrincipalKindService, actor.GetPrincipalKind())
	require.Equal(t, delegationID, actor.GetDelegationId(), "the hop carries the delegation the occurrence resolved, which is what a consumer reads as a reference")
	requireScopes(t, []*basev0.WorkScopeV1{{ResourceKind: "collections", Actions: []string{"write"}}}, signed.GetAuthorityScopes())

	// Occurrence two: the same reference, a second mint. Nothing is cached, so
	// this must be a fresh capability rather than the first one again.
	second, err := mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{SourceID: delegationSource})
	require.NoError(t, err)
	require.NotEqual(t, first.GetToken(), second.GetToken(), "a cached capability would outlive the revocation this test is about")

	// Each occurrence's child is recorded, which is the only record of one: a
	// capability lives about a minute and nothing stores it.
	require.Len(t, audit.of(business.EventSourceDelegationUsed), 2)

	// The delegation is revoked between occurrences.
	revoked, err := store.RevokeSourceDelegations(t.Context(),
		business.SourceDelegationFilter{OrgID: delegationOrg, ID: delegationID}, business.SourceDelegationRevokedByAdmin, delegationPerson)
	require.NoError(t, err)
	require.Len(t, revoked, 1)

	// Occurrence three is refused, and refused as a decision rather than as an
	// outage: a module reading this must not retry it as a transient failure.
	// By source, a revoked delegation is indistinguishable from none at all.
	_, err = mintSourceContext("docstore", moduleToken(t, "docstore"), business.SourceDelegationRef{SourceID: delegationSource})
	requireRefusal(t, err, codes.FailedPrecondition, SourceDelegationMissingReason)
	require.Len(t, audit.of(business.EventSourceDelegationUsed), 2, "a refused occurrence mints nothing and records no child")
}

// The headless mint a binding with its own audience and headless scopes
// answers: a capability for work with no person anywhere in it.
//
// IT IS NOT THE INSTALLATION-BINDING ARM OF A DECLARED TRIGGER, and calling it
// "the first proof" of that arm was a leftover (issue #1112 M4, corrected in
// #1121). No declaration this host builds sets `Identity.Binding`, and this
// trigger cannot use one: the mint lives 60 seconds against a 10-minute
// declared deadline, and the capability is the module's own principal while
// ReconcileSource requires an organization member. What this test proves is the
// mint itself — the capability is owned and actored by the module's own
// principal, so there is no delegation to revoke and no authority to outlive
// the token, and the run is bounded by the capability's own short lifetime.
func TestTriggerOccurrenceMintsFromTheInstallationBinding(t *testing.T) {
	store, audit := installModuleOperationContextService(t, executionPrincipals)
	installModuleWorkContextAuthority(t)
	// The declared tenant's existence is a real database read in production; a
	// fake that reported it absent would make every mint fail closed.
	store.tenantExists = true

	minted, err := mintModuleOperationContext("runtime", moduleToken(t, "runtime"), "occurrences")
	require.NoError(t, err)
	require.Equal(t, "runtime", minted.GetAudience(), "the audience is the binding's declared one, which a composition must declare for this arm to have anywhere to mint")
	require.Equal(t, moduleWorkContextTenant, minted.GetTenant())
	require.WithinDuration(t, time.Now().Add(business.ModuleOperationContextTTL), minted.GetExpiresAt().AsTime(), 2*time.Second)

	signed := verifyExecutionContext(t, minted.GetToken())
	module := business.ModulePrincipalID("runtime")
	require.Equal(t, module, signed.GetOwnerPrincipalId(), "no person is present, so the module's own principal owns it — which is exactly the limit of this arm")
	require.Empty(t, signed.GetActorChain(), "the module acts as the owner it is, with no hop delegating to itself")
	// Exactly the headless grant, never the invoke scopes the same binding
	// carries for work a person asked for.
	want := []*basev0.WorkScopeV1{{ResourceKind: "collections", Actions: []string{"read"}}}
	requireScopes(t, want, signed.GetAuthorityScopes())
	require.Len(t, audit.of(business.EventModuleOperationContextMint), 1)

	// A binding with no headless grant cannot be minted this way at all: the
	// invoke scopes are never reused implicitly for work with no person.
	registry, err := moduleregistrytest.Parse(executionPrincipals)
	require.NoError(t, err)
	binding := registry[module].OperationAudiences["occurrences"]
	require.NotEmpty(t, binding.SourceDelegationScopes, "the same binding carries the reference arm's grant, separately")
	require.NotEqual(t, binding.InvokeScopes, binding.HeadlessScopes)
}

// A composition that declares neither grant leaves both arms with nowhere to
// mint, and the host says so by refusing rather than by minting its own
// authority. That is the state every composition is in until it declares one,
// so the refusal is the normal case and not an edge.
func TestTriggerIdentityWithoutADeclarationRefusesBothArms(t *testing.T) {
	store, _ := installModuleOperationContextService(t, headlessPrincipals)
	installModuleWorkContextAuthority(t)
	store.tenantExists = true

	// No binding declares delegation scopes, so a connect records no
	// delegation and no reference exists for a trigger to name.
	registry, err := moduleregistrytest.Parse(headlessPrincipals)
	require.NoError(t, err)
	require.Empty(t, registry.SourceDelegationTargets(), "a module with no delegation grant is not a delegation target")

	// And a binding declared for a person's behalf only is refused headless.
	_, err = mintModuleOperationContext("documents", moduleToken(t, "documents"), "interactive")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// verifyExecutionContext verifies a capability minted for the audience a
// composition declares its executing module under. It is the issuer and
// audience check a consumer runs, not a decode: a token this host could not
// verify is not one an occurrence may be admitted with.
func verifyExecutionContext(t *testing.T, token string) *basev0.WorkContextV1 {
	t.Helper()
	verified, err := WorkContextSingleton().verifyFor(context.Background(), token, "runtime")
	require.NoError(t, err)
	return verified.Context()
}
