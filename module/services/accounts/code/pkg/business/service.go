package business

import (
	"accounts/pkg/auth"
	"accounts/pkg/email"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/jobs"
	"context"
	"strings"

	"github.com/codefly-dev/core/wool"
)

type Service struct {
	store                     Store
	hasher                    KeyHasher
	validator                 auth.TokenValidator // production: validates provider tokens after OAuth code exchange
	exchanger                 CodeExchanger       // production: exchanges OAuth codes for provider tokens
	devValidator              auth.TokenValidator // development only: allowlists explicit fixture identities
	resolver                  auth.IdentityResolver
	minter                    auth.JWTMinter
	emailOutbox               *email.Outbox    // optional durable email producer; transport is worker-only
	billing                   BillingClient    // optional: Stripe client for checkout/portal
	billingURLs               BillingRedirects // server-owned Stripe return destinations
	appBaseURL                string           // public URL of the frontend, used in email bodies
	audit                     AuditEmitter
	entitlements              EntitlementChecker
	features                  FeatureChecker
	membership                MembershipInvalidator
	slack                     *SlackNotifier // optional: sends critical notifications to Slack
	oauthState                *auth.OAuthStateSigner
	oauthPolicy               *auth.OAuthRequestPolicy
	webhookJobs               jobs.Producer // request-scoped, transactional outbound producer
	mfaCipher                 SecretCipher  // required for TOTP enrollment and verification
	webhookCipher             SecretCipher  // required for outbound-webhook signing keys
	webhookPolicy             *WebhookEndpointPolicy
	webAuthn                  WebAuthnEngine  // required for passkey registration and assertion
	jobOperations             jobs.Operations // isolated, payload-free platform operations
	acquisitionMode           gen.AcquisitionMode
	waitlistEmailVerification bool
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
	return &Service{
		store:                     store,
		acquisitionMode:           gen.AcquisitionMode_ACQUISITION_MODE_OPEN_SIGNUP,
		waitlistEmailVerification: true,
	}, nil
}

func (s *Service) SetHasher(h KeyHasher) {
	s.hasher = h
}

// SetJobOperations wires the product-neutral administration boundary backed by
// the isolated app_job_worker pool. It is intentionally separate from Store so
// tenant request traffic cannot inherit cross-tenant job access.
func (s *Service) SetJobOperations(operations jobs.Operations) {
	s.jobOperations = operations
}

// SetWebhookJobProducer wires the request-scoped producer used by Test and
// Replay. Its implementation must require the caller's organization
// transaction so delivery history and generated work commit atomically.
func (s *Service) SetWebhookJobProducer(producer jobs.Producer) {
	s.webhookJobs = producer
}

// SetMFASecretCipher wires fail-closed encryption for TOTP seeds. Production
// uses Vault Transit; tests may provide an explicit in-memory implementation.
func (s *Service) SetMFASecretCipher(cipher SecretCipher) {
	s.mfaCipher = cipher
}

