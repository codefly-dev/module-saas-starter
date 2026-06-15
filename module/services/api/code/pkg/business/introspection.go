package business

import (
	"context"
	"sort"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/grpc"

	"api/pkg/gen"
)

// ServiceVersion is the semver of THIS service's API surface (the
// api service of the saas-starter module). Bump on proto-breaking
// changes; clients can branch on it via GetServiceInfo.
//
// Module-level version is a separate concern owned by the module
// declaration (module.codefly.yaml), not by this service.
const ServiceVersion = "0.1.0"

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
// Drift resistance: the RPC LIST is derived from the gRPC service
// descriptors generated from proto/api.proto — adding/removing a
// proto RPC mechanically changes the response. We hand-maintain
// only the per-RPC METADATA (handler_authz, scopes, emits_audit,
// http_*) in `rpcMetadata`. If a new proto RPC lands without
// metadata, it shows up with empty fields and a test in
// introspection_test.go fails — loud, not silent.
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
	Name:        "api",
	Module:      "saas-starter",
	Version:     ServiceVersion,
	Description: "Tenant-facing API for saas-starter: auth, orgs, teams, RBAC, billing, webhooks, audit. Three-layer authz (handler gates + RBAC + Postgres RLS).",
	RepoUrl:     "https://github.com/codefly-dev/saas-starter",
}

// rpcMeta is the hand-maintained metadata for one (Service, Method).
// Service+Method are the dictionary key (see rpcMetadata below).
type rpcMeta struct {
	HTTPMethod   string   // "GET" | "POST" | "DELETE" | "PUT"
	HTTPPath     string   // grpc-gateway annotation, e.g. "/v1/roles"
	Description  string   // one-line summary
	Scopes       []string // required scopes for API-key callers
	HandlerAuthz string   // "public"|"auth"|"org_member"|"org_admin"|"platform_admin"|"mfa"
	EmitsAudit   bool
}

