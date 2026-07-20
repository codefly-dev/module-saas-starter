package adapters

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

func TestConsumeUsageRequestValidationBoundsMeterMetadata(t *testing.T) {
	valid := &gen.ConsumeUsageRequest{
		OrganizationId: "11111111-1111-4111-8111-111111111111",
		Meter:          "api_calls_monthly",
		Quantity:       1,
		IdempotencyKey: "request-1",
		Dimensions:     map[string]string{"source.service": "warden"},
	}
	require.NoError(t, Validate(valid))

	invalidKey := proto.Clone(valid).(*gen.ConsumeUsageRequest)
	invalidKey.Dimensions = map[string]string{"Not Canonical": "value"}
	require.Error(t, Validate(invalidKey))

	tooMany := proto.Clone(valid).(*gen.ConsumeUsageRequest)
	tooMany.Dimensions = make(map[string]string, 33)
	for index := 0; index < 33; index++ {
		tooMany.Dimensions[fmt.Sprintf("key_%d", index)] = "value"
	}
	require.Error(t, Validate(tooMany))
}
