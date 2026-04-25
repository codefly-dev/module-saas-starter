package business

import (
	"api/pkg/auth"
	"api/pkg/email"
	"api/pkg/gen"
	"context"

	"github.com/codefly-dev/core/wool"
)

type Service struct {
	store        Store
	hasher       KeyHasher
	validator    auth.TokenValidator // optional: validates provider tokens at login
	exchanger    CodeExchanger       // optional: exchanges OAuth codes for provider tokens
	resolver     auth.IdentityResolver
	minter       auth.JWTMinter
	mail         email.Sender          // optional: transactional email for invites et al
	templateMail *email.TemplateSender // optional: template-backed email sender
	billing      BillingClient         // optional: Stripe client for checkout/portal
	appBaseURL   string                // public URL of the frontend, used in email bodies
	fromAddress  string                // "From" header for outgoing email
	audit        AuditEmitter
	entitlements EntitlementChecker
	features     FeatureChecker
	membership   MembershipInvalidator
	slack        *SlackNotifier // optional: sends critical notifications to Slack
	oauthState   *auth.OAuthStateSigner
}

// CodeExchanger abstracts the OAuth 2.0 code-for-token exchange so the
// business layer doesn't depend on the concrete oidc package. In
// production this is *oidc.Exchanger; tests use a fake.
//
// codeVerifier is the PKCE secret the FE generated for THIS sign-in
// attempt — empty when the FE didn't use PKCE (legacy flow). When
// non-empty, it is forwarded to the provider's token endpoint as
// `code_verifier`; the provider re-hashes it and compares with the
// `code_challenge` originally sent in the authorize URL.
type CodeExchanger interface {
	Exchange(ctx context.Context, code, redirectURI, codeVerifier string) (ExchangedTokens, error)
}

// ExchangedTokens is the subset of an OAuth token response the backend
// cares about. Mirrors oidc.TokenResponse without creating an import
// cycle on the auth package.
type ExchangedTokens struct {
	AccessToken string
	IDToken     string
}

func NewService(store Store) (*Service, error) {
	return &Service{store: store}, nil
}

func (s *Service) SetHasher(h KeyHasher) {
	s.hasher = h
}

// SetIdentityResolver wires the JIT provisioning + bootstrap layer.
// Required for /auth/login and /auth/signup flows.
func (s *Service) SetIdentityResolver(r auth.IdentityResolver) {
	s.resolver = r
}

// SetJWTMinter wires the token signing + refresh rotation layer.
// Required for every /auth/* endpoint.
func (s *Service) SetJWTMinter(m auth.JWTMinter) {
	s.minter = m
}

// SetOAuthStateSigner wires the server-side state signer used by
// BeginOAuth and verified in Authenticate. Optional: when nil, the
// FE-only state validation (sessionStorage) remains the sole defense.
// Production deployments SHOULD wire this with a stable seed (the
// JWT signing key works) so state survives api restarts and
// multi-instance deployments.
func (s *Service) SetOAuthStateSigner(signer *auth.OAuthStateSigner) {
	s.oauthState = signer
}

// JWTMinter returns the configured minter so adapters (e.g. the
// Connect auth interceptor) can verify access tokens. Nil if the
// minter has not been wired — callers should treat that as "auth
// disabled" rather than crashing.
func (s *Service) JWTMinter() auth.JWTMinter {
	return s.minter
}

// SetTokenValidator wires a provider-specific TokenValidator. When set,
// incoming Authenticate requests carrying an OAuth `code` in their
// profile map get routed through Exchanger → Validator → Resolver.
// Optional: if unset, Authenticate trusts the request's provider+sub
// directly (dev path).
func (s *Service) SetTokenValidator(v auth.TokenValidator) {
	s.validator = v
}

// SetCodeExchanger wires the OAuth code-for-token exchange used in
// production login flows. Optional: if unset, Authenticate cannot
// process OAuth `code` payloads.
func (s *Service) SetCodeExchanger(e CodeExchanger) {
	s.exchanger = e
}

