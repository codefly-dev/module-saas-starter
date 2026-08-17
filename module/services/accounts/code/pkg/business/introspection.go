package business

import (
	"context"
	"sort"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// ServiceVersion is the semver of THIS service's API surface (the
// api service of the saas-starter module). Bump on proto-breaking
// changes; clients can branch on it via GetServiceInfo.
//
// Module-level version is a separate concern owned by the module
// declaration (module.codefly.yaml), not by this service.
const ServiceVersion = "0.4.0"

// GetServiceInfo returns the machine-readable catalog of THIS
// service: its RPC surface, RBAC vocabulary, RLS-protected tables,
// and accepted API-key scopes.
//
// Each codefly service owns its own proto and exposes its own
// IntrospectionService. A "module-level" view of saas-starter is an
// AGGREGATION concern: a CLI/gateway/Mind walks every service in
// the module and merges each service's GetServiceInfo response.
// That aggregator is intentionally out of scope here.
//
// Drift resistance: RPCs, HTTP routes, and enforcement policy are derived from
// the protobuf descriptor graph. Adding a method without a complete
// saas.policy.v1.method_policy option leaves it unclassified, so interceptors
// deny it and the completeness test fails. Only editorial descriptions remain
// in rpcDescriptions until P1-DOC-001 makes source comments compiler-readable.
//
// The redaction pass at the end strips platform_admin / mfa-tier
// RPCs for unauthenticated callers (the catalog is exposed publicly
// at GET /v1/.well-known/service-info; no need to advertise the
// privileged attack surface to anonymous probes).
func (s *Service) GetServiceInfo(ctx context.Context, _ *gen.GetServiceInfoRequest) (*gen.GetServiceInfoResponse, error) {
	rpcs := buildRPCList()
	if !callerIsAuthenticated(ctx) {
		rpcs = redactPrivilegedRPCs(rpcs)
	}
	return &gen.GetServiceInfoResponse{
		Capabilities: &gen.ServiceCapabilities{
			Info:        serviceInfo,
			Rpcs:        rpcs,
			Permissions: servicePermissions,
			RlsTables:   serviceRLSTables,
			Scopes:      serviceScopes,
		},
	}, nil
}

var serviceInfo = &gen.ServiceInfo{
	Name:        "accounts",
	Module:      "saas-starter",
	Version:     ServiceVersion,
	Description: "Tenant-facing API for saas-starter: auth, orgs, teams, RBAC, billing, webhooks, audit. Three-layer authz (handler gates + RBAC + Postgres RLS).",
	RepoUrl:     "https://github.com/codefly-dev/saas-starter",
}

// rpcDescriptions is the only hand-maintained per-method catalog data.
// Routing and enforcement metadata comes exclusively from protobuf method
// options; descriptions remain editorial prose until source comments are
// compiled into the service catalog.
var rpcDescriptions = withWorkContextConsumerDescriptions(map[string]string{
	"APIKeyService/CreateAPIKey":               "Mint an API key for an administered organization.",
	"APIKeyService/ListAPIKeys":                "List org's API keys.",
	"APIKeyService/RevokeAPIKey":               "Revoke an API key in an administered organization.",
	"APIKeyService/ValidateAPIKey":             "Internal: plaintext key → key + org id.",
	"AuditExportService/DeleteConfig":          "Stop exporting; clears cursor.",
	"AuditExportService/GetConfig":             "Read export config.",
	"AuditExportService/SaveConfig":            "Configure per-org S3 export.",
	"AuditService/ExportAuditLog":              "Download audit log as CSV/JSON.",
	"AuditService/QueryAuditLog":               "Read audit events (org member sees own org; platform admin sees all).",
	"AuthService/Authenticate":                 "Exchange typed OAuth-code or explicit fixture credentials for tokens.",
	"AuthService/BeginOAuth":                   "Mint signed OAuth state for an allowlisted redirect.",
	"AuthService/BeginWebAuthnMFAChallenge":    "Begin a WebAuthn assertion bound to an MFA login transaction.",
	"AuthService/CompleteMFAChallenge":         "Consume a one-use MFA login transaction and issue the session.",
	"AuthService/CompleteWebAuthnMFAChallenge": "Verify WebAuthn and atomically consume the MFA login transaction.",
	"AuthService/GetJWKS":                      "Public JWKS for JWT signature verification.",
	"AuthService/Logout":                       "Revoke session.",
	"AuthService/RefreshToken":                 "Refresh access token.",
	"AuthService/SwitchOrganization":           "Exchange the active device session for an access token scoped to another current membership.",
	"BillingService/ListInvoices":              "Past Stripe invoices.",
	"BillingService/ListPublicPlans":           "Sanitized public pricing and entitlement catalog.",
	"BillingService/OpenPortal":                "Stripe billing-portal session; requires billing:write and recent MFA.",
	"ConsentService/AcceptTerms":               "Record acceptance of the exact Terms version presented.",
	"ConsentService/GetStatus":                 "Read TOS acceptance state.",
	"ConsentService/UpdatePreferences":         "Persist purpose-based optional tracking choices and withdrawals.",
	"DelegationService/DecideDelegation":       "Approve or deny a delegation request.",
	"DelegationService/ListPendingDelegations": "List pending organization delegations.",
	"DelegationService/RequestDelegation":      "Request a scoped authority delegation.",
	"DelegationService/WaitForDelegation":      "Stream the terminal delegation decision.",
	"GDPRService/GetDeletionStatus":            "Status of a GDPR deletion request.",
	"GDPRService/GetExportStatus":              "Status of a GDPR export request.",
	"GDPRService/RequestDeletion":              "Request account deletion when a complete privacy workflow is configured. Requires MFA.",
	"GDPRService/RequestExport":                "Request data export when a complete privacy workflow is configured.",
	"IdentityService/ResolveIdentity":          "Internal: provider id → user/org/roles.",
	"IntrospectionService/GetServiceInfo":      "Self-describing service catalog (this RPC).",
	"InvitationService/AcceptInvitation":       "Accept an invite by token.",
	"InvitationService/CreateInvitation":       "Invite a user to an org.",
	"InvitationService/InspectInvitation":      "Resolve a privacy-limited invitation summary from a secret credential.",
	"InvitationService/InspectInvitationById":  "Resolve an authenticated invitee's invitation summary.",
	"InvitationService/ListInvitations":        "List pending invites for an org.",
	"InvitationService/ResendInvitation":       "Rotate and requeue a pending invitation after its cooldown.",
	"InvitationService/RevokeInvitation":       "Revoke a pending invite.",
	"MFAService/BeginWebAuthnRegistration":     "Begin passkey registration with server-side ceremony state.",
	"MFAService/FinishWebAuthnRegistration":    "Verify and persist a passkey credential.",
	"MFAService/GenerateBackupCodes":           "Mint backup codes (one-time view).",
	"MFAService/ListDevices":                   "List user's MFA devices.",
	"MFAService/RevokeDevice":                  "Remove an MFA device.",
	"MFAService/SetupTOTP":                     "Begin TOTP enrollment.",
	"MFAService/VerifyTOTP":                    "Confirm TOTP code; activate device.",
	"NotificationService/DeleteNotification":   "Delete one.",
	"NotificationService/GetUnreadCount":       "Count of unread.",
	"NotificationService/ListNotifications":    "List the caller's notifications.",
	"NotificationService/MarkAllRead":          "Mark all read.",
	"NotificationService/MarkRead":             "Mark one read.",
	"OnboardingService/CompleteStep":           "Confirm a step only after its represented product state exists.",
	"OnboardingService/GetProgress":            "Versioned organization activation checklist for the caller.",
	"OnboardingService/SkipStep":               "Record an explicit skip for an optional step.",
	"OrganizationService/AddMember":            "Add a user to an org.",
	"OrganizationService/CreateOrganization":   "Create a new org; caller becomes owner.",
	"OrganizationService/GetOrgSettings":       "Read branding.",
	"OrganizationService/GetOrganization":      "Read an org.",
	"OrganizationService/ListMembers":          "List members of an org.",
	"OrganizationService/ListOrganizations":    "Orgs the caller belongs to.",
	"OrganizationService/RemoveMember":         "Remove a member; last-admin guard.",
	"OrganizationService/UpdateOrgSettings":    "Update branding (logo, color, custom domain).",
	"PermissionService/AssignRole":             "Grant a role to a principal/team.",
	"PermissionService/CheckPermission":        "Internal authz decision (auth-sidecar caller).",
	"PermissionService/CreateRole":             "Create a role (org-scoped or platform).",
	"PermissionService/Decide":                 "Internal principal-aware authz decision (successor to CheckPermission).",
	"PermissionService/DeleteRole":             "Delete a custom role.",
	"PermissionService/ListRoleAssignments":    "List assignments in an org.",
	"PermissionService/ListRoles":              "List built-in + org-scoped roles.",
	"PermissionService/RevokeRole":             "Revoke a role assignment.",
	"PlatformAdminService/GetOrgEntitlements":  "Plan + overrides + usage.",
	"PlatformAdminService/GetJob":              "Payload-free job metadata, attempts, and state history.",
	"PlatformAdminService/GetJobOperations":    "Durable queue depth, readiness, and lease-health snapshots.",
	"PlatformAdminService/GrantPlatformRole":   "Grant a platform role.",
	"PlatformAdminService/ImpersonateUser":     "Mint an impersonation session.",
	"PlatformAdminService/ListActiveSessions":  "Active sessions for a user.",
	"PlatformAdminService/ListFeatureFlags":    "List the legacy feature-flag migration inventory.",
	"PlatformAdminService/UpsertFeatureFlag":   "Deprecated compatibility method; always rejects writes to the legacy feature-flag inventory.",
	"PlatformAdminService/ListJobs":            "Seek-paginated payload-free job operations view.",
	"PlatformAdminService/ListPlatformAdmins":  "List platform admins.",
	"PlatformAdminService/OverrideEntitlement": "Per-org limit override.",
	"PlatformAdminService/RevokePlatformRole":  "Revoke a platform role.",
	"PlatformAdminService/RevokeSession":       "Revoke a single session.",
	"PlatformAdminService/ReplayJob":           "Idempotently copy dead-lettered work for another attempt.",
	"PlatformAdminService/SearchUsers":         "Search across all users.",
	"PlatformAdminService/SuspendUser":         "Suspend a user account.",
	"PlatformAdminService/UnsuspendUser":       "Restore a suspended user.",
	"PrincipalService/CreateAgentPrincipal":    "Create an agent principal in an organization.",
	"PrincipalService/GetAgentPrincipal":       "Internal agent-principal lookup.",
	"PrincipalService/GetPrincipal":            "Internal principal lookup.",
	"PrincipalService/ListPrincipals":          "List principals in an organization.",
	"PrincipalService/RevokePrincipal":         "Revoke an organization or platform principal.",
	"SSOAdminService/Disable":                  "Pause SSO; preserves WorkOS state for re-enable.",
	"SSOAdminService/GetSSO":                   "Read org SSO state.",
	"SSOAdminService/StartSetup":               "Mint WorkOS portal link.",
	"TeamService/AddMember":                    "Add a user to a team.",
	"TeamService/CreateTeam":                   "Create a team within an org.",
	"TeamService/DeleteTeam":                   "Delete a team.",
	"TeamService/ListMembers":                  "List team members.",
	"TeamService/ListTeams":                    "List teams in an org.",
	"TeamService/RemoveMember":                 "Remove a user from a team.",
	"TeamService/UpdateTeam":                   "Update a team's name or description.",
	"UserService/AddIdentity":                  "Link an additional auth provider.",
	"UserService/DeleteUser":                   "Soft-delete a user (self or platform admin).",
	"UserService/FindUserByIdentity":           "Platform-admin identity lookup.",
	"UserService/GetSelf":                      "Authenticated user + their orgs.",
	"UserService/GetUser":                      "Look up a user (self or platform admin).",
	"UserService/ListUserIdentities":           "List a user's auth methods.",
	"UserService/ListUsers":                    "Paginated user list (platform admin).",
	"UserService/RegisterUser":                 "Bootstrap a new user + personal org.",
	"UserService/UpdateUser":                   "Update profile (self or platform admin).",
	"UserService/Version":                      "Service version (smoke test).",
	"UsageService/ConsumeUsage":                "Atomically consume a monthly meter with an idempotent receipt.",
	"UsageService/GetUsageHistory":             "UTC hourly, daily, or monthly usage buckets for a bounded range.",
	"UsageService/GetUsage":                    "Current monthly meter total and effective limit.",
	"UsageService/ListUsageMeters":             "Customer-visible meter catalog with current totals and limits.",
	"WorkContextService/ExchangeAudience":      "Reissue one Task and Session lineage for another audience with attenuated authority.",
	"WorkContextService/StartChildSession":     "Exchange a current Work Context for an attenuated child-agent Session.",
	"WorkContextService/StartRootSession":      "Exchange a current Work Context for another root Session under the same Task.",
	"WorkContextService/StartTask":             "Issue a signed Work Context for a new Task and root Session.",
	"UserSettingsService/Get":                  "JSONB user prefs.",
	"UserSettingsService/Update":               "Patch user prefs.",
	"WaitlistService/GetAcquisitionStatus":     "Read the configured signup and waitlist mode.",
	"WaitlistService/Invite":                   "Queue the approved signup handoff for one waitlist entry.",
	"WaitlistService/Join":                     "Submit an enumeration-safe public waitlist request.",
	"WaitlistService/List":                     "Search and filter the waitlist as a platform administrator.",
	"WaitlistService/Review":                   "Approve or reject a waitlist entry with audit context.",
	"WaitlistService/Verify":                   "Verify a waitlist email using an expiring hashed credential.",
	"WebhookService/CreateSubscription":        "Create a public-HTTPS endpoint and reveal its encrypted-at-rest signing secret once.",
	"WebhookService/DeleteSubscription":        "Delete a subscription.",
	"WebhookService/GetDelivery":               "Read one delivery.",
	"WebhookService/ListDeliveries":            "Past delivery attempts.",
	"WebhookService/ListSubscriptions":         "List webhook subscriptions.",
	"WebhookService/ReplayDelivery":            "Create and audit a new attempt for a past delivery using its stable event ID.",
	"WebhookService/RotateSecret":              "Rotate the reveal-once signing secret with bounded dual-signature overlap. Requires recent MFA.",
	"WebhookService/TestWebhook":               "Send a test ping.",
})

func withWorkContextConsumerDescriptions(descriptions map[string]string) map[string]string {
	descriptions["WorkContextService/AuthorizeEvidenceRead"] =
		"Authorize a filtered Evidence read from current tenant membership and RBAC facts."
	descriptions["WorkContextService/CheckAuthorizationRevision"] =
		"Revalidate every subject and scope in a signed Work Context against current authority."
	return descriptions
}

// buildRPCList enumerates every (Service, Method) and its policy from protobuf
// descriptors. Stable sort by (service, method) makes the response deterministic.
func buildRPCList() []*gen.RPCInfo {
	policies := RPCPolicies()
	out := make([]*gen.RPCInfo, 0, len(policies))
	for _, policy := range policies {
		out = append(out, &gen.RPCInfo{
			Service:      policy.Service,
			Method:       policy.Method,
			HttpMethod:   policy.HTTPMethod,
			HttpPath:     policy.HTTPPath,
			Description:  policy.Description,
			Scopes:       policy.Scopes,
			HandlerAuthz: string(policy.Tier),
			EmitsAudit:   policy.EmitsAudit,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// callerIsAuthenticated reads the wool ctx for a stamped UserAuthID.
// The auth interceptor (gRPC + Connect) populates it after token
// validation. Empty / missing → unauthenticated.
func callerIsAuthenticated(ctx context.Context) bool {
	id, ok := wool.Get(ctx).UserAuthID()
	return ok && id != ""
}

// redactPrivilegedRPCs strips RPCs whose handler authz tier is
// platform_admin or mfa from the response. They're not secret —
// anyone with the binary can grep them — but unauthenticated probes
// shouldn't get a free attack-surface map. The full list remains
// visible to authenticated callers.
func redactPrivilegedRPCs(in []*gen.RPCInfo) []*gen.RPCInfo {
	out := make([]*gen.RPCInfo, 0, len(in))
	for _, r := range in {
		if r.HandlerAuthz == "platform_admin" || r.HandlerAuthz == "mfa" || r.HandlerAuthz == "internal" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// serviceRLSTables — RLS-protected tables this api service depends
// on. The tables themselves live in the store service's schema (via
// store/migrations); this api is the enforcer that wraps every
// per-tenant Store call in WithOrgTx / WithControlPlane.
//
// Source: store migrations through 60.
var serviceRLSTables = []*gen.RLSPolicyInfo{
	{Table: "audit_export_configs", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "webhook_subscriptions", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "webhook_deliveries", PolicyShape: "join", FailClosed: true, ScopeColumn: "subscription_id", Notes: "JOIN walks subscription_id → webhook_subscriptions.org_id"},
	{Table: "api_keys", PolicyShape: "direct", FailClosed: true, ScopeColumn: "organization_id"},
	{Table: "org_settings", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "invitations", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "organization_activations", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "organization_members", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "subscriptions", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "entitlement_overrides", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "usage_events", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id", Notes: "Immutable accepted/rejected usage attempt ledger."},
	{Table: "usage_totals", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id", Notes: "Transactionally maintained monthly meter aggregates."},
	{Table: "teams", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "team_members", PolicyShape: "join", FailClosed: true, ScopeColumn: "team_id", Notes: "JOIN walks team_id → teams.org_id"},
	{Table: "audit_events", PolicyShape: "polymorphic", FailClosed: true, ScopeColumn: "org_id", Notes: "NULL org_id rows visible only via WithControlPlane (system events)."},
	{Table: "roles", PolicyShape: "polymorphic", FailClosed: true, ScopeColumn: "org_id", Notes: "Built-in roles (org_id IS NULL) globally readable; tenant rows scoped."},
	{Table: "role_assignments", PolicyShape: "polymorphic", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "organizations", PolicyShape: "self_referential", FailClosed: true, ScopeColumn: "id"},
	{Table: "mfa_devices", PolicyShape: "direct", FailClosed: true, ScopeColumn: "user_id"},
	{Table: "mfa_backup_codes", PolicyShape: "direct", FailClosed: true, ScopeColumn: "user_id"},
	{Table: "mfa_login_transactions", PolicyShape: "direct", FailClosed: true, ScopeColumn: "user_id", Notes: "Public completion uses exact opaque-token hash lookup under audited bypass."},
	{Table: "webauthn_credentials", PolicyShape: "direct", FailClosed: true, ScopeColumn: "user_id", Notes: "Complete credential record is Vault-encrypted; public credential ID is unique."},
	{Table: "webauthn_ceremonies", PolicyShape: "direct", FailClosed: true, ScopeColumn: "user_id", Notes: "Short-lived server-side state; login ceremonies are bound to an MFA login transaction."},
	{Table: "waitlist_entries", PolicyShape: "control_plane", FailClosed: true, ScopeColumn: "id", Notes: "Public writes and platform administration use bounded service operations under the control-plane role."},
	{Table: "user_consent_preferences", PolicyShape: "direct", FailClosed: true, ScopeColumn: "user_id"},
	{Table: "user_consent_events", PolicyShape: "direct", FailClosed: true, ScopeColumn: "user_id"},
}
