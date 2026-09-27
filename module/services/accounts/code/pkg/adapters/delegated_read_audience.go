package adapters

import (
	"context"
	"errors"
	"reflect"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type delegatedAudienceBinding struct {
	audience string
	scopes   []*gen.WorkContextScope
	policy   any
}

// delegatedAudienceResolver resolves the caller's binding for a parent in
// tenant. delegation is the source delegation that authorizes that tenant when
// the parent carries one (ConfirmSourceDelegationParent), and nil otherwise.
type delegatedAudienceResolver func(caller business.ModuleCaller, tenant, parentAudience string, delegation *business.SourceDelegation) (delegatedAudienceBinding, error)

type delegatedAudienceRequest struct {
	bindingID string
	kind      string
	lookup    bool
}

func (s *ModuleCapabilitiesServer) exchangeDelegatedAudience(ctx context.Context, encodedParent string, request delegatedAudienceRequest, resolve delegatedAudienceResolver) (*gen.IssuedWorkContext, error) {
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get(workcontext.WorkContextHeaderName)) != 1 {
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
	token, err := workcontext.ParseWorkContextToken(encodedParent)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "invalid delegated parent")
	}
	verified, err := authority.verifier.Verify(token, workcontext.WorkContextExpectations{Issuer: authority.issuer})
	if err != nil || verified.TenantId == "" || verified.OwnerPrincipalId == "" {
		return nil, status.Error(codes.PermissionDenied, "invalid delegated parent")
	}
	// Identity is sealed in the verified parent and bounded by the authenticated
	// module installation. No user-supplied owner/actor enters database context.
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, verified.OwnerPrincipalId, verified.TenantId)
	parent := verified
	var actor *business.Principal
	audit := func(outcome, audience string, exchangeErr error) {
		service.ObserveDelegatedAudienceExchange(ctx, business.DelegatedAudienceExchangeObservation{
			Caller:      caller,
			OwnerID:     parent.OwnerPrincipalId,
			Actor:       actor,
			ActorID:     delegatedAudienceActorID(parent),
			Tenant:      parent.TenantId,
			BindingKind: request.kind,
			BindingID:   request.bindingID,
			Audience:    audience,
			Lookup:      request.lookup,
			Outcome:     outcome,
			RefusalCode: delegatedAudienceRefusalCode(exchangeErr),
		})
	}
	// A parent carrying a source delegation (MintSourceOperationContext, or an
	// exchange of it) is owned by a person and actored by a declared module
	// principal, which the row-backed agent path cannot resolve. It is re-checked
	// against the delegation instead, and that delegation — not the caller's
	// declared tenant or cross_tenant grant — admits the parent's tenant. Every
	// other parent takes verifyParent and the tenant check exactly as before.
	confirmDelegation := func() (bool, *business.SourceDelegation, error) {
		if service == nil {
			return false, nil, nil
		}
		return service.ConfirmSourceDelegationParent(ctx, verified.TenantId, verified.OwnerPrincipalId,
			verified.AuthorizationRevision, operationScopesFromCore(verified.AuthorityScopes), sourceDelegationHops(verified.ActorChain))
	}
	delegated, delegation, err := confirmDelegation()
	if err != nil {
		err = sourceDelegationParentError(err)
		audit(business.DelegatedAudienceExchangeRefused, "", err)
		return nil, err
	}
	var parentToken workcontext.WorkContextToken
	if delegated {
		parentToken = token
	} else {
		if len(verified.ActorChain) > 0 && authority.journal == nil {
			err = status.Error(codes.Unavailable, "delegation journal unavailable")
			audit(business.DelegatedAudienceExchangeRefused, "", err)
			return nil, err
		}
		var currentParent *basev0.WorkContextV1
		parentToken, currentParent, actor, err = authority.verifyParent(ctx, verified.TenantId, verified.OwnerPrincipalId, encodedParent)
		if err != nil {
			audit(business.DelegatedAudienceExchangeRefused, "", err)
			return nil, err
		}
		parent = currentParent
	}
	binding, err := resolve(caller, parent.TenantId, parent.Audience, delegation)
	if err != nil {
		audit(business.DelegatedAudienceExchangeRefused, "", err)
		return nil, err
	}
	ttl := min(int64(60), parent.ExpiresAtUnix-time.Now().Unix()-2)
	if ttl <= 0 {
		err = status.Error(codes.PermissionDenied, "parent expires before exchange")
		audit(business.DelegatedAudienceExchangeRefused, binding.audience, err)
		return nil, err
	}
	issued, err := authority.exchangeVerifiedParent(parentToken, parent, actor, &gen.ExchangeWorkContextAudienceRequest{OrgId: parent.TenantId, Audience: binding.audience, AttenuatedScopes: binding.scopes, ReplayPolicy: gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_IDEMPOTENT, TtlSeconds: int32(ttl)})
	if err != nil {
		audit(business.DelegatedAudienceExchangeRefused, binding.audience, err)
		return nil, err
	}
	// Current authority is checked again after signing; no stale token is released
	// if a revision, any delegation hop, or the source delegation changed during
	// the exchange.
	if delegated {
		stillDelegated, again, err := confirmDelegation()
		if err == nil && (!stillDelegated || again == nil || again.ID != delegation.ID) {
			err = business.ErrSourceDelegationContextStale
		}
		if err != nil {
			err = sourceDelegationParentError(err)
			audit(business.DelegatedAudienceExchangeRefused, binding.audience, err)
			return nil, err
		}
		delegation = again
	} else if _, err = authority.requireCurrentAuthority(ctx, parent.TenantId, parent); err != nil {
		audit(business.DelegatedAudienceExchangeRefused, binding.audience, err)
		return nil, err
	}
	current, err := resolve(caller, parent.TenantId, parent.Audience, delegation)
	if err != nil {
		audit(business.DelegatedAudienceExchangeRefused, binding.audience, err)
		return nil, err
	}
	if !reflect.DeepEqual(current.policy, binding.policy) {
		err = status.Error(codes.PermissionDenied, "installed audience binding changed")
		audit(business.DelegatedAudienceExchangeRefused, binding.audience, err)
		return nil, err
	}
	audit(business.DelegatedAudienceExchangeIssued, binding.audience, nil)
	return issued, nil
}

