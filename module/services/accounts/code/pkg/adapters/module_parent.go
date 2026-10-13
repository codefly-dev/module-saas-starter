package adapters

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	"context"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type moduleParentAuthentication struct {
	ctx    context.Context
	caller business.ModuleCaller
	parent *basev0.WorkContextV1
	actor  *business.Principal
}

func authenticateModuleParent(ctx context.Context, encoded string) (moduleParentAuthentication, error) {
	if err := requireInternalCredential(ctx); err != nil {
		return moduleParentAuthentication{}, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get(workContextHeader)) != 1 {
		return moduleParentAuthentication{}, status.Error(codes.Unauthenticated, "one module Work Context required")
	}
	authority := WorkContextSingleton()
	if service == nil || !authority.configured() || authority.authority == nil {
		return moduleParentAuthentication{}, status.Error(codes.Unavailable, "module parent authority unavailable")
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return moduleParentAuthentication{}, err
	}
	if _, err := service.AuthorizeModuleCapability(ctx, caller); err != nil {
		return moduleParentAuthentication{}, err
	}
	presented, err := authority.readBack(ctx, encoded)
	if err != nil {
		return moduleParentAuthentication{}, refusedReadBack(ctx, err, status.Error(codes.PermissionDenied, "invalid module parent"))
	}
	if presented.Context().GetTenantId() == "" || presented.Context().GetOwnerPrincipalId() == "" {
		return moduleParentAuthentication{}, status.Error(codes.PermissionDenied, "invalid module parent")
	}
	verified := presented.Context()
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, verified.OwnerPrincipalId, verified.TenantId)
	if len(verified.ActorChain) > 0 && authority.journal == nil {
		return moduleParentAuthentication{}, status.Error(codes.Unavailable, "delegation journal unavailable")
	}
	actor, err := authority.requireCurrentAuthority(ctx, verified.TenantId, verified)
	if err != nil {
		return moduleParentAuthentication{}, err
	}
	parent := verified
	return moduleParentAuthentication{ctx, caller, parent, actor}, nil
}
