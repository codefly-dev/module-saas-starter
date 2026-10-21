package adapters

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	"connectrpc.com/connect"
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (s *ModuleCapabilitiesServer) ListExecutableArtifactSources(ctx context.Context, req *gen.ModuleExecutableArtifactSourcesRequest) (*gen.ModuleExecutableArtifactSourcesResponse, error) {
	ctx, releaseAuthority, err := service.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "artifact source authority unavailable")
	}
	defer releaseAuthority()
	if err := Validate(req); err != nil {
		return nil, err
	}
	a, err := authenticateModuleParent(ctx, req.ParentWorkContextToken)
	if err != nil {
		return nil, err
	}
	return service.ModuleExecutableArtifactSources(a.ctx, a.caller, a.parent.TenantId, a.parent.OwnerPrincipalId, a.parent.Audience, req.InstallationId, req.PolicyId)
}

func (h *moduleCapabilitiesConnectHandler) ListExecutableArtifactSources(ctx context.Context, req *connect.Request[gen.ModuleExecutableArtifactSourcesRequest]) (*connect.Response[gen.ModuleExecutableArtifactSourcesResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(workContextHeader, req.Header().Values(workContextHeader)...)
	out, err := h.inner.ListExecutableArtifactSources(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}
