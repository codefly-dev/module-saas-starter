//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// OrganizationIDExists is the first thing that runs on a caller-chosen
// organization id — the fixture seeder's declared ids and the tenant a module
// principal grant declares. organizations.id is a UUID column, so handing a
// malformed value straight to the driver returns "invalid input syntax for
// type uuid", which names neither the id nor the caller. Parse first so the
// error the operator sees is the useful one.
func TestOrganizationIDExistsRejectsAMalformedID(t *testing.T) {
	ctx := testCtx

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		for _, id := range []string{"acme", "", "00000000-0000-7000-8000-0000000000b"} {
			exists, err := testStore.OrganizationIDExists(ctx, id)
			require.Error(t, err, "OrganizationIDExists accepted %q", id)
			require.False(t, exists)
			require.NotContains(t, err.Error(), "invalid input syntax",
				"the driver's uuid error must not be what surfaces")
		}
		return nil
	}))
}
