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
	"github.com/codefly-dev/core/workcontext"
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
	ctx, releaseAuthority, fenceErr := service.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return nil, fenceErr
	}
	defer releaseAuthority()

	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get(workContextHeader)) != 1 {
		return nil, status.Error(codes.Unauthenticated, "one module Work Context required")
	}
	authority := WorkContextSingleton()
	if !authority.configured() || authority.authority == nil {
		return nil, status.Error(codes.Unavailable, "work context authority unavailable")
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	// A parent that does not parse or verify names nobody, so until this point
	// there is no owner to record. That used to mean these refusals — the ones
	// a probe produces — were not recorded at all, because the record required
	// an owner. It no longer does, so they are.
	refuseUnidentifiedParent := func() error {
		err := status.Error(codes.PermissionDenied, "invalid delegated parent")
		if service != nil {
			service.ObserveDelegatedAudienceExchange(ctx, business.DelegatedAudienceExchangeObservation{
				Caller:      caller,
				ActorID:     caller.PrincipalID,
				BindingKind: request.kind,
				BindingID:   request.bindingID,
				Lookup:      request.lookup,
				Outcome:     business.DelegatedAudienceExchangeRefused,
				RefusalCode: delegatedAudienceRefusalCode(err),
			})
		}
		return err
	}
	presented, err := authority.verifyIssued(ctx, encodedParent)
	var verified *basev0.WorkContextV1
	switch {
	case err == nil:
		verified = presented.Context()
	case errors.Is(err, workcontext.ErrRevoked), errors.Is(err, workcontext.ErrNoSeal):
		// Refused for live state that moved since the mint. Core reaches those
		// checks only after the signature, issuer, window and audience held, so
		// the claims are the issuer's own and the refusal is attributed to the
		// owner they name — it is not a probe.
		decoded, decodeErr := workcontext.Decode(encodedParent)
		if decodeErr != nil {
			return nil, refuseUnidentifiedParent()
		}
		stale := decoded.Context()
		staleErr := refusedReadBack(ctx, err, nil)
		if service != nil {
			service.ObserveDelegatedAudienceExchange(auth.WithVerifiedDatabaseIdentity(ctx, stale.OwnerPrincipalId, stale.TenantId),
				business.DelegatedAudienceExchangeObservation{
					Caller:      caller,
					OwnerID:     stale.OwnerPrincipalId,
					ActorID:     delegatedAudienceActorID(stale),
					Tenant:      stale.TenantId,
					BindingKind: request.kind,
					BindingID:   request.bindingID,
					Lookup:      request.lookup,
					Outcome:     business.DelegatedAudienceExchangeRefused,
					RefusalCode: delegatedAudienceRefusalCode(staleErr),
				})
		}
		return nil, staleErr
	default:
		return nil, refuseUnidentifiedParent()
	}
	if verified.GetTenantId() == "" || verified.GetOwnerPrincipalId() == "" {
		return nil, refuseUnidentifiedParent()
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
			verified.AuthorizationRevision, operationScopesFromCore(verified.AuthorityScopes), sourceDelegationHops(verified.ActorChain),
			verified.GetOperationBinding().GetBindingId())
	}
	delegated, delegation, err := confirmDelegation()
	if err != nil {
		err = sourceDelegationParentError(err)
		audit(business.DelegatedAudienceExchangeRefused, "", err)
		return nil, err
	}
	if !delegated {
		if len(verified.ActorChain) > 0 && authority.journal == nil {
			err = status.Error(codes.Unavailable, "delegation journal unavailable")
			audit(business.DelegatedAudienceExchangeRefused, "", err)
			return nil, err
		}
		actor, err = authority.requireCurrentAuthority(ctx, verified.TenantId, verified)
		if err != nil {
			audit(business.DelegatedAudienceExchangeRefused, "", err)
			return nil, err
		}
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
	// Derived from the capability the module presented, in a fresh session under
	// the parent's: the module exchanging it holds no session of its own to
	// bind, and the parent link is what lets whoever admitted that session
	// recognise this capability as its descendant.
	var issued *gen.IssuedWorkContext
	if delegated {
		issued, err = authority.exchangeDelegatedParent(ctx, caller, presented, delegation, binding, time.Duration(ttl)*time.Second)
	} else {
		issued, err = authority.exchangeVerifiedParent(ctx, presented, actor, &gen.ExchangeWorkContextAudienceRequest{OrgId: parent.TenantId, Audience: binding.audience, AttenuatedScopes: binding.scopes, ReplayPolicy: gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_IDEMPOTENT, TtlSeconds: int32(ttl)})
	}
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

// exchangeDelegationReference is the arm that carries long-running delegated
// work: the caller presents a reference to a host-owned, revocable grant and no
// capability at all.
//
// It is deliberately a mint and not an exchange. There is no parent to
// attenuate against, so the child's ceiling comes from the delegation and the
// binding the person made it to, re-read on every call — which is also why this
// arm is not bounded by any token's remaining life and can be called again
// after the previous child has already expired. That is the whole of the
// renewal path for work in submit mode: a fresh short capability, never a
// renewed one, with no moment at which the worker must still hold a valid token
// in order to obtain the next.
//
// The caller's own module Work Context is still required, and still
// authenticates the caller rather than the parent — a reference is an
// identifier, not a credential, and on its own it authorizes nothing.
func (s *ModuleCapabilitiesServer) exchangeDelegationReference(ctx context.Context, delegationID string, request delegatedAudienceRequest) (*gen.IssuedWorkContext, error) {
	ctx, releaseAuthority, fenceErr := service.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return nil, fenceErr
	}
	defer releaseAuthority()

	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get(workContextHeader)) != 1 {
		return nil, status.Error(codes.Unauthenticated, "one module Work Context required")
	}
	authority := WorkContextSingleton()
	if !authority.configured() {
		return nil, status.Error(codes.Unavailable, "work context authority unavailable")
	}
	caller, err := moduleCaller(ctx)
	if err != nil {
		return nil, err
	}
	if service == nil {
		return nil, status.Error(codes.Unavailable, "source delegation authority is unavailable")
	}
	// The owner is unknown until the delegation resolves, and stays unknown when
	// it does not. The reference is what identifies the attempt in that case,
	// which is why a refusal here is still a complete record.
	granted := business.SourceOperationContextAuthority{}
	audit := func(outcome string, exchangeErr error) {
		service.ObserveDelegatedAudienceExchange(ctx, business.DelegatedAudienceExchangeObservation{
			Caller:       caller,
			OwnerID:      granted.OwnerPrincipalID,
			DelegationID: delegationID,
			ActorID:      caller.PrincipalID,
			Tenant:       granted.Tenant,
			BindingKind:  request.kind,
			BindingID:    request.bindingID,
			Audience:     granted.Audience,
			Lookup:       request.lookup,
			Outcome:      outcome,
			RefusalCode:  delegatedAudienceRefusalCode(exchangeErr),
		})
	}
	granted, err = service.AuthorizeDelegationReferenceExchange(ctx, caller, delegationID, request.bindingID, request.lookup)
	if err != nil {
		err = delegationReferenceError(err)
		audit(business.DelegatedAudienceExchangeRefused, err)
		return nil, err
	}
	token, signed, err := authority.StartSourceOperationTask(ctx, granted)
	if err != nil {
		if errors.Is(err, ErrWorkContextAuthorityUnconfigured) {
			err = status.Error(codes.Unavailable, "work context authority unavailable")
		} else {
			err = mapWorkContextError(err)
		}
		audit(business.DelegatedAudienceExchangeRefused, err)
		return nil, err
	}
	// The delegation is re-read once more in the transaction that records the
	// mint, so a revocation that raced the signing withholds the capability
	// instead of being outrun by it. Nothing signed above is returned unless
	// this commits.
	if err := service.RecordSourceOperationContextMint(ctx, granted); err != nil {
		err = delegationReferenceError(err)
		audit(business.DelegatedAudienceExchangeRefused, err)
		return nil, err
	}
	audit(business.DelegatedAudienceExchangeIssued, nil)
	return issuedWorkContext(token, signed), nil
}

