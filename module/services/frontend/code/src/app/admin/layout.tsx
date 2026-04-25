"use client";

/**
 * (admin) layout — the gate for the entire admin surface.
 *
 * Pages inside this route group automatically get:
 *   1. Authentication required (redirects to /auth/login)
 *   2. Role check: user must be admin-tier (platform role OR org admin/owner)
 *   3. The full AdminLayout chrome (sidebar, impersonation banner)
 *
 * The constraint baked in here: saas-starter is a module, not the main
 * app. Admin pages live ONLY under /admin/* so the hosting app can keep
 * its main dashboard at / minimal. Non-admins who hit any /admin/* route
 * get redirected straight back to /.
 */

import { useEffect } from "react";
import { useRouter } from "next/navigation";

import { AdminLayout } from "@/components/admin-layout";
import { ImpersonationBanner } from "@/components/impersonation-banner";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuth } from "@/lib/auth";
import { isAdmin } from "@/lib/permissions";

export default function AdminRouteLayout({ children }: { children: React.ReactNode }) {
  const { isAuthenticated, isLoading, platformRole, orgRole } = useAuth();
  const router = useRouter();

  const admin = isAdmin(platformRole, orgRole);

  useEffect(() => {
    if (isLoading) return;
    if (!isAuthenticated) {
      router.replace("/auth/login");
      return;
    }
    if (!admin) {
      // Send non-admins home — the main app dashboard is what they should
      // see. The server would reject their requests anyway; this just
      // avoids a flash of the admin UI during the API 403.
      router.replace("/");
    }
  }, [isLoading, isAuthenticated, admin, router]);

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <Skeleton className="h-8 w-48" />
      </div>
    );
  }

  if (!isAuthenticated || !admin) {
    // The useEffect above will redirect; render nothing in the meantime
    // to avoid flashing admin chrome for unauthorized users.
    return null;
  }

  return (
    <>
      <ImpersonationBanner />
      <AdminLayout>{children}</AdminLayout>
    </>
  );
}
