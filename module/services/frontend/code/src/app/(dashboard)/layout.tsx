"use client";

import { useAuth } from "@/lib/auth";
import { AdminLayout } from "@/components/admin-layout";
import { ImpersonationBanner } from "@/components/impersonation-banner";
import { Skeleton } from "@/components/ui/skeleton";
import { useRouter } from "next/navigation";
import { useEffect } from "react";

/**
 * Dashboard layout — requires authentication.
 * Redirects to /auth/login if not authenticated.
 * Shows sidebar with role-based navigation.
 */
export default function DashboardLayout({ children }: { children: React.ReactNode }) {
  const { isAuthenticated, isLoading } = useAuth();
  const router = useRouter();

  useEffect(() => {
    if (!isLoading && !isAuthenticated) {
      router.replace("/auth/login");
    }
  }, [isLoading, isAuthenticated, router]);

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <Skeleton className="h-8 w-48" />
      </div>
    );
  }

  if (!isAuthenticated) {
    return null; // Will redirect
  }

  return (
    <>
      <ImpersonationBanner />
      <AdminLayout>{children}</AdminLayout>
    </>
  );
}
