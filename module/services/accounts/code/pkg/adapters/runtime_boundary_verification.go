package adapters

import (
	"context"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"connectrpc.com/connect"
	"github.com/codefly-dev/sdk-go/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (s *ModuleCapabilitiesServer) VerifyWorkContextRuntimeBoundary(ctx context.Context, req *gen.VerifyWorkContextRuntimeBoundaryRequest) (*gen.VerifyWorkContextRuntimeBoundaryResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get(workcontext.WorkContextHeaderName)) != 1 {
		return nil, status.Error(codes.Unauthenticated, "one module Work Context required")
	}
	authority := WorkContextSingleton()
	if service == nil || authority == nil || authority.verifier == nil || authority.configureErr != nil || authority.authority == nil {
		return nil, status.Error(codes.Unavailable, "work context authority unavailable")
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	token, err := workcontext.ParseWorkContextToken(req.ForwardedWorkContextToken)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "invalid forwarded Work Context")
	}
	claims, err := authority.verifier.Verify(token, workcontext.WorkContextExpectations{Issuer: authority.issuer})
	if err != nil || claims.GetTenantId() == "" || claims.GetOwnerPrincipalId() == "" || claims.GetTaskId() == "" {
		return nil, status.Error(codes.PermissionDenied, "invalid forwarded Work Context")
	}
	if len(claims.ActorChain) > 0 && authority.journal == nil {
		return nil, status.Error(codes.Unavailable, "delegation journal unavailable")
	}
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, claims.OwnerPrincipalId, claims.TenantId)
	if _, err := authority.requireCurrentAuthority(ctx, claims.TenantId, claims); err != nil {
		return nil, err
	}
	if err := service.VerifyModuleRuntimeBoundary(ctx, caller, claims.TenantId, claims.Audience, claims.TaskId); err != nil {
		return nil, err
	}
	// Recheck authority after the registry read, as on the placed-record oracle.
	if _, err := authority.requireCurrentAuthority(ctx, claims.TenantId, claims); err != nil {
		return nil, err
	}
	return &gen.VerifyWorkContextRuntimeBoundaryResponse{TenantId: claims.TenantId, BoundaryId: claims.TaskId}, nil
}

func (h *moduleCapabilitiesConnectHandler) VerifyWorkContextRuntimeBoundary(ctx context.Context, req *connect.Request[gen.VerifyWorkContextRuntimeBoundaryRequest]) (*connect.Response[gen.VerifyWorkContextRuntimeBoundaryResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(workcontext.WorkContextHeaderName, req.Header().Values(workcontext.WorkContextHeaderName)...)
	out, err := h.inner.VerifyWorkContextRuntimeBoundary(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}
