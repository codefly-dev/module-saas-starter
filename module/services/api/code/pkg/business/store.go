package business

import (
	"context"
	"time"

	"api/pkg/gen"
)

type Store interface {
	// Transactions
	RunInTransaction(ctx context.Context, fn func(ctx context.Context) error) error

	// WithOrgTx wraps fn in a transaction that has app.current_org_id
	// set, so RLS policies on per-tenant tables filter to that org.
	// Empty orgID is rejected (loud, fail-closed). Wrap every
	// per-tenant Service path in this — see AUTHZ.md.
	WithOrgTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error

	// WithBypass wraps fn in a transaction that has app.bypass='1'
	// so RLS policies allow cross-tenant access. Use only for
	// background workers + platform-admin views; every call site is
	// deliberate.
	WithBypass(ctx context.Context, fn func(ctx context.Context) error) error

	// Users
	RegisterUser(ctx context.Context, user *gen.User, identity *gen.UserIdentity) error
	GetUserByIdentity(ctx context.Context, id *gen.UserIdentity) (*gen.User, error)
	GetUser(ctx context.Context, id string) (*gen.User, error)
	GetUserByEmail(ctx context.Context, email string) (*gen.User, error)
	ListUsers(ctx context.Context, orgID string, statusFilter string, pageSize int32, pageToken string) ([]*gen.User, string, error)
	UpdateUser(ctx context.Context, userID string, updates map[string]any) (*gen.User, error)
	DeleteUser(ctx context.Context, userID string) error

	// Identities
	AddIdentity(ctx context.Context, identity *gen.UserIdentity) error
	FindUserByIdentity(ctx context.Context, provider, providerID string) (*gen.User, error)
	ListUserIdentities(ctx context.Context, userID string) ([]*gen.UserIdentity, error)

	// Organizations
	CreateOrganization(ctx context.Context, org *gen.Organization) error
	GetOrganization(ctx context.Context, id string) (*gen.Organization, error)
	ListOrganizationsForUser(ctx context.Context, userID string) ([]*gen.Organization, error)
	AddOrgMember(ctx context.Context, orgID string, userID string, role string) error
	RemoveOrgMember(ctx context.Context, orgID string, userID string) error
	ListOrgMembers(ctx context.Context, orgID string) ([]*gen.OrgMembership, error)

	// Teams
	CreateTeam(ctx context.Context, team *gen.Team) error
	ListTeams(ctx context.Context, orgID string) ([]*gen.Team, error)
	AddTeamMember(ctx context.Context, teamID string, userID string, role string) error
	RemoveTeamMember(ctx context.Context, teamID string, userID string) error
	ListTeamMembers(ctx context.Context, teamID string) ([]*gen.TeamMembership, error)

	// Roles
	CreateRole(ctx context.Context, role *gen.Role) error
	ListRoles(ctx context.Context, orgID string) ([]*gen.Role, error)
	DeleteRole(ctx context.Context, roleID string) error

	// Role assignments
	AssignRole(ctx context.Context, assignment *gen.RoleAssignment) error
	RevokeRole(ctx context.Context, subjectID string, roleID string, orgID string, scope string) error

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
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*gen.APIKey, error)
	ListAPIKeys(ctx context.Context, orgID string, pageSize int32, pageToken string) ([]*gen.APIKey, string, error)
	RevokeAPIKey(ctx context.Context, keyID string) error
	TouchAPIKeyUsage(ctx context.Context, keyID string, ip string) error

	// Audit
	InsertAuditEvent(ctx context.Context, entry AuditEntry) error
	QueryAuditLog(ctx context.Context, orgID, actorID, action, resource, resourceID string,
		from, to *time.Time, pageSize int32, pageToken string) ([]AuditEntry, string, int32, error)

	// Invitations
	CreateInvitation(ctx context.Context, inv *Invitation) error
	GetInvitationByTokenHash(ctx context.Context, hash string) (*Invitation, error)
	ListInvitations(ctx context.Context, orgID string, status string) ([]*Invitation, error)
	UpdateInvitationStatus(ctx context.Context, id string, status string, acceptedBy string) error
	CountPendingInvitations(ctx context.Context, orgID string) (int32, error)

	// SSO
	GetOrgSSO(ctx context.Context, orgID string) (*OrgSSOConfig, error)
	UpsertOrgSSO(ctx context.Context, cfg *OrgSSOConfig) error

	// User settings (JSONB blob — see business/user_settings.go)
	GetUserSettings(ctx context.Context, userID string) ([]byte, error)
	UpdateUserSettings(ctx context.Context, userID string, patch []byte) error

	// Entitlements
	GetOrgPlanID(ctx context.Context, orgID string) (string, error)
	GetPlanByID(ctx context.Context, planID string) (*Plan, error)
	GetPlanEntitlement(ctx context.Context, planID string, feature string) (int64, error)
	ListPlanEntitlements(ctx context.Context, planID string) ([]PlanFeatureLimit, error)
	GetEntitlementOverride(ctx context.Context, orgID string, feature string) (*EntitlementOverride, error)
	ListEntitlementOverrides(ctx context.Context, orgID string) ([]*EntitlementOverride, error)
	CreateEntitlementOverride(ctx context.Context, override *EntitlementOverride) error
	GetUsageForPeriod(ctx context.Context, orgID string, feature string, period string) (int64, error)
	RecordUsage(ctx context.Context, orgID string, feature string, quantity int64, period string) error
	GetSubscription(ctx context.Context, orgID string) (*Subscription, error)
	CreateSubscription(ctx context.Context, sub *Subscription) error
	UpdateSubscription(ctx context.Context, sub *Subscription) error

	// Billing — Stripe customer + plan-by-name lookups used by the
	// StartCheckout / OpenBillingPortal flows.
	GetOrgStripeCustomerID(ctx context.Context, orgID string) (string, error)
	SetOrgStripeCustomerID(ctx context.Context, orgID, stripeCustomerID string) error
	GetPlanByName(ctx context.Context, name string) (*PlanFull, error)

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
	RevokeSession(ctx context.Context, sessionID string, reason string) error
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
	UpdateWebhookDelivery(ctx context.Context, delivery *WebhookDelivery) error
	ListWebhookDeliveries(ctx context.Context, subscriptionID string, pageSize int) ([]*WebhookDelivery, error)
	GetPendingDeliveries(ctx context.Context, limit int) ([]*WebhookDelivery, error)

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
	ErrTypeNotFound StoreErrorType = "not_found"
	ErrTypeConflict StoreErrorType = "conflict"
	ErrTypeInternal StoreErrorType = "internal"
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
	ExpiresAt        time.Time
	RevokedAt        *time.Time
	RevokedReason    string
}
