package adapters

import (
	"context"
	"errors"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
)

// hopPrincipals composes two modules that call each other on a person's
// behalf: tools reads documents' records through "records", and documents
// reads tools's items through "items".
const hopPrincipals = `{` +
	`"tools":{"workload":{"service_account":"tool","namespace":"acme-prod","container":"app"},"tenant":"` + moduleWorkContextTenant + `",` +
	`"resources":["tool.items"],"operation_audiences":{"records":{"audience":"documents",` +
	`"invoke_scopes":[{"resource_kind":"example-records","actions":["invoke","read"]}],` +
	`"lookup_scopes":[{"resource_kind":"example-records","actions":["read"]}]}}},` +
	`"documents":{"workload":{"service_account":"documents","namespace":"acme-prod","container":"app"},"tenant":"` + moduleWorkContextTenant + `",` +
	`"resources":["example-records"],"operation_audiences":{"items":{"audience":"tools",` +
	`"invoke_scopes":[{"resource_kind":"tool.items","actions":["read"]}]}}}}`

const hopOwner = "33333333-3333-4333-8333-333333333333"

// holdingAuthority holds the owner to exactly the kind:action pairs it names.
type holdingAuthority struct {
	workContextAuthorityFake
	held map[string]bool
}

func (f *holdingAuthority) ResolveWorkContextAuthority(ctx context.Context, org, owner, actor string, permissions []business.WorkContextPermission) (*business.WorkContextAuthorityFacts, error) {
	for _, p := range permissions {
		if !f.held[p.ResourceKind+":"+p.Action] {
			return nil, business.NewStoreError(errors.New(p.ResourceKind+":"+p.Action+" is not held"), business.ErrTypePermission)
		}
	}
	return f.workContextAuthorityFake.ResolveWorkContextAuthority(ctx, org, owner, actor, permissions)
}

type hopFixture struct {
	server  *HostAuthorityServer
	issuer  *WorkContextAuthorityServer
	tool    string // tools's workload credential
	docs    string // documents' workload credential
	holding *holdingAuthority
}

// newHopFixture composes hopPrincipals with an owner holding held.
func newHopFixture(t *testing.T, held ...string) hopFixture {
	t.Helper()
	installModuleWorkContextService(t, hopPrincipals)
	holding := &holdingAuthority{workContextAuthorityFake: workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 12}}, held: map[string]bool{}}
	for _, h := range held {
		holding.held[h] = true
	}
	issuer := newCoreTestServer(t, holding, moduleSealAnswers(12))
	return hopFixture{server: NewHostAuthorityServer(issuer, nil), issuer: issuer, tool: moduleToken(t, "tools"), docs: moduleToken(t, "documents"), holding: holding}
}

// parent is the owner's capability addressed to audience, carrying scopes.
func (f hopFixture) parent(t *testing.T, audience string, scopes ...*basev0.WorkScopeV1) (string, *basev0.WorkContextV1) {
	t.Helper()
	return mintTestCapability(t, f.issuer, testCapability{Audience: audience, Tenant: moduleWorkContextTenant, Owner: hopOwner, Scopes: scopes})
}

func (f hopFixture) hop(parent, credential, binding, audience string, asked ...*authorityv1.WorkScopeV1) (*authorityv1.IssueResponse, error) {
	return f.server.Issue(context.Background(), &authorityv1.IssueRequest{
		As: &authorityv1.IssueRequest_ParentToken{ParentToken: parent}, Actor: &authorityv1.WorkloadCredential{Credential: credential},
		OperationBindingId: binding, Audience: audience, Scopes: asked,
	})
}

func hopScope(kind string, actions []string, ids ...string) *basev0.WorkScopeV1 {
	return &basev0.WorkScopeV1{ResourceKind: kind, Actions: actions, ResourceIds: ids}
}

// claims is the issued capability as core reads it.
func claims(t *testing.T, out *authorityv1.IssueResponse) *basev0.WorkContextV1 {
	t.Helper()
	decoded, err := workcontext.Decode(out.GetToken())
	require.NoError(t, err)
	return decoded.Context()
}

func ask(kind string, actions []string, ids ...string) *authorityv1.WorkScopeV1 {
	return &authorityv1.WorkScopeV1{ResourceKind: kind, Actions: actions, ResourceIds: ids}
}

func requireRefused(t *testing.T, err error, code codes.Code, reason string) {
	t.Helper()
	require.Equal(t, code, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), reason)
}

// A: the child is the owner's current grants at the binding's audience within
// the binding — here read, since the owner does not hold invoke — although the
// parent never carried the audience's kind. It is owned by the owner, actored
// by the module, sealed to the installation the binding is granted within,
// names that binding, descends from the parent's session, and verifies there.
func TestAHopThroughABindingCarriesTheOwnersCurrentGrantsWithinIt(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	parent, parentClaims := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))

	out, err := f.hop(parent, f.tool, "records", "documents")
	require.NoError(t, err)
	wc := claims(t, out)
	require.Equal(t, hopOwner, wc.GetOwnerPrincipalId())
	require.Equal(t, "documents", wc.GetAudience())
	require.Len(t, wc.GetActorChain(), 1)
	require.Equal(t, business.ModulePrincipalID("tools"), wc.GetActorChain()[0].GetPrincipalId())
	require.Equal(t, business.ModuleOperationBindingID(business.ModulePrincipalID("tools"), "records"), wc.GetOperationBinding().GetBindingId())
	require.Equal(t, parentClaims.GetSessionId(), wc.GetParentSessionId())
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("example-records", []string{"read"})}, wc.GetActorChain()[0].GetGrantedScopes())
	_, err = f.issuer.verifyFor(context.Background(), out.GetToken(), "documents")
	require.NoError(t, err)

	// The revision check confirms the hop by the binding it seals.
	require.NoError(t, service.ConfirmModuleHopBinding(context.Background(), moduleWorkContextTenant,
		business.ModulePrincipalID("tools"), wc.GetOperationBinding().GetBindingId(), operationScopesFromCore(wc.GetActorChain()[0].GetGrantedScopes())))
}

