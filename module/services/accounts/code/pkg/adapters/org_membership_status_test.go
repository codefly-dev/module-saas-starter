package adapters

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// An organization that may not be left without an administrator is a
// precondition the caller failed, not an internal fault and not a quota. The
// three map to different codes on purpose: a client retries an exhausted quota
// after buying seats, and retries nothing here.
func TestOrgMembershipStatusMapsContinuityToFailedPrecondition(t *testing.T) {
	err := orgMembershipStatusError(
		fmt.Errorf("AddOrgMember: cannot add member: %w", business.ErrOrgAdminContinuity))
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("continuity status = %s, want %s", got, codes.FailedPrecondition)
	}
	if got, want := status.Convert(err).Message(), business.ErrOrgAdminContinuity.Error(); got != want {
		t.Fatalf("continuity message = %q, want %q", got, want)
	}
}

func TestOrgMembershipStatusKeepsQuotaExhaustionDistinct(t *testing.T) {
	err := orgMembershipStatusError(
		fmt.Errorf("cannot add member: %w", business.ErrEntitlementQuotaExceeded))
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Fatalf("quota status = %s, want %s", got, codes.ResourceExhausted)
	}
}

func TestOrgMembershipStatusPreservesUnrelatedFailure(t *testing.T) {
	want := errors.New("database unavailable")
	if got := orgMembershipStatusError(want); !errors.Is(got, want) {
		t.Fatalf("orgMembershipStatus replaced unrelated error: %v", got)
	}
}

// Invitation redemption is a membership upsert too, so a rejected redemption
// must not collapse into the generic internal fallback.
func TestInvitationStatusMapsContinuityToFailedPrecondition(t *testing.T) {
	err := invitationStatusError(
		fmt.Errorf("accept invitation: %w", business.ErrOrgAdminContinuity))
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("continuity status = %s, want %s", got, codes.FailedPrecondition)
	}
	if got, want := status.Convert(err).Message(), business.ErrOrgAdminContinuity.Error(); got != want {
		t.Fatalf("continuity message = %q, want %q", got, want)
	}
}
