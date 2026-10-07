package adapters

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	"context"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
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
	if len(md.Get(workcontext.WorkContextHeaderName)) != 1 {
		return moduleParentAuthentication{}, status.Error(codes.Unauthenticated, "one module Work Context required")
	}
	authority := WorkContextSingleton()
	if service == nil || authority == nil || authority.verifier == nil || authority.configureErr != nil || authority.authority == nil {
		return moduleParentAuthentication{}, status.Error(codes.Unavailable, "module parent authority unavailable")
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return moduleParentAuthentication{}, err
	}
	token, err := workcontext.ParseWorkContextToken(encoded)
	if err != nil {
		return moduleParentAuthentication{}, status.Error(codes.PermissionDenied, "invalid module parent")
	}
	verified, err := authority.verifier.Verify(token, workcontext.WorkContextExpectations{Issuer: authority.issuer})
	if err != nil || verified.TenantId == "" || verified.OwnerPrincipalId == "" {
		return moduleParentAuthentication{}, status.Error(codes.PermissionDenied, "invalid module parent")
	}
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, verified.OwnerPrincipalId, verified.TenantId)
	if len(verified.ActorChain) > 0 && authority.journal == nil {
		return moduleParentAuthentication{}, status.Error(codes.Unavailable, "delegation journal unavailable")
	}
	_, parent, actor, err := authority.verifyParent(ctx, verified.TenantId, verified.OwnerPrincipalId, encoded)
	if err != nil {
		return moduleParentAuthentication{}, err
	}
	return moduleParentAuthentication{ctx, caller, parent, actor}, nil
}
