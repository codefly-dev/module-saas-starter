"use client";

import { useRouter } from "next/navigation";
import { useEffect, type ReactNode } from "react";
import { AdminLayout } from "@/components/admin-layout";
import { ImpersonationBanner } from "@/components/impersonation-banner";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuth } from "@/lib/auth";
import { isAdmin } from "@/lib/permissions";

export function AdminRouteShell({ children }: { children: ReactNode }) {
	const { isAuthenticated, isLoading, platformRole, orgRole } = useAuth();
	const router = useRouter();
	const admin = isAdmin(platformRole, orgRole);

	useEffect(() => {
		if (isLoading) return;
		if (!isAuthenticated) {
			router.replace("/auth/login");
			return;
		}
		if (!admin) router.replace("/");
	}, [isLoading, isAuthenticated, admin, router]);

	if (isLoading) {
		return (
			<div className="min-h-screen flex items-center justify-center">
				<Skeleton className="h-8 w-48" />
			</div>
		);
	}
	if (!isAuthenticated || !admin) return null;

	return (
		<>
			<ImpersonationBanner />
			<AdminLayout>{children}</AdminLayout>
		</>
	);
}
