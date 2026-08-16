package business

import (
	"fmt"
	"sort"

	gen "accounts/pkg/gen/saas/accounts/v1"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
	"accounts/pkg/permissioncatalog"
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

func composedPermissionVocabulary(
	base []servicePermissionDefinition,
	contributed []permissioncatalog.Permission,
) ([]servicePermissionDefinition, error) {
	out := append([]servicePermissionDefinition(nil), base...)
	seen := make(map[string]struct{}, len(base)+len(contributed))
	for _, definition := range base {
		seen[definition.Permission] = struct{}{}
	}
	for _, permission := range contributed {
		if permission.Name != permission.Resource+":"+permission.Action {
			return nil, fmt.Errorf("contributed permission %q does not match resource and action", permission.Name)
		}
		if _, exists := seen[permission.Name]; exists {
			return nil, fmt.Errorf("contributed permission %q collides with the service vocabulary", permission.Name)
		}
		seen[permission.Name] = struct{}{}
		out = append(out, servicePermissionDefinition{
			Permission:  permission.Name,
			Description: "Contributed permission " + permission.Name + ".",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Permission < out[j].Permission })
	return out, nil
}

func completeServicePermissionVocabulary() []servicePermissionDefinition {
	definitions, err := composedPermissionVocabulary(servicePermissionVocabulary, permissioncatalog.Permissions())
	if err != nil {
		panic(err)
	}
	return definitions
}

var serviceEntitlementVocabulary = []*catalogv1.EntitlementDefinition{
	{Key: EntitlementAPICallsMonthly, Kind: catalogv1.EntitlementKind_ENTITLEMENT_KIND_QUOTA, Unit: "requests/month", Description: "Monthly API request allowance."},
	{Key: EntitlementAPIKeys, Kind: catalogv1.EntitlementKind_ENTITLEMENT_KIND_QUOTA, Unit: "keys", Description: "Active API key allowance."},
	{Key: EntitlementAuditLog, Kind: catalogv1.EntitlementKind_ENTITLEMENT_KIND_FEATURE, Unit: "enabled", Description: "Audit-log access."},
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
	vocabulary := completeServicePermissionVocabulary()
	out := make([]*catalogv1.PermissionDefinition, 0, len(vocabulary))
	for _, definition := range vocabulary {
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
	vocabulary := completeServicePermissionVocabulary()
	out := make([]*gen.PermissionInfo, 0, len(vocabulary))
	for _, definition := range vocabulary {
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
	for _, definition := range completeServicePermissionVocabulary() {
		if definition.APIKeyScope {
			out = append(out, &gen.ScopeInfo{Scope: definition.Permission, Description: definition.Description})
		}
	}
	return out
}()
