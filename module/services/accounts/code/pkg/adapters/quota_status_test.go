package adapters

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

func TestQuotaStatusMapsWrappedExhaustion(t *testing.T) {
	err := quotaStatusError(fmt.Errorf("create resource: %w", business.ErrEntitlementQuotaExceeded))
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Fatalf("quota status = %s, want %s", got, codes.ResourceExhausted)
	}
	if got, want := status.Convert(err).Message(), "create resource: entitlement quota exceeded"; got != want {
		t.Fatalf("quota message = %q, want %q", got, want)
	}
}

func TestQuotaStatusPreservesUnrelatedFailure(t *testing.T) {
	want := errors.New("database unavailable")
	if got := quotaStatusError(want); !errors.Is(got, want) {
		t.Fatalf("quotaStatus replaced unrelated error: %v", got)
	}
}
