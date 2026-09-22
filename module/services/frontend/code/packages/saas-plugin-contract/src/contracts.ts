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
	/**
	 * Optional product-owned REST capability endpoint. This is a path only,
	 * never an origin. Several narrow aliases backed by one Codefly service can
	 * therefore share a service-level handshake owned by a backend plugin.
	 */
	probePath?: string;
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

import type {
	FrontendControlSizeOverrides,
	FrontendControlSizes,
	FrontendTypeRoleOverrides,
	FrontendTypeRoles,
	FrontendTypeScale,
	FrontendTypeScaleOverrides,
	FrontendTypeSlotOverrides,
	FrontendTypeSlots,
} from "./typography.js";

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
	// Interaction states of the primary action. OPTIONAL in the resolved
	// theme: a skin that decides them gets flat hover/pressed/disabled fills
	// (the way a design sheet specifies a button); one that does not keeps the
	// derived states the stylesheet falls back to. There is no neutral default
	// value for "the pressed shade of this blue", so absence is the default.
	"primaryHover",
	"primaryActive",
	"disabled",
	"disabledForeground",
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
/** The tokens a resolved theme may leave absent; the stylesheet derives them. */
export const FRONTEND_OPTIONAL_APPEARANCE_TOKEN_NAMES = [
	"primaryHover",
	"primaryActive",
	"disabled",
	"disabledForeground",
] as const satisfies readonly FrontendAppearanceTokenName[];
export type FrontendOptionalAppearanceTokenName =
	(typeof FRONTEND_OPTIONAL_APPEARANCE_TOKEN_NAMES)[number];
export type FrontendThemePreference = "light" | "dark" | "system";
export type FrontendDatePattern = "compact" | "parts";
export type FrontendThemeTokens = Readonly<
	Record<
		Exclude<FrontendAppearanceTokenName, FrontendOptionalAppearanceTokenName>,
		string
	> &
		Partial<Record<FrontendOptionalAppearanceTokenName, string>>
>;
export type FrontendThemeTokenOverrides = Readonly<
	Partial<Record<FrontendAppearanceTokenName, string>>
>;

/** Application-owned appearance input. Omitted values inherit the neutral preset. */
export interface FrontendAppearanceDefinition {
	defaultTheme?: FrontendThemePreference;
	/**
	 * Which date-entry control the product presents: one native date input
	 * ("compact") or day/month/year under a legend ("parts"). Like
	 * `defaultTheme`, this is a behaviour a component branches on rather than a
	 * token that reaches CSS — the two shapes differ in DOM, in what the person
	 * is asked to type and in what an error can point at.
	 */
	datePattern?: FrontendDatePattern;
	radius?: string;
	/**
	 * The corner of an ACTION (buttons), separate from `radius` so a design can
	 * pill its buttons without pilling every card, field and dialog. Absent, a
	 * button's corner derives from `radius` exactly as before.
	 */
	buttonRadius?: string;
	fontSans?: string;
	fontHeading?: string;
	fontMono?: string;
	/** Base spacing unit driving every Tailwind spacing utility (density). */
	spacing?: string;
	/** Root font size; rescales all rem-based typography. */
	fontSizeBase?: string;
	/** Expanded navigation sidebar width. */
	sidebarWidth?: string;
	/** Collapsed (icon-rail) navigation sidebar width. */
	sidebarWidthIcon?: string;
	/** Width of the default `border` utility applied app-wide. */
	borderWidth?: string;
	/** Unitless multiplier (0–2) on the elevation/shadow scale. */
	shadowStrength?: string;
	/** Layer 1: the ordinal type ramp every role points into. */
	typeScale?: FrontendTypeScaleOverrides;
	/** Layer 2: named bundles of size, weight, line height, tracking and family. */
	typeRoles?: FrontendTypeRoleOverrides;
	/** Layer 3: which role each surface uses. */
	typeSlots?: FrontendTypeSlotOverrides;
	/** Control geometry rungs; each carries the type role its label uses. */
	controlSizes?: FrontendControlSizeOverrides;
	light?: FrontendThemeTokenOverrides;
	dark?: FrontendThemeTokenOverrides;
}

/**
 * Every field `resolveFrontendAppearance` accepts inside `appearance`. Exported
 * because a repository that authors skin descriptors must be able to see the
 * vocabulary it is writing against: the validator is fail-closed, so a field
 * outside this list costs the descriptor its whole appearance, and a private
 * copy of the list in the authoring repository would pass while the real
 * validator disagreed.
 *
 * `appearance-fields.test.ts` holds it to the resolved appearance's own keys, so
 * the list cannot drift from what the validator reads.
 */
export const FRONTEND_APPEARANCE_FIELD_NAMES = [
	"defaultTheme",
	"datePattern",
	"radius",
	"buttonRadius",
	"fontSans",
	"fontHeading",
	"fontMono",
	"spacing",
	"fontSizeBase",
	"sidebarWidth",
	"sidebarWidthIcon",
	"borderWidth",
	"shadowStrength",
	"typeScale",
	"typeRoles",
	"typeSlots",
	"controlSizes",
	"light",
	"dark",
] as const satisfies readonly (keyof FrontendAppearanceDefinition)[];

export type FrontendAppearanceFieldName =
	(typeof FRONTEND_APPEARANCE_FIELD_NAMES)[number];

/** Fields a resolved appearance may leave absent; the stylesheet derives them. */
export const FRONTEND_OPTIONAL_APPEARANCE_FIELD_NAMES = [
	"buttonRadius",
] as const satisfies readonly FrontendAppearanceFieldName[];
export type FrontendOptionalAppearanceFieldName =
	(typeof FRONTEND_OPTIONAL_APPEARANCE_FIELD_NAMES)[number];

/** Fully resolved immutable appearance consumed by the host runtime. */
export interface FrontendAppearance {
	defaultTheme: FrontendThemePreference;
	datePattern: FrontendDatePattern;
	radius: string;
	/** Absent when the skin left it to `radius`; the stylesheet then derives it. */
	buttonRadius?: string;
	fontSans: string;
	fontHeading: string;
	fontMono: string;
	spacing: string;
	fontSizeBase: string;
	sidebarWidth: string;
	sidebarWidthIcon: string;
	borderWidth: string;
	shadowStrength: string;
	typeScale: FrontendTypeScale;
	typeRoles: FrontendTypeRoles;
	typeSlots: FrontendTypeSlots;
	controlSizes: FrontendControlSizes;
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
