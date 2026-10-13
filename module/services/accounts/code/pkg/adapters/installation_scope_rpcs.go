package adapters

import (
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *InstallationServer) NarrowInstallation(ctx context.Context, req *gen.NarrowInstallationRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}
	scope, err := business.ParseInstallationScope(req.GetScope())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := service.NarrowInstallation(ctx, actorID, req.GetOrgId(), req.GetInstallationId(), scope); err != nil {
		return nil, mapInstallationError(err)
	}
	return &emptypb.Empty{}, nil
}

func (h *installationConnectHandler) NarrowInstallation(ctx context.Context, req *connect.Request[gen.NarrowInstallationRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.NarrowInstallation)
}
