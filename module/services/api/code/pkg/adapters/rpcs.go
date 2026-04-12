package adapters

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/codefly-dev/core/wool"

	"backend/pkg/business"
	"backend/pkg/gen"
	"backend/pkg/infra"
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
	return service.GetUser(ctx, req)
}

func (s *UserServer) ListUsers(ctx context.Context, req *gen.ListUsersRequest) (*gen.ListUsersResponse, error) {
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
	return service.UpdateUser(ctx, userID, req)
}

func (s *UserServer) DeleteUser(ctx context.Context, req *gen.GetUserRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("DeleteUser")
	w.GRPC().Inject()
	userID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	if err := service.DeleteUser(ctx, userID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *UserServer) AddIdentity(ctx context.Context, req *gen.AddIdentityRequest) (*gen.UserIdentity, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.AddIdentity(ctx, req)
}

func (s *UserServer) FindUserByIdentity(ctx context.Context, req *gen.FindUserByIdentityRequest) (*gen.User, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.FindUserByIdentity(ctx, req)
}

func (s *UserServer) ListUserIdentities(ctx context.Context, req *gen.ListUserIdentitiesRequest) (*gen.ListUserIdentitiesResponse, error) {
	if err := Validate(req); err != nil {
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
	return service.ListOrgMembers(ctx, req)
}

// ============================================================================
// TeamService RPCs (on TeamServer)
// ============================================================================

func (s *TeamServer) CreateTeam(ctx context.Context, req *gen.CreateTeamRequest) (*gen.CreateTeamResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.CreateTeam(ctx, req)
}

func (s *TeamServer) ListTeams(ctx context.Context, req *gen.ListTeamsRequest) (*gen.ListTeamsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.ListTeams(ctx, req)
}

func (s *TeamServer) AddMember(ctx context.Context, req *gen.AddTeamMemberRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("AddTeamMember")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
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
	w := wool.Get(ctx).In("RemoveTeamMember")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
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

func (s *PermServer) CreateRole(ctx context.Context, req *gen.CreateRoleRequest) (*gen.CreateRoleResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.CreateRole(ctx, req)
}

func (s *PermServer) ListRoles(ctx context.Context, req *gen.ListRolesRequest) (*gen.ListRolesResponse, error) {
	return service.ListRoles(ctx, req)
}

func (s *PermServer) DeleteRole(ctx context.Context, req *gen.DeleteRoleRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("DeleteRole")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
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
	return service.AssignRole(ctx, req)
}

func (s *PermServer) RevokeRole(ctx context.Context, req *gen.RevokeRoleRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	w := wool.Get(ctx).In("RevokeRole")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
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
	return service.ListAPIKeys(ctx, req)
}

func (s *APIKeyServer) RevokeAPIKey(ctx context.Context, req *gen.RevokeAPIKeyRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := service.RevokeAPIKey(ctx, req); err != nil {
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

func (s *AuthServer) Logout(ctx context.Context, req *gen.LogoutRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := service.Logout(ctx, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
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
	w := wool.Get(ctx).In("CreateInvitation")
	w.GRPC().Inject()
	userID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
	}
	return service.CreateInvitation(ctx, userID, req)
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
	return nil, status.Error(codes.Unimplemented, "GetOrgEntitlements not yet implemented")
}

func (s *PlatformAdminServer) OverrideEntitlement(ctx context.Context, req *gen.OverrideEntitlementRequest) (*gen.OverrideEntitlementResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return nil, status.Error(codes.Unimplemented, "OverrideEntitlement not yet implemented")
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
