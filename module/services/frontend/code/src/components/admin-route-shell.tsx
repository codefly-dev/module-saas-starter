"use client";

import { useRouter, usePathname } from "next/navigation";
import { useEffect, type ReactNode } from "react";
import { AdminLayout } from "@/components/admin-layout";
import { ImpersonationBanner } from "@/components/impersonation-banner";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuth } from "@/lib/auth";
import { isAdmin } from "@/lib/permissions";

export function AdminRouteShell({ children }: { children: ReactNode }) {
	const { isAuthenticated, isLoading, platformRole, orgRole } = useAuth();
	const router = useRouter();
	const pathname = usePathname();
	const admin = isAdmin(platformRole, orgRole);
	// Team rosters are tenant-visible; team administrators need not be org admins.
	const teamRoute =
		pathname === "/admin/teams" || pathname.startsWith("/admin/teams/");
	const allowed = admin || (teamRoute && !!orgRole);

	useEffect(() => {
		if (isLoading) return;
		if (!isAuthenticated) {
			router.replace("/auth/login");
			return;
		}
		if (!allowed) router.replace("/");
	}, [isLoading, isAuthenticated, allowed, router]);

	if (isLoading) {
		return (
			<div className="min-h-screen flex items-center justify-center">
				<Skeleton className="h-8 w-48" />
			</div>
		);
	}
	if (!isAuthenticated || !allowed) return null;

	return (
		<>
			<ImpersonationBanner />
			<AdminLayout>{children}</AdminLayout>
		</>
	);
}
