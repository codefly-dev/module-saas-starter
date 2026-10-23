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

func installedBindingError(err error) error {
	if errors.Is(err, business.ErrNoOperationBinding) || errors.Is(err, business.ErrModuleHopStale) || status.Code(err) == codes.PermissionDenied {
		return status.Error(codes.PermissionDenied, "installed binding authority no longer stands")
	}
	return status.Error(codes.Unavailable, "installed binding authority unavailable")
}

// exchangeInstalledBinding issues the person's child of a binding installed
// under mode: Core derives owner, tenant, lineage and expiry from the presented
// parent, and the one hop it adds is the authenticated caller, never a
// principal the request names. The hop is journaled like any other
// on-behalf-of hop, in either mode, so the record of who acted outlives the
// token.
func (s *WorkContextAuthorityServer) exchangeInstalledBinding(ctx context.Context, parent *workcontext.Verified, caller business.ModuleCaller, request delegatedAudienceRequest, selected delegatedAudienceBinding, mode string, ttl time.Duration) (*gen.IssuedWorkContext, error) {
	claims := parent.Context()
	identity, binding, err := service.InstalledBinding(ctx, caller, request.bindingID, mode)
	if err != nil {
		return nil, installedBindingError(err)
	}
	if binding.Audience != selected.audience || claims.GetSeal().GetInstallationId() != identity.InstallationID {
		return nil, status.Error(codes.PermissionDenied, "installed binding parent mismatch")
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
		return nil, mapWorkContextError(ctx, err)
	}
	if err := s.requireInstalledBinding(ctx, signed); err != nil {
		return nil, err
	}
	if err := s.journalActorHop(ctx, signed); err != nil {
		return nil, err
	}
	return issuedWorkContext(token, signed), nil
}

// The installed module is authenticated by Core and the exact current binding;
// only the person supplies visibility. This branch accepts no other chain.
func (s *WorkContextAuthorityServer) requireInstalledBinding(ctx context.Context, claims *basev0.WorkContextV1) error {
	if claims.GetOwnerPrincipalKind() != business.PrincipalKindHuman || len(claims.ActorChain) != 1 {
		return status.Error(codes.PermissionDenied, "installed binding child shape refused")
	}
	actor := claims.ActorChain[0]
	identity, err := service.WorkContextOperationBinding(ctx, claims.TenantId, claims.GetOperationBinding().GetBindingId())
	if err != nil {
		return installedBindingError(err)
	}
	if identity.PrincipalID != actor.PrincipalId || identity.InstallationID != claims.GetSeal().GetInstallationId() {
		return status.Error(codes.PermissionDenied, "installed binding child identity refused")
	}
	if err := service.ConfirmModuleHopBinding(ctx, claims.TenantId, actor.PrincipalId, identity.ID, operationScopesFromCore(actor.GrantedScopes)); err != nil {
		return installedBindingError(err)
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
