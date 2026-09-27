package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The google.rpc.ErrorInfo reasons a MintSourceOperationContext refusal
// carries, under SolutionRegistryErrorDomain (this service's one error domain).
// They are a wire contract with consuming modules and with the gateway, which
// maps them onto its HTTP answer; the status code alone already separates
// DELEGATION_MISSING (FAILED_PRECONDITION) from the other two
// (PERMISSION_DENIED).
const (
	// SourceDelegationMissingReason: the source has no active delegation to
	// the calling module. A person must connect or reconnect the source.
	SourceDelegationMissingReason = "DELEGATION_MISSING"
	// SourceDelegationRevokedReason: the delegation is revoked — explicitly, by
	// a reconnect, or because the host found the source, the person's
	// membership or role, or the binding no longer supports it.
	SourceDelegationRevokedReason = "DELEGATION_REVOKED"
	// SourceDelegationInvalidReason: the delegation does not exist or belongs
	// to another module.
	SourceDelegationInvalidReason = "DELEGATION_INVALID"
)

func sourceDelegationRefusal(code codes.Code, reason, message string) error {
	refused := status.New(code, message)
	detailed, err := refused.WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: SolutionRegistryErrorDomain})
	if err != nil {
		return refused.Err()
	}
	return detailed.Err()
}

// MintSourceOperationContext issues the Work Context one datasource source's
// sync runs with, from the delegation a person made by connecting the source.
// It authenticates exactly like MintModuleWorkContext; the business layer then
// re-checks the delegation against current facts, and nothing in the request
// decides the tenant, owner, audience or scopes.
//
// Codes: Unauthenticated when the module's identity is not proven;
// FailedPrecondition with reason DELEGATION_MISSING when the named source has
// no active delegation to the module; PermissionDenied with reason
// DELEGATION_REVOKED or DELEGATION_INVALID when the delegation cannot be used;
// FailedPrecondition with no reason when the issuer is unconfigured.
func (s *ModuleCapabilitiesServer) MintSourceOperationContext(ctx context.Context, req *gen.ModuleMintSourceOperationContextRequest) (*gen.ModuleMintSourceOperationContextResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	authority, err := service.AuthorizeSourceOperationContext(ctx, req.GetPrefix(), req.GetSecret(), business.SourceDelegationRef{
		DelegationID: req.GetDelegationId(),
		SourceID:     req.GetSourceId(),
	})
	if err != nil {
		return nil, mapSourceDelegationError(err)
	}
	token, signed, err := WorkContextSingleton().StartSourceOperationTask(authority)
	if err != nil {
		if errors.Is(err, ErrWorkContextAuthorityUnconfigured) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, mapWorkContextError(err)
	}
	// Written once the capability exists, and withheld when it cannot be — or
	// when the delegation was revoked while the capability was being signed.
	if err := service.RecordSourceOperationContextMint(ctx, authority); err != nil {
		return nil, mapSourceDelegationError(err)
	}
	return &gen.ModuleMintSourceOperationContextResponse{
		Token:            token.Encoded(),
		ExpiresAt:        timestamppb.New(time.Unix(signed.GetExpiresAtUnix(), 0).UTC()),
		PrincipalId:      authority.PrincipalID,
		Tenant:           authority.Tenant,
		Audience:         authority.Audience,
		Binding:          authority.Delegation.BindingID,
		DelegationId:     authority.Delegation.ID,
		SourceId:         authority.Delegation.SourceID,
		OwnerPrincipalId: authority.OwnerPrincipalID,
	}, nil
}

func mapSourceDelegationError(err error) error {
	switch {
	case errors.Is(err, business.ErrModuleRegistrationDenied):
		return status.Error(codes.Unauthenticated, "module source operation context denied")
	case errors.Is(err, business.ErrSourceDelegationMissing):
		return sourceDelegationRefusal(codes.FailedPrecondition, SourceDelegationMissingReason,
			"the source has no active delegation to this module; a person must connect or reconnect it")
	case errors.Is(err, business.ErrSourceDelegationRevoked):
		return sourceDelegationRefusal(codes.PermissionDenied, SourceDelegationRevokedReason, "the source delegation is revoked")
	case errors.Is(err, business.ErrSourceDelegationInvalid):
		return sourceDelegationRefusal(codes.PermissionDenied, SourceDelegationInvalidReason, "the source delegation is not usable by this module")
	default:
		return err
	}
}

