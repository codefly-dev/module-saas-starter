"use client";

import { ShieldOff } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { type ReactNode, useEffect } from "react";
import { AdminLayout } from "@/components/admin-layout";
import { EmptyState } from "@/components/empty-state";
import { ImpersonationBanner } from "@/components/impersonation-banner";
import { Skeleton } from "@/components/ui/skeleton";
import { RoleViewBanner } from "@/features/role-view/role-view";
import { useAuth } from "@/lib/auth";
import { isAdmin } from "@/lib/permissions";

export function AdminRouteShell({ children }: { children: ReactNode }) {
	const { isAuthenticated, isLoading, platformRole, orgRole, sessionRead } = useAuth();
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
	// "No role" is only an answer once the host has confirmed the session:
	// while it is being read, or could not be read, the shell says so and does
	// not call it a denial.
	if (sessionRead === "reading" || sessionRead === "failed") {
		return (
			<>
				<ImpersonationBanner />
				<RoleViewBanner />
				{sessionRead === "reading" ? (
					<div className="min-h-screen flex items-center justify-center">
						<Skeleton className="h-8 w-48" />
					</div>
				) : (
					<EmptyState
						icon={ShieldOff}
						title="Could not confirm what this session is"
						description="The host could not say what this session is, so Admin is not shown. This is not a statement about your role. Reload to try again."
						action={<Link href="/">Back to the dashboard</Link>}
					/>
				)}
			</>
		);
	}
	// A refusal says why and who can change it; sending the person to the
	// dashboard unexplained looks like the page does not exist.
	if (!allowed) {
		return (
			<>
				<ImpersonationBanner />
				<RoleViewBanner />
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
			{/* A view or a cast is said on every page, the admin ones included:
			    this shell had left it to the dashboard's. */}
			<RoleViewBanner />
			<AdminLayout>{children}</AdminLayout>
		</>
	);
}
