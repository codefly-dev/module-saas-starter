import type {
	FrontendConfig,
	NavItem,
	NavigationSurface,
	PresentationRequirement,
} from "@codefly-dev/saas-plugin-contract";
import type {
	FrontendReactConfig,
	ReactPluginRoute,
} from "@codefly-dev/ui/plugin-host";
import { isPermission } from "@/gen/saas/accounts/v1/frontend_catalog";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { hasPermission, isAdmin, isSuperAdmin } from "@/lib/permissions";
import {
	isProductRouteEnabled,
	type ProductFeatures,
} from "@/lib/product-features";

export interface PresentationPrincipal {
	isAuthenticated: boolean;
	platformRole?: PlatformRole;
	orgRole?: OrgRole;
	/**
	 * The plugins that are pages this person holds a role on (pagePlugins):
	 * a page the host bundles is presented to its role holders, as a remote
	 * page is, whatever administrator gate its manifest names.
	 */
	heldPlugins?: ReadonlySet<string>;
}

/**
 * The plugins that are the pages `pages` names (by alias, the module a page
 * is): each plugin with a service bound to one of those modules, as the
 * composition bound it (`contributedServiceBindings`).
 */
export function pagePlugins(
	pages: readonly string[],
	bindings: readonly { plugin: string; target: { module: string } }[],
): ReadonlySet<string> {
	return new Set(
		bindings
			.filter((binding) => pages.includes(binding.target.module))
			.map((binding) => binding.plugin),
	);
}

export function canPresent(
	requirement: PresentationRequirement & { plugin?: string },
	principal: PresentationPrincipal,
): boolean {
	// A page's own administrator gate yields to holding a role on that page:
	// a super administrator's gate never does.
	const held =
		requirement.plugin !== undefined &&
		principal.heldPlugins?.has(requirement.plugin) === true;
	if (held && principal.isAuthenticated) {
		requirement = {
			...requirement,
			access: requirement.access === "admin" ? undefined : requirement.access,
			requiredRole:
				requirement.requiredRole === "admin"
					? undefined
					: requirement.requiredRole,
		};
	}
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
	features: ProductFeatures,
): NavItem[] {
	// An item is its section's plugin's, which a held page's gate reads.
	const pluginOf = new Map(
		config.navSections.flatMap((section) =>
			section.items.map((item) => [item, section.plugin] as const),
		),
	);
	return config.navItems.filter(
		(item) =>
			(
				item.surfaces ?? ["sidebar", "command_palette", "plugin_registry"]
			).includes(surface) &&
			isProductRouteEnabled(item.href, features) &&
			canPresent(
				{ ...item, plugin: item.plugin ?? pluginOf.get(item) },
				principal,
			),
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
