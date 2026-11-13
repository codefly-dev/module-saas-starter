"use client";

import type {
	NavItem,
	PluginNavSection,
} from "@codefly-dev/saas-plugin-contract";
import {
	Boxes,
	ChevronRight,
	ChevronUp,
	LogOut,
	ShieldCheck,
	UsersRound,
} from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState } from "react";
import { BrandMark } from "@/components/brand-mark";
import { RateLimitBanner } from "@/components/rate-limit-banner";
import { ThemeToggle } from "@/components/theme-toggle";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Separator } from "@/components/ui/separator";
import {
	Sidebar,
	SidebarContent,
	SidebarFooter,
	SidebarGroup,
	SidebarGroupContent,
	SidebarGroupLabel,
	SidebarHeader,
	SidebarInset,
	SidebarMenu,
	SidebarMenuButton,
	SidebarMenuItem,
	SidebarProvider,
	SidebarTrigger,
} from "@/components/ui/sidebar";
import { Toaster } from "@/components/ui/sonner";
import { NotificationBanner } from "@/features/notifications/ui/notification-banner";
import {
	CastIndicator,
	setTeamSwitcherOpen,
	TeamSwitcher,
	useTeamDirectory,
} from "@/features/role-view/team-casting";
import { NotificationBell } from "@/features/notifications/ui/notification-bell";
import { OrgSwitcher } from "@/features/organizations/ui/org-switcher";
import { useAppearance } from "@/lib/appearance-provider";
import { useAuth } from "@/lib/auth";
import { sessionDisplayLabel } from "@/lib/auth-session";
import { getNavigationIcon } from "@/lib/navigation-icons";
import { useHeldPlugins } from "@/lib/plugins/held-pages";
import { canPresent, selectNavigation } from "@/lib/plugins/presentation";
import { useFrontendConfig } from "@/lib/providers";
import { usePublicRuntimeConfig } from "@/lib/public-runtime-config-provider";
import { signOutAndLeave } from "@/lib/sign-out-journey";
import { cn } from "@/lib/utils";
import { useRegisteredSolutions } from "@/solutions/SolutionsMenu";
import { groupSolutionNavigation } from "@/solutions/solution-groups";

function isNavActive(pathname: string, href: string): boolean {
	if (href === "/") return pathname === "/";
	return pathname === href || pathname.startsWith(`${href}/`);
}

function groupPluginItems(
	section: PluginNavSection,
): Array<[string | undefined, readonly NavItem[]]> {
	const groups = new Map<string | undefined, NavItem[]>();
	for (const item of section.items) {
		const items = groups.get(item.group) ?? [];
		items.push(item);
		groups.set(item.group, items);
	}
	return [...groups.entries()];
}

function PluginNavigationSection({
	section,
	pathname,
}: {
	section: PluginNavSection;
	pathname: string;
}) {
	return (
		<SidebarGroup>
			<SidebarGroupLabel>{section.label}</SidebarGroupLabel>
			<SidebarGroupContent className="space-y-2">
				{groupPluginItems(section).map(([group, items]) => (
					<div key={group ?? "default"}>
						{group ? (
							<div className="px-2 pb-1 text-[10px] font-medium uppercase tracking-wider text-muted-foreground/70">
								{group}
							</div>
						) : null}
						<SidebarMenu>
							{items.map((item) => {
								const Icon = getNavigationIcon(item.icon);
								return (
									<SidebarMenuItem key={item.href}>
										<SidebarMenuButton
											render={<Link href={item.href} />}
											isActive={isNavActive(pathname, item.href)}
										>
											<Icon className="h-4 w-4" />
											<span>{item.label}</span>
										</SidebarMenuButton>
									</SidebarMenuItem>
								);
							})}
						</SidebarMenu>
					</div>
				))}
			</SidebarGroupContent>
		</SidebarGroup>
	);
}

