package adapters

import (
	"context"
	"strings"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/business/moduleregistrytest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"

	"google.golang.org/grpc/codes"
)

// A hop names its actor by the module's NAME (the host interface's decision):
// the binding it exercises is answered as granted to that name, and every
// comparison a verifier makes against it holds one module to its own binding.
// These are the comparisons, each with the module that must not pass it.

// mintExercising mints, through the issuer's own path (so core's binding check
// runs against this host's answer), a capability owned by owner that exercises
// the tools module's "records" binding: by a hop whose actor is actor, or by
// the owner itself when actor is empty.
func mintExercising(t *testing.T, f hopFixture, owner, ownerKind, actor string) error {
	t.Helper()
	sealed := business.ModuleOperationBindingID(moduleWorkContextTenant, "tools", "records")
	binding, err := service.WorkContextOperationBinding(context.Background(), moduleWorkContextTenant, sealed)
	require.NoError(t, err)
	scopes := []*basev0.WorkScopeV1{hopScope("example-records", []string{"read"})}
	start := workcontext.StartInput{
		TenantID: moduleWorkContextTenant, OwnerPrincipalID: owner, OwnerPrincipalKind: ownerKind,
		OrganizationID: moduleWorkContextTenant, TaskID: uuid.NewString(), Audience: "documents",
		AuthorityScopes: scopes, InstallationID: binding.InstallationID,
	}
	request := mintRequest{Start: start, Replay: workcontext.ReplayIdempotent}
	if actor == "" {
		request.Start.OperationBindingID = sealed
	} else {
		request.Hops = []delegationHop{{
			PrincipalID: actor, PrincipalKind: business.PrincipalKindService, DelegationID: uuid.NewString(),
			GrantedScopes: scopes, OperationBindingID: sealed,
		}}
	}
	_, _, err = f.issuer.mint(context.Background(), request)
	return err
}

func TestAHopNamesItsActorByTheModuleNameAndJournalsItsRegistryID(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	journal := &moduleActorJournal{}
	f.issuer.journal = journal
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))

	out, err := f.hop(parent, f.tool, "records", "documents")
	require.NoError(t, err)
	wc := claims(t, out)
	require.Equal(t, "tools", wc.GetActorChain()[0].GetPrincipalId())

	// The journal's principal columns are ids: the same module, stored by the
	// identity storage holds it under, never a name the column would drop.
	require.Len(t, journal.hops, 1)
	require.Equal(t, business.ModulePrincipalID("tools"), journal.hops[0].ActorPrincipalID)
	require.Equal(t, wc.GetActorChain()[0].GetDelegationId(), journal.hops[0].ID)
}

// The operation-binding principal core compares to the hop: the binding is
// granted to the module's name, so only that module, by that name, exercises it.
func TestOnlyTheBindingsModuleByNameExercisesItThroughAHop(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	sealed := business.ModuleOperationBindingID(moduleWorkContextTenant, "tools", "records")
	binding, err := service.WorkContextOperationBinding(context.Background(), moduleWorkContextTenant, sealed)
	require.NoError(t, err)
	require.Equal(t, "tools", binding.PrincipalID)
	require.Equal(t, business.ModulePrincipalID("tools"), binding.ModuleRegistryID)

	require.NoError(t, mintExercising(t, f, hopOwner, business.PrincipalKindHuman, "tools"))
	for _, impostor := range []string{
		"documents",                             // another declared module, by name
		business.ModulePrincipalID("documents"), // another declared module, by id
		business.ModulePrincipalID("tools"),     // this module, by the spelling a hop does not use
		"Tools",                                 // a name that is not declared
	} {
		err := mintExercising(t, f, hopOwner, business.PrincipalKindHuman, impostor)
		require.Error(t, err, "a hop actored by %q exercised tools's binding", impostor)
	}
}

