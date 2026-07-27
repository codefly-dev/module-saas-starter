import { resolveFrontendAppearance } from "./appearance.js";
import {
	type DashboardWidget,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
	type FrontendBranding,
	type FrontendConfig,
	type FrontendPlugin,
	type FrontendServiceRequirement,
	type InstalledDashboardWidget,
	type InstalledFrontendService,
	type InstalledPluginRoute,
	type NavItem,
	type PluginNavigation,
	type PluginRoute,
	type PresentationRequirement,
} from "./contracts.js";

function assertContribution(
	condition: unknown,
	message: string,
): asserts condition {
	if (!condition)
		throw new Error(`Invalid frontend plugin composition: ${message}`);
}

function normalizedPath(path: string): string {
	return path.length > 1 ? path.replace(/\/+$/, "") : path;
}

const LOGICAL_ID = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;
const PERMISSION_ID = /^[a-z][a-z0-9._-]*(?::[a-z][a-z0-9._-]*)*$/;
const SAFE_ROUTE_PREFIX = /^\/[A-Za-z0-9._~-]+(?:\/[A-Za-z0-9._~-]+)*$/;
const SAFE_BRAND_ASSET = /^(?:\/(?!\/)[^\r\n]*|https:\/\/[^\s]+)$/;

function assertExactKeys(
	value: object,
	allowed: readonly string[],
	context: string,
): void {
	const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
	assertContribution(
		unknown.length === 0,
		`${context} has unknown field '${unknown[0]}'`,
	);
}

function validatePresentationRequirement(
	pluginName: string,
	contribution: PresentationRequirement,
	context: string,
): void {
	assertContribution(
		contribution.access === undefined ||
			["public", "authenticated", "admin", "super_admin"].includes(
				contribution.access,
			),
		`plugin '${pluginName}' ${context} has unsupported access '${String(contribution.access)}'`,
	);
	assertContribution(
		contribution.requiredRole === undefined ||
			contribution.requiredRole === "admin" ||
			contribution.requiredRole === "super_admin",
		`plugin '${pluginName}' ${context} has unsupported required role '${String(contribution.requiredRole)}'`,
	);
	assertContribution(
		contribution.requiredPermission === undefined ||
			(typeof contribution.requiredPermission === "string" &&
				PERMISSION_ID.test(contribution.requiredPermission)),
		`plugin '${pluginName}' ${context} has unsafe required permission '${String(contribution.requiredPermission)}'`,
	);
}

function validateNavigation(
	pluginName: string,
	navigation: PluginNavigation,
): void {
	assertContribution(
		navigation !== null &&
			typeof navigation === "object" &&
			!Array.isArray(navigation),
		`plugin '${pluginName}' navigation must be an object`,
	);
	assertExactKeys(
		navigation,
		["label", "placement", "priority"],
		`plugin '${pluginName}' navigation`,
	);
	assertContribution(
		typeof navigation.label === "string" && navigation.label.trim().length > 0,
		`plugin '${pluginName}' navigation label cannot be empty`,
	);
	assertContribution(
		navigation.placement === "primary" || navigation.placement === "admin",
		`plugin '${pluginName}' navigation placement '${String(navigation.placement)}' is unsupported`,
	);
	assertContribution(
		navigation.priority === undefined ||
			Number.isSafeInteger(navigation.priority),
		`plugin '${pluginName}' navigation priority must be an integer`,
	);
}

function validateNavItem(pluginName: string, item: NavItem): void {
	assertContribution(
		item !== null && typeof item === "object" && !Array.isArray(item),
		`plugin '${pluginName}' has a non-object navigation item`,
	);
	assertExactKeys(
		item,
		[
			"id",
			"plugin",
			"label",
			"href",
			"icon",
			"group",
			"surfaces",
			"order",
			"access",
			"requiredRole",
			"requiredPermission",
		],
		`plugin '${pluginName}' navigation item`,
	);
	assertContribution(
		typeof item.label === "string" && item.label.trim().length > 0,
		`plugin '${pluginName}' navigation label cannot be empty`,
	);
	assertContribution(
		typeof item.href === "string" && item.href.startsWith("/"),
		`plugin '${pluginName}' navigation href '${String(item.href)}' must be absolute`,
	);
	assertContribution(
		item.id === undefined || LOGICAL_ID.test(item.id),
		`plugin '${pluginName}' navigation id '${String(item.id)}' is unsafe`,
	);
	assertContribution(
		item.plugin === undefined || item.plugin === pluginName,
		`plugin '${pluginName}' navigation item cannot claim plugin '${String(item.plugin)}'`,
	);
	assertContribution(
		item.icon === undefined ||
			(typeof item.icon === "string" && item.icon.trim().length > 0),
		`plugin '${pluginName}' navigation icon must be a non-empty string`,
	);
	assertContribution(
		item.group === undefined ||
			(typeof item.group === "string" && item.group.trim().length > 0),
		`plugin '${pluginName}' navigation group must be a non-empty string`,
	);
	assertContribution(
		item.order === undefined || Number.isSafeInteger(item.order),
		`plugin '${pluginName}' navigation order must be an integer`,
	);
	assertContribution(
		item.surfaces === undefined ||
			(Array.isArray(item.surfaces) &&
				item.surfaces.every((surface) =>
					[
						"command_palette",
						"plugin_registry",
						"sidebar",
						"user_menu",
					].includes(surface),
				)),
		`plugin '${pluginName}' navigation surfaces are invalid`,
	);
	validatePresentationRequirement(pluginName, item, "navigation item");
}