// delegationReferenceError maps a refused reference. Every way a delegation can
// fail to authorize is PermissionDenied and says the same thing, so a caller
// cannot tell an id that names nothing from one that names another module's
// delegation from one that was revoked a second ago. Only a genuine outage is
// Unavailable, because only an outage is worth retrying.
func delegationReferenceError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, business.ErrSourceDelegationInvalid),
		errors.Is(err, business.ErrSourceDelegationRevoked),
		errors.Is(err, business.ErrSourceDelegationMissing),
		errors.Is(err, business.ErrSourceDelegationContextStale):
		return status.Error(codes.PermissionDenied, "source delegation no longer authorizes this work")
	case errors.Is(err, business.ErrSourceDelegationLookupUnauthorized):
		return status.Error(codes.PermissionDenied, "source delegation does not authorize receipt lookup for this binding")
	case errors.Is(err, business.ErrModuleIdentityDenied):
		return status.Error(codes.Unauthenticated, "module source operation context denied")
	case status.Code(err) != codes.Unknown:
		return err
	default:
		return status.Error(codes.Unavailable, "source delegation authority is unavailable")
	}
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
	ctx, releaseAuthority, fenceErr := service.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return nil, fenceErr
	}
	defer releaseAuthority()

	if err := Validate(req); err != nil {
		return nil, err
	}
	// A source delegation authorizes operation bindings only; a read exchange
	// keeps the caller's own tenant check whatever the parent carries.
	return s.exchangeDelegatedAudience(ctx, req.GetParentWorkContextToken(), delegatedAudienceRequest{bindingID: req.GetBindingId(), kind: "read"}, func(caller business.ModuleCaller, tenant, parentAudience string, _ *business.SourceDelegation) (delegatedAudienceBinding, error) {
		binding, err := service.ModuleReadAudience(ctx, caller, tenant, parentAudience, req.BindingId)
		return delegatedAudienceBinding{audience: binding.Audience, scopes: binding.WireScopes(), policy: binding}, err
	})
}

