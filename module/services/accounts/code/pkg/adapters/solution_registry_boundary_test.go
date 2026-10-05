package adapters

import (
	"context"
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
	lease := time.Now().UTC().Add(time.Minute)
	store := &boundaryRegistryStore{record: &business.SolutionRegistration{
		SolutionID:      "example-solution",
		Publisher:       "solution:example-solution",
		Revision:        7,
		RuntimeBoundary: boundaryStoredSeed,
		Frontend: &business.SolutionFrontendHalf{
			Revision: 7, Manifest: `{"id":"example-solution"}`, LeaseExpiresAt: lease,
		},
		Backend: &business.SolutionBackendHalf{
			Revision: 7, Upstream: "http://upstream.example:8080",
			ServiceAlias: "example-solution", LeaseExpiresAt: lease,
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

func TestSolutionRegistrationResponsesCarryNoRuntimeBoundary(t *testing.T) {
	store := newBoundaryRegistryService(t)
	server := SolutionRegistrySingleton()

	t.Run("the whole-registry listing", func(t *testing.T) {
		out, err := server.ListSolutionRegistrations(
			context.Background(), &gen.ListSolutionRegistrationsRequest{IncludeTombstoned: true})
		require.NoError(t, err)
		require.Len(t, out.GetRegistrations(), 1)
		require.Empty(t, out.GetRegistrations()[0].GetRuntimeBoundary(),
			"a consumer that caches the registry would hold every solution's seed")
	})

	t.Run("a deregistration", func(t *testing.T) {
		out, err := server.DeleteSolutionRegistration(
			context.Background(), &gen.DeleteSolutionRegistrationRequest{SolutionId: "example-solution"})
		require.NoError(t, err)
		require.Empty(t, out.GetRuntimeBoundary())
		// The store still holds it: the tombstone keeps the seed so a
		// reactivated registration keeps naming the runs it already admitted.
		require.Equal(t, boundaryStoredSeed, store.saved.RuntimeBoundary)
	})

	// Both halves' own writes, including the backend half — the one that mints.
	// Even that registrant is not told: accounts seals the boundary from the
	// credential, so nothing in a solution has a use for the seed.
	for name, half := range map[string]*gen.PutSolutionRegistrationRequest{
		"the frontend half's own write": {
			Half: &gen.PutSolutionRegistrationRequest_Frontend{
				Frontend: &gen.SolutionFrontendRegistration{Manifest: `{"id":"example-solution"}`},
			},
		},
		"the backend half's own write": {
			Half: &gen.PutSolutionRegistrationRequest_Backend{
				Backend: &gen.SolutionBackendRegistration{
					Upstream:     "http://upstream.example:8080",
					ServiceAlias: "example-solution",
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newBoundaryRegistryService(t)
			server := SolutionRegistrySingleton()
			request := half
			request.SolutionId = "example-solution"
			request.Publisher = "solution:example-solution"
			request.LeaseSeconds = 120

			out, err := server.PutSolutionRegistration(context.Background(), request)
			require.NoError(t, err)
			require.Empty(t, out.GetRuntimeBoundary())
			require.Equal(t, boundaryStoredSeed, store.saved.RuntimeBoundary)
		})
	}
}
