"use client";

import type { NavItem, PluginNavSection } from "@codefly/saas-plugin-contract";
import { Boxes, ChevronRight, ChevronUp, LogOut, ShieldCheck } from "lucide-react";
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
import { NotificationBell } from "@/features/notifications/ui/notification-bell";
import { useAppearance } from "@/lib/appearance-provider";
import { useAuth } from "@/lib/auth";
import { getNavigationIcon } from "@/lib/navigation-icons";
import { canPresent, selectNavigation } from "@/lib/plugins/presentation";
import { useRegisteredSolutions } from "@/solutions/SolutionsMenu";
import { useFrontendConfig } from "@/lib/providers";
import { cn } from "@/lib/utils";

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
	const { user, platformRole, orgRole, isAuthenticated, logout } = useAuth();
	const config = useFrontendConfig();
	const { branding } = useAppearance();
	const principal = { isAuthenticated, platformRole, orgRole };

	const showAdmin = canPresent({ access: "admin" }, principal);

	const visibleSidebar = selectNavigation(config, "sidebar", principal);
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
	const userMenu = selectNavigation(config, "user_menu", principal);
	const registeredSolutions = useRegisteredSolutions();

	const userInitials = user?.email
		? user.email.slice(0, 2).toUpperCase()
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

					{registeredSolutions.length > 0 && (
						<SidebarGroup>
							<SidebarGroupLabel>Solutions</SidebarGroupLabel>
							<SidebarGroupContent>
								<SidebarMenu>
									{registeredSolutions.map((solution) => (
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
					)}

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
										<span className="text-sm font-medium">
											{user?.email || user?.id}
										</span>
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
									<DropdownMenuSeparator />
									<DropdownMenuItem onClick={logout}>
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
				<header className="flex h-14 items-center gap-2 border-b px-4">
					<SidebarTrigger />
					<Separator orientation="vertical" className="h-6" />
					<div className="flex-1" />
					<ThemeToggle />
					<NotificationBell />
				</header>
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

			<Toaster />
			<RateLimitBanner />
		</SidebarProvider>
	);
}
