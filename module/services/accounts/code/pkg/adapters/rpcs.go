package adapters

import (
	"context"
	"errors"
	"strings"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"accounts/pkg/abuse"
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"
)

var service *business.Service

func WithService(s *business.Service) {
	service = s
}

func quotaStatusError(err error) error {
	if errors.Is(err, business.ErrEntitlementQuotaExceeded) {
		return status.Error(codes.ResourceExhausted, err.Error())
	}
	return err
}

func invitationStatusError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, business.ErrInvitationUnavailable):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, business.ErrInvitationEmailMismatch):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, business.ErrInvitationEmailUnverified):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, business.ErrInvitationExpired):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, business.ErrEntitlementQuotaExceeded):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.Internal, "invitation operation failed")
	}
}

func jobOperationStatusError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jobs.ErrInvalidCommand), errors.Is(err, jobs.ErrInvalidPageToken):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, jobs.ErrJobNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, jobs.ErrReplayNotAllowed):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, jobs.ErrIdempotencyConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, business.ErrJobOperationsUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, "job operation failed")
	}
}

func privacyStatusError(err error) error {
	if errors.Is(err, business.ErrPrivacyWorkflowUnavailable) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return err
}

func abuseStatusError(err error) error {
	if errors.Is(err, abuse.ErrChallengeRejected) {
		return status.Error(codes.PermissionDenied, "challenge verification failed")
	}
	return err
}

// userDataIdentity converts an already-authenticated authorization decision
// into the database identity used by user-scoped operations. Self-service uses
// RLS; cross-user access requires platform administration and uses the named
// control-plane role.
func userDataIdentity(ctx context.Context, actorID, targetID string) (business.Identity, error) {
	if actorID == targetID {
		return business.Identity{UserID: actorID}, nil
	}
	if err := requirePlatformAdmin(ctx, actorID); err != nil {
		return business.Identity{}, err
	}
	return business.System(), nil
}

// ============================================================================
// UserService RPCs (on UserServer)
// ============================================================================

func (s *UserServer) GetSelf(ctx context.Context, _ *gen.GetSelfRequest) (*gen.GetSelfResponse, error) {
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	return service.GetSelf(ctx, userID)
}

func (s *UserServer) RegisterUser(ctx context.Context, req *gen.RegisterUserRequest) (*gen.RegisterUserResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	response, err := service.RegisterUser(ctx, req)
	return response, abuseStatusError(err)
}

func (s *UserServer) GetUser(ctx context.Context, req *gen.GetUserRequest) (*gen.User, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	var access business.Identity
	if targetID := req.GetUuid(); targetID != "" {
		access, err = userDataIdentity(ctx, actorID, targetID)
	} else {
		// Email is not an authorization identifier. Cross-account lookup by
		// email is therefore platform-admin-only; self-service uses the UUID
		// already present in the authenticated identity.
		err = requirePlatformAdmin(ctx, actorID)
		access = business.System()
	}
	if err != nil {
		return nil, err
	}
	return service.GetUser(ctx, access, req)
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
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// The target is the request's uuid (validated). The caller must be that user
	// (self-service profile edit) or a platform admin (the admin Users table) —
	// the same gate DeleteUser uses. Previously this ignored req.Uuid and updated
	// the caller, so an admin could never edit another user.
	access, err := userDataIdentity(ctx, actorID, req.Uuid)
	if err != nil {
		return nil, err
	}
	if err := requireScope(ctx, "users:write"); err != nil {
		return nil, err
	}
	return service.UpdateUser(ctx, actorID, access, req.Uuid, req)
}

