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

import type { ReactNode } from "react";
import type { Permission } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { canPresent } from "@/lib/plugins/presentation";

type RequireLevel = "admin" | "super_admin";

interface RoleGateProps {
	/** Required role tier ("admin" = any admin; "super_admin" = only super). */
	require?: RequireLevel;
	/** Alternative to `require`: a granular "resource:action" permission. */
	requirePermission?: Permission;
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

	const allowed = canPresent(
		{ requiredRole: require, requiredPermission: requirePermission },
		{ isAuthenticated, platformRole, orgRole },
	);

	return allowed ? <>{children}</> : <>{fallback}</>;
}

/**
 * useCanAccess — hook flavor of RoleGate for imperative checks (disabling
 * a button rather than hiding it, deciding between two renderings, etc.).
 */
export function useCanAccess(opts: {
	require?: RequireLevel;
	requirePermission?: Permission;
}): boolean {
	const { platformRole, orgRole, isAuthenticated } = useAuth();
	return canPresent(
		{ requiredRole: opts.require, requiredPermission: opts.requirePermission },
		{ isAuthenticated, platformRole, orgRole },
	);
}
