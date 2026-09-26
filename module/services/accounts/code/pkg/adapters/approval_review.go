package adapters

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// ApprovalReviewServer is the person-facing approval surface: the requester and
// the assigned approvers of a request read it and decide it as themselves.
// Visibility, the subject-hash binding and the decision are enforced by the
// business layer inside one org transaction; this adapter only establishes who
// the caller is and that they belong to the organization.
type ApprovalReviewServer struct {
	gen.UnimplementedApprovalReviewServiceServer
}

// ApprovalReviewSingleton is the connect_bindings.yaml source for the service.
func ApprovalReviewSingleton() *ApprovalReviewServer { return &ApprovalReviewServer{} }

type approvalReviewConnectHandler struct{ inner *ApprovalReviewServer }

func (h *approvalReviewConnectHandler) GetApprovalReview(ctx context.Context, r *connect.Request[gen.GetApprovalReviewRequest]) (*connect.Response[gen.ApprovalReview], error) {
	return unary(ctx, r, h.inner.GetApprovalReview)
}

func (h *approvalReviewConnectHandler) DecideApprovalReview(ctx context.Context, r *connect.Request[gen.DecideApprovalReviewRequest]) (*connect.Response[gen.ApprovalReview], error) {
	return unary(ctx, r, h.inner.DecideApprovalReview)
}

// approvalReviewActor returns the authenticated caller once it is known to be a
// member of orgID. The caller is the only identity a review ever acts as.
func approvalReviewActor(ctx context.Context, orgID string) (string, error) {
	actor, err := requireAuth(ctx)
	if err != nil {
		return "", err
	}
	if err := requireOrgMember(ctx, actor, orgID); err != nil {
		return "", err
	}
	return actor, nil
}

func (s *ApprovalReviewServer) GetApprovalReview(ctx context.Context, r *gen.GetApprovalReviewRequest) (*gen.ApprovalReview, error) {
	if err := Validate(r); err != nil {
		return nil, err
	}
	actor, err := approvalReviewActor(ctx, r.GetOrgId())
	if err != nil {
		return nil, err
	}
	review, err := service.ReviewApproval(ctx, r.GetOrgId(), r.GetId(), actor)
	if err != nil {
		return nil, mapApprovalReviewError(err)
	}
	return approvalReviewToProto(review)
}

func (s *ApprovalReviewServer) DecideApprovalReview(ctx context.Context, r *gen.DecideApprovalReviewRequest) (*gen.ApprovalReview, error) {
	if err := Validate(r); err != nil {
		return nil, err
	}
	actor, err := approvalReviewActor(ctx, r.GetOrgId())
	if err != nil {
		return nil, err
	}
	review, err := service.DecideReviewedApproval(ctx, r.GetOrgId(), r.GetId(), business.DecideInput{
		Decider:             actor,
		Decision:            business.ApprovalDecisionKind(r.GetDecision()),
		Reason:              r.GetReason(),
		ExpectedSubjectHash: r.GetSubjectHash(),
	})
	if err != nil {
		return nil, mapApprovalReviewError(err)
	}
	return approvalReviewToProto(review)
}

func approvalReviewToProto(review *business.ApprovalReview) (*gen.ApprovalReview, error) {
	req := review.Request
	subject, err := structpb.NewStruct(req.Subject)
	if err != nil {
		return nil, status.Error(codes.Internal, "approval subject is not representable")
	}
	out := &gen.ApprovalReview{
		Id:          req.ID,
		State:       string(req.State),
		Subject:     subject,
		SubjectHash: review.SubjectHash,
		RequestedBy: req.RequestedBy,
		Approvers:   req.Policy.ApproverSet,
		Quorum:      uint32(req.Quorum),
	}
	for _, d := range review.Decisions {
		out.Decisions = append(out.Decisions, &gen.ApprovalReviewDecision{
			Actor:     d.Decider,
			Decision:  string(d.Decision),
			Reason:    d.Reason,
			DecidedAt: timestamppb.New(d.DecidedAt),
		})
	}
	return out, nil
}

// mapApprovalReviewError maps the engine's typed errors. A request the caller
// is not named on is NotFound, the same answer as one that does not exist; a
// stale subject hash or a closed request is FailedPrecondition; a decision the
// approval policy refuses (self-approval, not an assigned approver) is
// PermissionDenied.
func mapApprovalReviewError(err error) error {
	var se *business.StoreError
	if errors.As(err, &se) {
		switch se.StoreErrorType {
		case business.ErrTypeNotFound:
			return status.Error(codes.NotFound, "approval not found")
		case business.ErrTypeConflict:
			return status.Error(codes.FailedPrecondition, err.Error())
		case business.ErrTypePermission:
			return status.Error(codes.PermissionDenied, err.Error())
		case business.ErrTypeValidation:
			return status.Error(codes.InvalidArgument, err.Error())
		}
	}
	return status.Error(codes.Internal, "approval review failed")
}

var _ gen.ApprovalReviewServiceServer = (*ApprovalReviewServer)(nil)