// rpcMetadata maps "<Service>/<Method>" → metadata. Add an entry
// here for every new proto RPC. The build pass below loops the gRPC
// service descriptors and joins this map; missing entries surface
// as RPCs with empty fields in the catalog (the drift-guard test
// catches that).
//
// Keep in sync with adapters/rpcs.go handler-level checks.
var rpcMetadata = map[string]rpcMeta{
	// ============== IntrospectionService ==============
	"IntrospectionService/GetServiceInfo": {HTTPMethod: "GET", HTTPPath: "/v1/.well-known/service-info", Description: "Self-describing service catalog (this RPC).", HandlerAuthz: "public"},

	// ============== AuthService ==============
	"AuthService/Authenticate": {HTTPMethod: "POST", HTTPPath: "/v1/auth/authenticate", Description: "Dev/fixture login (provider+id → tokens). Production uses BeginOAuth.", HandlerAuthz: "public"},
	"AuthService/BeginOAuth":   {HTTPMethod: "POST", HTTPPath: "/v1/auth/oauth/begin", Description: "Mint OAuth state for a redirect.", HandlerAuthz: "public"},
	"AuthService/RefreshToken": {HTTPMethod: "POST", HTTPPath: "/v1/auth/refresh", Description: "Refresh access token.", HandlerAuthz: "public"},
	"AuthService/Logout":       {HTTPMethod: "POST", HTTPPath: "/v1/auth/logout", Description: "Revoke session.", HandlerAuthz: "public"},
	"AuthService/GetJWKS":      {HTTPMethod: "GET", HTTPPath: "/v1/auth/.well-known/jwks.json", Description: "Public JWKS for JWT signature verification.", HandlerAuthz: "public"},

	// ============== UserService ==============
	"UserService/Version":            {HTTPMethod: "GET", HTTPPath: "/v1/version", Description: "Service version (smoke test).", HandlerAuthz: "public"},
	"UserService/GetSelf":            {HTTPMethod: "GET", HTTPPath: "/v1/users/self", Description: "Authenticated user + their orgs.", HandlerAuthz: "auth"},
	"UserService/RegisterUser":       {HTTPMethod: "POST", HTTPPath: "/v1/users/register", Description: "Bootstrap a new user + personal org.", HandlerAuthz: "public", EmitsAudit: true},
	"UserService/GetUser":            {HTTPMethod: "GET", HTTPPath: "/v1/users/{uuid}", Description: "Look up a user (self or platform admin).", HandlerAuthz: "auth"},
	"UserService/ListUsers":          {HTTPMethod: "GET", HTTPPath: "/v1/users", Description: "Paginated user list (platform admin).", HandlerAuthz: "platform_admin"},
	"UserService/UpdateUser":         {HTTPMethod: "PATCH", HTTPPath: "/v1/users/{uuid}", Description: "Update profile (self or platform admin).", HandlerAuthz: "auth", EmitsAudit: true},
	"UserService/DeleteUser":         {HTTPMethod: "DELETE", HTTPPath: "/v1/users/{uuid}", Description: "Soft-delete a user (self or platform admin).", HandlerAuthz: "auth", EmitsAudit: true},
	"UserService/AddIdentity":        {HTTPMethod: "POST", HTTPPath: "/v1/users/{user_uuid}/identities", Description: "Link an additional auth provider.", HandlerAuthz: "auth"},
	"UserService/FindUserByIdentity": {HTTPMethod: "GET", HTTPPath: "/v1/identities/find", Description: "Internal lookup (auth-sidecar).", HandlerAuthz: "public"},
	"UserService/ListUserIdentities": {HTTPMethod: "GET", HTTPPath: "/v1/users/{user_uuid}/identities", Description: "List a user's auth methods.", HandlerAuthz: "auth"},

	// ============== OrganizationService ==============
	"OrganizationService/CreateOrganization": {HTTPMethod: "POST", HTTPPath: "/v1/organizations", Description: "Create a new org; caller becomes owner.", HandlerAuthz: "auth", EmitsAudit: true},
	"OrganizationService/GetOrganization":    {HTTPMethod: "GET", HTTPPath: "/v1/organizations/{id}", Description: "Read an org.", HandlerAuthz: "org_member"},
	"OrganizationService/ListOrganizations":  {HTTPMethod: "GET", HTTPPath: "/v1/organizations", Description: "Orgs the caller belongs to.", HandlerAuthz: "auth"},
	"OrganizationService/AddMember":          {HTTPMethod: "POST", HTTPPath: "/v1/organizations/{org_id}/members", Description: "Add a user to an org.", HandlerAuthz: "org_admin", EmitsAudit: true},
	"OrganizationService/RemoveMember":       {HTTPMethod: "DELETE", HTTPPath: "/v1/organizations/{org_id}/members/{user_id}", Description: "Remove a member; last-admin guard.", HandlerAuthz: "org_admin", EmitsAudit: true},
	"OrganizationService/ListMembers":        {HTTPMethod: "GET", HTTPPath: "/v1/organizations/{org_id}/members", Description: "List members of an org.", HandlerAuthz: "org_member"},
	"OrganizationService/UpdateOrgSettings":  {HTTPMethod: "PUT", HTTPPath: "/v1/organizations/{org_id}/settings", Description: "Update branding (logo, color, custom domain).", HandlerAuthz: "org_admin", EmitsAudit: true},
	"OrganizationService/GetOrgSettings":     {HTTPMethod: "GET", HTTPPath: "/v1/organizations/{org_id}/settings", Description: "Read branding.", HandlerAuthz: "org_member"},

	// ============== TeamService ==============
	"TeamService/CreateTeam":   {HTTPMethod: "POST", HTTPPath: "/v1/organizations/{org_id}/teams", Description: "Create a team within an org.", HandlerAuthz: "org_admin", EmitsAudit: true},
	"TeamService/ListTeams":    {HTTPMethod: "GET", HTTPPath: "/v1/organizations/{org_id}/teams", Description: "List teams in an org.", HandlerAuthz: "org_member"},
	"TeamService/AddMember":    {HTTPMethod: "POST", HTTPPath: "/v1/teams/{team_id}/members", Description: "Add a user to a team.", HandlerAuthz: "org_admin", EmitsAudit: true},
	"TeamService/RemoveMember": {HTTPMethod: "DELETE", HTTPPath: "/v1/teams/{team_id}/members/{user_id}", Description: "Remove a user from a team.", HandlerAuthz: "org_admin", EmitsAudit: true},
	"TeamService/ListMembers":  {HTTPMethod: "GET", HTTPPath: "/v1/teams/{team_id}/members", Description: "List team members.", HandlerAuthz: "org_member"},

	// ============== PermissionService ==============
	"PermissionService/CreateRole":          {HTTPMethod: "POST", HTTPPath: "/v1/roles", Description: "Create a role (org-scoped or platform).", HandlerAuthz: "org_admin", EmitsAudit: true},
	"PermissionService/ListRoles":           {HTTPMethod: "GET", HTTPPath: "/v1/roles", Description: "List built-in + org-scoped roles.", HandlerAuthz: "auth"},
	"PermissionService/DeleteRole":          {HTTPMethod: "DELETE", HTTPPath: "/v1/roles/{id}", Description: "Delete a custom role.", HandlerAuthz: "platform_admin", EmitsAudit: true},
	"PermissionService/AssignRole":          {HTTPMethod: "POST", HTTPPath: "/v1/role-assignments", Description: "Grant a role to a user/team.", HandlerAuthz: "org_admin", EmitsAudit: true},
	"PermissionService/RevokeRole":          {HTTPMethod: "DELETE", HTTPPath: "/v1/role-assignments", Description: "Revoke a role assignment.", HandlerAuthz: "org_admin", EmitsAudit: true},
	"PermissionService/ListRoleAssignments": {HTTPMethod: "GET", HTTPPath: "/v1/role-assignments", Description: "List assignments in an org.", HandlerAuthz: "org_member"},
	"PermissionService/CheckPermission":     {HTTPMethod: "POST", HTTPPath: "/v1/permissions:check", Description: "Internal authz decision (auth-sidecar caller).", HandlerAuthz: "public"},
	"PermissionService/Decide":              {HTTPMethod: "POST", HTTPPath: "/v1/permissions:decide", Description: "Principal-aware authz decision (M2; successor to CheckPermission).", HandlerAuthz: "public"},

	// ============== IdentityService ==============
	"IdentityService/ResolveIdentity": {HTTPMethod: "POST", HTTPPath: "/v1/identity:resolve", Description: "Internal: provider id → user/org/roles.", HandlerAuthz: "public"},

	// ============== APIKeyService ==============
	"APIKeyService/CreateAPIKey":   {HTTPMethod: "POST", HTTPPath: "/v1/api-keys", Description: "Mint an API key with scopes.", HandlerAuthz: "auth", Scopes: []string{"api_keys:write"}, EmitsAudit: true},
	"APIKeyService/ListAPIKeys":    {HTTPMethod: "GET", HTTPPath: "/v1/api-keys", Description: "List org's API keys.", HandlerAuthz: "org_member", Scopes: []string{"api_keys:read"}},
	"APIKeyService/RevokeAPIKey":   {HTTPMethod: "DELETE", HTTPPath: "/v1/api-keys/{id}", Description: "Revoke an API key.", HandlerAuthz: "platform_admin", Scopes: []string{"api_keys:write"}, EmitsAudit: true},
	"APIKeyService/ValidateAPIKey": {HTTPMethod: "POST", HTTPPath: "/v1/api-keys:validate", Description: "Internal: plaintext key → key + org id.", HandlerAuthz: "public"},

	// ============== AuditService ==============
	"AuditService/QueryAuditLog":  {HTTPMethod: "GET", HTTPPath: "/v1/audit-log", Description: "Read audit events (org member sees own org; platform admin sees all).", HandlerAuthz: "org_member", Scopes: []string{"audit:read"}},
	"AuditService/ExportAuditLog": {HTTPMethod: "GET", HTTPPath: "/v1/audit-log/export", Description: "Download audit log as CSV/JSON.", HandlerAuthz: "org_admin", Scopes: []string{"audit:read"}},

	// ============== AuditExportService ==============
	"AuditExportService/SaveConfig":   {HTTPMethod: "PUT", HTTPPath: "/v1/audit-export/config", Description: "Configure per-org S3 export.", HandlerAuthz: "org_admin", EmitsAudit: true},
	"AuditExportService/GetConfig":    {HTTPMethod: "GET", HTTPPath: "/v1/audit-export/config", Description: "Read export config.", HandlerAuthz: "org_admin"},
	"AuditExportService/DeleteConfig": {HTTPMethod: "DELETE", HTTPPath: "/v1/audit-export/config", Description: "Stop exporting; clears cursor.", HandlerAuthz: "org_admin", EmitsAudit: true},

	// ============== InvitationService ==============
	"InvitationService/CreateInvitation": {HTTPMethod: "POST", HTTPPath: "/v1/invitations", Description: "Invite a user to an org.", HandlerAuthz: "org_admin", EmitsAudit: true},
	"InvitationService/AcceptInvitation": {HTTPMethod: "POST", HTTPPath: "/v1/invitations:accept", Description: "Accept an invite by token.", HandlerAuthz: "auth", EmitsAudit: true},
	"InvitationService/ListInvitations":  {HTTPMethod: "GET", HTTPPath: "/v1/invitations", Description: "List pending invites for an org.", HandlerAuthz: "org_admin"},
	"InvitationService/RevokeInvitation": {HTTPMethod: "DELETE", HTTPPath: "/v1/invitations/{id}", Description: "Revoke a pending invite.", HandlerAuthz: "org_admin", EmitsAudit: true},

	// ============== WebhookService ==============
	"WebhookService/CreateSubscription": {HTTPMethod: "POST", HTTPPath: "/v1/webhooks", Description: "Subscribe to events.", HandlerAuthz: "org_admin", Scopes: []string{"webhooks:write"}, EmitsAudit: true},
	"WebhookService/ListSubscriptions":  {HTTPMethod: "GET", HTTPPath: "/v1/webhooks", Description: "List webhook subscriptions.", HandlerAuthz: "org_member", Scopes: []string{"webhooks:read"}},
	"WebhookService/DeleteSubscription": {HTTPMethod: "DELETE", HTTPPath: "/v1/webhooks/{id}", Description: "Delete a subscription.", HandlerAuthz: "org_admin", Scopes: []string{"webhooks:write"}, EmitsAudit: true},
	"WebhookService/ListDeliveries":     {HTTPMethod: "GET", HTTPPath: "/v1/webhooks/{subscription_id}/deliveries", Description: "Past delivery attempts.", HandlerAuthz: "org_member", Scopes: []string{"webhooks:read"}},
	"WebhookService/GetDelivery":        {HTTPMethod: "GET", HTTPPath: "/v1/webhooks/deliveries/{id}", Description: "Read one delivery.", HandlerAuthz: "org_member", Scopes: []string{"webhooks:read"}},
	"WebhookService/ReplayDelivery":     {HTTPMethod: "POST", HTTPPath: "/v1/webhooks/deliveries/{id}:replay", Description: "Re-send a past delivery.", HandlerAuthz: "org_admin", Scopes: []string{"webhooks:write"}, EmitsAudit: true},
	"WebhookService/TestWebhook":        {HTTPMethod: "POST", HTTPPath: "/v1/webhooks/{id}:test", Description: "Send a test ping.", HandlerAuthz: "org_admin", Scopes: []string{"webhooks:write"}},
	"WebhookService/RotateSecret":       {HTTPMethod: "POST", HTTPPath: "/v1/webhooks/{id}:rotateSecret", Description: "Rotate signing secret. Requires MFA.", HandlerAuthz: "mfa", Scopes: []string{"webhooks:write"}, EmitsAudit: true},

	// ============== NotificationService ==============
	"NotificationService/ListNotifications":  {HTTPMethod: "GET", HTTPPath: "/v1/notifications", Description: "List the caller's notifications.", HandlerAuthz: "auth"},
	"NotificationService/GetUnreadCount":     {HTTPMethod: "GET", HTTPPath: "/v1/notifications/unread-count", Description: "Count of unread.", HandlerAuthz: "auth"},
	"NotificationService/MarkRead":           {HTTPMethod: "POST", HTTPPath: "/v1/notifications/{id}:read", Description: "Mark one read.", HandlerAuthz: "auth"},
	"NotificationService/MarkAllRead":        {HTTPMethod: "POST", HTTPPath: "/v1/notifications:mark-all-read", Description: "Mark all read.", HandlerAuthz: "auth"},
	"NotificationService/DeleteNotification": {HTTPMethod: "DELETE", HTTPPath: "/v1/notifications/{id}", Description: "Delete one.", HandlerAuthz: "auth"},

	// ============== OnboardingService ==============
	"OnboardingService/GetProgress":  {HTTPMethod: "GET", HTTPPath: "/v1/onboarding/progress", Description: "Wizard step status for the caller.", HandlerAuthz: "auth"},
	"OnboardingService/CompleteStep": {HTTPMethod: "POST", HTTPPath: "/v1/onboarding/steps/{step_name}:complete", Description: "Mark a step done.", HandlerAuthz: "auth"},
	"OnboardingService/SkipStep":     {HTTPMethod: "POST", HTTPPath: "/v1/onboarding/steps/{step_name}:skip", Description: "Mark a step skipped.", HandlerAuthz: "auth"},

	// ============== GDPRService ==============
	"GDPRService/RequestExport":     {HTTPMethod: "POST", HTTPPath: "/v1/gdpr/export", Description: "Request data export. Async.", HandlerAuthz: "auth", EmitsAudit: true},
	"GDPRService/GetExportStatus":   {HTTPMethod: "GET", HTTPPath: "/v1/gdpr/export/{id}", Description: "Status of a GDPR export request.", HandlerAuthz: "auth"},
	"GDPRService/RequestDeletion":   {HTTPMethod: "POST", HTTPPath: "/v1/gdpr/delete", Description: "Request account deletion. Requires MFA.", HandlerAuthz: "mfa", EmitsAudit: true},
	"GDPRService/GetDeletionStatus": {HTTPMethod: "GET", HTTPPath: "/v1/gdpr/delete/{id}", Description: "Status of a GDPR deletion request.", HandlerAuthz: "auth"},

	// ============== ConsentService ==============
	"ConsentService/GetStatus": {HTTPMethod: "GET", HTTPPath: "/v1/consent", Description: "Read TOS acceptance state.", HandlerAuthz: "auth"},
	"ConsentService/Accept":    {HTTPMethod: "POST", HTTPPath: "/v1/consent:accept", Description: "Record TOS acceptance.", HandlerAuthz: "auth", EmitsAudit: true},

	// ============== SSOAdminService ==============
	"SSOAdminService/GetSSO":     {HTTPMethod: "GET", HTTPPath: "/v1/sso", Description: "Read org SSO state.", HandlerAuthz: "org_admin"},
	"SSOAdminService/StartSetup": {HTTPMethod: "POST", HTTPPath: "/v1/sso/setup", Description: "Mint WorkOS portal link.", HandlerAuthz: "org_admin", EmitsAudit: true},
	"SSOAdminService/Disable":    {HTTPMethod: "POST", HTTPPath: "/v1/sso:disable", Description: "Pause SSO; preserves WorkOS state for re-enable.", HandlerAuthz: "org_admin", EmitsAudit: true},

	// ============== BillingService ==============
	"BillingService/StartCheckout": {HTTPMethod: "POST", HTTPPath: "/v1/billing/checkout", Description: "Create Stripe checkout session.", HandlerAuthz: "mfa", Scopes: []string{"billing:write"}, EmitsAudit: true},
	"BillingService/OpenPortal":    {HTTPMethod: "POST", HTTPPath: "/v1/billing/connect/portal", Description: "Stripe billing-portal session.", HandlerAuthz: "mfa", Scopes: []string{"billing:write"}, EmitsAudit: true},
	"BillingService/ListInvoices":  {HTTPMethod: "GET", HTTPPath: "/v1/billing/invoices", Description: "Past Stripe invoices.", HandlerAuthz: "org_admin", Scopes: []string{"billing:read"}},

	// ============== UserSettingsService ==============
	"UserSettingsService/Get":    {HTTPMethod: "GET", HTTPPath: "/v1/users/self/settings", Description: "JSONB user prefs.", HandlerAuthz: "auth"},
	"UserSettingsService/Update": {HTTPMethod: "PATCH", HTTPPath: "/v1/users/self/settings", Description: "Patch user prefs.", HandlerAuthz: "auth"},

	// ============== MFAService ==============
	"MFAService/SetupTOTP":           {HTTPMethod: "POST", HTTPPath: "/v1/mfa/totp/setup", Description: "Begin TOTP enrollment.", HandlerAuthz: "auth", EmitsAudit: true},
	"MFAService/VerifyTOTP":          {HTTPMethod: "POST", HTTPPath: "/v1/mfa/totp/verify", Description: "Confirm TOTP code; activate device.", HandlerAuthz: "auth", EmitsAudit: true},
	"MFAService/ListDevices":         {HTTPMethod: "GET", HTTPPath: "/v1/mfa/devices", Description: "List user's MFA devices.", HandlerAuthz: "auth"},
	"MFAService/RevokeDevice":        {HTTPMethod: "DELETE", HTTPPath: "/v1/mfa/devices/{id}", Description: "Remove an MFA device.", HandlerAuthz: "auth", EmitsAudit: true},
	"MFAService/GenerateBackupCodes": {HTTPMethod: "POST", HTTPPath: "/v1/mfa/backup-codes", Description: "Mint backup codes (one-time view).", HandlerAuthz: "auth", EmitsAudit: true},

	// ============== PlatformAdminService ==============
	"PlatformAdminService/SuspendUser":         {HTTPMethod: "POST", HTTPPath: "/v1/platform/users/{user_id}:suspend", Description: "Suspend a user account.", HandlerAuthz: "platform_admin", EmitsAudit: true},
	"PlatformAdminService/UnsuspendUser":       {HTTPMethod: "POST", HTTPPath: "/v1/platform/users/{user_id}:unsuspend", Description: "Restore a suspended user.", HandlerAuthz: "platform_admin", EmitsAudit: true},
	"PlatformAdminService/ImpersonateUser":     {HTTPMethod: "POST", HTTPPath: "/v1/platform/users/{user_id}:impersonate", Description: "Mint an impersonation session.", HandlerAuthz: "platform_admin", EmitsAudit: true},
	"PlatformAdminService/SearchUsers":         {HTTPMethod: "GET", HTTPPath: "/v1/platform/users:search", Description: "Search across all users.", HandlerAuthz: "platform_admin"},
	"PlatformAdminService/ListActiveSessions":  {HTTPMethod: "GET", HTTPPath: "/v1/platform/users/{user_id}/sessions", Description: "Active sessions for a user.", HandlerAuthz: "platform_admin"},
	"PlatformAdminService/RevokeSession":       {HTTPMethod: "DELETE", HTTPPath: "/v1/platform/sessions/{id}", Description: "Revoke a single session.", HandlerAuthz: "platform_admin", EmitsAudit: true},
	"PlatformAdminService/RevokeAllSessions":   {HTTPMethod: "DELETE", HTTPPath: "/v1/platform/users/{user_id}/sessions", Description: "Revoke all sessions for a user.", HandlerAuthz: "platform_admin", EmitsAudit: true},
	"PlatformAdminService/GrantPlatformRole":   {HTTPMethod: "POST", HTTPPath: "/v1/platform/admins", Description: "Grant a platform role.", HandlerAuthz: "platform_admin", EmitsAudit: true},
	"PlatformAdminService/RevokePlatformRole":  {HTTPMethod: "DELETE", HTTPPath: "/v1/platform/admins/{user_id}", Description: "Revoke a platform role.", HandlerAuthz: "platform_admin", EmitsAudit: true},
	"PlatformAdminService/ListPlatformAdmins":  {HTTPMethod: "GET", HTTPPath: "/v1/platform/admins", Description: "List platform admins.", HandlerAuthz: "platform_admin"},
	"PlatformAdminService/GetOrgEntitlements":  {HTTPMethod: "GET", HTTPPath: "/v1/platform/orgs/{org_id}/entitlements", Description: "Plan + overrides + usage.", HandlerAuthz: "platform_admin"},
	"PlatformAdminService/OverrideEntitlement": {HTTPMethod: "POST", HTTPPath: "/v1/platform/entitlements:override", Description: "Per-org limit override.", HandlerAuthz: "platform_admin", EmitsAudit: true},
	"PlatformAdminService/ListFeatureFlags":    {HTTPMethod: "GET", HTTPPath: "/v1/platform/feature-flags", Description: "List platform feature flags.", HandlerAuthz: "platform_admin"},
	"PlatformAdminService/UpsertFeatureFlag":   {HTTPMethod: "PUT", HTTPPath: "/v1/platform/feature-flags", Description: "Create / update a feature flag.", HandlerAuthz: "platform_admin", EmitsAudit: true},
}

