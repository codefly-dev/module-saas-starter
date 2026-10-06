package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// catalogueStore holds a registry record that DOES carry deployment topology
// and a runtime-boundary seed, so the assertions below are about what the
// Catalogue chooses to send. Every other method is an embedded nil interface
// and panics if reached.
type catalogueStore struct {
	business.Store
	business.InstallationStore
	role          string
	registrations []*business.SolutionRegistration
	installations []*business.CatalogueInstallationRecord
}

func (s *catalogueStore) GetPlatformRole(context.Context, string) (string, error) {
	return s.role, nil
}

func (s *catalogueStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *catalogueStore) ListSolutionRegistrations(context.Context, bool) ([]*business.SolutionRegistration, int64, error) {
	return s.registrations, 12, nil
}

func (s *catalogueStore) ListCatalogueInstallations(context.Context) ([]*business.CatalogueInstallationRecord, error) {
	return s.installations, nil
}

// The Catalogue is a browser-facing projection of the registry, so it withholds
// the topology every other browser projection withholds — and the runtime
// boundary every response withholds — while keeping what the derived status is
// read from.
func TestListPlatformCatalogueWithholdsRegistryTopology(t *testing.T) {
	store := &catalogueStore{
		role: "super_admin",
		registrations: []*business.SolutionRegistration{{
			SolutionID:      "example-solution",
			Publisher:       "solution:example-solution",
			Revision:        12,
			RuntimeBoundary: boundaryStoredSeed,
			Frontend: &business.SolutionFrontendHalf{
				Revision: 11, Manifest: `{"id":"example-solution"}`, ContractVersion: "v1",
			},
			Backend: &business.SolutionBackendHalf{
				Revision: 12, Upstream: "http://upstream.example:8080", ServiceAlias: "example-solution",
				ContractVersion: "v1",
			},
			// An installation names the immutable target, so the declared
			// record is what maps it back to this solution. Without it the
			// installation resolves to no solution and is keyed by its target
			// id instead — which is correct behaviour, and not what this test
			// is about.
			Declared: &business.SolutionDeclaredBinding{
				BindingID: "acme.test.example-solution", Generation: 1,
				Release: "acme/example-solution@1.0.0", TargetID: platformTargetD,
			},
			UpdatedAt: time.Now().UTC(),
		}},
		installations: []*business.CatalogueInstallationRecord{{
			Installation: &gen.Installation{Id: platformTargetD, TargetId: platformTargetD},
			OrgName:      "Acme",
		}},
	}
	installLayeredAuthzService(t, store)

	ctx := stampVerifiedIdentity(context.Background(), platformActorID, "", auth.Assurance{})
	out, err := (&PlatformAdminServer{}).ListPlatformCatalogue(ctx, &gen.ListPlatformCatalogueRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(12), out.GetRegistryRevision())
	require.Len(t, out.GetEntries(), 1)

	entry := out.GetEntries()[0]
	require.Equal(t, gen.CatalogueEntryKind_CATALOGUE_ENTRY_KIND_SOLUTION, entry.GetKind())
	require.Equal(t, "solution:example-solution", entry.GetPublisher())
	require.Len(t, entry.GetInstallations(), 1)
	require.Equal(t, "Acme", entry.GetInstallations()[0].GetOrgName())

	registration := entry.GetRegistration()
	require.Equal(t, gen.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE, registration.GetStatus())
	// THE BOUNDARY IS WITHHELD STRUCTURALLY, not emptied. This asserted
	// `GetRuntimeBoundary()` was empty; the wire message has no such field at
	// all, so there is no getter to call and nothing a future change could
	// accidentally populate. Asserted over the DESCRIPTOR rather than a value,
	// because "the field does not exist" is the guarantee — a seed is the one
	// thing that must never leave this host, and an absent field cannot leak.
	require.Nil(t, registration.ProtoReflect().Descriptor().Fields().ByName("runtime_boundary"),
		"saas.accounts.v1.SolutionRegistration must carry no runtime_boundary field: "+
			"the seed selects the boundary a Work Context is sealed under, so a response that could "+
			"carry it lets one solution learn another's")
	require.Empty(t, registration.GetFrontend().GetManifest())
	require.Empty(t, registration.GetBackend().GetUpstream())
	require.Empty(t, registration.GetBackend().GetServiceAlias())
	require.Equal(t, int64(11), registration.GetFrontend().GetRevision())
	require.Equal(t, "v1", registration.GetBackend().GetContractVersion())
}
