package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// installationConnectHandler wraps the gRPC InstallationServer so the same auth
// gates and handler bodies run for a Connect HTTP request. Catalog generation
// owns registration through the shared singleton binding.
type installationConnectHandler struct {
	inner *InstallationServer
}

func (h *installationConnectHandler) InstallSolution(ctx context.Context, req *connect.Request[gen.InstallSolutionRequest]) (*connect.Response[gen.Installation], error) {
	return unary(ctx, req, h.inner.InstallSolution)
}

func (h *installationConnectHandler) UninstallSolution(ctx context.Context, req *connect.Request[gen.UninstallSolutionRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.UninstallSolution)
}

func (h *installationConnectHandler) TransferInstallationOwnership(ctx context.Context, req *connect.Request[gen.TransferInstallationOwnershipRequest]) (*connect.Response[gen.Installation], error) {
	return unary(ctx, req, h.inner.TransferInstallationOwnership)
}

func (h *installationConnectHandler) GetInstallation(ctx context.Context, req *connect.Request[gen.GetInstallationRequest]) (*connect.Response[gen.GetInstallationResponse], error) {
	return unary(ctx, req, h.inner.GetInstallation)
}