func (s *UserServer) DeleteUser(ctx context.Context, req *gen.GetUserRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	targetID := req.GetUuid()
	access := business.Identity{}
	if targetID != "" {
		access, err = userDataIdentity(ctx, actorID, targetID)
	} else {
		// Deleting by email first resolves a private identifier, so it is an
		// administrative operation from the start.
		if err = requirePlatformAdmin(ctx, actorID); err == nil {
			var target *gen.User
			target, err = service.GetUser(ctx, business.System(), req)
			if err == nil {
				targetID = target.Uuid
				access = business.System()
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if err := requireScope(ctx, "users:write"); err != nil {
		return nil, err
	}
	if err := service.DeleteUser(ctx, actorID, access, &gen.GetUserRequest{
		Identifier: &gen.GetUserRequest_Uuid{Uuid: targetID},
	}); err != nil {
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
	access, err := userDataIdentity(ctx, actorID, req.UserUuid)
	if err != nil {
		return nil, err
	}
	return service.AddIdentity(ctx, actorID, access, req)
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
	access, err := userDataIdentity(ctx, actorID, req.UserUuid)
	if err != nil {
		return nil, err
	}
	return service.ListUserIdentities(ctx, access, req)
}

// ============================================================================
// OrganizationService RPCs (on OrgServer)
// ============================================================================

func (s *OrgServer) CreateOrganization(ctx context.Context, req *gen.CreateOrganizationRequest) (*gen.CreateOrganizationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
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
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	return service.ListOrganizations(ctx, userID)
}

func (s *OrgServer) AddMember(ctx context.Context, req *gen.AddOrgMemberRequest) (*emptypb.Empty, error) {
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
	if err := service.AddOrgMember(ctx, actorID, req); err != nil {
		return nil, quotaStatusError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *OrgServer) RemoveMember(ctx context.Context, req *gen.RemoveOrgMemberRequest) (*emptypb.Empty, error) {
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
	orgID, err := requireTeamAdmin(ctx, actorID, req.TeamId)
	if err != nil {
		return nil, err
	}
	// Stamp team→org cache so Service.AddTeamMember skips the dup
	// WithControlPlane lookup. 4 transactions/request → 3.
	ctx = business.WithCachedTeamOrgID(ctx, req.TeamId, orgID)
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
	orgID, err := requireTeamAdmin(ctx, actorID, req.TeamId)
	if err != nil {
		return nil, err
	}
	ctx = business.WithCachedTeamOrgID(ctx, req.TeamId, orgID)
	if err := service.RemoveTeamMember(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *TeamServer) ListMembers(ctx context.Context, req *gen.ListTeamMembersRequest) (*gen.ListTeamMembersResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	orgID, err := requireTeamMember(ctx, actorID, req.TeamId)
	if err != nil {
		return nil, err
	}
	ctx = business.WithCachedTeamOrgID(ctx, req.TeamId, orgID)
	return service.ListTeamMembers(ctx, req)
}

func (s *TeamServer) UpdateTeam(ctx context.Context, req *gen.UpdateTeamRequest) (*gen.UpdateTeamResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	orgID, err := requireTeamAdmin(ctx, actorID, req.TeamId)
	if err != nil {
		return nil, err
	}
	ctx = business.WithCachedTeamOrgID(ctx, req.TeamId, orgID)
	return service.UpdateTeam(ctx, actorID, req)
}

func (s *TeamServer) DeleteTeam(ctx context.Context, req *gen.DeleteTeamRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	orgID, err := requireTeamAdmin(ctx, actorID, req.TeamId)
	if err != nil {
		return nil, err
	}
	ctx = business.WithCachedTeamOrgID(ctx, req.TeamId, orgID)
	if err := service.DeleteTeam(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
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

func (s *PermServer) ListRoleAssignments(ctx context.Context, req *gen.ListRoleAssignmentsRequest) (*gen.ListRoleAssignmentsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.OrgId); err != nil {
		// Platform admins can read any org's assignments for support
		// ops. Fall through to that check before refusing.
		if !IsPermissionDenied(err) {
			return nil, err
		}
		if paErr := requirePlatformAdmin(ctx, actorID); paErr != nil {
			return nil, err
		}
	}
	return service.ListRoleAssignments(ctx, req)
}

func (s *PermServer) CheckPermission(ctx context.Context, req *gen.CheckPermissionRequest) (*gen.CheckPermissionResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	// CheckPermission is an authorization decision oracle called by
	// internal services (auth-sidecar middleware). Anyone with access
	// to the api's gRPC port could otherwise probe "is user X allowed
	// to do Y?" — an information-disclosure risk especially in
	// stand-alone dev or any deploy where the network boundary isn't
	// strict.
	//
	// Defense: require either a JWT (the standard auth path — the
	// JWT proves the caller IS user X and is asking about themselves)
	// or a shared secret (X-Codefly-Internal-Token) that the auth-
	// sidecar carries. Production deploys set CODEFLY_INTERNAL_TOKEN
	// in both the api and sidecar configs.
	if err := requireInternalOrAuth(ctx); err != nil {
		return nil, err
	}
	return service.CheckPermission(ctx, req)
}

// CheckAccess is the hierarchical + per-record decision oracle (#178). Same
// trust boundary as CheckPermission — internal callers or a self-referential
// JWT — since it discloses "may subject X act on record Y."
func (s *PermServer) CheckAccess(ctx context.Context, req *gen.CheckAccessRequest) (*gen.CheckAccessResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalOrAuth(ctx); err != nil {
		return nil, err
	}
	return service.CheckAccess(ctx, req)
}

// The scope-tree and share management RPCs below are deliberately org-admin
// scoped (requireRoleScope). The starter has no per-record ownership model, so
// "the owner may share their own record" cannot be expressed yet; admin-only is
// the fail-closed v1 policy (RFC-0002 open question). Widen this to record
// owners once an ownership primitive exists.
func (s *PermServer) RegisterScopeNode(ctx context.Context, req *gen.RegisterScopeNodeRequest) (*gen.RegisterScopeNodeResponse, error) {
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
	return service.RegisterScopeNode(ctx, actorID, req)
}

func (s *PermServer) GrantScope(ctx context.Context, req *gen.GrantScopeRequest) (*gen.GrantScopeResponse, error) {
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
	return service.GrantScope(ctx, actorID, req)
}

func (s *PermServer) RevokeScope(ctx context.Context, req *gen.RevokeScopeRequest) (*emptypb.Empty, error) {
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
	if err := service.RevokeScope(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *PermServer) ShareRecord(ctx context.Context, req *gen.ShareRecordRequest) (*gen.ShareRecordResponse, error) {
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
	return service.ShareRecord(ctx, actorID, req)
}

func (s *PermServer) RevokeShare(ctx context.Context, req *gen.RevokeShareRequest) (*emptypb.Empty, error) {
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
	if err := service.RevokeShare(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *PermServer) ListShares(ctx context.Context, req *gen.ListSharesRequest) (*gen.ListSharesResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// A record's share list is its ACL; org-admin only, so a plain member can't
	// enumerate who a record they cannot see is shared with.
	if err := requireRoleScope(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	return service.ListShares(ctx, req)
}

// ============================================================================
// IdentityService RPCs (on IdentServer)
// ============================================================================

func (s *IdentServer) ResolveIdentity(ctx context.Context, req *gen.ResolveIdentityRequest) (*gen.ResolveIdentityResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalOrAuth(ctx); err != nil {
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
	if err := requireScope(ctx, "api_keys:write"); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.OrganizationId); err != nil {
		return nil, err
	}
	response, err := service.CreateAPIKey(ctx, actorID, req)
	return response, quotaStatusError(err)
}

func (s *APIKeyServer) ListAPIKeys(ctx context.Context, req *gen.ListAPIKeysRequest) (*gen.ListAPIKeysResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireScope(ctx, "api_keys:read"); err != nil {
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
	if err := requireScope(ctx, "api_keys:write"); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// The request now carries the owning org (the old TODO): an ORG admin may
	// revoke their org's keys; the business layer verifies the key actually
	// belongs to that org before revoking (no cross-org revocation by id).
	if err := requireOrgAdmin(ctx, actorID, req.GetOrganizationId()); err != nil {
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
	if err := requireInternalOrAuth(ctx); err != nil {
		return nil, err
	}
	return service.ValidateAPIKey(ctx, req.Key)
}

// ============================================================================
// AuthService RPCs (on AuthServer)
// ============================================================================

func (s *AuthServer) Authenticate(ctx context.Context, req *gen.AuthenticateRequest) (*gen.AuthenticateResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	resp, err := service.Authenticate(ctx, req)
	if err != nil {
		return nil, authenticateStatusError(ctx, err)
	}
	return resp, nil
}

// authenticateOracleErrors are the credential- and identity-resolution sentinels
// Authenticate can surface. Every one collapses to an identical generic response
// so an unauthenticated caller cannot tell "no account" from "inactive" from
// "not invited" — the enumeration oracle that accounts/pkg/auth/errors.go warns
// against ("Never leak these strings ... produce a generic 'invalid
// credentials'"). ErrGroupNotAllowed is deliberately excluded: it is a
// post-verification authorization outcome the frontend renders distinctly.
var authenticateOracleErrors = []error{
	auth.ErrMissingClaims,
	auth.ErrMissingSubject,
	auth.ErrMissingEmail,
	auth.ErrTokenExpired,
	auth.ErrTokenSignature,
	auth.ErrTokenMalformed,
	auth.ErrTokenWrongIssuer,
	auth.ErrTokenWrongAudience,
	auth.ErrTokenAlgForbidden,
	auth.ErrTokenReplay,
	auth.ErrTokenRevoked,
	auth.ErrDevelopmentAuthDisabled,
	auth.ErrInvalidOAuthRequest,
	auth.ErrInvalidOAuthState,
	auth.ErrActorChainTooDeep,
	auth.ErrActorSubjectMissing,
	auth.ErrUnknownIdentity,
	auth.ErrNoAccount,
	auth.ErrAccountInactive,
	auth.ErrSignupNotAllowed,
	auth.ErrOrgRequired,
	auth.ErrBootstrapClaimed,
	auth.ErrSsoEmailDomainNotAllowed,
	auth.ErrSsoProvisioningDisabled,
	auth.ErrSsoProvisioningMisconfigured,
	// Surfaced by the resolver on an SSO org-bound login when the asserted org
	// exists but the identity holds no membership. Distinct at SwitchOrganization
	// (an authenticated caller), but at the login boundary it is an org-state
	// oracle and must collapse like every other resolution failure.
	auth.ErrOrganizationAccessDenied,
	business.ErrInvitationUnavailable,
	business.ErrInvitationEmailMismatch,
	business.ErrInvitationEmailUnverified,
	business.ErrInvitationExpired,
}

// exposeAuthErrorDetail lets Authenticate return the underlying failure reason
// in the client-facing message instead of the generic one. It is enabled only
// for a local development environment (see SetExposeAuthErrorDetail) so a
// developer can see why a login failed; it defaults to false and every deployed
// environment leaves it false, keeping the enumeration oracle closed (#208).
var exposeAuthErrorDetail bool

// SetExposeAuthErrorDetail toggles verbose Authenticate error messages. Wire it
// from codefly.IsLocal() only: it must never be true in a deployed environment.
func SetExposeAuthErrorDetail(v bool) { exposeAuthErrorDetail = v }

// authenticateStatusError maps an Authenticate failure to the error the caller
// is allowed to see. Credential- and identity-resolution failures collapse to a
// single Unauthenticated "invalid credentials"; the detailed sentinel is logged
// for audit but never returned. Two deliberate exceptions keep a distinct code:
// ErrGroupNotAllowed (PermissionDenied, so the frontend can render "access not
// granted") and ErrJWKSUnavailable (Unavailable, a retryable operator-side
// outage rather than a credential failure — but still generic, never the
// verbatim sentinel). Anything else is a genuine server-side failure and passes
// through unchanged.
//
// The status code is identical in every environment so clients behave the same;
// only the human-readable message carries the underlying reason, and only in
// local development (exposeAuthErrorDetail).
func authenticateStatusError(ctx context.Context, err error) error {
	if errors.Is(err, auth.ErrGroupNotAllowed) {
		return status.Error(codes.PermissionDenied, "access not granted")
	}
	if errors.Is(err, auth.ErrJWKSUnavailable) {
		wool.Get(ctx).In("Authenticate").Warn("authentication key set unavailable", wool.ErrField(err))
		return status.Error(codes.Unavailable, clientAuthMessage(err, "authentication temporarily unavailable"))
	}
	for _, sentinel := range authenticateOracleErrors {
		if errors.Is(err, sentinel) {
			wool.Get(ctx).In("Authenticate").Warn("authentication rejected", wool.ErrField(err))
			return status.Error(codes.Unauthenticated, clientAuthMessage(err, "invalid credentials"))
		}
	}
	return err
}

// clientAuthMessage returns the verbatim failure reason in local development and
// the generic message everywhere else. It fails closed: an unset flag (the
// zero value, i.e. every deployed environment) yields the generic string.
func clientAuthMessage(err error, generic string) string {
	if exposeAuthErrorDetail {
		return err.Error()
	}
	return generic
}

func (s *AuthServer) CompleteMFAChallenge(ctx context.Context, req *gen.CompleteMFAChallengeRequest) (*gen.CompleteMFAChallengeResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	resp, err := service.CompleteMFAChallenge(ctx, req.MfaToken, req.Code)
	if errors.Is(err, business.ErrMFAChallengeRejected) {
		return nil, status.Error(codes.Unauthenticated, "MFA challenge rejected")
	}
	return resp, err
}

func (s *AuthServer) BeginWebAuthnMFAChallenge(ctx context.Context, req *gen.BeginWebAuthnMFAChallengeRequest) (*gen.BeginWebAuthnMFAChallengeResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	ceremonyToken, optionsJSON, err := service.BeginWebAuthnMFAChallenge(ctx, req.MfaToken)
	if errors.Is(err, business.ErrMFAChallengeRejected) {
		return nil, status.Error(codes.Unauthenticated, "MFA challenge rejected")
	}
	if err != nil {
		return nil, err
	}
	return &gen.BeginWebAuthnMFAChallengeResponse{
		CeremonyToken:        ceremonyToken,
		PublicKeyOptionsJson: optionsJSON,
	}, nil
}

func (s *AuthServer) CompleteWebAuthnMFAChallenge(ctx context.Context, req *gen.CompleteWebAuthnMFAChallengeRequest) (*gen.CompleteMFAChallengeResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	resp, err := service.CompleteWebAuthnMFAChallenge(ctx, req.MfaToken, req.CeremonyToken, req.CredentialResponseJson)
	if errors.Is(err, business.ErrMFAChallengeRejected) {
		return nil, status.Error(codes.Unauthenticated, "MFA challenge rejected")
	}
	return resp, err
}

func (s *AuthServer) RefreshToken(ctx context.Context, req *gen.RefreshTokenRequest) (*gen.RefreshTokenResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	return service.RefreshToken(ctx, req)
}

func (s *AuthServer) SwitchOrganization(ctx context.Context, req *gen.SwitchOrganizationRequest) (*gen.SwitchOrganizationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, ok := auth.VerifiedSessionID(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "verified device session not found")
	}
	resp, err := service.SwitchOrganization(ctx, userID, sessionID, req)
	switch {
	case errors.Is(err, auth.ErrOrganizationAccessDenied):
		return nil, status.Error(codes.PermissionDenied, "organization membership required")
	case errors.Is(err, auth.ErrSessionUnavailable):
		return nil, status.Error(codes.Unauthenticated, "device session is no longer active")
	default:
		return resp, err
	}
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

	q := business.AuditQuery{
		OrgID:      req.OrgId,
		ActorID:    req.ActorId,
		EventType:  req.EventType,
		Category:   req.Category,
		Resource:   req.Resource,
		ResourceID: req.ResourceId,
		PageSize:   req.PageSize,
		PageToken:  req.PageToken,
	}
	if len(req.PayloadContains) > 0 {
		q.PayloadContains = make(map[string]any, len(req.PayloadContains))
		for k, v := range req.PayloadContains {
			q.PayloadContains[k] = v
		}
	}
	if req.From != nil {
		t := req.From.AsTime()
		q.From = &t
	}
	if req.To != nil {
		t := req.To.AsTime()
		q.To = &t
	}

	entries, nextToken, totalCount, err := service.QueryAuditLog(ctx, q)
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

func (s *AuditServer) AggregateAuditLog(ctx context.Context, req *gen.AggregateAuditLogRequest) (*gen.AggregateAuditLogResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Same read-through-membership authorization as QueryAuditLog.
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

	q := business.AuditQuery{
		OrgID:     req.OrgId,
		ActorID:   req.ActorId,
		EventType: req.EventType,
		Category:  req.Category,
		Resource:  req.Resource,
	}
	if req.From != nil {
		t := req.From.AsTime()
		q.From = &t
	}
	if req.To != nil {
		t := req.To.AsTime()
		q.To = &t
	}

	buckets, err := service.AggregateAuditLog(ctx, q, req.GroupBy, req.Bucket)
	if err != nil {
		return nil, err
	}
	out := make([]*gen.AuditAggregateBucket, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, &gen.AuditAggregateBucket{Key: b.Key, Count: b.Count})
	}
	return &gen.AggregateAuditLogResponse{Buckets: out}, nil
}

func (s *AuditServer) ListAuditEventTypes(ctx context.Context, req *gen.ListAuditEventTypesRequest) (*gen.ListAuditEventTypesResponse, error) {
	if _, err := requireAuth(ctx); err != nil {
		return nil, err
	}
	defs := business.AuditEventCatalog()
	out := make([]*gen.AuditEventType, 0, len(defs))
	for _, d := range defs {
		out = append(out, &gen.AuditEventType{
			Name:        string(d.Type),
			Version:     int32(d.Version),
			Category:    string(d.Category),
			Owner:       d.Owner,
			Description: d.Description,
		})
	}
	return &gen.ListAuditEventTypesResponse{Types: out}, nil
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
	response, err := service.CreateInvitation(ctx, actorID, req)
	return response, quotaStatusError(err)
}

func (s *InvitationServer) InspectInvitation(ctx context.Context, req *gen.InspectInvitationRequest) (*gen.InvitationSummary, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	summary, err := service.InspectInvitation(ctx, req)
	if err != nil && !errors.Is(err, business.ErrInvitationUnavailable) {
		return nil, status.Error(codes.Unavailable, "invitation inspection unavailable")
	}
	return summary, invitationStatusError(err)
}

func (s *InvitationServer) InspectInvitationById(ctx context.Context, req *gen.InspectInvitationByIdRequest) (*gen.InvitationSummary, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	summary, err := service.InspectInvitationByID(ctx, userID, req)
	return summary, invitationStatusError(err)
}

func (s *InvitationServer) AcceptInvitation(ctx context.Context, req *gen.AcceptInvitationRequest) (*gen.AcceptInvitationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	response, err := service.AcceptInvitation(ctx, userID, req)
	return response, invitationStatusError(err)
}

func (s *InvitationServer) ResendInvitation(ctx context.Context, req *gen.ResendInvitationRequest) (*gen.Invitation, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	var orgID string
	if err := service.Store().WithControlPlane(ctx, func(ctx context.Context) error {
		var resolveErr error
		orgID, resolveErr = service.Store().GetInvitationOrgID(ctx, req.Id)
		return resolveErr
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "cannot resolve invitation organization: %v", err)
	}
	if orgID == "" {
		return nil, status.Error(codes.NotFound, "invitation not found")
	}
	if err := requireOrgAdmin(ctx, actorID, orgID); err != nil {
		return nil, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	idempotencyKey := ""
	if values := md.Get("idempotency-key"); len(values) > 0 {
		idempotencyKey = values[0]
	}
	if idempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "Idempotency-Key header required")
	}
	return service.ResendInvitation(ctx, actorID, req, idempotencyKey)
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
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// The request contains only the invitation id. Resolve its organization
	// under the narrow bypass, then authorize the actor before the business
	// update uses bypass. Without this binding any authenticated user could
	// revoke a foreign tenant's invitation by UUID.
	var orgID string
	if err := service.Store().WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		orgID, err = service.Store().GetInvitationOrgID(ctx, req.Id)
		return err
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "cannot resolve invitation organization: %v", err)
	}
	if orgID == "" {
		return nil, status.Error(codes.NotFound, "invitation not found")
	}
	if err := requireOrgAdmin(ctx, userID, orgID); err != nil {
		return nil, err
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
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	return service.SearchUsers(ctx, actorID, req)
}

func (s *PlatformAdminServer) SuspendUser(ctx context.Context, req *gen.SuspendUserRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
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
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
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
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
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
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	return service.ListActiveSessions(ctx, actorID, req)
}

func (s *PlatformAdminServer) RevokeSession(ctx context.Context, req *gen.RevokeSessionRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := service.RevokeSession(ctx, actorID, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *PlatformAdminServer) GetOrgEntitlements(ctx context.Context, req *gen.GetOrgEntitlementsRequest) (*gen.GetOrgEntitlementsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	// Any member of the org can read the org's entitlements (powers
	// the org-side /admin/billing usage card). Platform admins
	// implicitly satisfy this since they're members. Bare-JWT
	// callers from outside the org get PermissionDenied.
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.OrgId); err != nil {
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
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Override is privileged — only platform admins. Org admins
	// can't bump their own caps; that's the whole point of the
	// override mechanism.
	if err := requirePlatformAdmin(ctx, actorID); err != nil {
		return nil, err
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
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
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
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
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
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	return service.ListPlatformAdmins(ctx, actorID)
}

func (s *PlatformAdminServer) ListFeatureFlags(ctx context.Context, _ *gen.ListFeatureFlagsRequest) (*gen.ListFeatureFlagsResponse, error) {
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	return service.ListFeatureFlags(ctx, actorID)
}

// UpsertFeatureFlag remains implemented only because saas.accounts.v1 is a
// published stable contract. Authorization is repeated here for direct-server
// callers as well as enforced by the transport policy interceptor. No business
// or store mutation path exists.
func (s *PlatformAdminServer) UpsertFeatureFlag(ctx context.Context, req *gen.UpsertFeatureFlagRequest) (*gen.UpsertFeatureFlagResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	role, err := service.Store().GetPlatformRole(ctx, actorID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cannot resolve platform role: %v", err)
	}
	if role != "super_admin" {
		return nil, status.Error(codes.PermissionDenied, "requires platform super-admin role")
	}
	return nil, status.Error(codes.FailedPrecondition, "legacy feature-flag inventory is read-only; use feature-flags@1")
}

func (s *PlatformAdminServer) GetJobOperations(ctx context.Context, req *jobsv1.GetJobOperationsRequest) (*jobsv1.GetJobOperationsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	response, err := service.GetJobOperations(ctx, actorID, req)
	return response, jobOperationStatusError(err)
}

func (s *PlatformAdminServer) ListJobs(ctx context.Context, req *jobsv1.ListJobsRequest) (*jobsv1.ListJobsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	response, err := service.ListJobs(ctx, actorID, req)
	return response, jobOperationStatusError(err)
}

func (s *PlatformAdminServer) GetJob(ctx context.Context, req *jobsv1.GetJobRequest) (*jobsv1.GetJobResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	response, err := service.GetJob(ctx, actorID, req)
	return response, jobOperationStatusError(err)
}

func (s *PlatformAdminServer) ReplayJob(ctx context.Context, req *jobsv1.ReplayJobRequest) (*jobsv1.ReplayJobResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRecentMFA(ctx); err != nil {
		return nil, err
	}
	response, err := service.ReplayJob(ctx, actorID, req)
	return response, jobOperationStatusError(err)
}
