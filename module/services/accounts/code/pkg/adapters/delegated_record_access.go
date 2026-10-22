package adapters

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (s *ModuleCapabilitiesServer) CheckWorkContextRecordAccess(ctx context.Context, req *gen.CheckWorkContextRecordAccessRequest) (*gen.CheckWorkContextRecordAccessResponse, error) {
	ctx, releaseAuthority, fenceErr := service.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return nil, fenceErr
	}
	defer releaseAuthority()

	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	authority := WorkContextSingleton()
	if !authority.configured() {
		return nil, status.Error(codes.Unavailable, "Work Context authority is not configured")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get(workContextHeader)
	if len(values) != 1 {
		return nil, status.Error(codes.Unauthenticated, "one viewer Work Context is required")
	}
	verified, err := authority.readBack(ctx, values[0])
	if err != nil {
		return nil, refusedReadBack(ctx, err, status.Error(codes.Unauthenticated, "invalid viewer Work Context"))
	}
	claims := verified.Context()
	declared, err := service.ModuleContentResources(claims.GetAudience())
	if err != nil || !slices.Contains(declared, req.ResourceType) {
		return nil, status.Error(codes.PermissionDenied, "declared resource scope required")
	}
	if claims.GetTenantId() == "" || claims.GetOwnerPrincipalId() == "" {
		return nil, status.Error(codes.PermissionDenied, "tenant viewer required")
	}
	if !workContextGrants(claims, req.ResourceType, req.Action, req.ResourceId) {
		return nil, status.Error(codes.PermissionDenied, "record scope required")
	}
	if len(claims.GetActorChain()) > 0 && authority.journal == nil && !business.NamesInstalledContentRead(claims.GetOperationBinding().GetBindingId()) {
		return nil, status.Error(codes.Unavailable, "actor chain authority is not configured")
	}
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, claims.GetOwnerPrincipalId(), claims.GetTenantId())
	installedRead := business.NamesInstalledContentRead(claims.GetOperationBinding().GetBindingId())
	if installedRead {
		if err := authority.requireInstalledBinding(ctx, claims); err != nil {
			return nil, err
		}
	}
	subjects := []string{claims.GetOwnerPrincipalId()}
	for _, actor := range claims.GetActorChain() {
		if installedRead {
			continue
		}
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
	md.Set(workContextHeader, req.Header().Values(workContextHeader)...)
	out, err := h.inner.CheckWorkContextRecordAccess(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}
