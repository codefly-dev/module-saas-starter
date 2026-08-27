import type {
	FrontendConfig,
	NavItem,
	NavigationSurface,
	PresentationRequirement,
} from "@codefly/saas-plugin-contract";
import type {
	FrontendReactConfig,
	ReactPluginRoute,
} from "@codefly/ui/plugin-host";
import { isPermission } from "@/gen/saas/accounts/v1/frontend_catalog";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { hasPermission, isAdmin, isSuperAdmin } from "@/lib/permissions";

export interface PresentationPrincipal {
	isAuthenticated: boolean;
	platformRole?: PlatformRole;
	orgRole?: OrgRole;
}

export function canPresent(
	requirement: PresentationRequirement,
	principal: PresentationPrincipal,
): boolean {
	const access =
		requirement.access ?? requirement.requiredRole ?? "authenticated";
	const accessAllowed = (() => {
		switch (access) {
			case "public":
				return true;
			case "authenticated":
				return principal.isAuthenticated;
			case "admin":
				return (
					principal.isAuthenticated &&
					isAdmin(principal.platformRole, principal.orgRole)
				);
			case "super_admin":
				return (
					principal.isAuthenticated && isSuperAdmin(principal.platformRole)
				);
		}
	})();

	if (!accessAllowed) return false;
	if (
		requirement.requiredRole === "admin" &&
		!isAdmin(principal.platformRole, principal.orgRole)
	) {
		return false;
	}
	if (
		requirement.requiredRole === "super_admin" &&
		!isSuperAdmin(principal.platformRole)
	) {
		return false;
	}
	if (!requirement.requiredPermission) return true;
	return isPermission(requirement.requiredPermission)
		? hasPermission(
				principal.platformRole,
				principal.orgRole,
				requirement.requiredPermission,
			)
		: false;
}

export function selectNavigation(
	config: FrontendConfig,
	surface: NavigationSurface,
	principal: PresentationPrincipal,
): NavItem[] {
	return config.navItems.filter(
		(item) =>
			(
				item.surfaces ?? ["sidebar", "command_palette", "plugin_registry"]
			).includes(surface) && canPresent(item, principal),
	);
}

export function selectWidgets(
	config: FrontendReactConfig,
	slot: string,
	principal: PresentationPrincipal,
) {
	return config.widgets.filter(
		(widget) =>
			(widget.slot ?? "dashboard.widgets") === slot &&
			canPresent(widget, principal),
	);
}

export function selectRoute(
	config: FrontendReactConfig,
	pathname: string,
	principal: PresentationPrincipal,
): ReactPluginRoute | undefined {
	const normalize = (path: string) =>
		path.length > 1 ? path.replace(/\/+$/, "") : path;
	return config.routes.find(
		(route) =>
			normalize(route.path) === normalize(pathname) &&
			canPresent(route, principal),
	);
}

export interface NavigationGroup {
	group: string;
	items: NavItem[];
}

export function groupNavigation(items: readonly NavItem[]): NavigationGroup[] {
	const groups = new Map<string, NavItem[]>();
	for (const item of items) {
		if (!item.group) continue;
		const group = groups.get(item.group) ?? [];
		group.push(item);
		groups.set(item.group, group);
	}
	return [...groups].map(([group, groupItems]) => ({
		group,
		items: groupItems,
	}));
}
