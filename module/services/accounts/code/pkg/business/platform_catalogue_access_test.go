package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// catalogueAccessStore answers the role lookup the gate makes and records
// whether anything went on to read the control plane. Every other method is the
// embedded nil interface, so a path that got past the gate panics rather than
// passing quietly.
type catalogueAccessStore struct {
	business.Store
	role      string
	planeRead bool
}

func (s *catalogueAccessStore) GetPlatformRole(context.Context, string) (string, error) {
	return s.role, nil
}

func (s *catalogueAccessStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	s.planeRead = true
	return fn(ctx)
}

// The Catalogue is gated in the business method as well as in the handler, and
// this proves the business half on its own: the handler is not in the call at
// all. The handler half is covered by TestPlatformAdminHandlersRejectNonAdmin and
// TestPlatformAdminHandlersEnforceMinimumRole in pkg/adapters — which, as those
// tests now say, cannot isolate the handler gate, because both gates read the
// same stored role and return the same code. This test is the layer that can be
// isolated, and it is the one that has to hold if a transport is ever added.
func TestListPlatformCatalogueRequiresSuperAdminInBusiness(t *testing.T) {
	const actor = "019f6bf7-5b1c-730d-9687-fe6d4aff31ee"
	for _, role := range []string{"", "support", "billing"} {
		t.Run("role "+role, func(t *testing.T) {
			store := &catalogueAccessStore{role: role}
			service, err := business.NewService(store)
			require.NoError(t, err)

			catalogue, err := service.ListPlatformCatalogue(context.Background(), actor, false)

			require.Error(t, err)
			require.Nil(t, catalogue)
			var storeErr *business.StoreError
			require.ErrorAs(t, err, &storeErr)
			require.Equal(t, business.ErrTypePermission, storeErr.StoreErrorType)
			require.False(t, store.planeRead, "the role is refused before any record is read")
		})
	}
}
