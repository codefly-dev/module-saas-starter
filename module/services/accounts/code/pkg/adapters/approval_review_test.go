package adapters

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	reviewTestOrg = "00000000-0000-4000-8000-000000000001"
	reviewTestID  = "00000000-0000-4000-8000-000000000002"
)

func TestApprovalReviewRequiresAuthenticatedActor(t *testing.T) {
	s := ApprovalReviewSingleton()
	_, err := s.GetApprovalReview(context.Background(), &gen.GetApprovalReviewRequest{OrgId: reviewTestOrg, Id: reviewTestID})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = s.DecideApprovalReview(context.Background(), &gen.DecideApprovalReviewRequest{OrgId: reviewTestOrg, Id: reviewTestID, Decision: "approve", SubjectHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestApprovalReviewRequiresReviewedSubject(t *testing.T) {
	_, err := ApprovalReviewSingleton().DecideApprovalReview(context.Background(), &gen.DecideApprovalReviewRequest{OrgId: reviewTestOrg, Id: reviewTestID, Decision: "approve"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestMapApprovalReviewError(t *testing.T) {
	for typ, want := range map[business.StoreErrorType]codes.Code{
		business.ErrTypeNotFound:   codes.NotFound,
		business.ErrTypeConflict:   codes.FailedPrecondition,
		business.ErrTypePermission: codes.PermissionDenied,
		business.ErrTypeValidation: codes.InvalidArgument,
		business.ErrTypeInternal:   codes.Internal,
	} {
		require.Equal(t, want, status.Code(mapApprovalReviewError(business.NewStoreError(errors.New("x"), typ))), typ)
	}
	require.Equal(t, codes.Internal, status.Code(mapApprovalReviewError(errors.New("x"))))
	// A request the caller is not named on reads exactly like a missing one.
	require.Equal(t, "approval not found", status.Convert(mapApprovalReviewError(business.NewStoreError(errors.New("approval 1 not found"), business.ErrTypeNotFound))).Message())
}
