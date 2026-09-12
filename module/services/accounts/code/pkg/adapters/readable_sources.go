package adapters

import (
	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"

	"connectrpc.com/connect"
	codefly "github.com/codefly-dev/sdk-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ListReadableSourceCollections verifies the exact forwarded viewer capability;
// this operation does not use the installation's module capability identity.
func (s *ModuleCapabilitiesServer) ListReadableSourceCollections(ctx context.Context, req *gen.ListReadableSourceCollectionsRequest) (*gen.ListReadableSourceCollectionsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	authority := WorkContextSingleton()
	if authority == nil || authority.verifier == nil || authority.configureErr != nil {
		return nil, status.Error(codes.Unavailable, "Work Context authority is not configured")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get(codefly.WorkContextHeaderName)
	if len(values) != 1 {
		return nil, status.Error(codes.Unauthenticated, "one viewer Work Context is required")
	}
	token, err := codefly.ParseWorkContextToken(values[0])
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid viewer Work Context")
	}
	claims, err := authority.verifier.Verify(token, codefly.WorkContextExpectations{Issuer: authority.issuer, Audience: "documents"})
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid viewer Work Context")
	}
	if err = codefly.RequireWorkContextScope(claims, codefly.WorkContextScopeRequirement{ResourceKind: "documents", Action: "read"}); err != nil {
		return nil, status.Error(codes.PermissionDenied, "documents read scope required")
	}
	if claims.GetTenantId() == "" {
		return &gen.ListReadableSourceCollectionsResponse{}, nil
	}
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, claims.GetOwnerPrincipalId(), claims.GetTenantId())
	subjects := []string{claims.GetOwnerPrincipalId()}
	for _, actor := range claims.GetActorChain() {
		subjects = append(subjects, actor.GetPrincipalId())
	}
	return service.ReadableSourceCollections(ctx, claims.GetTenantId(), subjects, req, func(ctx context.Context) error {
		_, err := authority.requireCurrentAuthority(ctx, claims.GetTenantId(), claims)
		return err
	})
}
func (h *moduleCapabilitiesConnectHandler) ListReadableSourceCollections(ctx context.Context, req *connect.Request[gen.ListReadableSourceCollectionsRequest]) (*connect.Response[gen.ListReadableSourceCollectionsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(codefly.WorkContextHeaderName, req.Header().Values(codefly.WorkContextHeaderName)...)
	response, err := h.inner.ListReadableSourceCollections(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(response), nil
}
