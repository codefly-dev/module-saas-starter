package business

import (
	"context"
	"crypto"
	"testing"

	"accounts/internal/deliverytest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// signedModulePage is a delivered module serving example.note, with a page
// when page is set, and its signed authority document declaring an installer
// and a viewer role.
func signedModulePage(t *testing.T, page bool) (*solutionhost.ModuleManifest, *compositionv1.ApplyManifestRequest, *solutionhost.Delivered, *Service) {
	t.Helper()
	var frontend *solutionhost.Frontend
	if page {
		frontend = &solutionhost.Frontend{Title: "Example", ExposedModule: "./Page", HostContract: 1}
	}
	return signedModulePageWith(t, frontend, true)
}

// signedModulePageWith is signedModulePage with the page as given (nil for
// none) and the roles declared or left to the core to derive.
func signedModulePageWith(t *testing.T, frontend *solutionhost.Frontend, declared bool) (*solutionhost.ModuleManifest, *compositionv1.ApplyManifestRequest, *solutionhost.Delivered, *Service) {
	t.Helper()
	manifest := fixtureManifest(t, "example", 1)
	manifest.Authority = &solutionhost.Authority{Principal: "example", ResourceKinds: []string{"example.note"}}
	manifest.Frontend = frontend
	raw, err := solutionhost.SignedPayload(manifest)
	require.NoError(t, err)
	carrier := &solutionhost.Signed{Document: raw, Envelope: deliverytest.Envelope(t, raw, testDeliveryKey)}
	verified, err := solutionhost.VerifyDelivered(context.Background(), carrier, fixtureSigner(t))
	require.NoError(t, err)
	note := func(actions ...string) []*authorityv1.WorkScopeV1 {
		return []*authorityv1.WorkScopeV1{{ResourceKind: "example.note", Actions: actions}}
	}
	document := &compositionv1.AuthorityManifestV1{Module: manifest.Alias, Generation: manifest.Generation, CoreManifestDigest: delivery.Digest(raw),
		ScopeCeilings: note("read", "write")}
	if declared {
		document.Roles = []*compositionv1.RoleV1{{Name: InstallerRole, Scopes: note("read", "write")}, {Name: "viewer", Scopes: note("read")}}
	}
	payload, err := proto.Marshal(document)
	require.NoError(t, err)
	signer := deliverytest.Signer(t, testDeliveryKey)
	envelope, err := delivery.Sign(payload, signer.KeyID(), signer.Sign)
	require.NoError(t, err)
	svc := &Service{authorityCore: &authorityCoreState{core: pairCore(t)}}
	svc.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{signer.KeyID(): signer.Public()})
	})
	return manifest, &compositionv1.ApplyManifestRequest{Carrier: &compositionv1.SignedManifestV1{Document: raw, Envelope: carrier.Envelope},
		Authority: &compositionv1.SignedManifestV1{Document: payload, Envelope: envelope}}, verified, svc
}

// A module that declares a page declares roles as a solution does: this host
// accepts them and reads them as the module's role declaration.
func TestAModulePageDeclaresRolesLikeASolution(t *testing.T) {
	manifest, req, verified, svc := signedModulePage(t, true)
	require.NoError(t, svc.precheckCoreModule(context.Background(), req))
	doc, err := svc.verifyAuthorityDocument(req.GetAuthority(), verified)
	require.NoError(t, err, "a module page's roles are accepted")
	authority := svc.solutionAuthorityOf(doc, manifest)
	require.NotNil(t, authority)
	require.Equal(t, []string{InstallerRole, "viewer"}, authority.RoleNames())
}

// A module that declares roles and no page is refused before anything is
// recorded, with the shared core's reason, and its roles never decide here.
func TestAModuleWithoutAPageDeclaresNoRoles(t *testing.T) {
	manifest, req, verified, svc := signedModulePage(t, false)
	err := svc.precheckCoreModule(context.Background(), req)
	require.ErrorContains(t, err, "declares roles and no page")
	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	require.Equal(t, domain.Denied, code)
	doc, err := svc.verifyAuthorityDocument(req.GetAuthority(), verified)
	require.NoError(t, err)
	require.Nil(t, svc.solutionAuthorityOf(doc, manifest), "a module without a page has no roles that decide")
}

