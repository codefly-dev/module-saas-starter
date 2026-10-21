package business

import (
	"context"
	"maps"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type installedUseFixture struct {
	service  *Service
	live     map[string]*LiveModuleAuthority
	envelope *fakeEnvelopeStore
}

func (f *installedUseFixture) BeginModuleAuthorityUse(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (f *installedUseFixture) LiveModuleAuthority(_ context.Context, principal, org, installation string) (*LiveModuleAuthority, error) {
	live := f.live[principal]
	if org != testEnvelopeTenant || live == nil || installation != "" && installation != live.InstallationID {
		return nil, ErrModuleInstallationInactive
	}
	copy := *live
	copy.EnvelopeRevision = &f.envelope.latest
	return &copy, nil
}
func installedUseSetup(t *testing.T) *installedUseFixture {
	t.Helper()
	s := &Service{store: reviewControlPlaneStore{}}
	f := &installedUseFixture{service: s, live: map[string]*LiveModuleAuthority{}, envelope: &fakeEnvelopeStore{latest: 3}}
	registry := ModulePrincipalRegistry{}
	sources := map[string]modulePrincipalSource{}
	for _, prefix := range []string{"reader", "records", "compute"} {
		id := ModulePrincipalID(prefix)
		grant := ModulePrincipalGrant{Prefix: prefix, Tenant: testEnvelopeTenant, PresenceBinding: "acme." + prefix}
		if prefix == "records" {
			grant.Resources = []string{"records"}
		}
		if prefix == "reader" {
			grant.OperationAudiences = map[string]ModuleOperationAudience{
				"evidence":  {Audience: "records", InvokeScopes: []ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read"}}}},
				"execution": {Audience: "compute", InvokeScopes: []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"invoke", "read"}}}, LookupScopes: []ModuleOperationScope{{ResourceKind: "profiles", Actions: []string{"read"}}}},
			}
		}
		registry[id] = grant
		sources[id] = modulePrincipalSource{binding: grant.PresenceBinding, generation: 2, envelopeGeneration: 3}
		f.live[id] = &LiveModuleAuthority{RegistryBindingID: grant.PresenceBinding, InstallationID: uuid.NewString(), InstallationRevision: 1, ProducerEpoch: 1, AppliedManifestGeneration: 2, Scope: FullInstallationScope(grant)}
	}
	s.modulePrincipals.Store(&installedModuleRegistry{registry: registry, envelopeRevision: 3, sources: sources})
	s.SetModuleEnvelopeStore(f.envelope)
	s.SetModuleAuthorityReads(f, nil)
	return f
}

var installedUseOwner = "11111111-1111-4111-8111-111111111111"

