package business

import (
	gen "accounts/pkg/gen/saas/accounts/v1"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
)

// Entitlement keys are server-owned product-catalog identifiers. Keep runtime
// checks on these constants so Go, database parity tests, and generated
// TypeScript all consume the same vocabulary.
const (
	EntitlementSeats           = "seats"
	EntitlementAPIKeys         = "api_keys"
	EntitlementAPICallsMonthly = "api_calls_monthly"
	EntitlementSSO             = "sso"
	EntitlementAuditLog        = "audit_log"
	EntitlementPairedDevices   = "paired_devices"
)

type servicePermissionDefinition struct {
	Permission   string
	Description  string
	BuiltInRoles []string
	APIKeyScope  bool
}

// servicePermissionVocabulary is the one accounts RBAC and API-key vocabulary.
// Method policy values must be present here. Introspection and generated
// TypeScript are projections of this list rather than parallel inventories.
var servicePermissionVocabulary = []servicePermissionDefinition{
	{Permission: "*:*", Description: "Full access to all resources and actions.", BuiltInRoles: []string{"admin"}, APIKeyScope: true},
	{Permission: "api_keys:read", Description: "List API keys.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "api_keys:write", Description: "Mint and revoke API keys.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "audit:read", Description: "Read and export audit events.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "billing:read", Description: "View billing state and invoices.", BuiltInRoles: []string{"admin (via *:*)", "editor"}, APIKeyScope: true},
	{Permission: "billing:write", Description: "Open checkout and billing portal sessions.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "devices:read", Description: "List linked devices.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "devices:write", Description: "Mint device claim codes and revoke linked devices.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "entitlements:check", Description: "Service-to-service device entitlement check (API-key only).", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "entitlements:read", Description: "View entitlement limits, overrides, and usage.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "invitations:read", Description: "List organization invitations.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "invitations:write", Description: "Create and revoke organization invitations.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "knowledge:read", Description: "Read domain knowledge resources.", BuiltInRoles: []string{"admin (via *:*)", "editor", "viewer"}},
	{Permission: "knowledge:write", Description: "Create and update domain knowledge resources.", BuiltInRoles: []string{"admin (via *:*)", "editor"}},
	{Permission: "orgs:read", Description: "Read organization metadata.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "orgs:write", Description: "Manage organization settings and members.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "roles:read", Description: "List roles and assignments.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "roles:write", Description: "Create roles and assign or revoke them.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "teams:read", Description: "List teams and members.", BuiltInRoles: []string{"admin (via *:*)", "editor", "viewer"}, APIKeyScope: true},
	{Permission: "teams:write", Description: "Manage teams and memberships.", BuiltInRoles: []string{"admin (via *:*)", "editor"}, APIKeyScope: true},
	{Permission: "users:read", Description: "List and get users.", BuiltInRoles: []string{"admin (via *:*)", "editor", "viewer"}, APIKeyScope: true},
	{Permission: "users:write", Description: "Create, update, and delete users.", BuiltInRoles: []string{"admin (via *:*)", "editor"}, APIKeyScope: true},
	{Permission: "webhooks:read", Description: "List webhook subscriptions and deliveries.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
	{Permission: "webhooks:write", Description: "Manage, replay, and rotate webhook subscriptions.", BuiltInRoles: []string{"admin (via *:*)"}, APIKeyScope: true},
}

var serviceEntitlementVocabulary = []*catalogv1.EntitlementDefinition{
	{Key: EntitlementAPICallsMonthly, Kind: catalogv1.EntitlementKind_ENTITLEMENT_KIND_QUOTA, Unit: "requests/month", Description: "Monthly API request allowance."},
	{Key: EntitlementAPIKeys, Kind: catalogv1.EntitlementKind_ENTITLEMENT_KIND_QUOTA, Unit: "keys", Description: "Active API key allowance."},
	{Key: EntitlementAuditLog, Kind: catalogv1.EntitlementKind_ENTITLEMENT_KIND_FEATURE, Unit: "enabled", Description: "Audit-log access."},
	{Key: EntitlementPairedDevices, Kind: catalogv1.EntitlementKind_ENTITLEMENT_KIND_QUOTA, Unit: "devices", Description: "Linked (non-revoked) device allowance."},
	{Key: EntitlementSeats, Kind: catalogv1.EntitlementKind_ENTITLEMENT_KIND_QUOTA, Unit: "seats", Description: "Organization members plus pending invitations."},
	{Key: EntitlementSSO, Kind: catalogv1.EntitlementKind_ENTITLEMENT_KIND_FEATURE, Unit: "enabled", Description: "Single sign-on administration."},
}

func splitPermission(permission string) (string, string) {
	for i := 0; i < len(permission); i++ {
		if permission[i] == ':' {
			return permission[:i], permission[i+1:]
		}
	}
	return permission, ""
}

func catalogPermissionDefinitions() []*catalogv1.PermissionDefinition {
	out := make([]*catalogv1.PermissionDefinition, 0, len(servicePermissionVocabulary))
	for _, definition := range servicePermissionVocabulary {
		resource, action := splitPermission(definition.Permission)
		out = append(out, &catalogv1.PermissionDefinition{
			Permission:   definition.Permission,
			Resource:     resource,
			Action:       action,
			Description:  definition.Description,
			BuiltInRoles: append([]string(nil), definition.BuiltInRoles...),
			ApiKeyScope:  definition.APIKeyScope,
		})
	}
	return out
}

func catalogEntitlementDefinitions() []*catalogv1.EntitlementDefinition {
	out := make([]*catalogv1.EntitlementDefinition, 0, len(serviceEntitlementVocabulary))
	for _, definition := range serviceEntitlementVocabulary {
		out = append(out, &catalogv1.EntitlementDefinition{
			Key:         definition.GetKey(),
			Kind:        definition.GetKind(),
			Unit:        definition.GetUnit(),
			Description: definition.GetDescription(),
		})
	}
	return out
}

var servicePermissions = func() []*gen.PermissionInfo {
	out := make([]*gen.PermissionInfo, 0, len(servicePermissionVocabulary))
	for _, definition := range servicePermissionVocabulary {
		resource, action := splitPermission(definition.Permission)
		out = append(out, &gen.PermissionInfo{
			Resource: resource, Action: action, Description: definition.Description,
			BuiltInRoles: append([]string(nil), definition.BuiltInRoles...),
		})
	}
	return out
}()

var serviceScopes = func() []*gen.ScopeInfo {
	var out []*gen.ScopeInfo
	for _, definition := range servicePermissionVocabulary {
		if definition.APIKeyScope {
			out = append(out, &gen.ScopeInfo{Scope: definition.Permission, Description: definition.Description})
		}
	}
	return out
}()