func TestAHopThroughABindingItsModuleDoesNotDeclareIsNotFound(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	parent, _ := mintTestCapability(t, f.issuer, testCapability{Audience: "tools", Tenant: moduleWorkContextTenant, Owner: hopOwner,
		Replay: workcontext.ReplaySingleUse, Scopes: []*basev0.WorkScopeV1{hopScope("tool.items", []string{"read"})}})
	_, err := f.hop(parent, f.tool, "undeclared", "documents")
	requireRefused(t, err, codes.NotFound, `"undeclared" is not declared by tools`)
	// documents declares "items", tools does not: a binding is its own module's.
	_, err = f.hop(parent, f.tool, "items", "tools")
	requireRefused(t, err, codes.NotFound, `"items" is not declared by tools`)
	// Refused before the parent is verified, so a single-use parent is unspent.
	_, err = f.issuer.verifyFor(context.Background(), parent, "tools")
	require.NoError(t, err)
}

func TestAHopAddressedElsewhereThanItsBindingIsRefused(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
	_, err := f.hop(parent, f.tool, "records", "tools")
	requireRefused(t, err, codes.PermissionDenied, `"records" is addressed to documents, not tools`)
}

func TestAHopForAnOwnerHoldingNoneOfTheBindingIsRefused(t *testing.T) {
	f := newHopFixture(t, "tool.items:read")
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
	_, err := f.hop(parent, f.tool, "records", "documents")
	requireRefused(t, err, codes.PermissionDenied, "holds nothing at documents through operation binding \"records\"")
}

// The binding bounds the ask, and so does what the owner holds now.
func TestAHopAskingBeyondWhatItsOwnerHoldsIsRefused(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
	for _, actions := range [][]string{{"invoke"}, {"write"}} {
		_, err := f.hop(parent, f.tool, "records", "documents", ask("example-records", actions))
		requireRefused(t, err, codes.PermissionDenied, "widens what its owner holds")
	}
}

// A parent narrowed to one resource and one action stays narrowed: the owner
// holds more now, and the binding grants more, but the child carries only
// what the parent kept, and asking for what it gave up is refused by name.
func TestANarrowedParentStaysNarrowed(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:invoke", "example-records:read")
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}), hopScope("example-records", []string{"read"}, "c1"))

	out, err := f.hop(parent, f.tool, "records", "documents")
	require.NoError(t, err)
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("example-records", []string{"read"}, "c1")}, claims(t, out).GetActorChain()[0].GetGrantedScopes())

	for _, asked := range []*authorityv1.WorkScopeV1{ask("example-records", []string{"invoke"}), ask("example-records", []string{"read"}, "c2"), ask("example-records", []string{"read"})} {
		parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}), hopScope("example-records", []string{"read"}, "c1"))
		_, err := f.hop(parent, f.tool, "records", "documents", asked)
		requireRefused(t, err, codes.PermissionDenied, "widens what its parent narrowed")
	}
}

// Down a chain too: the first hop's child never carried tool.items in what it
// may do at documents, yet documents' hop back to tools is held to the one
// item the owner's first token named, which the child carried for it.
func TestANarrowingHoldsDownTheChain(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}, "t1"))
	first, err := f.hop(parent, f.tool, "records", "documents")
	require.NoError(t, err)

	second, err := f.hop(first.GetToken(), f.docs, "items", "tools")
	require.NoError(t, err)
	wc := claims(t, second)
	require.Len(t, wc.GetActorChain(), 2)
	require.Equal(t, hopOwner, wc.GetOwnerPrincipalId())
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("tool.items", []string{"read"}, "t1")}, wc.GetActorChain()[1].GetGrantedScopes())
	_, err = f.issuer.verifyFor(context.Background(), second.GetToken(), "tools")
	require.NoError(t, err)
	// The module the chain passed through stands while it is installed; the
	// last hop is confirmed by its own binding.
	sealed := wc.GetOperationBinding().GetBindingId()
	require.NoError(t, service.ConfirmModuleHopBinding(context.Background(), moduleWorkContextTenant, business.ModulePrincipalID("tools"), sealed, operationScopesFromCore(wc.GetActorChain()[0].GetGrantedScopes())))
	require.NoError(t, service.ConfirmModuleHopBinding(context.Background(), moduleWorkContextTenant, business.ModulePrincipalID("documents"), sealed, operationScopesFromCore(wc.GetActorChain()[1].GetGrantedScopes())))

	_, err = f.hop(first.GetToken(), f.docs, "items", "tools", ask("tool.items", []string{"read"}, "t2"))
	requireRefused(t, err, codes.PermissionDenied, "widens what its parent narrowed")
}

// A module hops only on a token addressed to it, refused by name before the
// parent is verified, so another module's single-use parent is not spent.
func TestAHopOnATokenAddressedElsewhereIsRefusedUnspent(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	parent, _ := mintTestCapability(t, f.issuer, testCapability{Audience: "documents", Tenant: moduleWorkContextTenant, Owner: hopOwner,
		Replay: workcontext.ReplaySingleUse, Scopes: []*basev0.WorkScopeV1{hopScope("example-records", []string{"read"})}})
	_, err := f.hop(parent, f.tool, "records", "documents")
	requireRefused(t, err, codes.PermissionDenied, "tools hops only on a token addressed to it")
	_, err = f.issuer.verifyFor(context.Background(), parent, "documents")
	require.NoError(t, err)
}
