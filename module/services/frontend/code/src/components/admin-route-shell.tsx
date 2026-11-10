"use client";

import { ShieldOff } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { type ReactNode, useEffect } from "react";
import { AdminLayout } from "@/components/admin-layout";
import { EmptyState } from "@/components/empty-state";
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
	}, [isLoading, isAuthenticated, router]);

	if (isLoading) {
		return (
			<div className="min-h-screen flex items-center justify-center">
				<Skeleton className="h-8 w-48" />
			</div>
		);
	}
	if (!isAuthenticated) return null;
	// A refusal says why and who can change it; sending the person to the
	// dashboard unexplained looks like the page does not exist.
	if (!allowed) {
		return (
			<>
				<ImpersonationBanner />
				<EmptyState
					icon={ShieldOff}
					title="You don't have access to Admin"
					description={`Admin is for organization and platform administrators. Your role in this organization is ${orgRole ?? "none"}. Ask an administrator of this organization to give you the role you need.`}
					action={<Link href="/">Back to the dashboard</Link>}
				/>
			</>
		);
	}

	return (
		<>
			<ImpersonationBanner />
			<AdminLayout>{children}</AdminLayout>
		</>
	);
}
