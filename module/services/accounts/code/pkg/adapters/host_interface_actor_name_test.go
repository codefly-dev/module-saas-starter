package adapters

import (
	"context"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
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
