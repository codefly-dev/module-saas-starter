package adapters

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/codefly-dev/core/wool"

	"api/pkg/business"
	"api/pkg/gen"
	"api/pkg/infra"
)

var service *business.Service

func WithService(s *business.Service) {
	service = s
}

// ============================================================================
// UserService RPCs (on UserServer)
// ============================================================================

func (s *UserServer) GetSelf(ctx context.Context, _ *gen.GetSelfRequest) (*gen.GetSelfResponse, error) {
	w := wool.Get(ctx).In("GetSelf")
	w.GRPC().Inject()
	userID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found in headers")
	}
	return service.GetSelf(ctx, userID)
}

func (s *UserServer) RegisterUser(ctx context.Context, req *gen.RegisterUserRequest) (*gen.RegisterUserResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.RegisterUser(ctx, req)
}

func (s *UserServer) GetUser(ctx context.Context, req *gen.GetUserRequest) (*gen.User, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// GetUser is callable on self (any user) or on any user by a platform
	// admin. Resolve the target's id from the oneof uuid/email before the
	// self check — otherwise an email lookup can't be authorized.
	u, err := service.GetUser(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := requireSelfOrPlatformAdmin(ctx, actorID, u.Uuid); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *UserServer) ListUsers(ctx context.Context, req *gen.ListUsersRequest) (*gen.ListUsersResponse, error) {
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// API-key path also needs `users:read`. JWT (interactive) callers
	// bypass via requireScope's empty-context fast path.
	if err := requireScope(ctx, "users:read"); err != nil {
		return nil, err
	}
	// Platform-wide user listing is a platform-admin-only operation. Scoped
	// per-org listings should go through OrgServer.ListMembers, not here.
	if err := requirePlatformAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	return service.ListUsers(ctx, req)
}

func (s *UserServer) UpdateUser(ctx context.Context, req *gen.UpdateUserRequest) (*gen.User, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("UpdateUser")
	w.GRPC().Inject()
	userID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	if err := requireScope(ctx, "users:write"); err != nil {
		return nil, err
	}
	return service.UpdateUser(ctx, userID, req)
}

func (s *UserServer) DeleteUser(ctx context.Context, req *gen.GetUserRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Resolve the target first so we can authorize against the real uuid
	// (GetUserRequest is a uuid/email oneof; email lookups must be authz'd
	// against the resolved id, not the email string).
	target, err := service.GetUser(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := requireSelfOrPlatformAdmin(ctx, actorID, target.Uuid); err != nil {
		return nil, err
	}
	if err := requireScope(ctx, "users:write"); err != nil {
		return nil, err
	}
	if err := service.DeleteUser(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// AddIdentity links a provider identity (e.g. WorkOS sub) to an existing
// user. Only the user themselves OR a platform admin can do this — without
// the gate, any caller knowing a target user UUID could attach an identity
// they control and take over the account.
func (s *UserServer) AddIdentity(ctx context.Context, req *gen.AddIdentityRequest) (*gen.UserIdentity, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireSelfOrPlatformAdmin(ctx, actorID, req.UserUuid); err != nil {
		return nil, err
	}
	return service.AddIdentity(ctx, req)
}

// FindUserByIdentity resolves (provider, provider_id) → user. This is
// an internal admin lookup — exposing it to authenticated end users
// would let any caller enumerate "is account X registered with provider
// Y?". Restricted to platform admins.
func (s *UserServer) FindUserByIdentity(ctx context.Context, req *gen.FindUserByIdentityRequest) (*gen.User, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requirePlatformAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	return service.FindUserByIdentity(ctx, req)
}

// ListUserIdentities returns all linked providers for a user. Same
// gating as AddIdentity — the user themselves or a platform admin.
// Without this, any caller could enumerate the auth providers any user
// has connected (a subtle privacy leak useful for targeted phishing).
func (s *UserServer) ListUserIdentities(ctx context.Context, req *gen.ListUserIdentitiesRequest) (*gen.ListUserIdentitiesResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireSelfOrPlatformAdmin(ctx, actorID, req.UserUuid); err != nil {
		return nil, err
	}
	return service.ListUserIdentities(ctx, req)
}

// ============================================================================
// OrganizationService RPCs (on OrgServer)
// ============================================================================

func (s *OrgServer) CreateOrganization(ctx context.Context, req *gen.CreateOrganizationRequest) (*gen.CreateOrganizationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("CreateOrganization")
	w.GRPC().Inject()
	userID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	return service.CreateOrganization(ctx, userID, req)
}

func (s *OrgServer) GetOrganization(ctx context.Context, req *gen.GetOrganizationRequest) (*gen.Organization, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Cross-org read leak fix: only members of the target org (or platform
	// admins) can view its data.
	if err := requireOrgMember(ctx, actorID, req.Id); err != nil {
		if !IsPermissionDenied(err) {
			return nil, err
		}
		if paErr := requirePlatformAdmin(ctx, actorID); paErr != nil {
			return nil, err
		}
	}
	return service.GetOrganization(ctx, req)
}

func (s *OrgServer) ListOrganizations(ctx context.Context, _ *gen.ListOrganizationsRequest) (*gen.ListOrganizationsResponse, error) {
	w := wool.Get(ctx).In("ListOrganizations")
	w.GRPC().Inject()
	userID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	return service.ListOrganizations(ctx, userID)
}

func (s *OrgServer) AddMember(ctx context.Context, req *gen.AddOrgMemberRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("AddMember")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	if err := service.AddOrgMember(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *OrgServer) RemoveMember(ctx context.Context, req *gen.RemoveOrgMemberRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("RemoveMember")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	if err := service.RemoveOrgMember(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *OrgServer) ListMembers(ctx context.Context, req *gen.ListOrgMembersRequest) (*gen.ListOrgMembersResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	return service.ListOrgMembers(ctx, req)
}

// ============================================================================
// TeamService RPCs (on TeamServer)
// ============================================================================

func (s *TeamServer) CreateTeam(ctx context.Context, req *gen.CreateTeamRequest) (*gen.CreateTeamResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Only org admins/owners (or platform super_admin) can create teams.
	if err := requireOrgAdmin(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	return service.CreateTeam(ctx, actorID, req)
}

func (s *TeamServer) ListTeams(ctx context.Context, req *gen.ListTeamsRequest) (*gen.ListTeamsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	return service.ListTeams(ctx, req)
}

func (s *TeamServer) AddMember(ctx context.Context, req *gen.AddTeamMemberRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireTeamAdmin(ctx, actorID, req.TeamId); err != nil {
		return nil, err
	}
	if err := service.AddTeamMember(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *TeamServer) RemoveMember(ctx context.Context, req *gen.RemoveTeamMemberRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// A user removing themselves from a team is allowed; otherwise require
	// team admin.
	if req.UserId != actorID {
		if err := requireTeamAdmin(ctx, actorID, req.TeamId); err != nil {
			return nil, err
		}
	}
	if err := service.RemoveTeamMember(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *TeamServer) ListMembers(ctx context.Context, req *gen.ListTeamMembersRequest) (*gen.ListTeamMembersResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.ListTeamMembers(ctx, req)
}

// ============================================================================
// PermissionService RPCs (on PermServer)
// ============================================================================

// requireRoleScope gates mutations on a role scope. Global roles (OrgId
// empty) require platform super_admin; org-scoped roles require org admin
// on the target org (or platform super_admin bypass).
func requireRoleScope(ctx context.Context, actorID, orgID string) error {
	if orgID == "" {
		return requirePlatformAdmin(ctx, actorID)
	}
	return requireOrgAdmin(ctx, actorID, orgID)
}

func (s *PermServer) CreateRole(ctx context.Context, req *gen.CreateRoleRequest) (*gen.CreateRoleResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRoleScope(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	return service.CreateRole(ctx, actorID, req)
}

func (s *PermServer) ListRoles(ctx context.Context, req *gen.ListRolesRequest) (*gen.ListRolesResponse, error) {
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Global roles are visible to any authed caller (they're the "standard
	// roles" menu). Org-scoped roles require org membership.
	if req.OrgId != "" {
		if err := requireOrgMember(ctx, actorID, req.OrgId); err != nil {
			return nil, err
		}
	}
	return service.ListRoles(ctx, req)
}

func (s *PermServer) DeleteRole(ctx context.Context, req *gen.DeleteRoleRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// DeleteRole only carries the role id — we can't resolve its org scope
	// cheaply here, so require platform admin as the safe default. Org-scoped
	// role deletion can be added once the proto is extended with org_id.
	// TODO(saas-starter): add org_id to DeleteRoleRequest + regen for finer
	// authz.
	if err := requirePlatformAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	if err := service.DeleteRole(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *PermServer) AssignRole(ctx context.Context, req *gen.AssignRoleRequest) (*gen.AssignRoleResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRoleScope(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	return service.AssignRole(ctx, req)
}

func (s *PermServer) RevokeRole(ctx context.Context, req *gen.RevokeRoleRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRoleScope(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	if err := service.RevokeRole(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *PermServer) CheckPermission(ctx context.Context, req *gen.CheckPermissionRequest) (*gen.CheckPermissionResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	// CheckPermission is an authorization decision gate called BY internal
	// services (auth-sidecar middleware). We don't require user auth on
	// the caller — the sidecar is trusted by network boundary. If the
	// endpoint is ever exposed publicly, this needs to be tightened.
	return service.CheckPermission(ctx, req)
}

// ============================================================================
// IdentityService RPCs (on IdentServer)
// ============================================================================

func (s *IdentServer) ResolveIdentity(ctx context.Context, req *gen.ResolveIdentityRequest) (*gen.ResolveIdentityResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.ResolveIdentity(ctx, req)
}

// ============================================================================
// APIKeyService RPCs (on APIKeyServer)
// ============================================================================

func (s *APIKeyServer) CreateAPIKey(ctx context.Context, req *gen.CreateAPIKeyRequest) (*gen.CreateAPIKeyResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("CreateAPIKey")
	w.GRPC().Inject()
	userID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	return service.CreateAPIKey(ctx, userID, req)
}

func (s *APIKeyServer) ListAPIKeys(ctx context.Context, req *gen.ListAPIKeysRequest) (*gen.ListAPIKeysResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.OrganizationId); err != nil {
		return nil, err
	}
	return service.ListAPIKeys(ctx, req)
}

func (s *APIKeyServer) RevokeAPIKey(ctx context.Context, req *gen.RevokeAPIKeyRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// RevokeAPIKeyRequest carries only Id — the owning org isn't on the
	// request. For now require platform_admin; a proper fix is to extend
	// the proto with organization_id and enforce requireOrgAdmin on that.
	// TODO(saas-starter): add org_id to RevokeAPIKeyRequest + regen.
	if err := requirePlatformAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	if err := service.RevokeAPIKey(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *APIKeyServer) ValidateAPIKey(ctx context.Context, req *gen.ValidateAPIKeyRequest) (*gen.ValidateAPIKeyResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.ValidateAPIKey(ctx, req.KeyHash)
}

// ============================================================================
// AuthService RPCs (on AuthServer)
// ============================================================================

func (s *AuthServer) Authenticate(ctx context.Context, req *gen.AuthenticateRequest) (*gen.AuthenticateResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.Authenticate(ctx, req)
}

func (s *AuthServer) RefreshToken(ctx context.Context, req *gen.RefreshTokenRequest) (*gen.RefreshTokenResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.RefreshToken(ctx, req)
}

// BeginOAuth issues the server-signed state token for the OAuth code
// flow. Public — no auth required (this IS the login). Returns
// FailedPrecondition if the operator hasn't wired the state signer.
func (s *AuthServer) BeginOAuth(ctx context.Context, req *gen.BeginOAuthRequest) (*gen.BeginOAuthResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	state, err := service.BeginOAuth(ctx, req.Provider, req.RedirectUri)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot mint oauth state: %v", err)
	}
	return &gen.BeginOAuthResponse{State: state}, nil
}

func (s *AuthServer) Logout(ctx context.Context, req *gen.LogoutRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	// Pull the caller's access token from the Authorization header so
	// the business layer can revoke its jti alongside the refresh
	// family. Empty when called from a non-authenticated context (e.g.
	// the FE only forwards the refresh, never the access).
	accessToken := bearerFromContext(ctx)
	if err := service.Logout(ctx, req, accessToken); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// bearerFromContext extracts the bearer token from the incoming
// Authorization header (gRPC metadata or Connect-mapped HTTP header).
// Returns "" when absent or malformed.
func bearerFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	v := md.Get("authorization")
	if len(v) == 0 {
		return ""
	}
	if t, ok := strings.CutPrefix(v[0], "Bearer "); ok {
		return t
	}
	return ""
}

func (s *AuthServer) GetJWKS(ctx context.Context, _ *emptypb.Empty) (*gen.JWKSResponse, error) {
	jwks, err := service.GetJWKS(ctx)
	if err != nil {
		return nil, err
	}
	return &gen.JWKSResponse{KeysJson: jwks}, nil
}

// ============================================================================
// AuditService RPCs (on AuditServer)
// ============================================================================

func (s *AuditServer) QueryAuditLog(ctx context.Context, req *gen.QueryAuditLogRequest) (*gen.QueryAuditLogResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Audit log is read-through-membership: org members see their own org's
	// audit trail; platform admins see anything. No org_id means platform
	// admin scope is required.
	if req.OrgId == "" {
		if err := requirePlatformAdmin(ctx, actorID); err != nil {
			return nil, err
		}
	} else {
		if err := requireOrgMember(ctx, actorID, req.OrgId); err != nil {
			if !IsPermissionDenied(err) {
				return nil, err
			}
			if paErr := requirePlatformAdmin(ctx, actorID); paErr != nil {
				return nil, err
			}
		}
	}

	var from, to *time.Time
	if req.From != nil {
		t := req.From.AsTime()
		from = &t
	}
	if req.To != nil {
		t := req.To.AsTime()
		to = &t
	}

	entries, nextToken, totalCount, err := service.QueryAuditLog(ctx,
		req.OrgId, req.ActorId, req.Action, req.Resource, req.ResourceId,
		from, to, req.PageSize, req.PageToken)
	if err != nil {
		return nil, err
	}

	var events []*gen.AuditEvent
	for _, e := range entries {
		events = append(events, infra.AuditEntryToProto(e))
	}

	return &gen.QueryAuditLogResponse{
		Events:        events,
		NextPageToken: nextToken,
		TotalCount:    totalCount,
	}, nil
}

// ============================================================================
// InvitationService RPCs (on InvitationServer)
// ============================================================================

func (s *InvitationServer) CreateInvitation(ctx context.Context, req *gen.CreateInvitationRequest) (*gen.CreateInvitationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Only org admins can invite — plain members shouldn't be able to
	// pull strangers into someone else's org.
	if err := requireOrgAdmin(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	return service.CreateInvitation(ctx, actorID, req)
}

func (s *InvitationServer) AcceptInvitation(ctx context.Context, req *gen.AcceptInvitationRequest) (*gen.AcceptInvitationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("AcceptInvitation")
	w.GRPC().Inject()
	userID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	return service.AcceptInvitation(ctx, userID, req)
}

func (s *InvitationServer) ListInvitations(ctx context.Context, req *gen.ListInvitationsRequest) (*gen.ListInvitationsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	return service.ListInvitations(ctx, req)
}

func (s *InvitationServer) RevokeInvitation(ctx context.Context, req *gen.RevokeInvitationRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("RevokeInvitation")
	w.GRPC().Inject()
	userID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	if err := service.RevokeInvitation(ctx, userID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ============================================================================
// PlatformAdminService RPCs (on PlatformAdminServer — requires platform_role)
// ============================================================================

func (s *PlatformAdminServer) SearchUsers(ctx context.Context, req *gen.SearchUsersRequest) (*gen.SearchUsersResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("SearchUsers")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	return service.SearchUsers(ctx, actorID, req)
}

func (s *PlatformAdminServer) SuspendUser(ctx context.Context, req *gen.SuspendUserRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("SuspendUser")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	if err := service.SuspendUser(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *PlatformAdminServer) UnsuspendUser(ctx context.Context, req *gen.UnsuspendUserRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("UnsuspendUser")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	if err := service.UnsuspendUser(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *PlatformAdminServer) ImpersonateUser(ctx context.Context, req *gen.ImpersonateUserRequest) (*gen.ImpersonateUserResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("ImpersonateUser")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	if err := requireMFA(ctx, actorID); err != nil {
		return nil, err
	}
	return service.ImpersonateUser(ctx, actorID, req)
}

func (s *PlatformAdminServer) ListActiveSessions(ctx context.Context, req *gen.ListActiveSessionsRequest) (*gen.ListActiveSessionsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("ListActiveSessions")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	return service.ListActiveSessions(ctx, actorID, req)
}

func (s *PlatformAdminServer) GetOrgEntitlements(ctx context.Context, req *gen.GetOrgEntitlementsRequest) (*gen.GetOrgEntitlementsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	view, err := service.GetOrgEntitlements(ctx, req.OrgId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &gen.GetOrgEntitlementsResponse{
		PlanName:     view.PlanName,
		Entitlements: make([]*gen.EntitlementInfo, 0, len(view.Entitlements)),
	}
	for _, e := range view.Entitlements {
		out.Entitlements = append(out.Entitlements, &gen.EntitlementInfo{
			Feature:     e.Feature,
			Limit:       e.Limit,
			Used:        e.Used,
			HasOverride: e.HasOverride,
		})
	}
	return out, nil
}

func (s *PlatformAdminServer) OverrideEntitlement(ctx context.Context, req *gen.OverrideEntitlementRequest) (*gen.OverrideEntitlementResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("OverrideEntitlement")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	id, err := service.OverrideEntitlement(ctx, actorID, req)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gen.OverrideEntitlementResponse{Id: id}, nil
}

func (s *PlatformAdminServer) GrantPlatformRole(ctx context.Context, req *gen.GrantPlatformRoleRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("GrantPlatformRole")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	if err := requireMFA(ctx, actorID); err != nil {
		return nil, err
	}
	if err := service.GrantPlatformRole(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *PlatformAdminServer) RevokePlatformRole(ctx context.Context, req *gen.RevokePlatformRoleRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("RevokePlatformRole")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	if err := requireMFA(ctx, actorID); err != nil {
		return nil, err
	}
	if err := service.RevokePlatformRole(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *PlatformAdminServer) ListPlatformAdmins(ctx context.Context, _ *gen.ListPlatformAdminsRequest) (*gen.ListPlatformAdminsResponse, error) {
	w := wool.Get(ctx).In("ListPlatformAdmins")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	return service.ListPlatformAdmins(ctx, actorID)
}

func (s *PlatformAdminServer) ListFeatureFlags(ctx context.Context, _ *gen.ListFeatureFlagsRequest) (*gen.ListFeatureFlagsResponse, error) {
	w := wool.Get(ctx).In("ListFeatureFlags")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	return service.ListFeatureFlags(ctx, actorID)
}

func (s *PlatformAdminServer) UpsertFeatureFlag(ctx context.Context, req *gen.UpsertFeatureFlagRequest) (*gen.UpsertFeatureFlagResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("UpsertFeatureFlag")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	return service.UpsertFeatureFlag(ctx, actorID, req)
}