function validateRoute(pluginName: string, route: PluginRoute): void {
	assertContribution(
		route !== null && typeof route === "object" && !Array.isArray(route),
		`plugin '${pluginName}' has a non-object route`,
	);
	assertExactKeys(
		route,
		["id", "path", "access", "requiredRole", "requiredPermission"],
		`plugin '${pluginName}' route`,
	);
	assertContribution(
		typeof route.id === "string" && LOGICAL_ID.test(route.id),
		`plugin '${pluginName}' route id '${String(route.id)}' is unsafe`,
	);
	assertContribution(
		typeof route.path === "string" && route.path.startsWith("/"),
		`plugin '${pluginName}' route '${String(route.path)}' must be absolute`,
	);
	validatePresentationRequirement(pluginName, route, `route '${route.id}'`);
}

function validateWidget(pluginName: string, widget: DashboardWidget): void {
	assertContribution(
		widget !== null && typeof widget === "object" && !Array.isArray(widget),
		`plugin '${pluginName}' has a non-object widget`,
	);
	assertExactKeys(
		widget,
		["id", "slot", "priority", "access", "requiredRole", "requiredPermission"],
		`plugin '${pluginName}' widget`,
	);
	assertContribution(
		typeof widget.id === "string" && LOGICAL_ID.test(widget.id),
		`plugin '${pluginName}' widget id '${String(widget.id)}' is unsafe`,
	);
	assertContribution(
		widget.slot === undefined ||
			(typeof widget.slot === "string" && LOGICAL_ID.test(widget.slot)),
		`plugin '${pluginName}' widget '${widget.id}' has an unsafe slot`,
	);
	assertContribution(
		widget.priority === undefined || Number.isSafeInteger(widget.priority),
		`plugin '${pluginName}' widget '${widget.id}' priority must be an integer`,
	);
	validatePresentationRequirement(pluginName, widget, `widget '${widget.id}'`);
}

function validateServiceRequirement(
	pluginName: string,
	service: FrontendServiceRequirement,
): void {
	assertContribution(
		service !== null && typeof service === "object" && !Array.isArray(service),
		`plugin '${pluginName}' has a non-object service requirement`,
	);
	assertExactKeys(
		service,
		["alias", "protocol", "routePrefix", "compatibility"],
		`plugin '${pluginName}' service requirement`,
	);
	assertContribution(
		typeof service.alias === "string" && LOGICAL_ID.test(service.alias),
		`plugin '${pluginName}' service alias '${String(service.alias)}' is unsafe`,
	);
	assertContribution(
		service.protocol === "connect" || service.protocol === "rest",
		`plugin '${pluginName}' service '${service.alias}' uses unsupported protocol '${String(service.protocol)}'`,
	);
	const routeSegments =
		typeof service.routePrefix === "string"
			? service.routePrefix.slice(1).split("/")
			: [];
	assertContribution(
		typeof service.routePrefix === "string" &&
			SAFE_ROUTE_PREFIX.test(service.routePrefix) &&
			routeSegments.every((segment) => segment !== "." && segment !== ".."),
		`plugin '${pluginName}' service '${service.alias}' route prefix '${String(service.routePrefix)}' is unsafe`,
	);
	assertContribution(
		service.compatibility !== null &&
			typeof service.compatibility === "object" &&
			!Array.isArray(service.compatibility),
		`plugin '${pluginName}' service '${service.alias}' is missing compatibility metadata`,
	);
	assertExactKeys(
		service.compatibility,
		["contract", "major", "probePath"],
		`plugin '${pluginName}' service '${service.alias}' compatibility`,
	);
	assertContribution(
		typeof service.compatibility.contract === "string" &&
			LOGICAL_ID.test(service.compatibility.contract),
		`plugin '${pluginName}' service '${service.alias}' compatibility contract '${String(service.compatibility.contract)}' is unsafe`,
	);
	assertContribution(
		Number.isSafeInteger(service.compatibility.major) &&
			service.compatibility.major > 0,
		`plugin '${pluginName}' service '${service.alias}' compatibility major must be a positive integer`,
	);
	if (service.compatibility.probePath !== undefined) {
		const probeSegments =
			typeof service.compatibility.probePath === "string"
				? service.compatibility.probePath.slice(1).split("/")
				: [];
		assertContribution(
			service.protocol === "rest" &&
				typeof service.compatibility.probePath === "string" &&
				SAFE_ROUTE_PREFIX.test(service.compatibility.probePath) &&
				probeSegments.every((segment) => segment !== "." && segment !== ".."),
			`plugin '${pluginName}' service '${service.alias}' compatibility probe path '${String(service.compatibility.probePath)}' is unsafe`,
		);
	}
}

