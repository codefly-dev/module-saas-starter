package business

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A principal pinned to a tenant is issued in that tenant only; cross_tenant
// never moves a pin. One rule, read by the mint and the installation issue
// alike.
func TestAPinnedPrincipalNeverActsOutsideItsPin(t *testing.T) {
	const pinned, other = "00000000-0000-7000-8000-0000000000a1", "00000000-0000-7000-8000-0000000000b2"
	for _, cross := range []bool{false, true} {
		grant := ModulePrincipalGrant{Tenant: pinned, CrossTenant: cross}
		require.True(t, grant.IssuedIn(pinned))
		require.False(t, grant.IssuedIn(other), "cross_tenant=%v moved the pin", cross)

		_, err := moduleMintOrganization(grant, []string{other})
		require.Equal(t, codes.PermissionDenied, status.Code(err), "cross_tenant=%v minted outside the pin", cross)
		org, err := moduleMintOrganization(grant, nil)
		require.NoError(t, err)
		require.Equal(t, pinned, org)
	}
	shared := ModulePrincipalGrant{CrossTenant: true}
	require.True(t, shared.IssuedIn(other))
	require.False(t, shared.IssuedIn(""))
}
