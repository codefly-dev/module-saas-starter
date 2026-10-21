package adapters

import (
	"context"
	"errors"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func installedContentReadError(err error) error {
	if errors.Is(err, business.ErrNoOperationBinding) || errors.Is(err, business.ErrModuleHopStale) || status.Code(err) == codes.PermissionDenied {
		return status.Error(codes.PermissionDenied, "installed content read authority no longer stands")
	}
	return status.Error(codes.Unavailable, "installed content read authority unavailable")
}

func (s *WorkContextAuthorityServer) exchangeInstalledContentRead(ctx context.Context, parent *workcontext.Verified, caller business.ModuleCaller, request delegatedAudienceRequest, selected delegatedAudienceBinding, ttl time.Duration) (*gen.IssuedWorkContext, error) {
	claims := parent.Context()
	identity, binding, err := service.InstalledContentReadBinding(ctx, caller, request.bindingID)
	if err != nil {
		return nil, installedContentReadError(err)
	}
	if binding.Audience != selected.audience || claims.GetSeal().GetInstallationId() != identity.InstallationID {
		return nil, status.Error(codes.PermissionDenied, "installed content parent mismatch")
	}
	_, scopes, err := workContextScopes(selected.scopes, recheckContentReads())
	if err != nil {
		return nil, err
	}
	scopes, err = attenuateEffective(claims, scopes)
	if err != nil {
		return nil, err
	}
	token, signed, err := s.coreAuthority(claims.TenantId, claims.OwnerPrincipalId).Child(ctx, parent, workcontext.ChildInput{DelegationID: uuid.NewString(), PrincipalID: caller.PrincipalID, PrincipalKind: business.PrincipalKindService, OrganizationID: claims.TenantId, Audience: selected.audience, GrantedScopes: scopes, OperationBindingID: identity.ID, ReplayPolicy: workcontext.ReplayIdempotent, TTL: ttl})
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	if err := s.requireInstalledContentRead(ctx, signed); err != nil {
		return nil, err
	}
	return issuedWorkContext(token, signed), nil
}

// The installed module is authenticated by Core and the exact current binding;
// only the person supplies node visibility. This branch accepts no other chain.
func (s *WorkContextAuthorityServer) requireInstalledContentRead(ctx context.Context, claims *basev0.WorkContextV1) error {
	if claims.GetOwnerPrincipalKind() != business.PrincipalKindHuman || len(claims.ActorChain) != 1 {
		return status.Error(codes.PermissionDenied, "installed content child shape refused")
	}
	actor := claims.ActorChain[0]
	identity, err := service.WorkContextOperationBinding(ctx, claims.TenantId, claims.GetOperationBinding().GetBindingId())
	if err != nil {
		return installedContentReadError(err)
	}
	if identity.PrincipalID != actor.PrincipalId || identity.InstallationID != claims.GetSeal().GetInstallationId() {
		return status.Error(codes.PermissionDenied, "installed content child identity refused")
	}
	if err := service.ConfirmModuleHopBinding(ctx, claims.TenantId, actor.PrincipalId, identity.ID, operationScopesFromCore(actor.GrantedScopes)); err != nil {
		return installedContentReadError(err)
	}
	facts, err := s.resolveAuthority(ctx, claims.TenantId, claims.OwnerPrincipalId, "", permissionsFromCoreScopes(actor.GrantedScopes, recheckContentReads()))
	if err != nil {
		return err
	}
	if facts.EffectiveRevision() != claims.AuthorizationRevision {
		return status.Error(codes.FailedPrecondition, "parent Work Context authorization revision is stale")
	}
	return nil
}