func (h *moduleCapabilitiesConnectHandler) MintSourceOperationContext(ctx context.Context, req *connect.Request[gen.ModuleMintSourceOperationContextRequest]) (*connect.Response[gen.ModuleMintSourceOperationContextResponse], error) {
	return unary(ctx, req, h.inner.MintSourceOperationContext)
}

// StartSourceOperationTask mints the capability a delegation yields. The owner
// is the person who delegated the source and the tenant is its organization;
// the module's service principal is the sole actor, its hop carrying the
// delegation's id; the authority is exactly the binding's delegation scopes,
// sealed because the audience decides from the token alone; the revision binds
// the delegation, so CheckAuthorizationRevision stops confirming the context
// the moment the delegation ends. It lives the operation-context ceiling.
//
// Nothing is journaled: the module principal is declared, not registered, so
// the actor-chain journal has no row to record it against, and the durable
// record is the delegation's use event.
func (s *WorkContextAuthorityServer) StartSourceOperationTask(
	authority business.SourceOperationContextAuthority,
) (workcontext.WorkContextToken, *basev0.WorkContextV1, error) {
	if s == nil || s.configureErr != nil || s.signer == nil {
		return workcontext.WorkContextToken{}, nil, ErrWorkContextAuthorityUnconfigured
	}
	if authority.Audience == "" || authority.Audience == ModuleWorkContextAudience {
		return workcontext.WorkContextToken{}, nil, fmt.Errorf("%w: source operation context audience", workcontext.ErrWorkContextInvalid)
	}
	if authority.Revision == 0 || authority.OwnerPrincipalID == "" || authority.OwnerPrincipalID == authority.PrincipalID {
		return workcontext.WorkContextToken{}, nil, fmt.Errorf("%w: source operation context authority", workcontext.ErrWorkContextInvalid)
	}
	_, scopes, err := workContextScopes(authority.WireScopes(), mintContentReads(authority.Audience))
	if err != nil || len(scopes) == 0 {
		return workcontext.WorkContextToken{}, nil, fmt.Errorf("%w: source operation context scopes", workcontext.ErrWorkContextInvalid)
	}
	// A plain mint grants the actor everything the person delegated. A
	// reference-backed exchange grants it only what this call needs, already
	// narrowed to the delegation, so the hop carries less than the owner does —
	// the same shape the signer's attenuation produces on the parent-token arm.
	actorScopes := cloneWorkScopes(scopes)
	if len(authority.ActorScopes) > 0 {
		_, actorScopes, err = workContextScopes(authority.WireActorScopes(), mintContentReads(authority.Audience))
		if err != nil || len(actorScopes) == 0 {
			return workcontext.WorkContextToken{}, nil, fmt.Errorf("%w: source operation context actor scopes", workcontext.ErrWorkContextInvalid)
		}
	}
	return s.signer.StartTask(workcontext.StartTaskInput{
		Audience:              authority.Audience,
		TenantID:              authority.Tenant,
		OwnerPrincipalID:      authority.OwnerPrincipalID,
		TaskID:                uuid.NewString(),
		SessionID:             uuid.NewString(),
		AuthorizationRevision: authority.Revision,
		ReplayPolicy:          workcontext.WorkContextReplayIdempotent,
		AuthorityScopes:       scopes,
		ActorChain: []*basev0.WorkActorV1{{
			PrincipalId:   authority.PrincipalID,
			PrincipalKind: business.PrincipalKindService,
			DelegationId:  authority.Delegation.ID,
			GrantedScopes: actorScopes,
		}},
		TTL: business.SourceOperationContextTTL,
	})
}

// checkSourceDelegationContextRevision confirms a context MintSourceOperationContext
// issued: owned by a person, actored by a declared module principal. It reports
// handled=false for every other shape. Every refusal is PermissionDenied, so a
// consumer reads a revoked delegation as denied, never as an outage to retry.
func checkSourceDelegationContextRevision(ctx context.Context, req *gen.CheckAuthorizationRevisionRequest) (bool, error) {
	if service == nil {
		return false, nil
	}
	handled, err := service.CheckSourceDelegationContextRevision(
		ctx, req.GetOrgId(), req.GetOwnerPrincipalId(), req.GetAuthorizationRevision(), revisionSubjects(req),
	)
	if !handled {
		return false, nil
	}
	if errors.Is(err, business.ErrSourceDelegationContextStale) {
		return true, status.Error(codes.PermissionDenied, err.Error())
	}
	if err != nil {
		return true, status.Error(codes.Unavailable, "source delegation authority is unavailable")
	}
	return true, nil
}