// A module's signed installation_role names one of its own roles, as a
// solution's does; one naming a role the module does not declare is refused
// with the core's reason.
func TestAModuleInstallationRoleNamesADeclaredRole(t *testing.T) {
	page := domain.Module{Name: "example", Kind: domain.KindModule, InstallationRole: "viewer", Roles: map[string][]*basev0.WorkScopeV1{"viewer": nil}}
	require.NoError(t, undeclaredInstallationRole(page))
	require.NoError(t, undeclaredInstallationRole(domain.Module{Name: "example", Kind: domain.KindModule, InstallationRole: domain.NoInstallationRole}))
	require.NoError(t, undeclaredInstallationRole(domain.Module{Name: "example", Kind: domain.KindModule}))
	err := undeclaredInstallationRole(domain.Module{Name: "example", Kind: domain.KindModule, InstallationRole: "reader"})
	require.ErrorContains(t, err, `role "reader" is not one example declares`)
	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	require.Equal(t, domain.NotFound, code)
}

// A page the host bundles into its own frontend (solutionhost.Frontend.Bundled)
// is a page: its roles are accepted by the core and read here, exactly as a
// remote's. This is the manifest shape of a module contributing a package
// this host builds in, which before was headless to both.
func TestAPageTheHostBundlesDeclaresRolesLikeARemote(t *testing.T) {
	bundled := &solutionhost.Frontend{Title: "Installed models", Order: 60, Bundled: &solutionhost.FrontendBundled{Package: "@example/models-ui", Export: "modelsPlugin"}}
	manifest, req, verified, svc := signedModulePageWith(t, bundled, true)
	require.NoError(t, svc.precheckCoreModule(context.Background(), req))
	doc, err := svc.verifyAuthorityDocument(req.GetAuthority(), verified)
	require.NoError(t, err)
	authority := svc.solutionAuthorityOf(doc, manifest)
	require.NotNil(t, authority)
	require.Equal(t, []string{InstallerRole, "viewer"}, authority.RoleNames())
}

// A module page delivered with no roles is read with the standard three the
// core derives from what it serves (the viewer reads, the editor reads and
// writes, the installer holds every action served): one rule, the core's,
// so the teams this host offers are the roles the core gates by. A module
// without a page still has none.
func TestAModulePageWithoutRolesIsReadWithTheStandardOnes(t *testing.T) {
	manifest, req, verified, svc := signedModulePageWith(t, &solutionhost.Frontend{Title: "Example", ExposedModule: "./Page", HostContract: 1}, false)
	require.NoError(t, svc.precheckCoreModule(context.Background(), req))
	doc, err := svc.verifyAuthorityDocument(req.GetAuthority(), verified)
	require.NoError(t, err)
	authority := svc.solutionAuthorityOf(doc, manifest)
	require.NotNil(t, authority)
	require.Equal(t, []string{"editor", InstallerRole, "viewer"}, authority.RoleNames())
	require.Equal(t, []string{"read"}, authority.RoleActions("example.note")[:1])
	require.ElementsMatch(t, []string{"read"}, actionsOf(authority.Roles["viewer"], "example.note"))
	require.ElementsMatch(t, []string{"read", "write"}, actionsOf(authority.Roles["editor"], "example.note"))
	require.ElementsMatch(t, []string{"read", "write"}, actionsOf(authority.Roles[InstallerRole], "example.note"))

	manifest, req, verified, svc = signedModulePageWith(t, nil, false)
	require.NoError(t, svc.precheckCoreModule(context.Background(), req))
	doc, err = svc.verifyAuthorityDocument(req.GetAuthority(), verified)
	require.NoError(t, err)
	require.Nil(t, svc.solutionAuthorityOf(doc, manifest), "a module without a page derives no roles")
}

func actionsOf(scopes []*basev0.WorkScopeV1, kind string) []string {
	var out []string
	for _, sc := range scopes {
		if sc.GetResourceKind() == kind {
			out = append(out, sc.GetActions()...)
		}
	}
	return out
}
