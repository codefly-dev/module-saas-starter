package adapters

import (
	"context"
	"slices"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func installedUseRefusal() error {
	return status.Error(codes.PermissionDenied, "installed delegation no longer authorizes this work")
}

// Keep an unanswered authority lookup distinct from a permission decision.
func installedUseError(err error) error {
	if decidedWorkContextAuthority(err) {
		return installedUseRefusal()
	}
	return status.Error(codes.Unavailable, "installed delegation authority is unavailable")
}

func (s *WorkContextAuthorityServer) requireInstalledUseAuthority(ctx context.Context, org string, claims *basev0.WorkContextV1) error {
	if service == nil {
		return installedUseError(nil)
	}
	if claims.GetOwnerPrincipalKind() != business.PrincipalKindHuman || claims.GetTenantId() != org {
		return installedUseRefusal()
	}
	binding := claims.GetOperationBinding().GetBindingId()
	live, err := service.WorkContextOperationBinding(ctx, org, binding)
	if err != nil {
		return installedUseError(err)
	}
	scopes := claims.AuthorityScopes
	switch len(claims.ActorChain) {
	case 0:
		if live.PrincipalID != claims.OwnerPrincipalId {
			return installedUseRefusal()
		}
	case 1:
		actor := claims.ActorChain[0]
		if actor.PrincipalKind != business.PrincipalKindService {
			return installedUseRefusal()
		}
		scopes = actor.GrantedScopes
		if err := service.CheckInstalledUseActor(ctx, org, claims.OwnerPrincipalId, binding, actor.PrincipalId, claims.Audience, operationScopesFromCore(scopes)); err != nil {
			return installedUseError(err)
		}
	default:
		return installedUseRefusal()
	}
	facts, err := s.resolveAuthority(ctx, org, claims.OwnerPrincipalId, "", permissionsFromCoreScopes(scopes, recheckContentReads()))
	if err != nil {
		return installedUseError(err)
	}
	if facts.EffectiveRevision() != claims.AuthorizationRevision {
		return installedUseRefusal()
	}
	return nil
}

// readSubjects verifies the installed actor's route before selecting the
// subjects whose personal grants the collection/record oracle intersects.
// Ordinary chains retain every actor's personal grant requirement.
func (s *WorkContextAuthorityServer) readSubjects(ctx context.Context, claims *basev0.WorkContextV1) ([]string, error) {
	subjects := []string{claims.OwnerPrincipalId}
	if business.IsInstalledUseBinding(claims.GetOperationBinding().GetBindingId()) {
		if err := s.requireInstalledUseAuthority(ctx, claims.TenantId, claims); err != nil {
			return nil, err
		}
		return subjects, nil
	}
	for _, actor := range claims.ActorChain {
		if !slices.Contains(subjects, actor.PrincipalId) {
			subjects = append(subjects, actor.PrincipalId)
		}
	}
	return subjects, nil
}

func (s *WorkContextAuthorityServer) exchangeInstalledUse(ctx context.Context, parent *workcontext.Verified, caller business.ModuleCaller, request delegatedAudienceRequest, ttl time.Duration) (*gen.IssuedWorkContext, error) {
	claims := parent.Context()
	if request.kind != "operation" || len(claims.ActorChain) != 0 {
		return nil, installedUseRefusal()
	}
	id, audience, granted, err := service.InstalledUseChild(ctx, claims.TenantId, claims.OwnerPrincipalId, claims.GetOperationBinding().GetBindingId(), caller, request.bindingID, request.lookup, operationScopesFromCore(parent.EffectiveScopes()))
	if err != nil {
		return nil, installedUseError(err)
	}
	scopes := make([]*basev0.WorkScopeV1, 0, len(granted))
	for _, scope := range granted {
		scopes = append(scopes, &basev0.WorkScopeV1{ResourceKind: scope.ResourceKind, Actions: scope.Actions, ResourceIds: scope.ResourceIDs})
	}
	token, signed, err := s.coreAuthority(claims.TenantId, claims.OwnerPrincipalId).Child(ctx, parent, workcontext.ChildInput{
		PrincipalID: caller.PrincipalID, PrincipalKind: business.PrincipalKindService,
		OrganizationID: claims.TenantId, DelegationID: uuid.NewString(), GrantedScopes: scopes,
		Audience: audience, OperationBindingID: id, ReplayPolicy: workcontext.ReplayIdempotent, TTL: ttl,
	})
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	if err := s.requireInstalledUseAuthority(ctx, claims.TenantId, signed); err != nil {
		return nil, err
	}
	return issuedWorkContext(token, signed), nil
}
