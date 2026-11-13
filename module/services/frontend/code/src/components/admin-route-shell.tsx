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
import { CastBar, TeamSwitcher } from "@codefly-dev/saas-ui/casting";
import { HostCasting } from "@/features/role-view/team-casting";
import { useAuth } from "@/lib/auth";
import { isAdmin } from "@/lib/permissions";
import { useHeldEntries } from "@/lib/plugins/held-pages";
import { selectRoute } from "@/lib/plugins/presentation";
import { useFrontendConfig } from "@/lib/providers";
import { useRegisteredSolutionsRead } from "@/solutions/SolutionsMenu";

/** The admin pages, under this host's team casting (the cast bar and switcher, and every "view as"). */
export function AdminRouteShell({ children }: { children: ReactNode }) {
	return (
		<HostCasting>
			<AdminRoutes>{children}</AdminRoutes>
		</HostCasting>
	);
}

function AdminRoutes({ children }: { children: ReactNode }) {
	const {
		isAuthenticated,
		isLoading,
		platformRole,
		orgRole,
		sessionRead,
		castTeamId,
	} = useAuth();
	const router = useRouter();
	const pathname = usePathname();
	const admin = isAdmin(platformRole, orgRole);
	// Team rosters are tenant-visible; team administrators need not be org admins.
	// A cast holds no organization role (useAuth reads it as none) but is a
	// member of the team it acts as, so the team pages are its own to read.
	const teamRoute =
		pathname === "/admin/teams" || pathname.startsWith("/admin/teams/");
	// A page the host bundles is its role holders' to open, administrators or
	// not, its entry alone, as the sidebar presents it to them (useHeldEntries).
	const config = useFrontendConfig();
	const heldEntries = useHeldEntries();
	const pagesRead = useRegisteredSolutionsRead();
	const pageRoute = selectRoute(config, pathname, {
		isAuthenticated,
		platformRole,
		orgRole,
		heldEntries,
	});
	const heldPageRoute =
		!!pageRoute && heldEntries.get(pageRoute.plugin) === pageRoute.path;
	const allowed =
		admin || (teamRoute && (!!orgRole || !!castTeamId)) || heldPageRoute;

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
	// Whether a plugin's page asked for is one this person holds is known
	// once their listing has answered; until then it is not a refusal, and a
	// listing that could not be read says so rather than waiting for ever.
	if (
		!allowed &&
		pagesRead !== "read" &&
		config.routes.some((route) => route.path === pathname)
	) {
		return pagesRead === "reading" ? (
			<div className="min-h-screen flex items-center justify-center">
				<Skeleton className="h-8 w-48" />
			</div>
		) : (
			<>
				<ImpersonationBanner />
				<RoleViewBanner />
				<EmptyState
					icon={ShieldOff}
					title="Could not read the pages you hold"
					description="The host could not say which pages you hold a role on, so this page is not shown. This is not a statement about your access. It is asked again shortly; reload to try now."
					action={<Link href="/">Back to the dashboard</Link>}
				/>
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
				<CastBar />
				<TeamSwitcher />
				<EmptyState
					icon={ShieldOff}
					title="You don't have access to Admin"
					description={
						castTeamId
							? "Admin is for organization and platform administrators. This session acts as a team and holds what that team holds, none of your own roles. Go back to your own view, from the bar above, to use your own access."
							: `Admin is for organization and platform administrators. Your role in this organization is ${orgRole ?? "none"}. Ask an administrator of this organization to give you the role you need.`
					}
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