// allServiceDescs returns every gRPC service descriptor THIS service
// (api) exposes. Used by buildRPCList to enumerate the surface.
func allServiceDescs() []*grpc.ServiceDesc {
	return []*grpc.ServiceDesc{
		&gen.IntrospectionService_ServiceDesc,
		&gen.AuthService_ServiceDesc,
		&gen.UserService_ServiceDesc,
		&gen.OrganizationService_ServiceDesc,
		&gen.TeamService_ServiceDesc,
		&gen.PermissionService_ServiceDesc,
		&gen.IdentityService_ServiceDesc,
		&gen.APIKeyService_ServiceDesc,
		&gen.AuditService_ServiceDesc,
		&gen.AuditExportService_ServiceDesc,
		&gen.InvitationService_ServiceDesc,
		&gen.WebhookService_ServiceDesc,
		&gen.NotificationService_ServiceDesc,
		&gen.OnboardingService_ServiceDesc,
		&gen.GDPRService_ServiceDesc,
		&gen.ConsentService_ServiceDesc,
		&gen.SSOAdminService_ServiceDesc,
		&gen.BillingService_ServiceDesc,
		&gen.UserSettingsService_ServiceDesc,
		&gen.MFAService_ServiceDesc,
		&gen.PlatformAdminService_ServiceDesc,
	}
}

