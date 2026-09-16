package adapters

import (
	"context"
	"reflect"
	"time"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	codefly "github.com/codefly-dev/sdk-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (s *ModuleCapabilitiesServer) ExchangeDelegatedReadAudience(ctx context.Context, req *gen.ModuleExchangeDelegatedReadAudienceRequest) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get(codefly.WorkContextHeaderName)) != 1 {
		return nil, status.Error(codes.Unauthenticated, "one module Work Context required")
	}
	authority := WorkContextSingleton()
	if authority == nil || authority.verifier == nil || authority.configureErr != nil || authority.authority == nil {
		return nil, status.Error(codes.Unavailable, "work context authority unavailable")
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	token, err := codefly.ParseWorkContextToken(req.ParentWorkContextToken)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "invalid delegated parent")
	}
	verified, err := authority.verifier.Verify(token, codefly.WorkContextExpectations{Issuer: authority.issuer})
	if err != nil || verified.TenantId == "" || verified.OwnerPrincipalId == "" {
		return nil, status.Error(codes.PermissionDenied, "invalid delegated parent")
	}
	binding, err := service.ModuleReadAudience(caller, verified.TenantId, verified.Audience, req.BindingId)
	if err != nil {
		return nil, err
	}
	if len(verified.ActorChain) > 0 && authority.journal == nil {
		return nil, status.Error(codes.Unavailable, "delegation journal unavailable")
	}
	// Identity is sealed in the verified parent and bounded by the authenticated
	// module installation. No user-supplied owner/actor enters database context.
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, verified.OwnerPrincipalId, verified.TenantId)
	parentToken, parent, actor, err := authority.verifyParent(ctx, verified.TenantId, verified.OwnerPrincipalId, req.ParentWorkContextToken)
	if err != nil {
		return nil, err
	}
	ttl := min(int64(60), parent.ExpiresAtUnix-time.Now().Unix()-2)
	if ttl <= 0 {
		return nil, status.Error(codes.PermissionDenied, "parent expires before exchange")
	}
	issued, err := authority.exchangeVerifiedParent(parentToken, parent, actor, &gen.ExchangeWorkContextAudienceRequest{OrgId: parent.TenantId, Audience: binding.Audience, AttenuatedScopes: binding.WireScopes(), ReplayPolicy: gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_IDEMPOTENT, TtlSeconds: int32(ttl)})
	if err != nil {
		return nil, err
	}
	// Current authority is checked again after signing; no stale token is released
	// if a revision or any delegation hop changed during the exchange.
	if _, err = authority.requireCurrentAuthority(ctx, parent.TenantId, parent); err != nil {
		return nil, err
	}
	current, err := service.ModuleReadAudience(caller, parent.TenantId, parent.Audience, req.BindingId)
	if err != nil || !reflect.DeepEqual(current, binding) {
		return nil, status.Error(codes.PermissionDenied, "installed read binding changed")
	}
	return issued, nil
}
func (h *moduleCapabilitiesConnectHandler) ExchangeDelegatedReadAudience(ctx context.Context, req *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) (*connect.Response[gen.IssuedWorkContext], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(codefly.WorkContextHeaderName, req.Header().Values(codefly.WorkContextHeaderName)...)
	out, err := h.inner.ExchangeDelegatedReadAudience(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}
