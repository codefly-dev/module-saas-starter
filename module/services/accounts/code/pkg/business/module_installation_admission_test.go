//go:build pure

package business

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A module capability names the installation it acts under, and every way that
// naming can be wrong.

type fakeInstallationResolver struct {
	resolved *ResolvedModuleInstallation
	err      error
	// askedOrg and askedInstallation record what was resolved, so "the request's
	// installation reached the read" is observable rather than assumed — the
	// whole point is that the host does not pick one.
	askedOrg          string
	askedInstallation string
}

func (f *fakeInstallationResolver) ResolveModuleInstallation(
	_ context.Context, orgID, installationID string,
) (*ResolvedModuleInstallation, error) {
	f.askedOrg, f.askedInstallation = orgID, installationID
	if f.err != nil {
		return nil, f.err
	}
	return f.resolved, nil
}

func admittingService(
	resolver ModuleInstallationResolver, authority ModuleAuthorityStore,
) *Service {
	service := &Service{store: noopControlPlaneStore{}}
	service.SetModulePrincipals(ModulePrincipalRegistry{
		"principal-1": {Prefix: "crm", Tenant: "org-1", Queues: []string{"a"}},
	})
	service.SetModuleInstallations(resolver)
	service.SetModuleAuthorityReads(authority, nil)
	return service
}

func composedBy(modules ...string) *fakeInstallationResolver {
	return &fakeInstallationResolver{resolved: &ResolvedModuleInstallation{
		InstallationID:       "install-1",
		InstallationRevision: 7,
		TargetID:             "target-1",
		BindingID:            "acme.test.crm-suite",
		ComposedModules:      modules,
	}}
}

// The happy path: the request names an installation, the solution composes this
// module, and the admission carries what will be sealed.
func TestAdmissionSealsTheNamedInstallation(t *testing.T) {
	resolver := composedBy("billing", "crm")
	service := admittingService(resolver, liveAt(7, 3))

	admission, err := service.AdmitModuleUnderInstallation(context.Background(), caller(), "install-1")
	require.NoError(t, err)
	require.Equal(t, "install-1", admission.InstallationID)
	require.Equal(t, uint64(7), admission.InstallationRevision)
	require.Equal(t, uint64(3), admission.ProducerEpoch)

	// The REQUEST's installation reached the read. The host must not pick one.
	require.Equal(t, "install-1", resolver.askedInstallation)
	require.Equal(t, "org-1", resolver.askedOrg)

	// And the seal carries all three terms, so the verify side has something to
	// compare — which is the entire point of the contract.
	sealed := admission.SealFor()
	require.NotNil(t, sealed.InstallationID)
	require.NotNil(t, sealed.InstallationRevision)
	require.NotNil(t, sealed.ProducerEpoch)
	require.Equal(t, "install-1", *sealed.InstallationID)
}

// Naming NO installation is refused before any database work.
//
// A default would be the host choosing whose consent the module acts under,
// which is the one decision the request exists to carry.
func TestAdmissionRefusesARequestNamingNoInstallation(t *testing.T) {
	resolver := composedBy("crm")
	service := admittingService(resolver, liveAt(7, 3))

	_, err := service.AdmitModuleUnderInstallation(context.Background(), caller(), "")
	require.ErrorIs(t, err, ErrModuleInstallationNotNamed)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Empty(t, resolver.askedInstallation, "an unnamed installation must cost no database work")
}

// THE AUTHORISATION STEP THE REGISTRY CANNOT DO. A module may be perfectly well
// declared and still have no business acting under a given installation: the
// declared registry is a ceiling over what it may do ANYWHERE, while this says
// the organisation's consent covers this module at all.
func TestAdmissionRefusesAnInstallationWhoseSolutionDoesNotComposeThisModule(t *testing.T) {
	resolver := composedBy("billing", "inventory")
	service := admittingService(resolver, liveAt(7, 3))

	_, err := service.AdmitModuleUnderInstallation(context.Background(), caller(), "install-1")
	require.ErrorIs(t, err, ErrModuleNotComposedBySolution)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	// The refusal names the solution binding, or an operator cannot act on it.
	require.Contains(t, err.Error(), "acme.test.crm-suite")
}

