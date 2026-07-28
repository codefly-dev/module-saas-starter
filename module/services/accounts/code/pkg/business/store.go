package business

import (
	"context"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

type Store interface {
	// Transactions
	RunInTransaction(ctx context.Context, fn func(ctx context.Context) error) error

	// WithOrgTx wraps fn in a transaction that has app.current_org_id
	// set, so RLS policies on per-tenant tables filter to that org.
	// Empty orgID is rejected (loud, fail-closed). Wrap every
	// per-tenant Service path in this — see AUTHZ.md.
	WithOrgTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error

	// WithUserTx wraps fn in a transaction that has app.current_user_id
	// set, so RLS policies on user-scoped tables (notifications,
	// mfa_devices, sessions) filter to that user. Empty userID is
	// rejected (loud, fail-closed). Same no-nesting rule as WithOrgTx.
	// See AUTHZ.md for the user-scope policy shape.
	WithUserTx(ctx context.Context, userID string, fn func(ctx context.Context) error) error

	// WithControlPlane wraps fn in a transaction that assumes the named
	// app_control_plane role. Use only for
	// background workers + platform-admin views; every call site is
	// deliberate.
	WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error

	// As returns a Store handle bound to an identity (see Scoped). It is the
	// identity-first entry point the With* wrappers collapse into: As(id).Within
	// sets the same RLS context, but the identity is explicit and carried by the
	// handle. As(System()) is the one named, audited bypass.
	As(id Identity) Scoped

	// Users
	RegisterUser(ctx context.Context, user *gen.User, identity *gen.UserIdentity) error
	GetUserByIdentity(ctx context.Context, id *gen.UserIdentity) (*gen.User, error)
	GetUser(ctx context.Context, id string) (*gen.User, error)
	GetUserByEmail(ctx context.Context, email string) (*gen.User, error)
	GetOrganizationMemberPrimaryEmail(ctx context.Context, userID string) (string, error)
	ListUsers(ctx context.Context, orgID string, statusFilter string, pageSize int32, pageToken string) ([]*gen.User, string, error)
	UpdateUser(ctx context.Context, userID string, updates map[string]any) (*gen.User, error)
	DeleteUser(ctx context.Context, userID string) error

	// Identities
	AddIdentity(ctx context.Context, identity *gen.UserIdentity) error
	FindUserByIdentity(ctx context.Context, provider, providerID string) (*gen.User, error)
	ListUserIdentities(ctx context.Context, userID string) ([]*gen.UserIdentity, error)
	DeleteUserIdentities(ctx context.Context, userID string) error

	// Organizations
	CreateOrganization(ctx context.Context, org *gen.Organization) error
	GetOrganization(ctx context.Context, id string) (*gen.Organization, error)
	ListOrganizationsForUser(ctx context.Context, userID string) ([]*gen.Organization, error)
	AddOrgMember(ctx context.Context, orgID string, userID string, role string) error
	OrgMemberExists(ctx context.Context, orgID string, userID string) (bool, error)
	RemoveOrgMember(ctx context.Context, orgID string, userID string) error
	// GetOrgMembership is the authorization hot path. It must be an indexed
	// point lookup, never an org-roster scan. Nil means the user is not a member.
	GetOrgMembership(ctx context.Context, orgID string, userID string) (*gen.OrgMembership, error)
	ListOrgMembers(ctx context.Context, orgID string) ([]*gen.OrgMembership, error)

	// Teams
	CreateTeam(ctx context.Context, team *gen.Team) error
	ListTeams(ctx context.Context, orgID string) ([]*gen.Team, error)
	UpdateTeam(ctx context.Context, teamID, name, description string) (*gen.Team, error)
	DeleteTeam(ctx context.Context, teamID string) error
	AddTeamMember(ctx context.Context, teamID string, userID string, role string) error
	RemoveTeamMember(ctx context.Context, teamID string, userID string) error
	// GetTeamMembership is the authorization hot path. Nil means the user is
	// not a member; list access remains a separate, explicitly authorized API.
	GetTeamMembership(ctx context.Context, orgID string, teamID string, userID string) (*gen.TeamMembership, error)
	ListTeamMembers(ctx context.Context, teamID string) ([]*gen.TeamMembership, error)
	// GetTeamOrgID resolves a team's owning org. Used by callers that
	// only have a team_id (e.g. AddTeamMember handlers) so they can
	// enter WithOrgTx with the right scope. Implementations bypass RLS
	// internally — the result is then handed to WithOrgTx for the
	// real op, which IS scoped.
	GetTeamOrgID(ctx context.Context, teamID string) (string, error)
	// GetTeamPath returns (orgID, path) — the parent lookup CreateTeam uses
	// to derive a child team's path. ("", "") with no error when not found.
	GetTeamPath(ctx context.Context, teamID string) (string, string, error)

	// Identity Claims v1 (the validate-key read surface — see postgres_claims.go)
	ListTeamPathsForUser(ctx context.Context, userID string, orgID string) ([]string, error)
	ListRoleNamesForUser(ctx context.Context, userID string, orgID string) ([]string, error)
	GetUserAttributes(ctx context.Context, userID string) (map[string]string, error)
	// GetAPIKeyAuthentication is the narrow pre-authentication projection. It
	// resolves a presented key and its current owner policy facts atomically,
	// without exposing a raw pool or general control-plane capability.
	GetAPIKeyAuthentication(ctx context.Context, keyHash string) (*APIKeyAuthentication, error)

	// Roles
	CreateRole(ctx context.Context, role *gen.Role) error
	ListRoles(ctx context.Context, orgID string) ([]*gen.Role, error)
	DeleteRole(ctx context.Context, roleID string) error

	// Role assignments
	AssignRole(ctx context.Context, assignment *gen.RoleAssignment) error
	RevokeRole(ctx context.Context, subjectID string, roleID string, orgID string, scope string) error
	// ListRoleAssignments returns assignments in an org. When subjectID is
	// empty, every assignment in that org is returned. subjectKind ==
	// SUBJECT_KIND_UNSPECIFIED returns both direct principals and teams.
	ListRoleAssignments(ctx context.Context, orgID string, subjectID string, subjectKind gen.SubjectKind) ([]*gen.RoleAssignment, error)

	// Permission checking
	CheckPermission(ctx context.Context, subjectID string, subjectKind gen.SubjectKind, resource string, action string, orgID string, scope string) (bool, string, error)

	// Identity resolution
	ResolveIdentity(ctx context.Context, provider string, providerID string) (*ResolvedIdentity, error)

	// Platform admin
	GetPlatformRole(ctx context.Context, userID string) (string, error)
	GrantPlatformRole(ctx context.Context, userID, role, grantedBy string) error
	RevokePlatformRole(ctx context.Context, userID string) error
	ListPlatformAdmins(ctx context.Context) ([]PlatformAdmin, error)

	// API Keys
	CreateAPIKey(ctx context.Context, key *gen.APIKey, keyHash string) error
	CountActiveAPIKeys(ctx context.Context, orgID string) (int64, error)
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*gen.APIKey, error)
	ListAPIKeys(ctx context.Context, orgID string, pageSize int32, pageToken string) ([]*gen.APIKey, string, error)
	RevokeAPIKey(ctx context.Context, keyID string, orgID string) error
	TouchAPIKeyUsage(ctx context.Context, keyID string, ip string) error

	// Audit
	InsertAuditEvent(ctx context.Context, entry AuditEntry) error
	QueryAuditLog(ctx context.Context, orgID, actorID, action, resource, resourceID string,
		from, to *time.Time, pageSize int32, pageToken string) ([]AuditEntry, string, int32, error)

	// Invitations
	CreateInvitation(ctx context.Context, inv *Invitation) error
	ExpirePendingInvitations(ctx context.Context, orgID string) error
	GetInvitationByTokenHash(ctx context.Context, hash string) (*Invitation, error)
	GetInvitationOrgID(ctx context.Context, id string) (string, error)
	ListInvitations(ctx context.Context, orgID string, status string) ([]*Invitation, error)
	UpdateInvitationStatus(ctx context.Context, id string, status string, acceptedBy string) error
	CountPendingInvitations(ctx context.Context, orgID string) (int32, error)

	// SSO
	GetOrgSSO(ctx context.Context, orgID string) (*OrgSSOConfig, error)
	UpsertOrgSSO(ctx context.Context, cfg *OrgSSOConfig) error

	// User settings. The Store boundary is protobuf-typed; only the Postgres
	// implementation sees the ProtoJSON representation stored in JSONB.
	GetUserSettings(ctx context.Context, userID string) (*gen.UserSettings, error)
	UpdateUserSettings(ctx context.Context, userID string, patch *gen.UserSettings, resetPaths []string) error

	// Entitlements
	GetOrgPlanID(ctx context.Context, orgID string) (string, error)
	GetPlanByID(ctx context.Context, planID string) (*Plan, error)
	GetPlanEntitlement(ctx context.Context, planID string, feature string) (int64, error)
	ListPlanEntitlements(ctx context.Context, planID string) ([]PlanFeatureLimit, error)
	GetEntitlementOverride(ctx context.Context, orgID string, feature string) (*EntitlementOverride, error)
	ListEntitlementOverrides(ctx context.Context, orgID string) ([]*EntitlementOverride, error)
	CreateEntitlementOverride(ctx context.Context, override *EntitlementOverride) error
	LockEntitlementQuota(ctx context.Context, orgID string, feature string) error
	GetUsageTotal(ctx context.Context, orgID string, meter string, periodStart time.Time) (int64, error)
	ConsumeUsage(ctx context.Context, consumption UsageConsumption) (*UsageReceipt, error)
	GetSubscription(ctx context.Context, orgID string) (*Subscription, error)
	CreateSubscription(ctx context.Context, sub *Subscription) error
	UpdateSubscription(ctx context.Context, sub *Subscription) error

	// Billing — Stripe customer + plan-by-name lookups used by the
	// StartCheckout / OpenBillingPortal flows.
	GetOrgStripeCustomerID(ctx context.Context, orgID string) (string, error)
	SetOrgStripeCustomerID(ctx context.Context, orgID, stripeCustomerID string) error
	GetPlanByName(ctx context.Context, name string) (*PlanFull, error)
	ListPublicPlans(ctx context.Context) ([]PublicPlan, error)

	// Feature Flags
	GetFeatureFlag(ctx context.Context, name string) (*FeatureFlag, error)
	ListFeatureFlags(ctx context.Context) ([]*FeatureFlag, error)
	UpsertFeatureFlag(ctx context.Context, flag *FeatureFlag) error

	// Platform admin - user operations
	SearchUsers(ctx context.Context, query string, pageSize int32, pageToken string) ([]*gen.User, string, error)
	UpdateUserStatus(ctx context.Context, userID string, status string) error
	ListActiveSessions(ctx context.Context, userID string, pageSize int32) ([]*Session, error)

	// Sessions
	CreateSession(ctx context.Context, session *Session) error
	GetSessionByRefreshTokenHash(ctx context.Context, hash string) (*Session, error)
	RevokeSession(ctx context.Context, deviceSessionID string, reason string) error
	RevokeSessionFamily(ctx context.Context, familyID string, reason string) error
	RevokeAllUserSessions(ctx context.Context, userID string, reason string) error
	UpdateSessionActivity(ctx context.Context, sessionID string) error

	// Webhooks
	CreateWebhookSubscription(ctx context.Context, sub *WebhookSubscription) error
	GetWebhookSubscription(ctx context.Context, id string) (*WebhookSubscription, error)
	UpdateWebhookSubscription(ctx context.Context, sub *WebhookSubscription) error
	DeleteWebhookSubscription(ctx context.Context, id string) error
	ListWebhookSubscriptions(ctx context.Context, orgID string) ([]*WebhookSubscription, error)
	GetActiveWebhookSubscriptions(ctx context.Context, eventType string) ([]*WebhookSubscription, error)
	CreateWebhookDelivery(ctx context.Context, delivery *WebhookDelivery) error
	GetWebhookDelivery(ctx context.Context, id string) (*WebhookDelivery, error)
	ListWebhookDeliveries(ctx context.Context, subscriptionID string, pageSize int) ([]*WebhookDelivery, error)

	// Organization Settings (branding)
	GetOrgSettings(ctx context.Context, orgID string) (*OrgSettings, error)
	UpsertOrgSettings(ctx context.Context, settings *OrgSettings) error

	// Notifications
	CreateNotification(ctx context.Context, n *Notification) error
	ListNotifications(ctx context.Context, userID string, pageSize int, pageToken string) ([]*Notification, string, error)
	GetUnreadCount(ctx context.Context, userID string) (int, error)
	MarkNotificationRead(ctx context.Context, id string) error
	MarkAllNotificationsRead(ctx context.Context, userID string) error
	DeleteNotification(ctx context.Context, id string) error
	// GetNotificationUserID resolves notification.id → user_id.
	// Called under WithControlPlane by Service methods that only have an
	// id (MarkRead / DeleteNotification) and need to enter the
	// user's WithUserTx for the actual mutation. Returns "" on miss.
	GetNotificationUserID(ctx context.Context, id string) (string, error)

	// MFA — exposed on the main Store interface so the auth layer's
	// requireMFA gate can check enrollment without casting to MFAStore.
	HasVerifiedMFA(ctx context.Context, userID string) (bool, error)

	// Onboarding
	GetOnboardingProgress(ctx context.Context, userID string) ([]*OnboardingStep, error)
	UpsertOnboardingStep(ctx context.Context, userID string, stepName string, status string) error

	// Magic Links
	CreateMagicLink(ctx context.Context, ml *MagicLink) error
	GetMagicLinkByTokenHash(ctx context.Context, tokenHash string) (*MagicLink, error)
	MarkMagicLinkUsed(ctx context.Context, id string) error

	// Audit log → S3 export — per-org config + cursor + error tracking.
	GetAuditExportConfig(ctx context.Context, orgID string) (*AuditExportConfig, error)
	UpsertAuditExportConfig(ctx context.Context, cfg *AuditExportConfig) error
	DeleteAuditExportConfig(ctx context.Context, orgID string) error
	ListDueAuditExportConfigs(ctx context.Context, now time.Time) ([]*AuditExportConfig, error)
	MarkAuditExportSucceeded(ctx context.Context, orgID string, exportedAt time.Time) error
	RecordAuditExportError(ctx context.Context, orgID, message string) error

	// User consent — server-side TOS/privacy acceptance trail.
	GetUserConsent(ctx context.Context, userID string) (version string, acceptedAt *time.Time, err error)
	SetUserConsent(ctx context.Context, userID, version string, acceptedAt time.Time) error

	// Data Retention
	GetRetentionPolicies(ctx context.Context) ([]*RetentionPolicy, error)
	DeleteOldAuditEvents(ctx context.Context, before time.Time) (int64, error)
	DeleteOldSessions(ctx context.Context, before time.Time) (int64, error)
	DeleteOldWebhookDeliveries(ctx context.Context, before time.Time) (int64, error)
	DeleteOldNotifications(ctx context.Context, before time.Time) (int64, error)
}

