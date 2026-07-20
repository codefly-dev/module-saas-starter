/**
 * permissions.ts — Role → permission matrix used by <RoleGate> and
 * sidebar visibility logic.
 *
 * Role model (matches backend JWT `pr` / `or` claims):
 *   platform: super_admin > billing > support (higher includes lower for reads)
 *   org:      owner > admin > member
 *
 * Permissions use the generated "<resource>:<action>" vocabulary. The
 * generated actions are currently "read" and "write"; a role matrix entry
 * ending in ":*" grants every generated action for that resource.
 *
 * The backend is ALWAYS the authoritative gate — this matrix exists so
 * the UI can hide things the user can't do, not to enforce authorization.
 */

import {
	PERMISSIONS,
	type Permission,
	type PermissionGrant,
} from "@/gen/saas/accounts/v1/frontend_catalog";
import type { OrgRole, PlatformRole } from "./auth-session";

type Role = PlatformRole | OrgRole;

// Resource shorthand: `*` means any generated action for that resource.
const permissionsByRole = {
	super_admin: [PERMISSIONS.ALL],
	// Billing role can read the org surface but only write billing.
	billing: [
		PERMISSIONS.USERS_READ,
		PERMISSIONS.ORGS_READ,
		"billing:*",
		PERMISSIONS.ENTITLEMENTS_READ,
		PERMISSIONS.AUDIT_READ,
	],
	// Support role: read-only visibility across orgs for triage + impersonate.
	support: [
		PERMISSIONS.USERS_READ,
		PERMISSIONS.ORGS_READ,
		PERMISSIONS.TEAMS_READ,
		PERMISSIONS.ROLES_READ,
		PERMISSIONS.API_KEYS_READ,
		PERMISSIONS.AUDIT_READ,
		PERMISSIONS.INVITATIONS_READ,
	],
	// Org owner: everything within their own org.
	owner: [
		PERMISSIONS.USERS_READ,
		"orgs:*",
		"teams:*",
		"roles:*",
		"api_keys:*",
		PERMISSIONS.AUDIT_READ,
		"invitations:*",
		"webhooks:*",
		"knowledge:*",
	],
	// Org admin: same as owner minus org delete (enforced server-side).
	// roles:* matches the backend's requireOrgAdmin gate on AssignRole/
	// RevokeRole/CreateRole — admins can manage role assignments within
	// their org. The "delete a custom role" path is platform-admin
	// (handler enforces requirePlatformAdmin) so admin's roles:write
	// here doesn't grant that.
	admin: [
		PERMISSIONS.USERS_READ,
		PERMISSIONS.ORGS_READ,
		"teams:*",
		"roles:*",
		"api_keys:*",
		PERMISSIONS.AUDIT_READ,
		"invitations:*",
		PERMISSIONS.WEBHOOKS_READ,
		"knowledge:*",
	],
	// Org member: limited read access. Most of the admin surface is hidden.
	member: [PERMISSIONS.TEAMS_READ, PERMISSIONS.ORGS_READ],
} satisfies Record<Role, readonly PermissionGrant[]>;

/**
 * hasPermission returns true if either the platform role or org role
 * carried by the current session grants the requested permission.
 *
 * The format is finite: exact values come from the generated service catalog,
 * while "users:*" grants every generated users action. The generated `*:*`
 * root permission is the effective super-admin bypass.
 */
export function hasPermission(
	platformRole: PlatformRole | undefined,
	orgRole: OrgRole | undefined,
	permission: Permission,
): boolean {
	// Platform super_admin has the generated global `*:*` bypass.
	const roles: Role[] = [];
	if (platformRole) roles.push(platformRole);
	if (orgRole) roles.push(orgRole);
	const [wantResource, wantAction] = permission.split(":");
	for (const role of roles) {
		const grants = permissionsByRole[role] ?? [];
		for (const g of grants) {
			if (g === PERMISSIONS.ALL) return true;
			const [gRes, gAct] = g.split(":");
			if (gRes !== wantResource) continue;
			if (gAct === "*" || gAct === wantAction) return true;
		}
	}
	return false;
}

/**
 * isAdmin returns true when the session carries ANY admin-tier role
 * (platform support/billing/super_admin, or org admin/owner). This is
 * the gate for showing the admin module's UI surface at all.
 */
export function isAdmin(
	platformRole: PlatformRole | undefined,
	orgRole: OrgRole | undefined,
): boolean {
	if (platformRole) return true; // any platform role implies admin
	return orgRole === "admin" || orgRole === "owner";
}

/**
 * isSuperAdmin gates the platform-wide features (platform admins list,
 * feature flags, global settings). Only super_admin qualifies.
 */
export function isSuperAdmin(platformRole: PlatformRole | undefined): boolean {
	return platformRole === "super_admin";
}