// SetWebhookSecurity wires the fail-closed signing-key cipher and the shared
// registration/connect-time endpoint policy. Production uses Vault Transit and
// the public-Internet-only dialer; tests can provide deterministic substitutes.
func (s *Service) SetWebhookSecurity(cipher SecretCipher, policy *WebhookEndpointPolicy) {
	s.webhookCipher = cipher
	s.webhookPolicy = policy.ensureDefaults()
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

// SetOAuthStateSigner wires the server-side state signer used by BeginOAuth
// and verified in Authenticate. OAuth fails closed when this is nil.
func (s *Service) SetOAuthStateSigner(signer *auth.OAuthStateSigner) {
	s.oauthState = signer
}

// SetOAuthRequestPolicy restricts OAuth initiation and callback exchange to
// the configured provider and exact redirect URI allowlist. OAuth fails closed
// when this is nil.
func (s *Service) SetOAuthRequestPolicy(policy *auth.OAuthRequestPolicy) {
	s.oauthPolicy = policy
}

// JWTMinter returns the configured minter so adapters (e.g. the
// Connect auth interceptor) can verify access tokens. Nil if the
// minter has not been wired — callers should treat that as "auth
// disabled" rather than crashing.
func (s *Service) JWTMinter() auth.JWTMinter {
	return s.minter
}

// SetTokenValidator wires a production provider TokenValidator. Authenticate
// uses it only after exchanging an OAuth authorization code. It is deliberately
// separate from the development fixture validator: a missing production
// validator never enables caller-supplied identities as a fallback.
func (s *Service) SetTokenValidator(v auth.TokenValidator) {
	s.validator = v
}

// SetDevelopmentTokenValidator explicitly enables fixture authentication.
// The validator must resolve an opaque fixture token to allowlisted claims;
// Authenticate never trusts provider identity fields supplied by the caller.
// Production wiring must leave this nil.
func (s *Service) SetDevelopmentTokenValidator(v auth.TokenValidator) {
	s.devValidator = v
}

// SetCodeExchanger wires the OAuth code-for-token exchange used in
// production login flows. Optional: if unset, Authenticate cannot
// process OAuth `code` payloads.
func (s *Service) SetCodeExchanger(e CodeExchanger) {
	s.exchanger = e
}

// SetEmailOutbox wires the transactional producer used by invitations,
// authentication, and other request paths. The provider sender is deliberately
// absent from Service: only the generic email worker may perform delivery.
func (s *Service) SetEmailOutbox(outbox *email.Outbox, appBaseURL string) {
	s.emailOutbox = outbox
	s.appBaseURL = appBaseURL
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
		Id:   orgID,
		Name: "Personal",
		// Use the tail of the uuid (random bits) not the head (v7 timestamp prefix)
		// so two users created in the same millisecond get distinct slugs.
		Slug:    "personal-" + userID[len(userID)-12:],
		OwnerId: userID,
	}
	// Personal-org bootstrap: see CreateOrganization comment — at
	// this moment the org doesn't exist; WithControlPlane is correct.
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.store.CreateOrganization(ctx, org)
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create default organization")
	}

	// Assign admin role to user in their org. The built-in roles are
	// org_id=NULL — RLS-protected as of Phase 2E, so the read needs
	// bypass (built-ins are global). The assignment row carries
	// concrete orgID so it goes through WithOrgTx.
	var roles []*gen.Role
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		rs, err := s.store.ListRoles(ctx, "")
		roles = rs
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list roles")
	}
	for _, role := range roles {
		if role.Name == "admin" && role.BuiltIn {
			assignment := &gen.RoleAssignment{
				Id:          NewIDString(),
				SubjectId:   userID,
				SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
				RoleId:      role.Id,
				OrgId:       orgID,
			}
			if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
				return s.store.AssignRole(ctx, assignment)
			}); err != nil {
				return nil, w.Wrapf(err, "cannot assign admin role")
			}
			break
		}
	}

	s.emit(ctx, userID, "user", "user.registered", "user", userID, orgID)

	return &gen.RegisterUserResponse{User: user, Identity: identity}, nil
}

// CheckPermission checks if a subject has permission to perform an action on a resource.
//
// The query JOINs role_assignments + roles + role_permissions
// (+ team_members for team inheritance). All but role_permissions
// are RLS-protected (Phase 2E + 2C); a tenant-scoped check needs
// WithOrgTx so the JOIN sees the right rows. Global checks
// (req.OrgId == "") run under bypass — only platform-internal
// callers exercise that path.
func (s *Service) CheckPermission(ctx context.Context, req *gen.CheckPermissionRequest) (*gen.CheckPermissionResponse, error) {
	var allowed bool
	var reason string
	wrap := func(ctx context.Context) error {
		a, r, err := s.store.CheckPermission(
			ctx, req.SubjectId, req.SubjectKind,
			req.Resource, req.Action, req.OrgId, req.Scope,
		)
		allowed, reason = a, r
		return err
	}
	var err error
	if req.OrgId == "" {
		err = s.store.WithControlPlane(ctx, wrap)
	} else {
		err = s.store.WithOrgTx(ctx, req.OrgId, wrap)
	}
	if err != nil {
		return nil, err
	}
	return &gen.CheckPermissionResponse{Allowed: allowed, Reason: reason}, nil
}

// ResolveIdentity maps an auth provider ID to internal user/org/roles.
//
// Auth-flow read: at login we don't yet know the user's tenant. The
// Store implementation (postgres_permissions.go:ResolveIdentity)
// opens its own tx and assumes app_control_plane inline so
// the JOINs against organization_members + role_assignments (both
// RLS-protected) see all tenants.
//
// The auth interceptor stamps the resolved OrgID on every
// subsequent ctx so downstream queries run properly scoped via
// WithOrgTx.
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
//
// Bootstrap path: at this moment the tenant doesn't exist yet, so
// there's no org context to set. WithControlPlane (which keeps the
// connection's session_user role) is correct here — the role-switch
// in WithOrgTx would put us as app_tenant with no app.current_org_id,
// and the WITH CHECK on organization_members would reject the insert.
// User authz is at the handler — only authenticated users can create
// orgs; abuse is rate-limited.
func (s *Service) CreateOrganization(ctx context.Context, ownerID string, req *gen.CreateOrganizationRequest) (*gen.CreateOrganizationResponse, error) {
	slug := req.Slug
	if slug == "" {
		slug = Slugify(req.Name)
	}
	if slug == "" {
		return nil, wool.Get(ctx).In("CreateOrganization").NewError("organization name yields an empty slug")
	}
	org := &gen.Organization{
		Id:      NewIDString(),
		Name:    req.Name,
		Slug:    slug,
		OwnerId: ownerID,
	}
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.store.CreateOrganization(ctx, org)
	}); err != nil {
		return nil, err
	}
	s.emit(ctx, ownerID, "user", "org.created", "organization", org.Id, org.Id)
	return &gen.CreateOrganizationResponse{Organization: org}, nil
}

