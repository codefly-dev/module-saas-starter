package adapters

import (
	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"slices"

	"connectrpc.com/connect"
	workcontext "github.com/codefly-dev/sdk-go/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (s *ModuleCapabilitiesServer) CheckWorkContextRecordAccess(ctx context.Context, req *gen.CheckWorkContextRecordAccessRequest) (*gen.CheckWorkContextRecordAccessResponse, error) {
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
	values := md.Get(workcontext.WorkContextHeaderName)
	if len(values) != 1 {
		return nil, status.Error(codes.Unauthenticated, "one viewer Work Context is required")
	}
	token, err := workcontext.ParseWorkContextToken(values[0])
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid viewer Work Context")
	}
	claims, err := authority.verifier.Verify(token, workcontext.WorkContextExpectations{Issuer: authority.issuer})
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid viewer Work Context")
	}
	declared, err := service.ModuleContentResources(claims.GetAudience())
	if err != nil || !slices.Contains(declared, req.ResourceType) {
		return nil, status.Error(codes.PermissionDenied, "declared resource scope required")
	}
	if claims.GetTenantId() == "" || claims.GetOwnerPrincipalId() == "" {
		return nil, status.Error(codes.PermissionDenied, "tenant viewer required")
	}
	if err = workcontext.RequireWorkContextScope(claims, workcontext.WorkContextScopeRequirement{ResourceKind: req.ResourceType, Action: req.Action, ResourceID: req.ResourceId}); err != nil {
		return nil, status.Error(codes.PermissionDenied, "record scope required")
	}
	if len(claims.GetActorChain()) > 0 && authority.journal == nil {
		return nil, status.Error(codes.Unavailable, "actor chain authority is not configured")
	}
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, claims.GetOwnerPrincipalId(), claims.GetTenantId())
	subjects := []string{claims.GetOwnerPrincipalId()}
	for _, actor := range claims.GetActorChain() {
		if !slices.Contains(subjects, actor.GetPrincipalId()) {
			subjects = append(subjects, actor.GetPrincipalId())
		}
	}
	current := func(ctx context.Context) error {
		_, err := authority.requireCurrentAuthority(ctx, claims.GetTenantId(), claims)
		return err
	}
	decision, err := service.CheckDelegatedRecordAccess(ctx, claims.GetTenantId(), subjects, req.ResourceType, req.ResourceId, req.Action, current)
	if err != nil {
		return nil, err
	}
	// Recheck after the record transaction so a concurrent authority revision
	// or delegation revocation cannot release a stale decision.
	if err = current(ctx); err != nil {
		return nil, err
	}
	return decision, nil
}
func (h *moduleCapabilitiesConnectHandler) CheckWorkContextRecordAccess(ctx context.Context, req *connect.Request[gen.CheckWorkContextRecordAccessRequest]) (*connect.Response[gen.CheckWorkContextRecordAccessResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(workcontext.WorkContextHeaderName, req.Header().Values(workcontext.WorkContextHeaderName)...)
	out, err := h.inner.CheckWorkContextRecordAccess(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}
