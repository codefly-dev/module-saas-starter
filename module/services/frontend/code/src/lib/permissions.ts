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
	// A platform role confers no organization permission: reading or administering
	// an organization is explicit, through membership of it. The only thing a
	// platform role holds here is the platform's own surface, which no tenant owns.
	super_admin: [
		PERMISSIONS.MODULE_ENVELOPE_READ,
		PERMISSIONS.MODULE_ENVELOPE_WRITE,
	],
	billing: [],
	support: [],
	// Org owner: everything within their own org. The catalog's `*:*` is the
	// built-in admin role, an explicit organization role, and only this org's.
	owner: [
		PERMISSIONS.ALL,
		PERMISSIONS.USERS_READ,
		"orgs:*",
		"billing:*",
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
		PERMISSIONS.ALL,
		PERMISSIONS.USERS_READ,
		PERMISSIONS.ORGS_READ,
		"billing:*",
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
 * while "users:*" grants every generated users action. No role is a bypass: a
 * platform role holds none of an organization's permissions.
 */
export function hasPermission(
	platformRole: PlatformRole | undefined,
	orgRole: OrgRole | undefined,
	permission: Permission,
): boolean {
	const roles: Role[] = [];
	if (platformRole) roles.push(platformRole);
	if (orgRole) roles.push(orgRole);
	const [wantResource, wantAction] = permission.split(":");
	for (const role of roles) {
		const grants = permissionsByRole[role] ?? [];
		for (const g of grants) {
			// Only an organization role holds the catalog's `*:*`; no platform role does.
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
