/**
 * permissions.ts — Role → permission matrix used by <RoleGate> and
 * sidebar visibility logic.
 *
 * Role model (matches backend JWT `pr` / `or` claims):
 *   platform: super_admin > billing > support (higher includes lower for reads)
 *   org:      owner > admin > member
 *
 * Permissions are dotted strings: "<resource>:<action>". Actions commonly
 * used: "read", "write", "delete", "*". A matrix entry with "*" grants
 * everything for that resource.
 *
 * The backend is ALWAYS the authoritative gate — this matrix exists so
 * the UI can hide things the user can't do, not to enforce authorization.
 */

import type { PlatformRole, OrgRole } from "./admin-core";

type Role = PlatformRole | OrgRole;

// Resource shorthand: `*` means any action; `read` means read-only views.
type Permission = string;

const permissionsByRole: Record<Role, Permission[]> = {
  super_admin: [
    "platform:*",
    "users:*",
    "orgs:*",
    "teams:*",
    "roles:*",
    "api_keys:*",
    "audit:*",
    "invitations:*",
    "feature_flags:*",
    "sessions:*",
    "webhooks:*",
    "billing:*",
    "entitlements:*",
  ],
  // Billing role can read the org surface but only write billing.
  billing: [
    "users:read",
    "orgs:read",
    "billing:*",
    "entitlements:*",
    "audit:read",
  ],
  // Support role: read-only visibility across orgs for triage + impersonate.
  support: [
    "users:read",
    "orgs:read",
    "teams:read",
    "roles:read",
    "api_keys:read",
    "audit:read",
    "invitations:read",
    "sessions:read",
  ],
  // Org owner: everything within their own org.
  owner: [
    "users:read",
    "orgs:*",
    "teams:*",
    "roles:*",
    "api_keys:*",
    "audit:read",
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
    "users:read",
    "orgs:read",
    "teams:*",
    "roles:*",
    "api_keys:*",
    "audit:read",
    "invitations:*",
    "webhooks:read",
    "knowledge:*",
  ],
  // Org member: limited read access. Most of the admin surface is hidden.
  member: [
    "teams:read",
    "orgs:read",
  ],
};

/**
 * hasPermission returns true if either the platform role or org role
 * carried by the current session grants the requested permission.
 *
 * The format is permissive: "users:read" grants read, "users:*" grants
 * everything for users. A role entry "platform:*" grants any permission
 * (it's the effective super_admin bypass at the matrix layer).
 */
export function hasPermission(
  platformRole: PlatformRole | undefined,
  orgRole: OrgRole | undefined,
  permission: Permission,
): boolean {
  // Platform super_admin has a global bypass via "platform:*".
  const roles: Role[] = [];
  if (platformRole) roles.push(platformRole);
  if (orgRole) roles.push(orgRole);
  const [wantResource, wantAction] = permission.split(":");
  for (const role of roles) {
    const grants = permissionsByRole[role] ?? [];
    for (const g of grants) {
      if (g === "platform:*") return true;
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
