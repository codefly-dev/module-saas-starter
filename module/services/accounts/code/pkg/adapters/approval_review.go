package adapters

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"connectrpc.com/connect"
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"slices"
)

type ApprovalReviewServer struct {
	gen.UnimplementedApprovalReviewServiceServer
}

func ApprovalReviewSingleton() *ApprovalReviewServer { return &ApprovalReviewServer{} }

type approvalReviewConnectHandler struct{ inner *ApprovalReviewServer }

func (h *approvalReviewConnectHandler) GetApprovalReview(ctx context.Context, r *connect.Request[gen.GetApprovalReviewRequest]) (*connect.Response[gen.ApprovalReview], error) {
	return unary(ctx, r, h.inner.GetApprovalReview)
}
func (h *approvalReviewConnectHandler) DecideApprovalReview(ctx context.Context, r *connect.Request[gen.DecideApprovalReviewRequest]) (*connect.Response[gen.ApprovalReview], error) {
	return unary(ctx, r, h.inner.DecideApprovalReview)
}
func approvalReviewAccess(ctx context.Context, org, id string) (string, *business.ApprovalRequest, error) {
	actor, err := requireAuth(ctx)
	if err != nil {
		return "", nil, err
	}
	if err = requireOrgMember(ctx, actor, org); err != nil {
		return "", nil, err
	}
	r, err := service.GetApprovalRequest(ctx, org, id)
	if err != nil {
		return "", nil, mapDelegationError(err)
	}
	if r.RequestedBy != actor && !slices.Contains(r.Policy.ApproverSet, actor) {
		return "", nil, status.Error(codes.NotFound, "approval not found")
	}
	return actor, r, nil
}
func approvalReviewProjection(ctx context.Context, r *business.ApprovalRequest) (*gen.ApprovalReview, error) {
	subject, err := structpb.NewStruct(r.Subject)
	if err != nil {
		return nil, status.Error(codes.Internal, "invalid subject")
	}
	decisions, err := service.ApprovalDecisions(ctx, r.OrgID, r.ID)
	if err != nil {
		return nil, mapDelegationError(err)
	}
	out := &gen.ApprovalReview{Id: r.ID, State: string(r.State), Subject: subject, SubjectHash: business.ApprovalSubjectHash(r.Subject), RequestedBy: r.RequestedBy, Approvers: r.Policy.ApproverSet, Quorum: uint32(r.Quorum)}
	for _, d := range decisions {
		out.Decisions = append(out.Decisions, &gen.ApprovalReviewDecision{Actor: d.Decider, Decision: string(d.Decision), Reason: d.Reason, DecidedAt: timestamppb.New(d.DecidedAt)})
	}
	return out, nil
}
func (s *ApprovalReviewServer) GetApprovalReview(ctx context.Context, r *gen.GetApprovalReviewRequest) (*gen.ApprovalReview, error) {
	if err := Validate(r); err != nil {
		return nil, err
	}
	_, a, err := approvalReviewAccess(ctx, r.OrgId, r.Id)
	if err != nil {
		return nil, err
	}
	return approvalReviewProjection(ctx, a)
}
func (s *ApprovalReviewServer) DecideApprovalReview(ctx context.Context, r *gen.DecideApprovalReviewRequest) (*gen.ApprovalReview, error) {
	if err := Validate(r); err != nil {
		return nil, err
	}
	actor, _, err := approvalReviewAccess(ctx, r.OrgId, r.Id)
	if err != nil {
		return nil, err
	}
	_, err = service.Decide(ctx, r.OrgId, r.Id, business.DecideInput{Decider: actor, Decision: business.ApprovalDecisionKind(r.Decision), Reason: r.Reason, ExpectedSubjectHash: r.SubjectHash})
	if err != nil {
		return nil, mapDelegationError(err)
	}
	a, err := service.GetApprovalRequest(ctx, r.OrgId, r.Id)
	if err != nil {
		return nil, mapDelegationError(err)
	}
	return approvalReviewProjection(ctx, a)
}