func (s *ModuleCapabilitiesServer) ExchangeDelegatedOperationAudience(ctx context.Context, req *gen.ModuleExchangeDelegatedOperationAudienceRequest) (*gen.IssuedWorkContext, error) {
	ctx, releaseAuthority, fenceErr := service.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return nil, fenceErr
	}
	defer releaseAuthority()

	if err := Validate(req); err != nil {
		return nil, err
	}
	request := delegatedAudienceRequest{bindingID: req.GetBindingId(), kind: "operation", lookup: req.GetLookup()}
	// The two arms, whose exclusivity Validate has already enforced. A grant
	// reference is not a weaker spelling of a parent token: it presents no
	// capability, so nothing the caller holds bounds what it gets, and
	// everything that would have come from a parent is re-read from the
	// delegation instead.
	if delegationID := req.GetDelegationId(); delegationID != "" {
		return s.exchangeDelegationReference(ctx, delegationID, request)
	}
	return s.exchangeDelegatedAudience(ctx, req.GetParentWorkContextToken(), request, func(caller business.ModuleCaller, tenant, parentAudience string, delegation *business.SourceDelegation) (delegatedAudienceBinding, error) {
		var binding business.ModuleOperationAudience
		var err error
		if delegation != nil {
			binding, err = service.ModuleOperationAudienceForDelegation(ctx, caller, delegation, parentAudience, req.BindingId)
		} else {
			binding, err = service.ModuleOperationAudience(ctx, caller, tenant, parentAudience, req.BindingId)
		}
		// A binding that declares no lookup redeems none: refused by name rather
		// than signed as a capability carrying no scope at all.
		if err == nil && req.Lookup && len(binding.LookupScopes) == 0 {
			err = status.Errorf(codes.PermissionDenied, "operation audience binding %q declares no lookup", req.BindingId)
		}
		return delegatedAudienceBinding{audience: binding.Audience, scopes: binding.WireScopes(req.Lookup), policy: binding}, err
	})
}
func (h *moduleCapabilitiesConnectHandler) ExchangeDelegatedReadAudience(ctx context.Context, req *connect.Request[gen.ModuleExchangeDelegatedReadAudienceRequest]) (*connect.Response[gen.IssuedWorkContext], error) {
	ctx = connectCtx(ctx, req.Header())
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set(workContextHeader, req.Header().Values(workContextHeader)...)
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
	md.Set(workContextHeader, req.Header().Values(workContextHeader)...)
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

// exchangeDelegatedParent re-issues a parent minted from a source delegation
// for the caller's own binding: the person's session and the delegating
// module's hop exactly as the parent carries them, re-pointed at the audience
// the caller's installed binding names and narrowed to that binding's scopes,
// in a new session under the parent's.
//
// WHO THE CAPABILITY SAYS IS ACTING is the delegating module, not the caller,
// and that is the change. This used to mint afresh and put the CALLER in the
// chain with the caller's own binding and attested build, which made the result
// a capability nobody could tie back to the admitted session: a re-mint starts
// at the owner, so the result descended from an intermediate this host never
// released. A holder of the parent had no way to tell a re-issue of its own
// capability from a re-issue of another, and refusing every one was its only
// safe answer.
//
// The caller's entitlement is not weakened by leaving it out of the chain, it
// is enforced earlier and recorded: the binding resolved for this exchange is
// the CALLER's installed operation audience, resolved against the delegation in
// the caller's own organization, re-resolved after signing, and the exchange is
// audited against the caller. What the capability carries is the authority, and
// the authority is the person's, exercised through the module they delegated to.
//
// The binding's scopes must attenuate what the parent's final hop actually
// holds. A binding declaring more than the person delegated through this parent
// is refused rather than signed, which is the one behaviour a re-mint could not
// give: it attenuated against the OWNER's authority scopes, so a parent whose
// hop had been narrowed could be exchanged back up to the installed binding's
// full set.
func (s *WorkContextAuthorityServer) exchangeDelegatedParent(
	ctx context.Context, caller business.ModuleCaller, parent *workcontext.Verified,
	delegation *business.SourceDelegation, binding delegatedAudienceBinding, ttl time.Duration,
) (*gen.IssuedWorkContext, error) {
	grant, declared := service.ModulePrincipals()[caller.PrincipalID]
	if !declared || delegation == nil || caller.Sealed == nil || caller.Sealed.InstallationID == nil {
		return nil, status.Error(codes.PermissionDenied, "the caller is not a declared module holding an installation in this organization")
	}
	// THE CALLER MUST BE A MODULE THIS DELEGATION ENTITLES, and asking it here
	// is the whole reason this function is not three lines.
	//
	// Exactly two modules may use a delegation — the one the person granted to,
	// and the audience its binding names — and WorkContextOperationBinding is
	// where that is decided. The mint used to ask as a SIDE EFFECT: it named
	// SourceDelegationBindingID(delegation, caller) as the capability's
	// operation binding, and core resolved that id live, which refuses an id
	// naming any other module. A derivation carries the PARENT's binding
	// instead, so that read no longer happens on its own.
	//
	// Leaving it out made the exchange transitive. The audience module is
	// entitled to CONSUME what it is handed; without this it could also pass it
	// on, re-issuing the child for any audience it declares a binding for,
	// because every remaining gate then holds — the parent's audience is now
	// the third module's own prefix, the delegation still confirms against the
	// delegating module's hop, and that module's own binding attenuates the
	// parent. The person's authority would reach a module they never delegated
	// to, with the delegating module still named as the actor.
	if _, err := service.WorkContextOperationBinding(ctx, parent.Context().GetTenantId(),
		business.SourceDelegationBindingID(delegation.ID, grant.Prefix)); err != nil {
		return nil, sourceDelegationCallerError(err)
	}
	// The audience is policy, not a caller's word, but it is still a mint
	// input: requireMintableAudience refuses an empty one — good at whichever
	// consumer accepts it — and the capability surface's own audience, which a
	// peer reads as a module identity. The mint ran it; a derivation has to run
	// it too, because an installed binding may name either (nothing in
	// validateOperationAudiences rejects the reserved spelling).
	if err := requireMintableAudience(binding.audience); err != nil {
		return nil, status.Errorf(codes.PermissionDenied, "installed audience binding names an audience no capability may be minted for: %s", binding.audience)
	}
	_, scopes, err := workContextScopes(binding.scopes, mintContentReads(binding.audience))
	if err != nil {
		return nil, status.Errorf(codes.PermissionDenied, "installed audience binding declares unusable scopes: %v", err)
	}
	if len(scopes) == 0 {
		return nil, status.Error(codes.PermissionDenied, "installed audience binding declares no scope")
	}
	token, signed, err := s.reissue(ctx, parent, binding.audience, workcontext.ReplayIdempotent, ttl, scopes)
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	return issuedWorkContext(token, signed), nil
}

// sourceDelegationCallerError maps the live binding read that decides whether
// the caller is a module this delegation entitles. Every way the answer can be
// "no" is PermissionDenied and says the same thing, so a module learns nothing
// from the refusal beyond the fact that it cannot exchange; only a genuine
// outage is Unavailable, because only an outage is worth retrying.
func sourceDelegationCallerError(err error) error {
	switch {
	case errors.Is(err, business.ErrNoOperationBinding),
		errors.Is(err, business.ErrSourceDelegationContextStale),
		errors.Is(err, business.ErrModuleInstallationInactive),
		errors.Is(err, business.ErrModuleIdentityDenied):
		return status.Error(codes.PermissionDenied, "the caller is not a module this source delegation entitles")
	case status.Code(err) != codes.Unknown:
		return err
	default:
		return status.Error(codes.Unavailable, "source delegation authority is unavailable")
	}
}