// A module's own headless context is owned by its registry id; the binding is
// answered in that owner's spelling only when the owner is the binding's module.
func TestOnlyTheBindingsModuleOwnsAHeadlessContextOnIt(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	require.NoError(t, mintExercising(t, f, business.ModulePrincipalID("tools"), business.PrincipalKindService, ""))
	require.Error(t, mintExercising(t, f, business.ModulePrincipalID("documents"), business.PrincipalKindService, ""),
		"another module's headless context exercised tools's binding")

	// The answer core asks for while verifying, as the remote verifier reads it.
	sealed := business.ModuleOperationBindingID(moduleWorkContextTenant, "tools", "records")
	for owner, want := range map[string]string{
		hopOwner:                                "tools",
		business.ModulePrincipalID("tools"):     business.ModulePrincipalID("tools"),
		business.ModulePrincipalID("documents"): "tools",
	} {
		seals, _, _ := f.issuer.moduleAuthoritySources(moduleWorkContextTenant, owner)
		live, err := seals.OperationBinding(context.Background(), sealed)
		require.NoError(t, err)
		require.Equal(t, want, live.PrincipalID, "binding answered to owner %s", owner)
	}
}

// perModuleAuthority answers each module its own installation, so a seal read
// under the wrong module is told apart from the right one.
type perModuleAuthority struct{ currentModuleAuthority }

func (f perModuleAuthority) LiveModuleAuthority(ctx context.Context, principal, org, installation string) (*business.LiveModuleAuthority, error) {
	live, err := f.currentModuleAuthority.LiveModuleAuthority(ctx, principal, org, installation)
	if err != nil {
		return nil, err
	}
	live.InstallationID = "installation-" + principal
	if installation != "" && installation != live.InstallationID {
		return nil, business.ErrModuleInstallationInactive
	}
	return live, nil
}

// The seal and epoch answers: a module's name resolves to that module, and to
// no other module's installation.
func TestSealAndEpochAnswerAModuleByItsName(t *testing.T) {
	newHopFixture(t, "tool.items:read", "example-records:read")
	service.SetModuleAuthorityReads(perModuleAuthority{currentModuleAuthority{service: service}}, nil)
	ctx := context.Background()
	installation, revision, err := service.WorkContextSeal(ctx, moduleWorkContextTenant, "tools", "")
	require.NoError(t, err)
	byID, byIDRevision, err := service.WorkContextSeal(ctx, moduleWorkContextTenant, business.ModulePrincipalID("tools"), "")
	require.NoError(t, err)
	require.Equal(t, "installation-"+business.ModulePrincipalID("tools"), installation)
	require.Equal(t, byID, installation)
	require.Equal(t, byIDRevision, revision)

	_, _, err = service.WorkContextSeal(ctx, moduleWorkContextTenant, "documents", installation)
	require.Error(t, err, "documents was answered tools's installation")

	epoch, err := service.WorkContextPrincipalEpoch(ctx, moduleWorkContextTenant, "tools")
	require.NoError(t, err)
	byIDEpoch, err := service.WorkContextPrincipalEpoch(ctx, moduleWorkContextTenant, business.ModulePrincipalID("tools"))
	require.NoError(t, err)
	require.Equal(t, byIDEpoch, epoch)

	require.True(t, service.IsDeclaredModulePrincipal("tools"))
	require.False(t, service.IsDeclaredModulePrincipal("Tools"))
	require.False(t, service.IsDeclaredModulePrincipal(uuid.NewString()))
}

// The revision check a consumer runs on a hop resolves the module by name and
// holds an undeclared name to nothing.
func TestARevisionCheckConfirmsAHopByItsModulesName(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
	out, err := f.hop(parent, f.tool, "records", "documents")
	require.NoError(t, err)
	wc := claims(t, out)
	scopes := operationScopesFromCore(wc.GetActorChain()[0].GetGrantedScopes())
	sealed := wc.GetOperationBinding().GetBindingId()
	require.NoError(t, service.ConfirmModuleHopBinding(context.Background(), moduleWorkContextTenant, "tools", sealed, scopes))
	require.ErrorIs(t, service.ConfirmModuleHopBinding(context.Background(), moduleWorkContextTenant, "not-composed", sealed, scopes), business.ErrModuleHopStale)
}