func installedUseRequested() []ModuleOperationScope {
	return []ModuleOperationScope{
		{ResourceKind: "records", Actions: []string{"read"}},
		{ResourceKind: "profiles", Actions: []string{"invoke", "read"}, ResourceIDs: []string{"standard"}},
		{ResourceKind: "runs", Actions: []string{"execute"}},
	}
}
func (f *installedUseFixture) capture(t *testing.T) string {
	t.Helper()
	id, resources, err := f.service.CaptureInstalledUse(context.Background(), testEnvelopeTenant, installedUseOwner, "reader", installedUseRequested())
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.Equal(t, []string{"records"}, resources)
	require.LessOrEqual(t, len(id), 512)
	return id
}
func (f *installedUseFixture) child(t *testing.T, root, route string, lookup bool) (string, []ModuleOperationScope) {
	t.Helper()
	id, _, scopes, err := f.service.InstalledUseChild(context.Background(), testEnvelopeTenant, installedUseOwner, root, ModuleCaller{PrincipalID: ModulePrincipalID("reader"), BoundOrg: testEnvelopeTenant}, route, lookup, installedUseRequested())
	require.NoError(t, err)
	require.NotEmpty(t, id)
	return id, scopes
}
func TestInstalledUsePartitionsAuthorityAndAuthenticatesItsActor(t *testing.T) {
	f := installedUseSetup(t)
	root := f.capture(t)
	child, scopes := f.child(t, root, "evidence", false)
	require.Equal(t, []ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read"}}}, scopes)
	require.NoError(t, f.service.CheckInstalledUseActor(context.Background(), testEnvelopeTenant, installedUseOwner, child, ModulePrincipalID("reader"), "records", scopes))
	require.Error(t, f.service.CheckInstalledUseActor(context.Background(), testEnvelopeTenant, installedUseOwner, child, ModulePrincipalID("compute"), "records", scopes))
	require.Error(t, f.service.CheckInstalledUseActor(context.Background(), testEnvelopeTenant, installedUseOwner, root, ModulePrincipalID("reader"), "records", scopes))
	for _, lookup := range []bool{false, true} {
		_, model := f.child(t, root, "execution", lookup)
		require.Len(t, model, 1)
		require.Equal(t, "profiles", model[0].ResourceKind)
		require.Equal(t, []string{"standard"}, model[0].ResourceIDs)
		if lookup {
			require.Equal(t, []string{"read"}, model[0].Actions)
		}
	}
	binding, err := f.service.WorkContextOperationBinding(context.Background(), testEnvelopeTenant, root)
	require.NoError(t, err)
	require.Equal(t, installedUseOwner, binding.PrincipalID)
	binding, err = f.service.WorkContextOperationBinding(context.Background(), testEnvelopeTenant, child)
	require.NoError(t, err)
	require.Equal(t, ModulePrincipalID("reader"), binding.PrincipalID)
	_, _, _, err = f.service.InstalledUseChild(context.Background(), testEnvelopeTenant, installedUseOwner, child, ModuleCaller{PrincipalID: ModulePrincipalID("reader"), BoundOrg: testEnvelopeTenant}, "evidence", false, installedUseRequested())
	require.Error(t, err, "a child cannot recursively redeem")
	_, _, _, err = f.service.InstalledUseChild(context.Background(), testEnvelopeTenant, installedUseOwner, root, ModuleCaller{PrincipalID: ModulePrincipalID("reader"), BoundOrg: testEnvelopeTenant}, "execution", false, []ModuleOperationScope{{ResourceKind: "records", Actions: []string{"read"}}})
	require.Error(t, err, "empty must not retain all")
}
func TestInstalledUseRetainedBindingsRefuseChangedDependencies(t *testing.T) {
	for _, principal := range []string{"reader", "records", "compute"} {
		for _, change := range []string{"uninstall", "reinstall", "revision", "declaration", "envelope", "history"} {
			t.Run(principal+"/"+change, func(t *testing.T) {
				f := installedUseSetup(t)
				root := f.capture(t)
				evidence, _ := f.child(t, root, "evidence", false)
				execution, _ := f.child(t, root, "execution", false)
				id := ModulePrincipalID(principal)
				live := f.live[id]
				snapshot := f.service.moduleRegistrySnapshot()
				source := snapshot.sources[id]
				switch change {
				case "uninstall":
					delete(f.live, id)
				case "reinstall":
					live.InstallationID = uuid.NewString()
				case "revision":
					live.InstallationRevision++
				case "declaration":
					live.AppliedManifestGeneration++
				case "envelope":
					source.envelopeGeneration++
					snapshot.sources[id] = source
				case "history":
					delete(snapshot.sources, id)
				}
				for _, token := range []string{root, evidence, execution} {
					_, err := f.service.WorkContextOperationBinding(context.Background(), testEnvelopeTenant, token)
					require.Error(t, err)
				}
			})
		}
	}
}
func TestInstalledUseUnrelatedEnvelopeEditOnlyFencesUntilRefresh(t *testing.T) {
	f := installedUseSetup(t)
	root := f.capture(t)
	f.envelope.latest++
	_, err := f.service.WorkContextOperationBinding(context.Background(), testEnvelopeTenant, root)
	require.ErrorIs(t, err, ErrModuleRegistryStale)
	old := f.service.moduleRegistrySnapshot()
	refreshed := *old
	refreshed.envelopeRevision = f.envelope.latest
	refreshed.sources = maps.Clone(old.sources)
	f.service.modulePrincipals.Store(&refreshed)
	_, err = f.service.WorkContextOperationBinding(context.Background(), testEnvelopeTenant, root)
	require.NoError(t, err)
	_, err = f.service.WorkContextOperationBinding(context.Background(), uuid.NewString(), root)
	require.Error(t, err)
}
func TestInstalledUseAdmissionNeverBorrowsAReadSubsetOfAWriteOrHeadlessRoute(t *testing.T) {
	for _, change := range []string{"write", "headless", "ambiguous", "undeclared", "scoped"} {
		t.Run(change, func(t *testing.T) {
			f := installedUseSetup(t)
			snapshot := f.service.moduleRegistrySnapshot()
			id := ModulePrincipalID("reader")
			grant := snapshot.registry[id]
			route := grant.OperationAudiences["evidence"]
			requested := installedUseRequested()
			switch change {
			case "write":
				route.InvokeScopes[0].Actions = []string{"read", "write"}
			case "headless":
				route.HeadlessScopes = route.InvokeScopes
				route.InvokeScopes = nil
			case "ambiguous":
				grant.OperationAudiences["duplicate"] = route
			case "undeclared":
				target := snapshot.registry[ModulePrincipalID("records")]
				target.Resources = nil
				snapshot.registry[ModulePrincipalID("records")] = target
			case "scoped":
				requested[0].ResourceIDs = []string{"known"}
			}
			grant.OperationAudiences["evidence"] = route
			snapshot.registry[id] = grant
			f.live[id].Scope = FullInstallationScope(grant)
			binding, _, err := f.service.CaptureInstalledUse(context.Background(), testEnvelopeTenant, installedUseOwner, "reader", requested)
			require.NoError(t, err)
			require.Empty(t, binding)
		})
	}
}
