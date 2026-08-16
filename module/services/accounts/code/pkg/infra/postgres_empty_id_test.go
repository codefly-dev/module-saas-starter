package infra_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A blank caller id must never reach a uuid-typed column: Postgres would raise
// `invalid input syntax for type uuid: ""` (22P02), which surfaces as an opaque
// HTTP 500. The store fails closed with a typed InvalidArgument instead. This is
// defense-in-depth behind requireAuth, which already rejects empty actors.
func TestGetUserRejectsBlankID(t *testing.T) {
	for _, id := range []string{"", "   "} {
		_, err := testStore.GetUser(testCtx, id)
		require.Equal(t, codes.InvalidArgument, status.Code(err),
			"blank user id %q must return a typed error, not a Postgres 22P02", id)
	}
}

func TestGetPlatformRoleRejectsBlankID(t *testing.T) {
	for _, id := range []string{"", "   "} {
		_, err := testStore.GetPlatformRole(testCtx, id)
		require.Equal(t, codes.InvalidArgument, status.Code(err),
			"blank actor id %q must return a typed error, not a Postgres 22P02", id)
	}
}
