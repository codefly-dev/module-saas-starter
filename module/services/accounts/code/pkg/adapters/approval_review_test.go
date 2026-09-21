package adapters

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestApprovalReviewRequiresAuthenticatedActor(t *testing.T) {
	s := ApprovalReviewSingleton()
	_, err := s.GetApprovalReview(context.Background(), &gen.GetApprovalReviewRequest{OrgId: "00000000-0000-4000-8000-000000000001", Id: "00000000-0000-4000-8000-000000000002"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = s.DecideApprovalReview(context.Background(), &gen.DecideApprovalReviewRequest{OrgId: "00000000-0000-4000-8000-000000000001", Id: "00000000-0000-4000-8000-000000000002", Decision: "approve", SubjectHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
func TestApprovalReviewRequiresReviewedSubject(t *testing.T) {
	_, err := ApprovalReviewSingleton().DecideApprovalReview(context.Background(), &gen.DecideApprovalReviewRequest{OrgId: "00000000-0000-4000-8000-000000000001", Id: "00000000-0000-4000-8000-000000000002", Decision: "approve"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
