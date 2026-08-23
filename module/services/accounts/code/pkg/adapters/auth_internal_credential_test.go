package adapters

import (
	"context"
	"testing"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestRequireInternalCredential_RejectsTenantCallers locks the H2 fix: the
// internal authority oracles no longer accept a bare tenant JWT. Before the
// fix, requireInternalOrAuth passed any authenticated caller, so the internal
// gRPC surface co-hosted on the public port was an any-JWT authorization
// oracle. Only a valid internal service token may pass now.
func TestRequireInternalCredential_RejectsTenantCallers(t *testing.T) {
	t.Run("verified tenant JWT without an internal token is rejected", func(t *testing.T) {
		// A direct-JWT caller that requireInternalOrAuth would have accepted.
		ctx := context.WithValue(context.Background(), wool.UserAuthIDKey, "tenant-user")
		require.Equal(t, codes.Unauthenticated, status.Code(requireInternalCredential(ctx)))
	})

	t.Run("anonymous caller is rejected", func(t *testing.T) {
		require.Equal(t, codes.Unauthenticated, status.Code(requireInternalCredential(context.Background())))
	})

	t.Run("valid internal token is accepted", func(t *testing.T) {
		SetInternalToken("internal-oracle-test-token")
		t.Cleanup(func() { SetInternalToken("") })
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("x-codefly-internal-token", "internal-oracle-test-token"))
		require.NoError(t, requireInternalCredential(ctx))
	})
}

// TestDecideRejectsTenantCallers locks that Decide, an EXPOSURE_INTERNAL
// authority oracle, no longer accepts a bare tenant JWT. A verified direct-JWT
// caller with no internal token is rejected; a valid internal token passes.
func TestDecideRejectsTenantCallers(t *testing.T) {
	req := &gen.DecideRequest{
		PrincipalId: "019fec91-1000-7000-8000-000000000001",
		OrgId:       "019fec91-1000-7000-8000-000000000002",
		Action:      "read",
	}

	tenant := context.WithValue(context.Background(), wool.UserAuthIDKey, "tenant-user")
	_, err := (&PermServer{}).Decide(tenant, req)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
