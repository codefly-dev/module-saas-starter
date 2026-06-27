"use client";

/**
 * Catch-all renderer for PLUGIN-PROVIDED admin pages.
 *
 * Plugins contribute pages as DATA via `AdminPlugin.routes`, not as files —
 * including PROJECT plugins that live OUTSIDE this shared starter tree (e.g. the
 * warden console, discovered via SAAS_PROJECT_PLUGINS_DIR). Next's filesystem
 * router can't see files outside `app/`, so this single catch-all matches the
 * current path to a registered `PluginRoute` and renders its component.
 *
 * Specific filesystem routes (users, billing, …) are matched by Next BEFORE this
 * catch-all, so only unmatched paths fall through here. The (admin) layout
 * already enforces auth + admin-tier; a route's optional `requiredRole` is an
 * additional, finer gate.
 */

import { Suspense } from "react";
import { notFound, usePathname } from "next/navigation";

import { RoleGate } from "@/components/auth/role-gate";
import { Skeleton } from "@/components/ui/skeleton";
import { adminConfig } from "@/lib/admin-config";

// Trailing-slash-insensitive match (keep "/" as-is).
const normalize = (p: string) => (p.length > 1 ? p.replace(/\/+$/, "") : p);

export default function PluginRoutePage() {
  const pathname = usePathname();
  const route = adminConfig.routes.find((r) => normalize(r.path) === normalize(pathname));

  if (!route) {
    notFound();
  }

  const Component = route.component;
  // PluginRoute.requiredRole is OrgRole | PlatformRole; RoleGate's tier is
  // coarser — only super_admin is meaningfully stricter than the admin-tier the
  // layout already guarantees, so map everything else to "admin".
  const requireTier = route.requiredRole === "super_admin" ? "super_admin" : "admin";

  return (
    <RoleGate require={requireTier}>
      <Suspense fallback={<Skeleton className="h-64 w-full" />}>
        <Component />
      </Suspense>
    </RoleGate>
  );
}