// SetEmailSender wires the transactional email provider used for
// invitations, password reset links, etc. Optional: if unset, email
// sends are skipped and logged at warn level — the underlying
// invitation/session row is still created.
func (s *Service) SetEmailSender(sender email.Sender, fromAddress, appBaseURL string) {
	s.mail = sender
	s.fromAddress = fromAddress
	s.appBaseURL = appBaseURL
}

// SetTemplateSender wires the template-backed email sender. When set,
// business methods prefer SendTemplate over raw Send for emails that
// match a seeded template (invitation, billing notifications, etc.).
func (s *Service) SetTemplateSender(ts *email.TemplateSender) {
	s.templateMail = ts
}

// TemplateSender returns the template email sender, or nil if not wired.
func (s *Service) TemplateSender() *email.TemplateSender {
	return s.templateMail
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

// MembershipInvalidator is called by the business layer on every mutation
// that changes (orgID, userID) membership — add/remove/role-change. A
// nil invalidator (SetMembershipInvalidator never called) is a no-op,
// which is the correct fallback when Redis caching is disabled.
//
// The implementation lives in adapters; this interface keeps the
// business layer from importing adapters or cache directly, matching
// the SetAuditEmitter / SetEntitlementChecker pattern.
type MembershipInvalidator interface {
	InvalidateMembership(ctx context.Context, orgID, userID string)
}

func (s *Service) SetMembershipInvalidator(i MembershipInvalidator) {
	s.membership = i
}

// invalidateMembership is the internal helper Service methods call after
// mutating membership. Nil-safe so non-cache-wired setups keep working.
func (s *Service) invalidateMembership(ctx context.Context, orgID, userID string) {
	if s.membership == nil {
		return
	}
	s.membership.InvalidateMembership(ctx, orgID, userID)
}

// SetSlackNotifier wires an optional Slack webhook notifier for critical events.
func (s *Service) SetSlackNotifier(n *SlackNotifier) {
	s.slack = n
}

// notifySlack sends a message to Slack if the notifier is configured. Best-effort.
func (s *Service) notifySlack(ctx context.Context, text string) {
	if s.slack != nil {
		_ = s.slack.Send(ctx, "", text)
	}
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

	userID := NewIDString()
	identityID := NewIDString()

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
	orgID := NewIDString()
	org := &gen.Organization{
		Id:      orgID,
		Name:    "Personal",
		// Use the tail of the uuid (random bits) not the head (v7 timestamp prefix)
		// so two users created in the same millisecond get distinct slugs.
		Slug:    "personal-" + userID[len(userID)-12:],
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
				Id:          NewIDString(),
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
		Id:      NewIDString(),
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
// actorID is the authenticated caller — recorded in the audit log so we
// know who stood up the team.
func (s *Service) CreateTeam(ctx context.Context, actorID string, req *gen.CreateTeamRequest) (*gen.CreateTeamResponse, error) {
	team := &gen.Team{
		Id:          NewIDString(),
		OrgId:       req.OrgId,
		Name:        req.Name,
		Description: req.Description,
	}
	if err := s.store.CreateTeam(ctx, team); err != nil {
		return nil, err
	}
	s.emit(ctx, actorID, "user", "team.created", "team", team.Id, req.OrgId)
	return &gen.CreateTeamResponse{Team: team}, nil
}

// CreateRole creates a new custom role.
// Audit trail records actorID so role additions are traceable — these are
// security-relevant changes (a new role = a new set of permissions).
func (s *Service) CreateRole(ctx context.Context, actorID string, req *gen.CreateRoleRequest) (*gen.CreateRoleResponse, error) {
	role := &gen.Role{
		Id:          NewIDString(),
		Name:        req.Name,
		Description: req.Description,
		Permissions: req.Permissions,
		BuiltIn:     false,
		OrgId:       req.OrgId,
	}
	if err := s.store.CreateRole(ctx, role); err != nil {
		return nil, err
	}
	s.emit(ctx, actorID, "user", "role.created", "role", role.Id, req.OrgId)
	return &gen.CreateRoleResponse{Role: role}, nil
}

// AssignRole assigns a role to a user or team.
func (s *Service) AssignRole(ctx context.Context, req *gen.AssignRoleRequest) (*gen.AssignRoleResponse, error) {
	assignment := &gen.RoleAssignment{
		Id:          NewIDString(),
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
