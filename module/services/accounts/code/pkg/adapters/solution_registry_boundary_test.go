package adapters

import (
	"context"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// The runtime-boundary seed never leaves accounts on any response (issue
// #1015).
//
// It is the only input to the per-organization boundary every Work Context
// minted for a solution is sealed under, and that boundary is now stable for
// the life of the registration — so a consumer holding one solution's seed
// could mint for runs that are not its own. No registrant needs it either:
// accounts derives and seals the boundary from the credential the solution
// already presents.
//
// This drives the registry server with a store that DOES hold a boundary, so
// the assertion is about what accounts chooses to send rather than about a fake
// that cleared the field itself. Making solutionRegistrationProto set
// RuntimeBoundary fails every case below.

const boundaryStoredSeed = "019f6c02-cccc-7ccc-8ccc-cccccccccc03"

// boundaryRegistryStore is the registry the server reads, holding one record
// with a seed. Only the registry methods are implemented; every other Store
// method is the embedded nil interface and would panic if the server reached
// for it, which is the point — this exercises the registry path alone.
type boundaryRegistryStore struct {
	business.Store
	record *business.SolutionRegistration
	saved  *business.SolutionRegistration
}

func (s *boundaryRegistryStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *boundaryRegistryStore) GetSolutionRegistrationForUpdate(
	_ context.Context, _ string,
) (*business.SolutionRegistration, error) {
	return s.record, nil
}

func (s *boundaryRegistryStore) NextSolutionRegistryRevision(_ context.Context) (int64, error) {
	return s.record.Revision + 1, nil
}

func (s *boundaryRegistryStore) SaveSolutionRegistration(
	_ context.Context, record *business.SolutionRegistration,
) error {
	// The relation reports back whatever it holds, which is how the seed stays
	// the host's. Mirror that: a save never loses it.
	record.RuntimeBoundary = boundaryStoredSeed
	s.saved = record
	return nil
}

func (s *boundaryRegistryStore) ListSolutionRegistrations(
	_ context.Context, _ bool,
) ([]*business.SolutionRegistration, int64, error) {
	return []*business.SolutionRegistration{s.record}, s.record.Revision, nil
}

func newBoundaryRegistryService(t *testing.T) *boundaryRegistryStore {
	t.Helper()
	store := &boundaryRegistryStore{record: &business.SolutionRegistration{
		SolutionID:      "example-solution",
		Publisher:       "solution:example-solution",
		Revision:        7,
		RuntimeBoundary: boundaryStoredSeed,
		Frontend: &business.SolutionFrontendHalf{
			Revision: 7, Manifest: `{"id":"example-solution"}`,
		},
		Backend: &business.SolutionBackendHalf{
			Revision: 7, Upstream: "http://upstream.example:8080",
			ServiceAlias: "example-solution",
		},
		UpdatedAt: time.Now().UTC(),
	}}
	previous := service
	svc, err := business.NewService(store)
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
	return store
}

// The registry's responses carry no runtime boundary, and on this branch that
// is STRUCTURAL rather than a value that happens to be empty.
//
// It used to assert `GetRuntimeBoundary()` was empty on a listing, on a
// deregistration and on each half's own write. Three of those four surfaces are
// gone with the runtime registration writer — `PutSolutionRegistration` and
// `DeleteSolutionRegistration` are deleted RPCs, and
// TestDeletedRegistrationRPCsAreUnimplemented pins them as such. The listing
// survives, because the declared registry still answers it, and it is the
// surface that mattered most anyway: a consumer that caches the registry would
// hold every solution's seed.
//
// The assertion is over the DESCRIPTOR, not a value. The wire message has no
// `runtime_boundary` field at all, so there is nothing a later change could
// populate — and "the field does not exist" is a guarantee an empty string is
// not.
func TestSolutionRegistrationResponsesCarryNoRuntimeBoundary(t *testing.T) {
	newBoundaryRegistryService(t)
	server := SolutionRegistrySingleton()

	out, err := server.ListSolutionRegistrations(
		context.Background(), &gen.ListSolutionRegistrationsRequest{IncludeTombstoned: true})
	require.NoError(t, err)
	require.Len(t, out.GetRegistrations(), 1, "the listing must answer, or the assertion below is vacuous")

	fields := out.GetRegistrations()[0].ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		name := string(fields.Get(i).Name())
		require.NotContains(t, strings.ToLower(name), "boundary",
			"saas.accounts.v1.SolutionRegistration carries field %q: the seed selects the boundary a "+
				"Work Context is sealed under, so a consumer that caches the registry would hold "+
				"every solution's seed", name)
	}
}
