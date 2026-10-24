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
	// The person who hit the limit reads this message; the wrapped call path is
	// not theirs to read.
	if got, want := status.Convert(err).Message(), "entitlement quota exceeded"; got != want {
		t.Fatalf("quota message = %q, want %q", got, want)
	}
}

// The refusal names the quota and its counts, so the portal can show the
// person which limit they hit instead of "Failed to create invitation".
func TestQuotaRefusalCarriesTheQuotaSentence(t *testing.T) {
	quota := &business.EntitlementQuotaError{Feature: business.EntitlementSeats, Used: 5, Limit: 5}
	for name, mapped := range map[string]error{
		"invitation": invitationStatusError(fmt.Errorf("CreateInvitation: cannot store invitation: %w", quota)),
		"membership": orgMembershipStatusError(fmt.Errorf("AddOrgMember: %w", quota)),
		"api key":    quotaStatusError(fmt.Errorf("cannot store API key: %w", quota)),
	} {
		if got := status.Code(mapped); got != codes.ResourceExhausted {
			t.Fatalf("%s: quota status = %s, want %s", name, got, codes.ResourceExhausted)
		}
		if got, want := status.Convert(mapped).Message(), "seats quota exhausted: used 5 of 5"; got != want {
			t.Fatalf("%s: quota message = %q, want %q", name, got, want)
		}
	}
}

func TestQuotaStatusPreservesUnrelatedFailure(t *testing.T) {
	want := errors.New("database unavailable")
	if got := quotaStatusError(want); !errors.Is(got, want) {
		t.Fatalf("quotaStatus replaced unrelated error: %v", got)
	}
}
