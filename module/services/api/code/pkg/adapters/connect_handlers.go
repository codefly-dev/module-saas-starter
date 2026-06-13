package adapters

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/codefly-dev/core/wool"

	"api/pkg/business"
	"api/pkg/gen"
)

// connectCtx converts Connect HTTP headers to gRPC incoming metadata so that
// existing gRPC handlers using `wool.GRPC().Inject()` continue to work unchanged
// when invoked from the Connect adapters.
func connectCtx(ctx context.Context, h http.Header) context.Context {
	md := metadata.New(nil)
	for header, key := range wool.HTTPMappings {
		if values := h.Values(header); len(values) > 0 {
			md.Set(string(key), values...)
		}
	}
	if v := h.Values("Authorization"); len(v) > 0 {
		md.Set("authorization", v...)
	}
	return metadata.NewIncomingContext(ctx, md)
}

// unary adapts a gRPC-style handler fn to a Connect handler by unwrapping the
// request, delegating, and wrapping the response.
//
// Errors from gRPC handlers are typed via `status.Error(codes.X, ...)`.
// Connect-Go has its own error code enum and won't recognize a raw
// gRPC status; without translation, every authenticated-but-denied
// call would surface as HTTP 500 / connect code "unknown" instead of
// the correct 401/403/404/etc. translateGRPCError fixes that.
func unary[Req, Resp any](
	ctx context.Context,
	req *connect.Request[Req],
	fn func(context.Context, *Req) (*Resp, error),
) (*connect.Response[Resp], error) {
	ctx = connectCtx(ctx, req.Header())
	resp, err := fn(ctx, req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(resp), nil
}

// translateGRPCError converts a google.golang.org/grpc/status error
// into a connect.Error with the matching code. Pass-through for
// errors that are already connect.Errors or don't carry a gRPC status.
//
// The mapping mirrors Connect's own grpc → connect lookup table; we
// only need a subset because handlers don't currently emit every
// possible code. Anything we don't list explicitly falls back to
// connect.CodeUnknown — the original Connect default — so behaviour
// for unmapped codes is unchanged.
func translateGRPCError(err error) error {
	if err == nil {
		return nil
	}
	// Already a connect error → pass through (handlers may produce
	// these directly when interacting with Connect-only paths).
	var ce *connect.Error
	if asConnectError(err, &ce) {
		return err
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	cc := connectCodeFromGRPC(st.Code())
	return connect.NewError(cc, st.Err())
}

// asConnectError is a tiny errors.As shim kept here to avoid pulling
// `errors` into every connect_handlers consumer. Returns true if err
// (or any wrapped error) is a *connect.Error and writes the result
// into out.
func asConnectError(err error, out **connect.Error) bool {
	for cur := err; cur != nil; {
		if ce, ok := cur.(*connect.Error); ok {
			*out = ce
			return true
		}
		// Unwrap manually to avoid a hard dep on errors.Unwrap from
		// callers; Connect doesn't expose its own.
		type unwrapper interface{ Unwrap() error }
		if u, ok := cur.(unwrapper); ok {
			cur = u.Unwrap()
			continue
		}
		break
	}
	return false
}

func connectCodeFromGRPC(c codes.Code) connect.Code {
	switch c {
	case codes.Canceled:
		return connect.CodeCanceled
	case codes.Unknown:
		return connect.CodeUnknown
	case codes.InvalidArgument:
		return connect.CodeInvalidArgument
	case codes.DeadlineExceeded:
		return connect.CodeDeadlineExceeded
	case codes.NotFound:
		return connect.CodeNotFound
	case codes.AlreadyExists:
		return connect.CodeAlreadyExists
	case codes.PermissionDenied:
		return connect.CodePermissionDenied
	case codes.ResourceExhausted:
		return connect.CodeResourceExhausted
	case codes.FailedPrecondition:
		return connect.CodeFailedPrecondition
	case codes.Aborted:
		return connect.CodeAborted
	case codes.OutOfRange:
		return connect.CodeOutOfRange
	case codes.Unimplemented:
		return connect.CodeUnimplemented
	case codes.Internal:
		return connect.CodeInternal
	case codes.Unavailable:
		return connect.CodeUnavailable
	case codes.DataLoss:
		return connect.CodeDataLoss
	case codes.Unauthenticated:
		return connect.CodeUnauthenticated
	}
	return connect.CodeUnknown
}

// callerID extracts the authenticated user id from request headers/context.
// Returns a gRPC Unauthenticated error if not present.
func callerID(ctx context.Context) (string, error) {
	w := wool.Get(ctx).In("callerID")
	w.GRPC().Inject()
	id, ok := w.UserID()
	if !ok {
		id, ok = w.UserAuthID()
	}
	if !ok {
		return "", status.Error(codes.Unauthenticated, "caller identity not found")
	}
	return id, nil
}

// callerOrg extracts the caller's org id from context, empty string if absent.
func callerOrg(ctx context.Context) string {
	w := wool.Get(ctx).In("callerOrg")
	w.GRPC().Inject()
	id, _ := w.OrgID()
	return id
}

// ============================================================================
// UserService
// ============================================================================

type userConnectHandler struct{ inner *UserServer }

func (h *userConnectHandler) Version(ctx context.Context, req *connect.Request[gen.VersionRequest]) (*connect.Response[gen.VersionResponse], error) {
	return unary(ctx, req, h.inner.Version)
}
func (h *userConnectHandler) GetSelf(ctx context.Context, req *connect.Request[gen.GetSelfRequest]) (*connect.Response[gen.GetSelfResponse], error) {
	return unary(ctx, req, h.inner.GetSelf)
}
func (h *userConnectHandler) RegisterUser(ctx context.Context, req *connect.Request[gen.RegisterUserRequest]) (*connect.Response[gen.RegisterUserResponse], error) {
	return unary(ctx, req, h.inner.RegisterUser)
}
func (h *userConnectHandler) GetUser(ctx context.Context, req *connect.Request[gen.GetUserRequest]) (*connect.Response[gen.User], error) {
	return unary(ctx, req, h.inner.GetUser)
}
func (h *userConnectHandler) ListUsers(ctx context.Context, req *connect.Request[gen.ListUsersRequest]) (*connect.Response[gen.ListUsersResponse], error) {
	return unary(ctx, req, h.inner.ListUsers)
}
func (h *userConnectHandler) UpdateUser(ctx context.Context, req *connect.Request[gen.UpdateUserRequest]) (*connect.Response[gen.User], error) {
	return unary(ctx, req, h.inner.UpdateUser)
}
func (h *userConnectHandler) DeleteUser(ctx context.Context, req *connect.Request[gen.GetUserRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.DeleteUser)
}
func (h *userConnectHandler) AddIdentity(ctx context.Context, req *connect.Request[gen.AddIdentityRequest]) (*connect.Response[gen.UserIdentity], error) {
	return unary(ctx, req, h.inner.AddIdentity)
}
func (h *userConnectHandler) FindUserByIdentity(ctx context.Context, req *connect.Request[gen.FindUserByIdentityRequest]) (*connect.Response[gen.User], error) {
	return unary(ctx, req, h.inner.FindUserByIdentity)
}
func (h *userConnectHandler) ListUserIdentities(ctx context.Context, req *connect.Request[gen.ListUserIdentitiesRequest]) (*connect.Response[gen.ListUserIdentitiesResponse], error) {
	return unary(ctx, req, h.inner.ListUserIdentities)
}

// ============================================================================
// OrganizationService
// ============================================================================

type orgConnectHandler struct{ inner *OrgServer }

func (h *orgConnectHandler) CreateOrganization(ctx context.Context, req *connect.Request[gen.CreateOrganizationRequest]) (*connect.Response[gen.CreateOrganizationResponse], error) {
	return unary(ctx, req, h.inner.CreateOrganization)
}
func (h *orgConnectHandler) GetOrganization(ctx context.Context, req *connect.Request[gen.GetOrganizationRequest]) (*connect.Response[gen.Organization], error) {
	return unary(ctx, req, h.inner.GetOrganization)
}
func (h *orgConnectHandler) ListOrganizations(ctx context.Context, req *connect.Request[gen.ListOrganizationsRequest]) (*connect.Response[gen.ListOrganizationsResponse], error) {
	return unary(ctx, req, h.inner.ListOrganizations)
}
func (h *orgConnectHandler) AddMember(ctx context.Context, req *connect.Request[gen.AddOrgMemberRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.AddMember)
}
func (h *orgConnectHandler) RemoveMember(ctx context.Context, req *connect.Request[gen.RemoveOrgMemberRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.RemoveMember)
}
func (h *orgConnectHandler) ListMembers(ctx context.Context, req *connect.Request[gen.ListOrgMembersRequest]) (*connect.Response[gen.ListOrgMembersResponse], error) {
	return unary(ctx, req, h.inner.ListMembers)
}
func (h *orgConnectHandler) GetOrgSettings(ctx context.Context, req *connect.Request[gen.GetOrgSettingsRequest]) (*connect.Response[gen.OrgSettings], error) {
	return unary(ctx, req, h.inner.GetOrgSettings)
}
func (h *orgConnectHandler) UpdateOrgSettings(ctx context.Context, req *connect.Request[gen.UpdateOrgSettingsRequest]) (*connect.Response[gen.OrgSettings], error) {
	return unary(ctx, req, h.inner.UpdateOrgSettings)
}

// ============================================================================
// TeamService
// ============================================================================

type teamConnectHandler struct{ inner *TeamServer }

func (h *teamConnectHandler) CreateTeam(ctx context.Context, req *connect.Request[gen.CreateTeamRequest]) (*connect.Response[gen.CreateTeamResponse], error) {
	return unary(ctx, req, h.inner.CreateTeam)
}
func (h *teamConnectHandler) ListTeams(ctx context.Context, req *connect.Request[gen.ListTeamsRequest]) (*connect.Response[gen.ListTeamsResponse], error) {
	return unary(ctx, req, h.inner.ListTeams)
}
func (h *teamConnectHandler) AddMember(ctx context.Context, req *connect.Request[gen.AddTeamMemberRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.AddMember)
}
func (h *teamConnectHandler) RemoveMember(ctx context.Context, req *connect.Request[gen.RemoveTeamMemberRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.RemoveMember)
}
func (h *teamConnectHandler) ListMembers(ctx context.Context, req *connect.Request[gen.ListTeamMembersRequest]) (*connect.Response[gen.ListTeamMembersResponse], error) {
	return unary(ctx, req, h.inner.ListMembers)
}

// ============================================================================
// PermissionService
// ============================================================================

type permConnectHandler struct{ inner *PermServer }

func (h *permConnectHandler) CreateRole(ctx context.Context, req *connect.Request[gen.CreateRoleRequest]) (*connect.Response[gen.CreateRoleResponse], error) {
	return unary(ctx, req, h.inner.CreateRole)
}
func (h *permConnectHandler) ListRoles(ctx context.Context, req *connect.Request[gen.ListRolesRequest]) (*connect.Response[gen.ListRolesResponse], error) {
	return unary(ctx, req, h.inner.ListRoles)
}
func (h *permConnectHandler) DeleteRole(ctx context.Context, req *connect.Request[gen.DeleteRoleRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.DeleteRole)
}
func (h *permConnectHandler) AssignRole(ctx context.Context, req *connect.Request[gen.AssignRoleRequest]) (*connect.Response[gen.AssignRoleResponse], error) {
	return unary(ctx, req, h.inner.AssignRole)
}
func (h *permConnectHandler) RevokeRole(ctx context.Context, req *connect.Request[gen.RevokeRoleRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.RevokeRole)
}
func (h *permConnectHandler) ListRoleAssignments(ctx context.Context, req *connect.Request[gen.ListRoleAssignmentsRequest]) (*connect.Response[gen.ListRoleAssignmentsResponse], error) {
	return unary(ctx, req, h.inner.ListRoleAssignments)
}
func (h *permConnectHandler) CheckPermission(ctx context.Context, req *connect.Request[gen.CheckPermissionRequest]) (*connect.Response[gen.CheckPermissionResponse], error) {
	return unary(ctx, req, h.inner.CheckPermission)
}
func (h *permConnectHandler) Decide(ctx context.Context, req *connect.Request[gen.DecideRequest]) (*connect.Response[gen.DecideResponse], error) {
	return unary(ctx, req, h.inner.Decide)
}

// ============================================================================
// IntrospectionService
// ============================================================================

type introspectionConnectHandler struct{ inner *IntrospectionServer }

func (h *introspectionConnectHandler) GetServiceInfo(ctx context.Context, req *connect.Request[gen.GetServiceInfoRequest]) (*connect.Response[gen.GetServiceInfoResponse], error) {
	return unary(ctx, req, h.inner.GetServiceInfo)
}

// ============================================================================
// IdentityService
// ============================================================================

type identConnectHandler struct{ inner *IdentServer }

func (h *identConnectHandler) ResolveIdentity(ctx context.Context, req *connect.Request[gen.ResolveIdentityRequest]) (*connect.Response[gen.ResolveIdentityResponse], error) {
	return unary(ctx, req, h.inner.ResolveIdentity)
}

// ============================================================================
// APIKeyService
// ============================================================================

type apiKeyConnectHandler struct{ inner *APIKeyServer }

func (h *apiKeyConnectHandler) CreateAPIKey(ctx context.Context, req *connect.Request[gen.CreateAPIKeyRequest]) (*connect.Response[gen.CreateAPIKeyResponse], error) {
	return unary(ctx, req, h.inner.CreateAPIKey)
}
func (h *apiKeyConnectHandler) ListAPIKeys(ctx context.Context, req *connect.Request[gen.ListAPIKeysRequest]) (*connect.Response[gen.ListAPIKeysResponse], error) {
	return unary(ctx, req, h.inner.ListAPIKeys)
}
func (h *apiKeyConnectHandler) RevokeAPIKey(ctx context.Context, req *connect.Request[gen.RevokeAPIKeyRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.RevokeAPIKey)
}
func (h *apiKeyConnectHandler) ValidateAPIKey(ctx context.Context, req *connect.Request[gen.ValidateAPIKeyRequest]) (*connect.Response[gen.ValidateAPIKeyResponse], error) {
	return unary(ctx, req, h.inner.ValidateAPIKey)
}

// ============================================================================
// AuthService
// ============================================================================

type authConnectHandler struct{ inner *AuthServer }

func (h *authConnectHandler) BeginOAuth(ctx context.Context, req *connect.Request[gen.BeginOAuthRequest]) (*connect.Response[gen.BeginOAuthResponse], error) {
	return unary(ctx, req, h.inner.BeginOAuth)
}

func (h *authConnectHandler) Authenticate(ctx context.Context, req *connect.Request[gen.AuthenticateRequest]) (*connect.Response[gen.AuthenticateResponse], error) {
	return unary(ctx, req, h.inner.Authenticate)
}
func (h *authConnectHandler) RefreshToken(ctx context.Context, req *connect.Request[gen.RefreshTokenRequest]) (*connect.Response[gen.RefreshTokenResponse], error) {
	return unary(ctx, req, h.inner.RefreshToken)
}
func (h *authConnectHandler) Logout(ctx context.Context, req *connect.Request[gen.LogoutRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.Logout)
}
func (h *authConnectHandler) GetJWKS(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[gen.JWKSResponse], error) {
	return unary(ctx, req, h.inner.GetJWKS)
}

// ============================================================================
// AuditService
// ============================================================================

type auditConnectHandler struct{ inner *AuditServer }

func (h *auditConnectHandler) QueryAuditLog(ctx context.Context, req *connect.Request[gen.QueryAuditLogRequest]) (*connect.Response[gen.QueryAuditLogResponse], error) {
	return unary(ctx, req, h.inner.QueryAuditLog)
}
func (h *auditConnectHandler) ExportAuditLog(ctx context.Context, req *connect.Request[gen.ExportAuditLogRequest]) (*connect.Response[gen.ExportAuditLogResponse], error) {
	return unary(ctx, req, h.inner.ExportAuditLog)
}

// ============================================================================
// InvitationService
// ============================================================================

type invitationConnectHandler struct{ inner *InvitationServer }

func (h *invitationConnectHandler) CreateInvitation(ctx context.Context, req *connect.Request[gen.CreateInvitationRequest]) (*connect.Response[gen.CreateInvitationResponse], error) {
	return unary(ctx, req, h.inner.CreateInvitation)
}
func (h *invitationConnectHandler) AcceptInvitation(ctx context.Context, req *connect.Request[gen.AcceptInvitationRequest]) (*connect.Response[gen.AcceptInvitationResponse], error) {
	return unary(ctx, req, h.inner.AcceptInvitation)
}
func (h *invitationConnectHandler) ListInvitations(ctx context.Context, req *connect.Request[gen.ListInvitationsRequest]) (*connect.Response[gen.ListInvitationsResponse], error) {
	return unary(ctx, req, h.inner.ListInvitations)
}
func (h *invitationConnectHandler) RevokeInvitation(ctx context.Context, req *connect.Request[gen.RevokeInvitationRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.RevokeInvitation)
}

// ============================================================================
// PlatformAdminService
// ============================================================================

type platformAdminConnectHandler struct{ inner *PlatformAdminServer }

func (h *platformAdminConnectHandler) SearchUsers(ctx context.Context, req *connect.Request[gen.SearchUsersRequest]) (*connect.Response[gen.SearchUsersResponse], error) {
	return unary(ctx, req, h.inner.SearchUsers)
}
func (h *platformAdminConnectHandler) SuspendUser(ctx context.Context, req *connect.Request[gen.SuspendUserRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.SuspendUser)
}
func (h *platformAdminConnectHandler) UnsuspendUser(ctx context.Context, req *connect.Request[gen.UnsuspendUserRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.UnsuspendUser)
}
func (h *platformAdminConnectHandler) ImpersonateUser(ctx context.Context, req *connect.Request[gen.ImpersonateUserRequest]) (*connect.Response[gen.ImpersonateUserResponse], error) {
	return unary(ctx, req, h.inner.ImpersonateUser)
}
func (h *platformAdminConnectHandler) ListActiveSessions(ctx context.Context, req *connect.Request[gen.ListActiveSessionsRequest]) (*connect.Response[gen.ListActiveSessionsResponse], error) {
	return unary(ctx, req, h.inner.ListActiveSessions)
}
func (h *platformAdminConnectHandler) GetOrgEntitlements(ctx context.Context, req *connect.Request[gen.GetOrgEntitlementsRequest]) (*connect.Response[gen.GetOrgEntitlementsResponse], error) {
	return unary(ctx, req, h.inner.GetOrgEntitlements)
}
func (h *platformAdminConnectHandler) OverrideEntitlement(ctx context.Context, req *connect.Request[gen.OverrideEntitlementRequest]) (*connect.Response[gen.OverrideEntitlementResponse], error) {
	return unary(ctx, req, h.inner.OverrideEntitlement)
}
func (h *platformAdminConnectHandler) GrantPlatformRole(ctx context.Context, req *connect.Request[gen.GrantPlatformRoleRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.GrantPlatformRole)
}
func (h *platformAdminConnectHandler) RevokePlatformRole(ctx context.Context, req *connect.Request[gen.RevokePlatformRoleRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.RevokePlatformRole)
}
func (h *platformAdminConnectHandler) ListPlatformAdmins(ctx context.Context, req *connect.Request[gen.ListPlatformAdminsRequest]) (*connect.Response[gen.ListPlatformAdminsResponse], error) {
	return unary(ctx, req, h.inner.ListPlatformAdmins)
}
func (h *platformAdminConnectHandler) ListFeatureFlags(ctx context.Context, req *connect.Request[gen.ListFeatureFlagsRequest]) (*connect.Response[gen.ListFeatureFlagsResponse], error) {
	return unary(ctx, req, h.inner.ListFeatureFlags)
}
func (h *platformAdminConnectHandler) UpsertFeatureFlag(ctx context.Context, req *connect.Request[gen.UpsertFeatureFlagRequest]) (*connect.Response[gen.UpsertFeatureFlagResponse], error) {
	return unary(ctx, req, h.inner.UpsertFeatureFlag)
}

// ============================================================================
// WebhookService — backed by business.Service, no gRPC Server struct yet.
// Secret is server-generated on CreateSubscription (returned to client as
// part of the subscription response when implemented — TODO).
// ============================================================================

type webhookConnectHandler struct{ svc *business.Service }

func (h *webhookConnectHandler) CreateSubscription(ctx context.Context, req *connect.Request[gen.CreateWebhookSubscriptionRequest]) (*connect.Response[gen.WebhookSubscription], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "webhooks:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	sub, err := h.svc.CreateSubscription(ctx, req.Msg.OrgId, req.Msg.Url, "", req.Msg.Events, req.Msg.Description)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(webhookSubToProto(sub)), nil
}

func (h *webhookConnectHandler) DeleteSubscription(ctx context.Context, req *connect.Request[gen.DeleteWebhookSubscriptionRequest]) (*connect.Response[emptypb.Empty], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "webhooks:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := h.svc.DeleteSubscription(ctx, callerOrg(ctx), req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *webhookConnectHandler) ListSubscriptions(ctx context.Context, req *connect.Request[gen.ListWebhookSubscriptionsRequest]) (*connect.Response[gen.ListWebhookSubscriptionsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "webhooks:read"); err != nil {
		return nil, translateGRPCError(err)
	}
	subs, err := h.svc.ListSubscriptions(ctx, req.Msg.OrgId)
	if err != nil {
		return nil, err
	}
	out := make([]*gen.WebhookSubscription, 0, len(subs))
	for _, s := range subs {
		out = append(out, webhookSubToProto(s))
	}
	return connect.NewResponse(&gen.ListWebhookSubscriptionsResponse{Subscriptions: out}), nil
}

func (h *webhookConnectHandler) ListDeliveries(ctx context.Context, req *connect.Request[gen.ListWebhookDeliveriesRequest]) (*connect.Response[gen.ListWebhookDeliveriesResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "webhooks:read"); err != nil {
		return nil, translateGRPCError(err)
	}
	pageSize := int(req.Msg.PageSize)
	if pageSize == 0 {
		pageSize = 50
	}
	deliveries, err := h.svc.ListDeliveries(ctx, callerOrg(ctx), req.Msg.SubscriptionId, pageSize)
	if err != nil {
		return nil, err
	}
	out := make([]*gen.WebhookDelivery, 0, len(deliveries))
	for _, d := range deliveries {
		out = append(out, webhookDeliveryToProto(d))
	}
	return connect.NewResponse(&gen.ListWebhookDeliveriesResponse{Deliveries: out}), nil
}

func (h *webhookConnectHandler) TestWebhook(ctx context.Context, req *connect.Request[gen.TestWebhookRequest]) (*connect.Response[gen.WebhookDelivery], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "webhooks:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	d, err := h.svc.TestWebhook(ctx, callerOrg(ctx), req.Msg.Id, req.Msg.EventType)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(webhookDeliveryToProto(d)), nil
}

// GetDelivery returns the full delivery row including response body
// — used by the deliveries detail panel on row-click. List endpoint
// returns the same shape so this is mostly an explicit fetch when
// the FE wants a fresh read after a replay or a status change.
func (h *webhookConnectHandler) GetDelivery(ctx context.Context, req *connect.Request[gen.GetWebhookDeliveryRequest]) (*connect.Response[gen.WebhookDelivery], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "webhooks:read"); err != nil {
		return nil, translateGRPCError(err)
	}
	d, err := h.svc.GetWebhookDelivery(ctx, callerOrg(ctx), req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(webhookDeliveryToProto(d)), nil
}

// ReplayDelivery re-fires an existing delivery's payload at the
// current subscription URL. Returns the new delivery row so the FE
// can show outcome immediately. Authz: caller must be org-admin on
// the original delivery's subscription org.
func (h *webhookConnectHandler) ReplayDelivery(ctx context.Context, req *connect.Request[gen.ReplayWebhookDeliveryRequest]) (*connect.Response[gen.WebhookDelivery], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "webhooks:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	d, err := h.svc.ReplayWebhookDelivery(ctx, callerOrg(ctx), req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(webhookDeliveryToProto(d)), nil
}

// RotateSecret generates a new HMAC signing secret. The secret is
// returned ONCE in the response — the FE shows it with a copy
// button and a warning that this is the last chance to grab it.
//
// Sensitive op: gated by requireMFA (consumer endpoints can be
// hijacked if an attacker rotates and re-signs; same blast radius as
// rotating an API key).
func (h *webhookConnectHandler) RotateSecret(ctx context.Context, req *connect.Request[gen.RotateWebhookSecretRequest]) (*connect.Response[gen.RotateWebhookSecretResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := requireScope(ctx, "webhooks:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireMFA(ctx, actorID); err != nil {
		return nil, err
	}
	secret, err := h.svc.RotateWebhookSecret(ctx, callerOrg(ctx), req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&gen.RotateWebhookSecretResponse{Secret: secret}), nil
}

// ============================================================================
// NotificationService — backed by business.Service.
// Caller identity comes from auth headers.
// ============================================================================

type notificationConnectHandler struct{ svc *business.Service }

func (h *notificationConnectHandler) ListNotifications(ctx context.Context, req *connect.Request[gen.ListNotificationsRequest]) (*connect.Response[gen.ListNotificationsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	pageSize := int(req.Msg.PageSize)
	if pageSize == 0 {
		pageSize = 50
	}
	notes, nextToken, err := h.svc.ListNotifications(ctx, userID, pageSize, req.Msg.PageToken)
	if err != nil {
		return nil, err
	}
	out := make([]*gen.Notification, 0, len(notes))
	for _, n := range notes {
		out = append(out, notificationToProto(n))
	}
	return connect.NewResponse(&gen.ListNotificationsResponse{Notifications: out, NextPageToken: nextToken}), nil
}

func (h *notificationConnectHandler) GetUnreadCount(ctx context.Context, req *connect.Request[gen.GetUnreadCountRequest]) (*connect.Response[gen.GetUnreadCountResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	count, err := h.svc.GetUnreadCount(ctx, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&gen.GetUnreadCountResponse{Count: int32(count)}), nil
}

func (h *notificationConnectHandler) MarkRead(ctx context.Context, req *connect.Request[gen.MarkNotificationReadRequest]) (*connect.Response[emptypb.Empty], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := h.svc.MarkRead(ctx, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *notificationConnectHandler) MarkAllRead(ctx context.Context, req *connect.Request[gen.MarkAllNotificationsReadRequest]) (*connect.Response[emptypb.Empty], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.MarkAllRead(ctx, userID); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *notificationConnectHandler) DeleteNotification(ctx context.Context, req *connect.Request[gen.DeleteNotificationRequest]) (*connect.Response[emptypb.Empty], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := h.svc.DeleteNotification(ctx, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

// ============================================================================
// OnboardingService — backed by business.Service.
// ============================================================================

type onboardingConnectHandler struct{ svc *business.Service }

func (h *onboardingConnectHandler) GetProgress(ctx context.Context, req *connect.Request[gen.GetOnboardingProgressRequest]) (*connect.Response[gen.OnboardingProgress], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.svc.GetProgress(ctx, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(onboardingToProto(p)), nil
}

func (h *onboardingConnectHandler) CompleteStep(ctx context.Context, req *connect.Request[gen.CompleteOnboardingStepRequest]) (*connect.Response[gen.OnboardingProgress], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.CompleteStep(ctx, userID, req.Msg.StepName); err != nil {
		return nil, err
	}
	p, err := h.svc.GetProgress(ctx, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(onboardingToProto(p)), nil
}

func (h *onboardingConnectHandler) SkipStep(ctx context.Context, req *connect.Request[gen.SkipOnboardingStepRequest]) (*connect.Response[gen.OnboardingProgress], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.SkipStep(ctx, userID, req.Msg.StepName); err != nil {
		return nil, err
	}
	p, err := h.svc.GetProgress(ctx, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(onboardingToProto(p)), nil
}

// ============================================================================
// GDPRService — backed by business.Service.
// ============================================================================

type gdprConnectHandler struct{ svc *business.Service }

func (h *gdprConnectHandler) RequestExport(ctx context.Context, req *connect.Request[gen.RequestDataExportRequest]) (*connect.Response[gen.GDPRRequest], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.svc.RequestExport(ctx, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(gdprToProto(r)), nil
}

func (h *gdprConnectHandler) GetExportStatus(ctx context.Context, req *connect.Request[gen.GetExportStatusRequest]) (*connect.Response[gen.GDPRRequest], error) {
	ctx = connectCtx(ctx, req.Header())
	r, err := h.svc.GetExportStatus(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(gdprToProto(r)), nil
}

func (h *gdprConnectHandler) RequestDeletion(ctx context.Context, req *connect.Request[gen.RequestDeletionRequest]) (*connect.Response[gen.GDPRRequest], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	// GDPR deletion is irreversible — gate behind MFA when enrolled.
	if err := requireMFA(ctx, userID); err != nil {
		return nil, err
	}
	r, err := h.svc.RequestDeletion(ctx, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(gdprToProto(r)), nil
}

func (h *gdprConnectHandler) GetDeletionStatus(ctx context.Context, req *connect.Request[gen.GetDeletionStatusRequest]) (*connect.Response[gen.GDPRRequest], error) {
	ctx = connectCtx(ctx, req.Header())
	r, err := h.svc.GetExportStatus(ctx, req.Msg.Id) // underlying lookup is by request id
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(gdprToProto(r)), nil
}

// ============================================================================
// MFAService — backed by business.Service.
// ============================================================================

type mfaConnectHandler struct{ svc *business.Service }

func (h *mfaConnectHandler) SetupTOTP(ctx context.Context, req *connect.Request[gen.SetupTOTPRequest]) (*connect.Response[gen.SetupTOTPResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	secret, uri, err := h.svc.SetupTOTP(ctx, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&gen.SetupTOTPResponse{Secret: secret, ProvisioningUri: uri}), nil
}

func (h *mfaConnectHandler) VerifyTOTP(ctx context.Context, req *connect.Request[gen.VerifyTOTPRequest]) (*connect.Response[gen.VerifyTOTPResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.VerifyTOTP(ctx, userID, req.Msg.Code); err != nil {
		return connect.NewResponse(&gen.VerifyTOTPResponse{Valid: false}), nil
	}
	return connect.NewResponse(&gen.VerifyTOTPResponse{Valid: true}), nil
}

func (h *mfaConnectHandler) ListDevices(ctx context.Context, req *connect.Request[gen.ListMFADevicesRequest]) (*connect.Response[gen.ListMFADevicesResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	devices, err := h.svc.ListMFADevices(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*gen.MFADevice, 0, len(devices))
	for _, d := range devices {
		out = append(out, mfaDeviceToProto(d))
	}
	return connect.NewResponse(&gen.ListMFADevicesResponse{Devices: out}), nil
}

func (h *mfaConnectHandler) RevokeDevice(ctx context.Context, req *connect.Request[gen.RevokeMFADeviceRequest]) (*connect.Response[emptypb.Empty], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.RevokeMFADevice(ctx, userID, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *mfaConnectHandler) GenerateBackupCodes(ctx context.Context, req *connect.Request[gen.GenerateBackupCodesRequest]) (*connect.Response[gen.GenerateBackupCodesResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	codes, err := h.svc.GenerateBackupCodes(ctx, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&gen.GenerateBackupCodesResponse{BackupCodes: codes}), nil
}

// ============================================================================
// business ↔ proto mappers
// ============================================================================

func webhookSubToProto(s *business.WebhookSubscription) *gen.WebhookSubscription {
	if s == nil {
		return nil
	}
	out := &gen.WebhookSubscription{
		Id:          s.ID,
		OrgId:       s.OrgID,
		Url:         s.URL,
		Events:      s.Events,
		Active:      s.Active,
		Description: s.Description,
	}
	if !s.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(s.CreatedAt)
	}
	return out
}

func webhookDeliveryToProto(d *business.WebhookDelivery) *gen.WebhookDelivery {
	if d == nil {
		return nil
	}
	out := &gen.WebhookDelivery{
		Id:             d.ID,
		SubscriptionId: d.SubscriptionID,
		EventType:      d.EventType,
		Payload:        d.Payload,
		Attempts:       int32(d.AttemptCount),
		HttpStatus:     int32(d.HTTPStatus),
		Status:         webhookDeliveryStatusToProto(d.Status),
		ResponseBody:   d.ResponseBody,
	}
	if d.NextRetryAt != nil && !d.NextRetryAt.IsZero() {
		out.NextRetryAt = timestamppb.New(*d.NextRetryAt)
	}
	if !d.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(d.CreatedAt)
	}
	if d.DeliveredAt != nil {
		out.DeliveredAt = timestamppb.New(*d.DeliveredAt)
	}
	return out
}

func webhookDeliveryStatusToProto(s string) gen.WebhookDeliveryStatus {
	switch s {
	case "delivered", "success":
		return gen.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_SUCCESS
	case "failed":
		return gen.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_FAILED
	case "pending", "retrying":
		return gen.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_PENDING
	}
	return gen.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_UNSPECIFIED
}

func notificationToProto(n *business.Notification) *gen.Notification {
	if n == nil {
		return nil
	}
	out := &gen.Notification{
		Id:        n.ID,
		UserId:    n.UserID,
		OrgId:     n.OrgID,
		Title:     n.Title,
		Body:      n.Body,
		Type:      n.Type,
		ActionUrl: n.ActionURL,
	}
	if !n.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(n.CreatedAt)
	}
	if n.ReadAt != nil {
		out.ReadAt = timestamppb.New(*n.ReadAt)
	}
	return out
}

func onboardingToProto(p *business.OnboardingProgress) *gen.OnboardingProgress {
	if p == nil {
		return &gen.OnboardingProgress{}
	}
	steps := make([]*gen.OnboardingStep, 0, len(p.Steps))
	for _, s := range p.Steps {
		step := &gen.OnboardingStep{
			StepName: s.StepName,
			Status:   onboardingStepStatusToProto(s.Status),
		}
		if s.CompletedAt != nil {
			step.CompletedAt = timestamppb.New(*s.CompletedAt)
		}
		steps = append(steps, step)
	}
	return &gen.OnboardingProgress{Steps: steps, Completed: p.Completed}
}

func onboardingStepStatusToProto(s string) gen.OnboardingStepStatus {
	switch s {
	case "pending":
		return gen.OnboardingStepStatus_ONBOARDING_STEP_STATUS_PENDING
	case "completed":
		return gen.OnboardingStepStatus_ONBOARDING_STEP_STATUS_COMPLETED
	case "skipped":
		return gen.OnboardingStepStatus_ONBOARDING_STEP_STATUS_SKIPPED
	}
	return gen.OnboardingStepStatus_ONBOARDING_STEP_STATUS_UNSPECIFIED
}

func gdprToProto(r *business.GDPRRequest) *gen.GDPRRequest {
	if r == nil {
		return nil
	}
	out := &gen.GDPRRequest{
		Id:          r.ID,
		UserId:      r.UserID,
		Type:        gdprTypeToProto(r.Type),
		Status:      gdprStatusToProto(r.Status),
		DownloadUrl: r.DownloadURL,
	}
	if !r.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(r.CreatedAt)
	}
	if r.CompletedAt != nil {
		out.CompletedAt = timestamppb.New(*r.CompletedAt)
	}
	if r.ExpiresAt != nil {
		out.ExpiresAt = timestamppb.New(*r.ExpiresAt)
	}
	return out
}

func gdprTypeToProto(t business.GDPRRequestType) gen.GDPRRequestType {
	switch t {
	case business.GDPRExport:
		return gen.GDPRRequestType_GDPR_REQUEST_TYPE_EXPORT
	case business.GDPRDeletion:
		return gen.GDPRRequestType_GDPR_REQUEST_TYPE_DELETION
	}
	return gen.GDPRRequestType_GDPR_REQUEST_TYPE_UNSPECIFIED
}

func gdprStatusToProto(s business.GDPRRequestStatus) gen.GDPRRequestStatus {
	switch string(s) {
	case "pending":
		return gen.GDPRRequestStatus_GDPR_REQUEST_STATUS_PENDING
	case "processing":
		return gen.GDPRRequestStatus_GDPR_REQUEST_STATUS_PROCESSING
	case "completed":
		return gen.GDPRRequestStatus_GDPR_REQUEST_STATUS_COMPLETED
	case "failed":
		return gen.GDPRRequestStatus_GDPR_REQUEST_STATUS_FAILED
	}
	return gen.GDPRRequestStatus_GDPR_REQUEST_STATUS_UNSPECIFIED
}

func mfaDeviceToProto(d *business.MFADevice) *gen.MFADevice {
	if d == nil {
		return nil
	}
	out := &gen.MFADevice{
		Id:         d.ID,
		UserId:     d.UserID,
		DeviceType: mfaDeviceTypeToProto(d.DeviceType),
		Name:       d.Name,
	}
	if !d.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(d.CreatedAt)
	}
	if d.VerifiedAt != nil {
		out.VerifiedAt = timestamppb.New(*d.VerifiedAt)
	}
	if d.LastUsedAt != nil {
		out.LastUsedAt = timestamppb.New(*d.LastUsedAt)
	}
	return out
}

func mfaDeviceTypeToProto(s string) gen.MFADeviceType {
	switch s {
	case "totp":
		return gen.MFADeviceType_MFA_DEVICE_TYPE_TOTP
	case "webauthn":
		return gen.MFADeviceType_MFA_DEVICE_TYPE_WEBAUTHN
	}
	return gen.MFADeviceType_MFA_DEVICE_TYPE_UNSPECIFIED
}
