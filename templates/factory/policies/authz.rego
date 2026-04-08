package authz

import rego.v1

default allow := false

# Role hierarchies — higher number = more privilege
platform_hierarchy := {"super_admin": 3, "billing": 2, "support": 1}
org_hierarchy := {"owner": 3, "admin": 2, "member": 1}

# ── Public endpoints — always allowed ────────────────────────

allow if {
	input.method in data.method_permissions.public
}

# ── Authenticated endpoints — just need a valid user_id ──────

allow if {
	input.method in data.method_permissions.authenticated
	input.user_id != ""
	not blocked_during_impersonation
}

# ── Platform-only endpoints ──────────────────────────────────

allow if {
	perm := data.method_permissions.platform_only[input.method]
	required := platform_hierarchy[perm.min_role]
	actual := platform_hierarchy[input.platform_role]
	actual >= required
	not blocked_during_impersonation
}

# ── Org-scoped endpoints ─────────────────────────────────────

allow if {
	perm := data.method_permissions.org_scoped[input.method]
	required := org_hierarchy[perm.min_role]
	actual := org_hierarchy[input.org_role]
	actual >= required
	not blocked_during_impersonation
}

# ── Platform super_admin bypasses org-scoped checks ──────────

allow if {
	data.method_permissions.org_scoped[input.method]
	input.platform_role == "super_admin"
	not blocked_during_impersonation
}

# ── Impersonation restrictions ───────────────────────────────
# When impersonating, block actions that could cause harm or
# create persistent access. This is provider-agnostic — works
# with WorkOS (act claim), Clerk, or our local impersonation.

blocked_during_impersonation if {
	input.is_impersonated == true
	input.method in data.method_permissions.restricted_during_impersonation
}
