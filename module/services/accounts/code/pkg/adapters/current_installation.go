package adapters

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"github.com/codefly-dev/sdk-go/workcontext"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (s *ModuleCapabilitiesServer) GetCurrentInstallation(ctx context.Context, req *gen.ModuleCurrentInstallationRequest) (*gen.ModuleCurrentInstallationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	a, err := authenticateModuleParent(ctx, req.ParentWorkContextToken)
	if err != nil {
		return nil, err
	}
	// No caller-selected tenant, source, grants or token minting. Module and
	// parent remain two independently authenticated identities.
	out, err := service.ModuleCurrentInstallation(a.ctx, a.caller, a.parent.TenantId, a.parent.OwnerPrincipalId, a.parent.Audience, req.InstallationId)
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return nil, mapInstallationError(err)
	}
	return out, nil
}

func (h *moduleCapabilitiesConnectHandler) GetCurrentInstallation(ctx context.Context, req *connect.Request[gen.ModuleCurrentInstallationRequest]) (*connect.Response[gen.ModuleCurrentInstallationResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(workcontext.WorkContextHeaderName, req.Header().Values(workcontext.WorkContextHeaderName)...)
	out, err := h.inner.GetCurrentInstallation(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}