// CreateTeam creates a new team within an org.
// actorID is the authenticated caller — recorded in the audit log so we
// know who stood up the team. teams is RLS-protected (Phase 2C) so
// the insert runs inside WithOrgTx scoped to the target org.
//
// Teams form a TREE: path = parent.path + "/" + slug (slug derived from the
// name when not given). Create-under-existing-parent only — there is no
// reparent RPC, so cycles cannot form; a future move feature adds a guard.
func (s *Service) CreateTeam(ctx context.Context, actorID string, req *gen.CreateTeamRequest) (*gen.CreateTeamResponse, error) {
	w := wool.Get(ctx).In("CreateTeam")

	slug := req.Slug
	if slug == "" {
		slug = Slugify(req.Name)
	}
	if slug == "" {
		return nil, w.NewError("team name yields an empty slug")
	}

	team := &gen.Team{
		Id:           NewIDString(),
		OrgId:        req.OrgId,
		Name:         req.Name,
		Description:  req.Description,
		ParentTeamId: req.ParentTeamId,
		Slug:         slug,
		Path:         slug, // root path; child path derived below
	}
	if err := s.store.WithOrgTx(ctx, req.OrgId, func(ctx context.Context) error {
		if req.ParentTeamId != "" {
			parentOrg, parentPath, err := s.store.GetTeamPath(ctx, req.ParentTeamId)
			if err != nil {
				return err
			}
			if parentOrg == "" {
				return w.NewError("parent team not found")
			}
			if parentOrg != req.OrgId {
				return w.NewError("parent team belongs to a different organization")
			}
			team.Path = parentPath + "/" + slug
		}
		return s.store.CreateTeam(ctx, team)
	}); err != nil {
		return nil, err
	}
	s.emit(ctx, actorID, "user", "team.created", "team", team.Id, req.OrgId)
	return &gen.CreateTeamResponse{Team: team}, nil
}

// Slugify derives a path segment from a display name: lowercase, runs of
// non-alphanumerics collapse to "-", trimmed. ("Platform Eng." → "platform-eng")
func Slugify(name string) string {
	var b strings.Builder
	lastDash := true // suppress a leading dash
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// CreateRole creates a new custom role.
// Audit trail records actorID so role additions are traceable — these are
// security-relevant changes (a new role = a new set of permissions).
//
// roles is RLS-protected (Phase 2E). req.OrgId == "" means a global
// built-in role write — only platform admin can do this (handler authz
// enforces requirePlatformAdmin in adapters/rpcs.go), and the WITH
// CHECK on roles requires either bypass or a concrete org. Use bypass
// for the global path and WithOrgTx for tenant roles.
func (s *Service) CreateRole(ctx context.Context, actorID string, req *gen.CreateRoleRequest) (*gen.CreateRoleResponse, error) {
	role := &gen.Role{
		Id:          NewIDString(),
		Name:        req.Name,
		Description: req.Description,
		Permissions: req.Permissions,
		BuiltIn:     false,
		OrgId:       req.OrgId,
	}
	wrap := func(ctx context.Context) error { return s.store.CreateRole(ctx, role) }
	var err error
	if req.OrgId == "" {
		err = s.store.WithControlPlane(ctx, wrap)
	} else {
		err = s.store.WithOrgTx(ctx, req.OrgId, wrap)
	}
	if err != nil {
		return nil, err
	}
	s.emit(ctx, actorID, "user", "role.created", "role", role.Id, req.OrgId)
	return &gen.CreateRoleResponse{Role: role}, nil
}

// AssignRole assigns a role to a user or team. role_assignments is
// RLS-protected (Phase 2E). Empty OrgId means a platform-level
// assignment (super_admin etc.) — handler authz already required
// platform_admin; the write goes through bypass.
func (s *Service) AssignRole(ctx context.Context, req *gen.AssignRoleRequest) (*gen.AssignRoleResponse, error) {
	assignment := &gen.RoleAssignment{
		Id:          NewIDString(),
		SubjectId:   req.SubjectId,
		SubjectKind: req.SubjectKind,
		RoleId:      req.RoleId,
		OrgId:       req.OrgId,
		Scope:       req.Scope,
	}
	wrap := func(ctx context.Context) error { return s.store.AssignRole(ctx, assignment) }
	var err error
	if req.OrgId == "" {
		err = s.store.WithControlPlane(ctx, wrap)
	} else {
		err = s.store.WithOrgTx(ctx, req.OrgId, wrap)
	}
	if err != nil {
		return nil, err
	}
	s.emit(ctx, req.SubjectId, "user", "role.assigned", "role", req.RoleId, req.OrgId)
	return &gen.AssignRoleResponse{Assignment: assignment}, nil
}