// buildRPCList enumerates every (Service, Method) from gRPC
// descriptors and joins per-method metadata. Stable sort by
// (service, method) so the response is deterministic across calls.
func buildRPCList() []*gen.RPCInfo {
	var out []*gen.RPCInfo
	for _, sd := range allServiceDescs() {
		// ServiceName is "customers.UserService" — strip the package.
		svc := sd.ServiceName
		if idx := lastDot(svc); idx >= 0 {
			svc = svc[idx+1:]
		}
		for _, m := range sd.Methods {
			key := svc + "/" + m.MethodName
			meta := rpcMetadata[key] // empty struct on miss
			out = append(out, &gen.RPCInfo{
				Service:      svc,
				Method:       m.MethodName,
				HttpMethod:   meta.HTTPMethod,
				HttpPath:     meta.HTTPPath,
				Description:  meta.Description,
				Scopes:       meta.Scopes,
				HandlerAuthz: meta.HandlerAuthz,
				EmitsAudit:   meta.EmitsAudit,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Method < out[j].Method
	})
	return out
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
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
		if r.HandlerAuthz == "platform_admin" || r.HandlerAuthz == "mfa" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// servicePermissions — RBAC vocabulary this service enforces. Each
// entry is one resource:action pair the api recognizes. Built-in
// role mapping mirrors migration 4. Note: admin holds the ones
// below via the seeded `*:*` wildcard, NOT via explicit
// role_permissions rows; the `built_in_roles: ["admin (via *:*)"]`
// notation is a documentation shortcut. Editor/viewer ARE seeded
// explicitly.
var servicePermissions = []*gen.PermissionInfo{
	{Resource: "*", Action: "*", Description: "Full access (root). Held by admin via explicit *:* row.", BuiltInRoles: []string{"admin"}},
	{Resource: "users", Action: "read", Description: "List + get users.", BuiltInRoles: []string{"admin (via *:*)", "editor", "viewer"}},
	{Resource: "users", Action: "write", Description: "Create / update / delete users.", BuiltInRoles: []string{"admin (via *:*)", "editor"}},
	{Resource: "teams", Action: "read", Description: "List teams.", BuiltInRoles: []string{"admin (via *:*)", "editor", "viewer"}},
	{Resource: "teams", Action: "write", Description: "Manage teams.", BuiltInRoles: []string{"admin (via *:*)", "editor"}},
	{Resource: "knowledge", Action: "read", Description: "Domain-resource read.", BuiltInRoles: []string{"admin (via *:*)", "editor", "viewer"}},
	{Resource: "knowledge", Action: "write", Description: "Domain-resource write.", BuiltInRoles: []string{"admin (via *:*)", "editor"}},
	{Resource: "billing", Action: "read", Description: "View billing state.", BuiltInRoles: []string{"admin (via *:*)", "editor"}},
	{Resource: "billing", Action: "write", Description: "Mutate billing.", BuiltInRoles: []string{"admin (via *:*)"}},
	{Resource: "audit", Action: "read", Description: "Read audit events.", BuiltInRoles: []string{"admin (via *:*)"}},
	{Resource: "webhooks", Action: "read", Description: "List webhook subs.", BuiltInRoles: []string{"admin (via *:*)"}},
	{Resource: "webhooks", Action: "write", Description: "Manage webhook subs.", BuiltInRoles: []string{"admin (via *:*)"}},
	{Resource: "api_keys", Action: "read", Description: "List API keys.", BuiltInRoles: []string{"admin (via *:*)"}},
	{Resource: "api_keys", Action: "write", Description: "Manage API keys.", BuiltInRoles: []string{"admin (via *:*)"}},
}

// serviceRLSTables — RLS-protected tables this api service depends
// on. The tables themselves live in the store service's schema (via
// store/migrations); this api is the enforcer that wraps every
// per-tenant Store call in WithOrgTx / WithBypass.
//
// Source: migrations 23, 27, 28, 29, 30, 31, 32, 33.
var serviceRLSTables = []*gen.RLSPolicyInfo{
	{Table: "audit_export_configs", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "webhook_subscriptions", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "webhook_deliveries", PolicyShape: "join", FailClosed: true, ScopeColumn: "subscription_id", Notes: "JOIN walks subscription_id → webhook_subscriptions.org_id"},
	{Table: "api_keys", PolicyShape: "direct", FailClosed: true, ScopeColumn: "organization_id"},
	{Table: "org_settings", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "invitations", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "organization_members", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "subscriptions", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "entitlement_overrides", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "usage_records", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "teams", PolicyShape: "direct", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "team_members", PolicyShape: "join", FailClosed: true, ScopeColumn: "team_id", Notes: "JOIN walks team_id → teams.org_id"},
	{Table: "audit_events", PolicyShape: "polymorphic", FailClosed: true, ScopeColumn: "org_id", Notes: "NULL org_id rows visible only via WithBypass (system events)."},
	{Table: "roles", PolicyShape: "polymorphic", FailClosed: true, ScopeColumn: "org_id", Notes: "Built-in roles (org_id IS NULL) globally readable; tenant rows scoped."},
	{Table: "role_assignments", PolicyShape: "polymorphic", FailClosed: true, ScopeColumn: "org_id"},
	{Table: "organizations", PolicyShape: "self_referential", FailClosed: true, ScopeColumn: "id"},
}

// serviceScopes — every scope this service accepts on an API key.
// Wildcard semantics: `*:*` matches everything; `users:*` matches
// all users:* pairs; `*:read` matches reads across resources.
var serviceScopes = []*gen.ScopeInfo{
	{Scope: "*:*", Description: "Root. All resources, all actions."},
	{Scope: "users:read", Description: "List + get users."},
	{Scope: "users:write", Description: "Create / update / delete users."},
	{Scope: "orgs:read", Description: "Read org metadata."},
	{Scope: "orgs:write", Description: "Manage org settings + members."},
	{Scope: "teams:read", Description: "List teams + members."},
	{Scope: "teams:write", Description: "Manage teams."},
	{Scope: "roles:read", Description: "List roles + assignments."},
	{Scope: "roles:write", Description: "Create roles + assign / revoke."},
	{Scope: "api_keys:read", Description: "List API keys."},
	{Scope: "api_keys:write", Description: "Mint / revoke API keys."},
	{Scope: "audit:read", Description: "Read audit events."},
	{Scope: "invitations:read", Description: "List invites."},
	{Scope: "invitations:write", Description: "Create / revoke invites."},
	{Scope: "webhooks:read", Description: "List webhook subs."},
	{Scope: "webhooks:write", Description: "Manage webhook subs (incl. RotateSecret)."},
	{Scope: "billing:read", Description: "View billing portal + invoices."},
	{Scope: "billing:write", Description: "Open checkout / portal sessions."},
	{Scope: "entitlements:read", Description: "View entitlement overrides + usage."},
}