function assertUnique(
	values: ReadonlyArray<{ value: string; owner: string }>,
	kind: string,
): void {
	const owners = new Map<string, string>();
	for (const { value, owner } of values) {
		const previous = owners.get(value);
		assertContribution(
			!previous,
			`${kind} '${value}' is contributed by both '${previous}' and '${owner}'`,
		);
		owners.set(value, owner);
	}
}

export function validateFrontendPlugins(
	plugins: readonly FrontendPlugin[],
	filesystemRoutes: readonly string[] = [],
): void {
	for (const plugin of plugins) {
		assertContribution(
			plugin !== null && typeof plugin === "object" && !Array.isArray(plugin),
			"plugin must be an object",
		);
		assertExactKeys(
			plugin,
			[
				"contractVersion",
				"name",
				"navigation",
				"navItems",
				"services",
				"widgets",
				"routes",
			],
			`plugin '${String(plugin.name)}'`,
		);
		assertContribution(
			typeof plugin.name === "string" && plugin.name.trim().length > 0,
			"plugin name cannot be empty",
		);
		assertContribution(
			LOGICAL_ID.test(plugin.name),
			`plugin name '${plugin.name}' is unsafe`,
		);
	}
	assertUnique(
		plugins.map((plugin) => ({ value: plugin.name, owner: plugin.name })),
		"plugin name",
	);

	const filesystem = new Set(filesystemRoutes.map(normalizedPath));
	for (const plugin of plugins) {
		assertContribution(
			plugin.contractVersion === FRONTEND_PLUGIN_CONTRACT_VERSION,
			`plugin '${plugin.name}' uses contract version ${String(plugin.contractVersion)}; expected ${FRONTEND_PLUGIN_CONTRACT_VERSION}`,
		);
		if ((plugin.navItems?.length ?? 0) > 0) {
			assertContribution(
				plugin.navigation,
				`plugin '${plugin.name}' contributes navigation without navigation metadata`,
			);
		}
		if (plugin.navigation) validateNavigation(plugin.name, plugin.navigation);
		assertContribution(
			plugin.navItems === undefined || Array.isArray(plugin.navItems),
			`plugin '${plugin.name}' navigation items must be an array`,
		);
		for (const item of plugin.navItems ?? [])
			validateNavItem(plugin.name, item);
		assertContribution(
			plugin.services === undefined || Array.isArray(plugin.services),
			`plugin '${plugin.name}' service requirements must be an array`,
		);
		for (const service of plugin.services ?? []) {
			validateServiceRequirement(plugin.name, service);
		}
		assertUnique(
			(plugin.services ?? []).map((service) => ({
				value: service.alias,
				owner: plugin.name,
			})),
			`service alias in plugin '${plugin.name}'`,
		);
		assertUnique(
			(plugin.services ?? []).map((service) => ({
				value: `${service.protocol}:${normalizedPath(service.routePrefix)}`,
				owner: service.alias,
			})),
			`service route in plugin '${plugin.name}'`,
		);
		assertContribution(
			plugin.routes === undefined || Array.isArray(plugin.routes),
			`plugin '${plugin.name}' routes must be an array`,
		);
		for (const route of plugin.routes ?? []) {
			validateRoute(plugin.name, route);
			const path = normalizedPath(route.path);
			assertContribution(
				!filesystem.has(path),
				`plugin '${plugin.name}' route '${path}' collides with a filesystem route`,
			);
		}
		assertUnique(
			(plugin.routes ?? []).map((route) => ({
				value: route.id,
				owner: plugin.name,
			})),
			`route id in plugin '${plugin.name}'`,
		);
		assertContribution(
			plugin.widgets === undefined || Array.isArray(plugin.widgets),
			`plugin '${plugin.name}' widgets must be an array`,
		);
		for (const widget of plugin.widgets ?? [])
			validateWidget(plugin.name, widget);
	}

	assertUnique(
		plugins.flatMap((plugin) =>
			(plugin.routes ?? []).map((route) => ({
				value: normalizedPath(route.path),
				owner: plugin.name,
			})),
		),
		"route",
	);
	assertUnique(
		plugins.flatMap((plugin) =>
			(plugin.widgets ?? []).map((widget) => ({
				value: widget.id,
				owner: plugin.name,
			})),
		),
		"widget id",
	);
	assertUnique(
		plugins.flatMap((plugin) =>
			(plugin.navItems ?? []).map((item) => ({
				value: normalizedPath(item.href),
				owner: plugin.name,
			})),
		),
		"navigation href",
	);
}

