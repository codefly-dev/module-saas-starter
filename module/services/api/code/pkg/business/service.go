package business

import (
	"backend/pkg/gen"
	"context"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
)

type Service struct {
	store        Store
	hasher       KeyHasher
	tokenSigner  TokenSigner
	audit        AuditEmitter
	entitlements EntitlementChecker
	features     FeatureChecker
}

func NewService(store Store) (*Service, error) {
	return &Service{store: store}, nil
}

func (s *Service) SetHasher(h KeyHasher) {
	s.hasher = h
}

func (s *Service) SetTokenSigner(t TokenSigner) {
	s.tokenSigner = t
}

func (s *Service) SetAuditEmitter(a AuditEmitter) {
	s.audit = a
}

func (s *Service) SetEntitlementChecker(e EntitlementChecker) {
	s.entitlements = e
}

func (s *Service) SetFeatureChecker(f FeatureChecker) {
	s.features = f
}

func (s *Service) SetStore(store Store) {
	s.store = store
}

func (s *Service) Store() Store {
	return s.store
}

// RegisterUser creates a new user with identity and a default personal organization.
func (s *Service) RegisterUser(ctx context.Context, input *gen.RegisterUserRequest) (*gen.RegisterUserResponse, error) {
	w := wool.Get(ctx).In("RegisterUser")

	// Check if identity already exists
	if u, err := s.store.GetUserByIdentity(ctx, input.Identity); err != nil {
		return nil, w.Wrapf(err, "error checking existing user")
	} else if u != nil {
		return nil, w.NewError("user already exists with this identity")
	}

	userID := uuid.New().String()
	identityID := uuid.New().String()

	user := &gen.User{
		Uuid:         userID,
		PrimaryEmail: input.PrimaryEmail,
		Status:       gen.UserStatus_USER_STATUS_ACTIVE,
		Profile:      input.Profile,
	}

	identity := input.Identity
	identity.Uuid = identityID
	identity.UserUuid = userID
	if identity.ProviderEmail == "" {
		identity.ProviderEmail = input.PrimaryEmail
	}

	// RegisterUser already uses its own transaction for user+identity
	if err := s.store.RegisterUser(ctx, user, identity); err != nil {
		return nil, w.Wrapf(err, "cannot register user")
	}

	// Create a default personal organization
	orgID := uuid.New().String()
	org := &gen.Organization{
		Id:      orgID,
		Name:    "Personal",
		Slug:    "personal-" + userID[:8],
		OwnerId: userID,
	}
	if err := s.store.CreateOrganization(ctx, org); err != nil {
		return nil, w.Wrapf(err, "cannot create default organization")
	}

	// Assign admin role to user in their org
	roles, err := s.store.ListRoles(ctx, "")
	if err != nil {
		return nil, w.Wrapf(err, "cannot list roles")
	}
	for _, role := range roles {
		if role.Name == "admin" && role.BuiltIn {
			assignment := &gen.RoleAssignment{
				Id:          uuid.New().String(),
				SubjectId:   userID,
				SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
				RoleId:      role.Id,
				OrgId:       orgID,
			}
			if err := s.store.AssignRole(ctx, assignment); err != nil {
				return nil, w.Wrapf(err, "cannot assign admin role")
			}
			break
		}
	}

	s.emit(ctx, userID, "user", "user.registered", "user", userID, orgID)

	return &gen.RegisterUserResponse{User: user, Identity: identity}, nil
}

// CheckPermission checks if a subject has permission to perform an action on a resource.
func (s *Service) CheckPermission(ctx context.Context, req *gen.CheckPermissionRequest) (*gen.CheckPermissionResponse, error) {
	allowed, reason, err := s.store.CheckPermission(
		ctx, req.SubjectId, req.SubjectKind,
		req.Resource, req.Action, req.OrgId, req.Scope,
	)
	if err != nil {
		return nil, err
	}
	return &gen.CheckPermissionResponse{Allowed: allowed, Reason: reason}, nil
}

// ResolveIdentity maps an auth provider ID to internal user/org/roles.
func (s *Service) ResolveIdentity(ctx context.Context, req *gen.ResolveIdentityRequest) (*gen.ResolveIdentityResponse, error) {
	resolved, err := s.store.ResolveIdentity(ctx, req.Provider, req.ProviderId)
	if err != nil {
		return nil, err
	}
	return &gen.ResolveIdentityResponse{
		UserId:       resolved.UserID,
		OrgId:        resolved.OrgID,
		Roles:        resolved.Roles,
		Found:        resolved.Found,
		OrgRole:      resolved.OrgRole,
		PlatformRole: resolved.PlatformRole,
	}, nil
}

// CreateOrganization creates a new org with the requesting user as owner.
func (s *Service) CreateOrganization(ctx context.Context, ownerID string, req *gen.CreateOrganizationRequest) (*gen.CreateOrganizationResponse, error) {
	org := &gen.Organization{
		Id:      uuid.New().String(),
		Name:    req.Name,
		Slug:    req.Slug,
		OwnerId: ownerID,
	}
	if err := s.store.CreateOrganization(ctx, org); err != nil {
		return nil, err
	}
	s.emit(ctx, ownerID, "user", "org.created", "organization", org.Id, org.Id)
	return &gen.CreateOrganizationResponse{Organization: org}, nil
}

// CreateTeam creates a new team within an org.
func (s *Service) CreateTeam(ctx context.Context, req *gen.CreateTeamRequest) (*gen.CreateTeamResponse, error) {
	team := &gen.Team{
		Id:          uuid.New().String(),
		OrgId:       req.OrgId,
		Name:        req.Name,
		Description: req.Description,
	}
	if err := s.store.CreateTeam(ctx, team); err != nil {
		return nil, err
	}
	return &gen.CreateTeamResponse{Team: team}, nil
}

// CreateRole creates a new custom role.
func (s *Service) CreateRole(ctx context.Context, req *gen.CreateRoleRequest) (*gen.CreateRoleResponse, error) {
	role := &gen.Role{
		Id:          uuid.New().String(),
		Name:        req.Name,
		Description: req.Description,
		Permissions: req.Permissions,
		BuiltIn:     false,
		OrgId:       req.OrgId,
	}
	if err := s.store.CreateRole(ctx, role); err != nil {
		return nil, err
	}
	return &gen.CreateRoleResponse{Role: role}, nil
}

// AssignRole assigns a role to a user or team.
func (s *Service) AssignRole(ctx context.Context, req *gen.AssignRoleRequest) (*gen.AssignRoleResponse, error) {
	assignment := &gen.RoleAssignment{
		Id:          uuid.New().String(),
		SubjectId:   req.SubjectId,
		SubjectKind: req.SubjectKind,
		RoleId:      req.RoleId,
		OrgId:       req.OrgId,
		Scope:       req.Scope,
	}
	if err := s.store.AssignRole(ctx, assignment); err != nil {
		return nil, err
	}
	s.emit(ctx, req.SubjectId, "user", "role.assigned", "role", req.RoleId, req.OrgId)
	return &gen.AssignRoleResponse{Assignment: assignment}, nil
}