// recompose replaces the composition with hopPrincipals as edited, the way a
// new delivery or envelope narrows a binding while capabilities are live.
func recompose(t *testing.T, edit func(string) string) {
	t.Helper()
	registry, err := moduleregistrytest.Parse(edit(hopPrincipals))
	require.NoError(t, err)
	service.SetModuleCapabilities(nil, nil, registry)
}

// A binding narrowed or withdrawn UPSTREAM of the last hop takes effect on the
// child below it: core re-reads only the binding the child seals (the last
// hop's), so every hop is re-read against the binding it went through.
func TestABindingNarrowedUpstreamEndsTheChildBelowIt(t *testing.T) {
	for name, edit := range map[string]func(string) string{
		"narrowed": func(s string) string {
			s = strings.Replace(s, `"invoke_scopes":[{"resource_kind":"example-records","actions":["invoke","read"]}]`, `"invoke_scopes":[{"resource_kind":"example-records","actions":["invoke"]}]`, 1)
			return strings.Replace(s, `,"lookup_scopes":[{"resource_kind":"example-records","actions":["read"]}]`, ``, 1)
		},
		"withdrawn": func(s string) string {
			return strings.Replace(s, `"audience":"documents"`, `"audience":"elsewhere"`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHopFixture(t, "tool.items:read", "example-records:read")
			parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
			first, err := f.hop(parent, f.tool, "records", "documents")
			require.NoError(t, err)
			second, err := f.hop(first.GetToken(), f.docs, "items", "tools")
			require.NoError(t, err)
			check := func() error {
				_, err := f.server.Check(context.Background(), &authorityv1.CheckRequest{Token: second.GetToken(), Audience: "tools"})
				return err
			}
			require.NoError(t, check())
			wc := claims(t, second)
			chain := []business.ModuleChainHop{}
			for _, hop := range wc.GetActorChain() {
				chain = append(chain, business.ModuleChainHop{Principal: hop.GetPrincipalId(), Scopes: operationScopesFromCore(hop.GetGrantedScopes())})
			}
			require.NoError(t, service.ConfirmModuleChain(context.Background(), moduleWorkContextTenant, "", chain))

			// tools's binding to documents moves; documents' binding back, the
			// one the child seals, does not.
			recompose(t, edit)
			requireRefused(t, check(), codes.PermissionDenied, "no longer grants")
			require.ErrorIs(t, service.ConfirmModuleChain(context.Background(), moduleWorkContextTenant, "", chain), business.ErrModuleHopStale)
			_, err = f.hop(second.GetToken(), f.tool, "records", "documents")
			require.Error(t, err, "a hop on a child whose upstream binding moved was issued")
		})
	}
}

// The installation issue reads the same pin rule: a pinned solution with
// cross_tenant still names only its own organisation.
func TestAnInstallationIssueNeverLeavesItsSolutionsPin(t *testing.T) {
	installModuleWorkContextService(t, strings.Replace(hopPrincipals, `"resources":["tool.items"]`, `"cross_tenant":true,"resources":["tool.items"]`, 1))
	_, err := installationOrganization("tools", "00000000-0000-7000-8000-0000000000b2")
	requireRefused(t, err, codes.PermissionDenied, "acts in organization")
	org, err := installationOrganization("tools", moduleWorkContextTenant)
	require.NoError(t, err)
	require.Equal(t, moduleWorkContextTenant, org)
}

// bindingJournal records hops, and answers the binding each went through, as
// the actor-chain journal does.
type bindingJournal struct {
	business.ActorChainJournal
	hops map[string]business.ActorChainHopInput
}

