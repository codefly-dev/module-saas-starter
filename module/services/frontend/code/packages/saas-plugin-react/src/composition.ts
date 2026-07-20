import {
	defineFrontend,
	definePlugin,
	type FrontendConfig,
	type FrontendDefinition,
	type FrontendPlugin,
	type InstalledDashboardWidget,
	type InstalledPluginRoute,
} from "@codefly/saas-plugin-contract";
import type { ComponentType, LazyExoticComponent } from "react";

export type FrontendPluginComponent = LazyExoticComponent<ComponentType>;

type RouteID<Plugin extends FrontendPlugin> = NonNullable<
	Plugin["routes"]
>[number]["id"];
type WidgetID<Plugin extends FrontendPlugin> = NonNullable<
	Plugin["widgets"]
>[number]["id"];

export interface ReactRouteRegistration<ID extends string = string> {
	id: ID;
	component: FrontendPluginComponent;
}

export interface ReactWidgetRegistration<ID extends string = string> {
	id: ID;
	component: FrontendPluginComponent;
}

export interface FrontendReactPluginDefinition<
	Plugin extends FrontendPlugin = FrontendPlugin,
> {
	manifest: Plugin;
	routes?: readonly ReactRouteRegistration<RouteID<Plugin>>[];
	widgets?: readonly ReactWidgetRegistration<WidgetID<Plugin>>[];
}

export interface FrontendReactPlugin<
	Plugin extends FrontendPlugin = FrontendPlugin,
> {
	manifest: Plugin;
	routes: readonly ReactRouteRegistration<RouteID<Plugin>>[];
	widgets: readonly ReactWidgetRegistration<WidgetID<Plugin>>[];
}

export interface ReactPluginRoute extends InstalledPluginRoute {
	component: FrontendPluginComponent;
}

export interface ReactDashboardWidget extends InstalledDashboardWidget {
	component: FrontendPluginComponent;
}

export type FrontendReactConfig = Omit<FrontendConfig, "routes" | "widgets"> & {
	/** Pure, JSON-safe application inventory before React bindings are attached. */
	metadata: FrontendConfig;
	routes: readonly ReactPluginRoute[];
	widgets: readonly ReactDashboardWidget[];
};

export type FrontendReactDefinition = Omit<FrontendDefinition, "plugins"> & {
	plugins: readonly FrontendReactPlugin[];
};

function assertComposition(
	condition: unknown,
	message: string,
): asserts condition {
	if (!condition)
		throw new Error(`Invalid frontend React composition: ${message}`);
}

function assertExactKeys(
	value: object,
	allowed: readonly string[],
	context: string,
): void {
	const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
	assertComposition(
		unknown.length === 0,
		`${context} has unknown field '${unknown[0]}'`,
	);
}

function validateRegistrations(
	pluginName: string,
	kind: "route" | "widget",
	metadataIDs: readonly string[],
	registrations: readonly (ReactRouteRegistration | ReactWidgetRegistration)[],
): void {
	const expected = new Set(metadataIDs);
	const seen = new Set<string>();
	for (const registration of registrations) {
		assertComposition(
			registration !== null &&
				typeof registration === "object" &&
				!Array.isArray(registration),
			`plugin '${pluginName}' has a non-object React ${kind} registration`,
		);
		assertExactKeys(
			registration,
			["id", "component"],
			`plugin '${pluginName}' React ${kind} registration`,
		);
		assertComposition(
			typeof registration.id === "string" && expected.has(registration.id),
			`plugin '${pluginName}' registers unknown React ${kind} '${String(registration.id)}'`,
		);
		assertComposition(
			!seen.has(registration.id),
			`plugin '${pluginName}' registers React ${kind} '${registration.id}' more than once`,
		);
		assertComposition(
			registration.component !== null &&
				(typeof registration.component === "object" ||
					typeof registration.component === "function"),
			`plugin '${pluginName}' React ${kind} '${registration.id}' has no component`,
		);
		seen.add(registration.id);
	}
	for (const id of metadataIDs) {
		assertComposition(
			seen.has(id),
			`plugin '${pluginName}' is missing React ${kind} '${id}'`,
		);
	}
}

/**
 * Binds React components to a previously serializable plugin manifest. Every
 * route and widget ID must have exactly one component and extras fail closed.
 */
export function defineReactPlugin<const Plugin extends FrontendPlugin>(
	definition: FrontendReactPluginDefinition<Plugin>,
): FrontendReactPlugin<Plugin> {
	assertComposition(
		definition !== null &&
			typeof definition === "object" &&
			!Array.isArray(definition),
		"plugin definition must be an object",
	);
	assertExactKeys(definition, ["manifest", "routes", "widgets"], "plugin");
	const manifest = definePlugin(definition.manifest);
	const routes = definition.routes ?? [];
	const widgets = definition.widgets ?? [];
	assertComposition(
		Array.isArray(routes),
		`plugin '${manifest.name}' React routes must be an array`,
	);
	assertComposition(
		Array.isArray(widgets),
		`plugin '${manifest.name}' React widgets must be an array`,
	);
	validateRegistrations(
		manifest.name,
		"route",
		(manifest.routes ?? []).map((route) => route.id),
		routes,
	);
	validateRegistrations(
		manifest.name,
		"widget",
		(manifest.widgets ?? []).map((widget) => widget.id),
		widgets,
	);
	return Object.freeze({
		manifest,
		routes: Object.freeze([...routes]),
		widgets: Object.freeze([...widgets]),
	});
}

function registrationKey(plugin: string, id: string): string {
	return `${plugin}\u0000${id}`;
}

/** Resolve pure application metadata and attach the validated React handles. */
export function defineReactFrontend(
	definition: FrontendReactDefinition,
): FrontendReactConfig {
	assertComposition(
		definition !== null &&
			typeof definition === "object" &&
			!Array.isArray(definition),
		"frontend definition must be an object",
	);
	assertExactKeys(
		definition,
		["branding", "appearance", "plugins", "filesystemRoutes"],
		"frontend definition",
	);
	assertComposition(
		Array.isArray(definition.plugins),
		"frontend plugins must be an array",
	);
	const plugins = definition.plugins.map((plugin) => defineReactPlugin(plugin));
	const metadata = defineFrontend({
		branding: definition.branding,
		appearance: definition.appearance,
		plugins: plugins.map((plugin) => plugin.manifest),
		filesystemRoutes: definition.filesystemRoutes,
	});
	const routeComponents = new Map<string, FrontendPluginComponent>();
	const widgetComponents = new Map<string, FrontendPluginComponent>();
	for (const plugin of plugins) {
		for (const route of plugin.routes)
			routeComponents.set(
				registrationKey(plugin.manifest.name, route.id),
				route.component,
			);
		for (const widget of plugin.widgets)
			widgetComponents.set(
				registrationKey(plugin.manifest.name, widget.id),
				widget.component,
			);
	}
	const routes = metadata.routes.map((route) =>
		Object.freeze({
			...route,
			component: routeComponents.get(registrationKey(route.plugin, route.id))!,
		}),
	);
	const widgets = metadata.widgets.map((widget) =>
		Object.freeze({
			...widget,
			component: widgetComponents.get(
				registrationKey(widget.plugin, widget.id),
			)!,
		}),
	);
	return Object.freeze({
		...metadata,
		metadata,
		routes: Object.freeze(routes),
		widgets: Object.freeze(widgets),
	});
}