// A solution composing NOTHING admits no module. Empty must not read as "any",
// which would make an undeclared solution the most permissive one.
func TestAdmissionRefusesASolutionComposingNothing(t *testing.T) {
	service := admittingService(composedBy(), liveAt(7, 3))

	_, err := service.AdmitModuleUnderInstallation(context.Background(), caller(), "install-1")
	require.ErrorIs(t, err, ErrModuleNotComposedBySolution)
}

// Composition is matched EXACTLY. `crm` must not be admitted by a solution
// composing `crm-reporting`: a substring test reads as lenient and is a
// privilege escalation between two real modules.
func TestAdmissionMatchesTheModuleNameExactly(t *testing.T) {
	for _, composed := range []string{"crm-reporting", "acme-crm", "CRM", "crm "} {
		service := admittingService(composedBy(composed), liveAt(7, 3))
		_, err := service.AdmitModuleUnderInstallation(context.Background(), caller(), "install-1")
		require.ErrorIs(t, err, ErrModuleNotComposedBySolution,
			"a solution composing %q must not admit module crm", composed)
	}
}

// A revoked, absent or foreign installation is refused — the installation
// freshness term, now answerable because the request names one.
func TestAdmissionRefusesADeadOrForeignInstallation(t *testing.T) {
	service := admittingService(
		&fakeInstallationResolver{err: ErrModuleInstallationInactive}, liveAt(7, 3))

	_, err := service.AdmitModuleUnderInstallation(context.Background(), caller(), "install-gone")
	require.ErrorIs(t, err, ErrModuleInstallationInactive)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// An unresolvable installation is Unavailable, not a denial: the host does not
// know whether the consent exists, and a blip must not read as revocation.
func TestAdmissionIsUndecidableWhenTheResolveFails(t *testing.T) {
	service := admittingService(
		&fakeInstallationResolver{err: errors.New("connection reset")}, liveAt(7, 3))

	_, err := service.AdmitModuleUnderInstallation(context.Background(), caller(), "install-1")
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotErrorIs(t, err, ErrModuleInstallationInactive)
}

// An unknown principal is refused before the installation is resolved: the
// registry is the allowlist, and an unauthenticated caller must not make this
// host do database work.
func TestAdmissionRefusesAnUnknownPrincipalBeforeResolving(t *testing.T) {
	resolver := composedBy("crm")
	service := admittingService(resolver, liveAt(7, 3))

	_, err := service.AdmitModuleUnderInstallation(context.Background(),
		ModuleCaller{PrincipalID: "stranger", BoundOrg: "org-1"}, "install-1")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Empty(t, resolver.askedInstallation)
}

// A host with no resolver wired refuses, rather than minting a capability whose
// installation nothing established.
func TestAdmissionRefusesWithoutAResolverWired(t *testing.T) {
	service := &Service{store: noopControlPlaneStore{}}
	service.SetModulePrincipals(ModulePrincipalRegistry{
		"principal-1": {Prefix: "crm", Tenant: "org-1"},
	})

	_, err := service.AdmitModuleUnderInstallation(context.Background(), caller(), "install-1")
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// Composition is checked BEFORE the epoch, because the two refusals have
// different remedies: "your solution does not include this module" is permanent,
// while a stale epoch is a re-mint. A caller that reads the wrong one retries
// forever or gives up wrongly.
func TestAdmissionReportsCompositionBeforeAStaleEpoch(t *testing.T) {
	// Not composed AND the authority read would fail: the composition refusal
	// must win.
	service := admittingService(composedBy("billing"),
		&fakeModuleAuthority{err: errors.New("connection reset")})

	_, err := service.AdmitModuleUnderInstallation(context.Background(), caller(), "install-1")
	require.ErrorIs(t, err, ErrModuleNotComposedBySolution)
	require.NotEqual(t, codes.Unavailable, status.Code(err))
}