func (j *bindingJournal) AppendActorChainHop(_ context.Context, hop business.ActorChainHopInput) (*business.ActorChainHop, error) {
	j.hops[hop.ID] = hop
	return &business.ActorChainHop{}, nil
}

func (j *bindingJournal) AnyActorChainHopRevoked(context.Context, string, []string) (bool, error) {
	return false, nil
}

func (j *bindingJournal) ActorChainHopBindings(_ context.Context, _ string, ids []string) (map[string]business.ActorChainHopBinding, error) {
	out := map[string]business.ActorChainHopBinding{}
	for _, id := range ids {
		if hop, ok := j.hops[id]; ok && hop.OperationBindingID != "" {
			out[id] = business.ActorChainHopBinding{BindingID: hop.OperationBindingID, Operation: hop.Operation}
		}
	}
	return out, nil
}

// A hop upstream in a chain is held to the scope set it was issued under, not
// to the union of its binding's sets (the last hop's own binding is re-read by
// core, whose revision moves with any change to it). Lookup and headless lie within invoke by declaration, so the
// union was invoke itself: a lookup hop stood for as long as invoke granted its
// action, though the binding's lookup had been withdrawn. Now withdrawing
// lookup ends a lookup hop and leaves an invoke hop standing, and narrowing
// invoke ends an invoke hop.
func TestAHopIsHeldToTheScopeSetItWasIssuedUnder(t *testing.T) {
	withdrawLookup := func(s string) string {
		return strings.Replace(s, `,"lookup_scopes":[{"resource_kind":"example-records","actions":["read"]}]`, ``, 1)
	}
	dropInvokeRead := func(s string) string {
		return strings.Replace(withdrawLookup(s), `"invoke_scopes":[{"resource_kind":"example-records","actions":["invoke","read"]}]`, `"invoke_scopes":[{"resource_kind":"example-records","actions":["invoke"]}]`, 1)
	}
	for name, tc := range map[string]struct {
		operation string
		edit      func(string) string
		stands    bool
	}{
		"lookup hop, lookup withdrawn while invoke still grants read": {"lookup", withdrawLookup, false},
		"invoke hop, lookup withdrawn":                                {"", withdrawLookup, true},
		"invoke hop, invoke narrowed":                                 {"", dropInvokeRead, false},
		"lookup hop, invoke narrowed":                                 {"lookup", dropInvokeRead, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHopFixture(t, "tool.items:read", "example-records:read")
			journal := &bindingJournal{hops: map[string]business.ActorChainHopInput{}}
			f.issuer.journal = journal
			parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
			out, err := f.server.Issue(context.Background(), &authorityv1.IssueRequest{
				As: &authorityv1.IssueRequest_ParentToken{ParentToken: parent}, Actor: &authorityv1.WorkloadCredential{Credential: f.tool},
				OperationBindingId: "records", Operation: tc.operation, Audience: "documents",
				Scopes: []*authorityv1.WorkScopeV1{ask("example-records", []string{"read"})},
			})
			require.NoError(t, err)
			hop := claims(t, out).GetActorChain()[0]
			recorded := journal.hops[hop.GetDelegationId()]
			require.Equal(t, moduleWorkContextTenant+"/tools/records", recorded.OperationBindingID)
			require.Equal(t, hopOperation(tc.operation), recorded.Operation)

			// The child below it seals documents' binding back to tools, which
			// does not move: only the re-read of the upstream hop can end it.
			below, err := f.hop(out.GetToken(), f.docs, "items", "tools")
			require.NoError(t, err)
			check := func() error {
				_, err := f.server.Check(context.Background(), &authorityv1.CheckRequest{Token: below.GetToken(), Audience: "tools"})
				return err
			}
			require.NoError(t, check())
			recompose(t, tc.edit)
			if tc.stands {
				require.NoError(t, check())
				return
			}
			requireRefused(t, check(), codes.PermissionDenied, "no longer grants")
		})
	}
}
