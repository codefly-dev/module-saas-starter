export const FRONTEND_PLUGIN_CONTRACT_VERSION = 2 as const;

export type PresentationAccess =
	| "public"
	| "authenticated"
	| "admin"
	| "super_admin";
export type PresentationRole = "admin" | "super_admin";
export type NavigationSurface =
	| "command_palette"
	| "plugin_registry"
	| "sidebar"
	| "user_menu";

export interface PresentationRequirement {
	access?: PresentationAccess;
	requiredRole?: PresentationRole;
	/** Host-evaluated presentation permission. Backend authorization remains authoritative. */
	requiredPermission?: string;
}

export interface NavItem extends PresentationRequirement {
	id?: string;
	plugin?: string;
	label: string;
	href: string;
	icon?: string;
	group?: string;
	surfaces?: readonly NavigationSurface[];
	order?: number;
}

export type NavigationPlacement = "primary" | "admin";

export interface PluginNavigation {
	label: string;
	placement: NavigationPlacement;
	priority?: number;
}

export interface DashboardWidget extends PresentationRequirement {
	id: string;
	slot?: string;
	priority?: number;
}

export interface PluginRoute extends PresentationRequirement {
	id: string;
	path: string;
}

/** Browser-facing protocols supported by the host's constrained plugin BFF. */
export type FrontendServiceProtocol = "connect" | "rest";

/** Exact backend API contract required by a frontend plugin. */
export interface FrontendServiceCompatibility {
	/** Product-owned, stable API contract identifier. */
	contract: string;
	/** Exact positive contract major required by this frontend package. */
	major: number;
}

/** Serializable backend requirement declared by a product plugin. */
export interface FrontendServiceRequirement {
	/** Plugin-local logical name; deployment addresses never enter this contract. */
	alias: string;
	protocol: FrontendServiceProtocol;
	/** Absolute upstream path prefix that the generated host allowlist may expose. */
	routePrefix: string;
	compatibility: FrontendServiceCompatibility;
}

/** Host composition inventory entry with explicit plugin ownership. */
export interface InstalledFrontendService extends FrontendServiceRequirement {
	plugin: string;
}

/** Logical Codefly deployment target; never a hostname, URL, or credential. */
export interface FrontendServiceTarget {
	module: string;
	service: string;
}

/** Application-owned mapping from one installed plugin alias to a logical target. */
export interface FrontendServiceBinding {
	plugin: string;
	alias: string;
	target: FrontendServiceTarget;
}

/** Server-only routing entry produced after requirements and bindings converge. */
export interface FrontendServiceAllowlistEntry
	extends InstalledFrontendService {
	target: FrontendServiceTarget & { endpoint: FrontendServiceProtocol };
}

export interface FrontendServiceAllowlist {
	schemaVersion: 1;
	contractVersion: typeof FRONTEND_PLUGIN_CONTRACT_VERSION;
	entries: readonly FrontendServiceAllowlistEntry[];
}

export interface FrontendPlugin {
	contractVersion: typeof FRONTEND_PLUGIN_CONTRACT_VERSION;
	name: string;
	navigation?: PluginNavigation;
	navItems?: readonly NavItem[];
	services?: readonly FrontendServiceRequirement[];
	widgets?: readonly DashboardWidget[];
	routes?: readonly PluginRoute[];
}

export interface FrontendBranding {
	name: string;
	mark: string;
	title: string;
	description: string;
	logo?: FrontendLogo;
	favicon?: string;
}

export interface FrontendLogo {
	lightSrc: string;
	darkSrc?: string;
	alt: string;
}

export const FRONTEND_APPEARANCE_TOKEN_NAMES = [
	"background",
	"foreground",
	"card",
	"cardForeground",
	"popover",
	"popoverForeground",
	"primary",
	"primaryForeground",
	"secondary",
	"secondaryForeground",
	"muted",
	"mutedForeground",
	"accent",
	"accentForeground",
	"destructive",
	"border",
	"input",
	"ring",
	"sidebar",
	"sidebarForeground",
	"sidebarPrimary",
	"sidebarPrimaryForeground",
	"sidebarAccent",
	"sidebarAccentForeground",
	"sidebarBorder",
	"sidebarRing",
	"chart1",
	"chart2",
	"chart3",
	"chart4",
	"chart5",
] as const;

export type FrontendAppearanceTokenName =
	(typeof FRONTEND_APPEARANCE_TOKEN_NAMES)[number];
export type FrontendThemePreference = "light" | "dark" | "system";
export type FrontendThemeTokens = Readonly<
	Record<FrontendAppearanceTokenName, string>
>;
export type FrontendThemeTokenOverrides = Readonly<
	Partial<Record<FrontendAppearanceTokenName, string>>
>;

/** Application-owned appearance input. Omitted values inherit the neutral preset. */
export interface FrontendAppearanceDefinition {
	defaultTheme?: FrontendThemePreference;
	radius?: string;
	fontSans?: string;
	fontHeading?: string;
	light?: FrontendThemeTokenOverrides;
	dark?: FrontendThemeTokenOverrides;
}

/** Fully resolved immutable appearance consumed by the host runtime. */
export interface FrontendAppearance {
	defaultTheme: FrontendThemePreference;
	radius: string;
	fontSans: string;
	fontHeading: string;
	light: FrontendThemeTokens;
	dark: FrontendThemeTokens;
}

export interface PluginNavSection {
	plugin: string;
	label: string;
	placement: NavigationPlacement;
	priority: number;
	items: readonly NavItem[];
}

export interface InstalledDashboardWidget extends DashboardWidget {
	plugin: string;
}

export interface InstalledPluginRoute extends PluginRoute {
	plugin: string;
}

export interface FrontendConfig {
	branding: FrontendBranding;
	appearance: FrontendAppearance;
	plugins: readonly FrontendPlugin[];
	navSections: readonly PluginNavSection[];
	navItems: readonly NavItem[];
	services: readonly InstalledFrontendService[];
	widgets: readonly InstalledDashboardWidget[];
	routes: readonly InstalledPluginRoute[];
}