func revisionSubjects(req *gen.CheckAuthorizationRevisionRequest) []business.ModuleOperationRevisionSubject {
	subjects := make([]business.ModuleOperationRevisionSubject, 0, len(req.GetSubjects()))
	for _, subject := range req.GetSubjects() {
		scopes := make([]business.ModuleOperationScope, 0, len(subject.GetScopes()))
		for _, scope := range subject.GetScopes() {
			scopes = append(scopes, business.ModuleOperationScope{
				ResourceKind: scope.GetResourceKind(),
				Actions:      append([]string(nil), scope.GetActions()...),
				ResourceIDs:  append([]string(nil), scope.GetResourceIds()...),
			})
		}
		subjects = append(subjects, business.ModuleOperationRevisionSubject{
			PrincipalID: subject.GetPrincipalId(),
			Scopes:      scopes,
		})
	}
	return subjects
}

// ---------------------------------------------------------------------------
// The organization administrator's surface (DatasourceService)
// ---------------------------------------------------------------------------

func (h *datasourceConnectHandler) ListSourceDelegations(
	ctx context.Context,
	req *connect.Request[gen.ListSourceDelegationsRequest],
) (*connect.Response[gen.ListSourceDelegationsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	delegations, err := h.svc.ListSourceDelegations(ctx, req.Msg.OrgId, req.Msg.GetSourceId(), req.Msg.IncludeRevoked)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	out := make([]*gen.SourceDelegation, 0, len(delegations))
	for _, delegation := range delegations {
		out = append(out, sourceDelegationToProto(delegation))
	}
	return connect.NewResponse(&gen.ListSourceDelegationsResponse{Delegations: out}), nil
}

func (h *datasourceConnectHandler) RevokeSourceDelegation(
	ctx context.Context,
	req *connect.Request[gen.RevokeSourceDelegationRequest],
) (*connect.Response[gen.RevokeSourceDelegationResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	delegation, err := h.svc.RevokeSourceDelegation(ctx, actorID, req.Msg.OrgId, req.Msg.Id)
	if err != nil {
		if errors.Is(err, business.ErrSourceDelegationNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.RevokeSourceDelegationResponse{Delegation: sourceDelegationToProto(delegation)}), nil
}

var sourceDelegationRevocations = map[string]gen.SourceDelegationRevocation{
	business.SourceDelegationRevokedByAdmin: gen.SourceDelegationRevocation_SOURCE_DELEGATION_REVOCATION_REVOKED,
	business.SourceDelegationReplaced:       gen.SourceDelegationRevocation_SOURCE_DELEGATION_REVOCATION_REPLACED,
	business.SourceDelegationSourceDeleted:  gen.SourceDelegationRevocation_SOURCE_DELEGATION_REVOCATION_SOURCE_DELETED,
	business.SourceDelegationMemberRemoved:  gen.SourceDelegationRevocation_SOURCE_DELEGATION_REVOCATION_MEMBER_REMOVED,
	business.SourceDelegationPermissionLost: gen.SourceDelegationRevocation_SOURCE_DELEGATION_REVOCATION_PERMISSION_LOST,
	business.SourceDelegationUserInactive:   gen.SourceDelegationRevocation_SOURCE_DELEGATION_REVOCATION_USER_INACTIVE,
	business.SourceDelegationBindingChanged: gen.SourceDelegationRevocation_SOURCE_DELEGATION_REVOCATION_BINDING_CHANGED,
}

func sourceDelegationToProto(delegation *business.SourceDelegation) *gen.SourceDelegation {
	out := &gen.SourceDelegation{
		Id:          delegation.ID,
		SourceId:    delegation.SourceID,
		PrincipalId: delegation.PrincipalID,
		Module:      delegation.ModulePrefix,
		Binding:     delegation.BindingID,
		CreatedAt:   timestamppb.New(delegation.CreatedAt),
		RevokedBy:   delegation.RevokedBy,
	}
	if delegation.RevokedAt != nil {
		out.RevokedAt = timestamppb.New(*delegation.RevokedAt)
		out.Revocation = sourceDelegationRevocations[delegation.RevokedReason]
	}
	return out
}
