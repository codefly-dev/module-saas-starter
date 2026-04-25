"use client";

/**
 * RoleGate — a single entry point for "show this only if the user has
 * the required permission / role". Uses `useAuth()` to read the current
 * session's platformRole + orgRole and the permissions matrix to decide.
 *
 * This component is UI-only — the backend is still the authoritative
 * authorization gate (see pkg/adapters/auth.go in the api service).
 * RoleGate is here so the admin surface doesn't flash for users who
 * would fail the server check anyway.
 *
 * Usage:
 *
 *     <RoleGate require="admin">
 *       <AdminUsersTable />
 *     </RoleGate>
 *
 *     <RoleGate requirePermission="users:write">
 *       <InviteButton />
 *     </RoleGate>
 *
 *     // With a fallback for unauthorized users:
 *     <RoleGate require="super_admin" fallback={<AccessDenied />}>
 *       <PlatformFlags />
 *     </RoleGate>
 */

import { ReactNode } from "react";
import { useAuth } from "@/lib/auth";
import { hasPermission, isAdmin, isSuperAdmin } from "@/lib/permissions";

type RequireLevel = "admin" | "super_admin";

interface RoleGateProps {
  /** Required role tier ("admin" = any admin; "super_admin" = only super). */
  require?: RequireLevel;
  /** Alternative to `require`: a granular "resource:action" permission. */
  requirePermission?: string;
  /** What to show when the gate fails. Defaults to `null` (hide silently). */
  fallback?: ReactNode;
  children: ReactNode;
}

export function RoleGate({
  require,
  requirePermission,
  fallback = null,
  children,
}: RoleGateProps) {
  const { platformRole, orgRole, isAuthenticated } = useAuth();

  if (!isAuthenticated) {
    return <>{fallback}</>;
  }

  let allowed = true;

  if (require === "super_admin") {
    allowed = isSuperAdmin(platformRole);
  } else if (require === "admin") {
    allowed = isAdmin(platformRole, orgRole);
  }

  if (allowed && requirePermission) {
    allowed = hasPermission(platformRole, orgRole, requirePermission);
  }

  return allowed ? <>{children}</> : <>{fallback}</>;
}

/**
 * useCanAccess — hook flavor of RoleGate for imperative checks (disabling
 * a button rather than hiding it, deciding between two renderings, etc.).
 */
export function useCanAccess(opts: {
  require?: RequireLevel;
  requirePermission?: string;
}): boolean {
  const { platformRole, orgRole, isAuthenticated } = useAuth();
  if (!isAuthenticated) return false;
  if (opts.require === "super_admin" && !isSuperAdmin(platformRole)) return false;
  if (opts.require === "admin" && !isAdmin(platformRole, orgRole)) return false;
  if (opts.requirePermission && !hasPermission(platformRole, orgRole, opts.requirePermission)) {
    return false;
  }
  return true;
}