export function AdminLayout({ children }: { children: React.ReactNode }) {
	const pathname = usePathname();
	const { user, platformRole, orgRole, isAuthenticated, logout, castTeamId } =
		useAuth();
	// "View as team…" is offered where there is a team to view as: one this
	// person could read holding something, or the cast they are in (to switch).
	// The host decides each cast when it is asked.
	const { teams: castable } = useTeamDirectory();
	const offerViewAs =
		!!castTeamId || castable.some((team) => team.holds.length > 0);
	const config = useFrontendConfig();
	const { branding } = useAppearance();
	const heldPlugins = useHeldPlugins();
	const principal = { isAuthenticated, platformRole, orgRole, heldPlugins };
	const { productFeatures } = usePublicRuntimeConfig();

	const showAdmin = canPresent({ access: "admin" }, principal);

	const visibleSidebar = selectNavigation(
		config,
		"sidebar",
		principal,
		productFeatures,
	);
	const userNav = visibleSidebar.filter(
		(item) =>
			!item.requiredRole &&
			item.access !== "admin" &&
			item.access !== "super_admin",
	);
	const visibleSections = config.navSections
		.map((section) => ({
			...section,
			items: section.items.filter(
				(item) => visibleSidebar.includes(item) && !userNav.includes(item),
			),
		}))
		.filter((section) => section.items.length > 0);
	const primarySections = visibleSections.filter(
		(section) => section.placement === "primary",
	);
	const adminSections = visibleSections.filter(
		(section) => section.placement === "admin",
	);
	const onAdminRoute = adminSections.some((section) =>
		section.items.some((item) => isNavActive(pathname, item.href)),
	);
	const onPrimaryRoute = primarySections.some((section) =>
		section.items.some((item) => isNavActive(pathname, item.href)),
	);
	const [adminOpen, setAdminOpen] = useState(
		pathname === "/admin" || (onAdminRoute && !onPrimaryRoute),
	);
	const userMenu = selectNavigation(
		config,
		"user_menu",
		principal,
		productFeatures,
	);
	const registeredSolutions = useRegisteredSolutions();

	const userLabel = sessionDisplayLabel(user);
	const userInitials =
		user?.name || user?.email
			? (user.name ?? user.email ?? "").slice(0, 2).toUpperCase()
			: (user?.id?.slice(0, 2).toUpperCase() ?? "U");

	return (
		<SidebarProvider>
			<Sidebar>
				<SidebarHeader>
					<SidebarMenu>
						<SidebarMenuItem>
							<SidebarMenuButton size="lg" render={<Link href="/" />}>
								<BrandMark
									className="flex h-8 w-8 items-center justify-center overflow-hidden rounded-lg bg-primary text-sm font-bold text-primary-foreground"
									imageClassName="p-1"
								/>
								<div className="flex flex-col gap-0.5 leading-none">
									<span className="font-semibold">{branding.name}</span>
									<span className="text-xs text-muted-foreground">
										{showAdmin ? "Admin" : "Dashboard"}
									</span>
								</div>
							</SidebarMenuButton>
						</SidebarMenuItem>
						{isAuthenticated && (
							<SidebarMenuItem>
								<OrgSwitcher />
							</SidebarMenuItem>
						)}
					</SidebarMenu>
				</SidebarHeader>

				<SidebarContent>
					{/* User nav — always visible */}
					<SidebarGroup>
						<SidebarGroupContent>
							<SidebarMenu>
								{userNav.map((item) => {
									const Icon = getNavigationIcon(item.icon);
									return (
										<SidebarMenuItem key={item.href}>
											<SidebarMenuButton
												render={<Link href={item.href} />}
												isActive={isNavActive(pathname, item.href)}
											>
												<Icon className="h-4 w-4" />
												<span>{item.label}</span>
											</SidebarMenuButton>
										</SidebarMenuItem>
									);
								})}
							</SidebarMenu>
						</SidebarGroupContent>
					</SidebarGroup>

					{groupSolutionNavigation(registeredSolutions).map((section) => (
						<SidebarGroup key={section.key}>
							<SidebarGroupLabel>{section.title}</SidebarGroupLabel>
							<SidebarGroupContent>
								<SidebarMenu>
									{section.items.map((solution) => (
										<SidebarMenuItem key={solution.id}>
											<SidebarMenuButton
												render={<Link href={solution.nav.path} />}
												isActive={isNavActive(pathname, solution.nav.path)}
											>
												<Boxes className="h-4 w-4" />
												<span>{solution.nav.title}</span>
											</SidebarMenuButton>
										</SidebarMenuItem>
									))}
								</SidebarMenu>
							</SidebarGroupContent>
						</SidebarGroup>
					))}

					{/* A page the host bundles, to a holder of a role on it who
					    administers nothing: its entry, outside Admin. */}
					{!showAdmin &&
						visibleSections
							.filter((section) => heldPlugins.has(section.plugin))
							.map((section) => (
								<PluginNavigationSection
									key={section.plugin}
									section={section}
									pathname={pathname}
								/>
							))}

					{showAdmin &&
						primarySections.map((section) => (
							<PluginNavigationSection
								key={section.plugin}
								section={section}
								pathname={pathname}
							/>
						))}

					{showAdmin && (
						<>
							<Separator />
							<SidebarGroup>
								<SidebarGroupContent>
									<SidebarMenu>
										<SidebarMenuItem>
											<SidebarMenuButton
												onClick={() => setAdminOpen((open) => !open)}
												isActive={onAdminRoute && !adminOpen}
											>
												<ShieldCheck className="h-4 w-4" />
												<span>Admin</span>
												<ChevronRight
													className={cn(
														"ml-auto h-4 w-4 transition-transform",
														adminOpen && "rotate-90",
													)}
												/>
											</SidebarMenuButton>
										</SidebarMenuItem>
									</SidebarMenu>
								</SidebarGroupContent>
							</SidebarGroup>
							{adminOpen &&
								adminSections.map((section) => (
									<PluginNavigationSection
										key={section.plugin}
										section={section}
										pathname={pathname}
									/>
								))}
						</>
					)}
				</SidebarContent>

				<SidebarFooter>
					<SidebarMenu>
						<SidebarMenuItem>
							<DropdownMenu>
								<DropdownMenuTrigger render={<SidebarMenuButton size="lg" />}>
									<Avatar className="h-8 w-8">
										<AvatarFallback>{userInitials}</AvatarFallback>
									</Avatar>
									<div className="flex flex-col gap-0.5 leading-none">
										<span className="text-sm font-medium">{userLabel}</span>
										{platformRole && (
											<span className="text-xs text-muted-foreground">
												{platformRole}
											</span>
										)}
									</div>
									<ChevronUp className="ml-auto h-4 w-4" />
								</DropdownMenuTrigger>
								<DropdownMenuContent side="top" align="start" className="w-56">
									{userMenu.map((item) => {
										const Icon = getNavigationIcon(item.icon);
										return (
											<DropdownMenuItem
												key={item.href}
												render={<Link href={item.href} />}
											>
												<Icon className="mr-2 h-4 w-4" />
												{item.label}
											</DropdownMenuItem>
										);
									})}
									{offerViewAs && (
										<DropdownMenuItem onClick={() => setTeamSwitcherOpen(true)}>
											<UsersRound className="mr-2 h-4 w-4" />
											{castTeamId ? "Switch team…" : "View as team…"}
										</DropdownMenuItem>
									)}
									<DropdownMenuSeparator />
									<DropdownMenuItem
										onClick={() => {
											// THE PRIMARY SIGN-OUT CONTROL: the account menu on
											// every dashboard and admin page. `onClick={logout}`
											// handed the promise to the menu, which discards it,
											// so an incomplete sign-out looked finished and the
											// route shell then sent the person to the sign-in
											// page as if the session had ended.
											void signOutAndLeave(logout);
										}}
									>
										<LogOut className="mr-2 h-4 w-4" />
										Sign out
									</DropdownMenuItem>
								</DropdownMenuContent>
							</DropdownMenu>
						</SidebarMenuItem>
					</SidebarMenu>
				</SidebarFooter>
			</Sidebar>

			<SidebarInset>
				<CastIndicator />
				<header className="flex h-14 items-center gap-2 border-b px-4">
					<SidebarTrigger />
					<Separator orientation="vertical" className="h-6" />
					<div className="flex-1" />
					<ThemeToggle />
					<NotificationBell />
				</header>
				<NotificationBanner />
				{/* Route-content fade-in. The key={pathname} forces React
            to remount the wrapper on every navigation, replaying the
            CSS animation. Snappy (200ms) so it feels like polish, not
            a delay. */}
				<main
					key={pathname}
					className="flex-1 p-6 animate-in fade-in slide-in-from-bottom-1 duration-200"
				>
					{children}
				</main>
			</SidebarInset>

			<TeamSwitcher />
			<Toaster />
			<RateLimitBanner />
		</SidebarProvider>
	);
}
