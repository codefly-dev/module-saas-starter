package adapters

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestQuotaStatusErrorMapsToResourceExhausted(t *testing.T) {
	err := quotaStatusError(&business.EntitlementQuotaError{
		Feature: business.EntitlementSeats, Used: 5, Limit: 5,
	})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))

	original := errors.New("database unavailable")
	require.ErrorIs(t, quotaStatusError(original), original)
}

func TestCreateAPIKeyRequestRequiresFutureExpiration(t *testing.T) {
	request := &gen.CreateAPIKeyRequest{
		OrganizationId: "11111111-1111-4111-8111-111111111111",
		Name:           "automation",
		Environment:    gen.APIKeyEnvironment_API_KEY_ENVIRONMENT_TEST,
		ExpiresAt:      timestamppb.New(time.Now().Add(-time.Minute)),
	}
	require.Error(t, Validate(request))

	request.ExpiresAt = timestamppb.New(time.Now().Add(time.Hour))
	require.NoError(t, Validate(request))
}