type StoreErrorType string

const (
	ErrTypeNotFound   StoreErrorType = "not_found"
	ErrTypeConflict   StoreErrorType = "conflict"
	ErrTypePermission StoreErrorType = "permission"
	ErrTypeInternal   StoreErrorType = "internal"
)

type StoreError struct {
	Err error
	StoreErrorType
}

func (e *StoreError) Error() string {
	return e.Err.Error()
}

func NewStoreError(err error, t StoreErrorType) *StoreError {
	return &StoreError{
		Err:            err,
		StoreErrorType: t,
	}
}

// ResolvedIdentity is the result of mapping an auth provider identity to internal user/org/roles.
type ResolvedIdentity struct {
	UserID       string
	OrgID        string
	OrgRole      string // "owner"|"admin"|"member"
	PlatformRole string // "super_admin"|"support"|"billing"|""
	Roles        []string
	Found        bool
}

type APIKeyIdentityClaims struct {
	Member       bool
	OrgRole      string
	PlatformRole string
	Workspaces   []string
	Roles        []string
	Attributes   map[string]string
}

type APIKeyAuthentication struct {
	Key    *gen.APIKey
	Claims APIKeyIdentityClaims
}

// PlatformAdmin represents a user with platform-level privileges.
type PlatformAdmin struct {
	UserID       string
	PlatformRole string
	GrantedBy    string
	GrantedAt    time.Time
}

// RetentionPolicy defines how long records of a given type should be kept.
type RetentionPolicy struct {
	ID            string
	ResourceType  string
	RetentionDays int
	CreatedAt     time.Time
}

// Session represents a refresh token session.
type Session struct {
	ID               string
	UserID           string
	RefreshTokenHash string
	FamilyID         string
	DeviceInfo       map[string]string
	IPAddress        string
	CreatedAt        time.Time
	LastActiveAt     time.Time
	IdleExpiresAt    time.Time
	ExpiresAt        time.Time
	RevokedAt        *time.Time
	RevokedReason    string
}