func delegatedAudienceActorID(parent *basev0.WorkContextV1) string {
	actors := parent.GetActorChain()
	if len(actors) == 0 {
		return parent.GetOwnerPrincipalId()
	}
	return actors[len(actors)-1].GetPrincipalId()
}

func delegatedAudienceRefusalCode(err error) string {
	if err == nil {
		return ""
	}
	switch code := status.Code(err); code {
	case codes.InvalidArgument, codes.Unauthenticated, codes.PermissionDenied, codes.FailedPrecondition, codes.Unavailable:
		return code.String()
	default:
		return codes.Internal.String()
	}
}

func (s *ModuleCapabilitiesServer) ExchangeDelegatedReadAudience(ctx context.Context, req *gen.ModuleExchangeDelegatedReadAudienceRequest) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	// A source delegation authorizes operation bindings only; a read exchange
	// keeps the caller's own tenant check whatever the parent carries.
	return s.exchangeDelegatedAudience(ctx, req.ParentWorkContextToken, delegatedAudienceRequest{bindingID: req.BindingId, kind: "read"}, func(caller business.ModuleCaller, tenant, parentAudience string, _ *business.SourceDelegation) (delegatedAudienceBinding, error) {
		binding, err := service.ModuleReadAudience(caller, tenant, parentAudience, req.BindingId)
		return delegatedAudienceBinding{audience: binding.Audience, scopes: binding.WireScopes(), policy: binding}, err
	})
}

func (s *ModuleCapabilitiesServer) ExchangeDelegatedOperationAudience(ctx context.Context, req *gen.ModuleExchangeDelegatedOperationAudienceRequest) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return s.exchangeDelegatedAudience(ctx, req.ParentWorkContextToken, delegatedAudienceRequest{bindingID: req.BindingId, kind: "operation", lookup: req.Lookup}, func(caller business.ModuleCaller, tenant, parentAudience string, delegation *business.SourceDelegation) (delegatedAudienceBinding, error) {
		var binding business.ModuleOperationAudience
		var err error
		if delegation != nil {
			binding, err = service.ModuleOperationAudienceForDelegation(caller, delegation, parentAudience, req.BindingId)
		} else {
			binding, err = service.ModuleOperationAudience(caller, tenant, parentAudience, req.BindingId)
		}
		return delegatedAudienceBinding{audience: binding.Audience, scopes: binding.WireScopes(req.Lookup), policy: binding}, err
	})
}
func (h *moduleCapabilitiesConnectHandler) ExchangeDelegatedReadAudience(ctx context.Context, req *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) (*connect.Response[gen.IssuedWorkContext], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(workcontext.WorkContextHeaderName, req.Header().Values(workcontext.WorkContextHeaderName)...)
	out, err := h.inner.ExchangeDelegatedReadAudience(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}

func (h *moduleCapabilitiesConnectHandler) ExchangeDelegatedOperationAudience(ctx context.Context, req *connect.Request[gen.ModuleExchangeDelegatedOperationAudienceRequest]) (*connect.Response[gen.IssuedWorkContext], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(workcontext.WorkContextHeaderName, req.Header().Values(workcontext.WorkContextHeaderName)...)
	out, err := h.inner.ExchangeDelegatedOperationAudience(metadata.NewIncomingContext(ctx, md), req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(out), nil
}

// sourceDelegationParentError maps a failed re-check of a delegation-bearing
// parent: a delegation that no longer confirms it is a denial, never an outage.
func sourceDelegationParentError(err error) error {
	if errors.Is(err, business.ErrSourceDelegationContextStale) {
		return status.Error(codes.PermissionDenied, "source delegation no longer authorizes this parent")
	}
	return status.Error(codes.Unavailable, "source delegation authority is unavailable")
}

func operationScopesFromCore(scopes []*basev0.WorkScopeV1) []business.ModuleOperationScope {
	out := make([]business.ModuleOperationScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, business.ModuleOperationScope{
			ResourceKind: scope.GetResourceKind(),
			Actions:      append([]string(nil), scope.GetActions()...),
			ResourceIDs:  append([]string(nil), scope.GetResourceIds()...),
		})
	}
	return out
}

func sourceDelegationHops(actors []*basev0.WorkActorV1) []business.SourceDelegationHop {
	out := make([]business.SourceDelegationHop, 0, len(actors))
	for _, actor := range actors {
		out = append(out, business.SourceDelegationHop{
			PrincipalID:  actor.GetPrincipalId(),
			DelegationID: actor.GetDelegationId(),
			Scopes:       operationScopesFromCore(actor.GetGrantedScopes()),
		})
	}
	return out
}