/**
 * Defines and validates one product package manifest while preserving its
 * literal contribution IDs, service aliases, protocols, and route paths.
 * The application host validates the complete installed set again so
 * cross-plugin and filesystem-route collisions still fail at composition.
 */
export function definePlugin<const Plugin extends FrontendPlugin>(
	plugin: Plugin,
): Plugin {
	validateFrontendPlugins([plugin]);
	return plugin;
}

export interface FrontendDefinition {
	branding: FrontendBranding;
	appearance?: import("./contracts.js").FrontendAppearanceDefinition;
	plugins: readonly FrontendPlugin[];
	filesystemRoutes?: readonly string[];
}

export function defineFrontend(definition: FrontendDefinition): FrontendConfig {
	assertExactKeys(
		definition,
		["branding", "appearance", "plugins", "filesystemRoutes"],
		"frontend definition",
	);
	assertExactKeys(
		definition.branding,
		["name", "mark", "title", "description", "logo", "favicon"],
		"branding",
	);
	assertContribution(
		definition.branding.name.trim().length > 0,
		"branding name cannot be empty",
	);
	assertContribution(
		definition.branding.mark.trim().length > 0,
		"branding mark cannot be empty",
	);
	if (definition.branding.logo) {
		assertExactKeys(
			definition.branding.logo,
			["lightSrc", "darkSrc", "alt"],
			"branding logo",
		);
		assertContribution(
			SAFE_BRAND_ASSET.test(definition.branding.logo.lightSrc) &&
				(definition.branding.logo.darkSrc === undefined ||
					SAFE_BRAND_ASSET.test(definition.branding.logo.darkSrc)),
			"branding logo must use an application-relative or HTTPS source",
		);
		assertContribution(
			definition.branding.logo.alt.trim().length > 0,
			"branding logo alt text cannot be empty",
		);
	}
	assertContribution(
		definition.branding.favicon === undefined ||
			SAFE_BRAND_ASSET.test(definition.branding.favicon),
		"branding favicon must use an application-relative or HTTPS source",
	);
	validateFrontendPlugins(definition.plugins, definition.filesystemRoutes);
	const branding = {
		...definition.branding,
		...(definition.branding.logo
			? { logo: Object.freeze({ ...definition.branding.logo }) }
			: {}),
	};

	return Object.freeze({
		branding: Object.freeze(branding),
		appearance: resolveFrontendAppearance(definition.appearance),
		plugins: Object.freeze([...definition.plugins]),
		navSections: Object.freeze(
			definition.plugins
				.filter((plugin) => (plugin.navItems?.length ?? 0) > 0)
				.map((plugin, index) => ({
					plugin: plugin.name,
					label: plugin.navigation?.label ?? plugin.name,
					placement: plugin.navigation?.placement ?? "admin",
					priority: plugin.navigation?.priority ?? index,
					items: plugin.navItems ?? [],
				}))
				.sort((a, b) => a.priority - b.priority),
		),
		navItems: Object.freeze(
			definition.plugins
				.flatMap((plugin) => plugin.navItems ?? [])
				.sort((a, b) => (a.order ?? 0) - (b.order ?? 0)),
		),
		services: Object.freeze(
			definition.plugins
				.flatMap((plugin): InstalledFrontendService[] =>
					(plugin.services ?? []).map((service) => ({
						plugin: plugin.name,
						alias: service.alias,
						protocol: service.protocol,
						routePrefix: service.routePrefix,
						compatibility: Object.freeze({ ...service.compatibility }),
					})),
				)
				.sort((a, b) =>
					a.plugin === b.plugin
						? a.alias.localeCompare(b.alias)
						: a.plugin.localeCompare(b.plugin),
				)
				.map((service) => Object.freeze(service)),
		),
		widgets: Object.freeze(
			definition.plugins
				.flatMap((plugin): InstalledDashboardWidget[] =>
					(plugin.widgets ?? []).map((widget) => ({
						...widget,
						plugin: plugin.name,
					})),
				)
				.sort((a, b) => (a.priority ?? 0) - (b.priority ?? 0)),
		),
		routes: Object.freeze(
			definition.plugins.flatMap((plugin): InstalledPluginRoute[] =>
				(plugin.routes ?? []).map((route) => ({
					...route,
					plugin: plugin.name,
				})),
			),
		),
	});
}
