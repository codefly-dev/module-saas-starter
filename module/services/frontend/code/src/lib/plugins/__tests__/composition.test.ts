import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
	type FrontendPlugin,
	validateFrontendPlugins,
} from "@codefly/saas-plugin-contract";
import {
	defineReactFrontend,
	defineReactPlugin,
	type FrontendReactPlugin,
} from "@codefly-dev/ui/plugin-host";
import { lazy } from "react";
import { describe, expect, it } from "vitest";

const Empty = lazy(async () => ({ default: () => null }));

function plugin(overrides: Partial<FrontendPlugin> = {}): FrontendReactPlugin {
	const manifest = definePlugin({
		contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
		name: "example",
		navigation: { label: "Example", placement: "primary" },
		navItems: [
			{ label: "Example", href: "/admin/example", requiredRole: "admin" },
		],
		routes: [{ id: "overview", path: "/admin/example", requiredRole: "admin" }],
		widgets: [{ id: "example.widget", slot: "dashboard.widgets" }],
		...overrides,
	});
	return defineReactPlugin({
		manifest,
		routes: (manifest.routes ?? []).map((route) => ({
			id: route.id,
			component: Empty,
		})),
		widgets: (manifest.widgets ?? []).map((widget) => ({
			id: widget.id,
			component: Empty,
		})),
	});
}

describe("frontend plugin composition", () => {
	it("composes every retained contribution and immutable application branding", () => {
		const config = defineReactFrontend({
			branding: {
				name: "Example",
				mark: "E",
				title: "Example app",
				description: "Test",
			},
			plugins: [plugin()],
		});

		expect(config.branding.name).toBe("Example");
		expect(config.navItems).toHaveLength(1);
		expect(config.routes).toHaveLength(1);
		expect(config.widgets).toHaveLength(1);
		expect(config.services).toEqual([]);
		expect(config.navSections[0]?.plugin).toBe("example");
		expect(Object.isFrozen(config)).toBe(true);
	});

	it("retains plugin-owned service requirements for host build adapters", () => {
		const config = defineReactFrontend({
			branding: {
				name: "Example",
				mark: "E",
				title: "Example app",
				description: "Test",
			},
			plugins: [
				plugin({
					services: [
						{
							alias: "api",
							protocol: "rest",
							routePrefix: "/api/v1/example",
							compatibility: { contract: "example.api", major: 1 },
						},
					],
				}),
			],
		});

		expect(config.services).toEqual([
			{
				plugin: "example",
				alias: "api",
				protocol: "rest",
				routePrefix: "/api/v1/example",
				compatibility: { contract: "example.api", major: 1 },
			},
		]);
	});

	it.each([
		["plugin name", [plugin(), plugin()]],
		["route", [plugin(), plugin({ name: "other", navItems: [], widgets: [] })]],
		[
			"widget id",
			[plugin(), plugin({ name: "other", navItems: [], routes: [] })],
		],
		[
			"navigation href",
			[plugin(), plugin({ name: "other", routes: [], widgets: [] })],
		],
	])("fails fast on duplicate %s", (_kind, plugins) => {
		expect(() =>
			validateFrontendPlugins(plugins.map((plugin) => plugin.manifest)),
		).toThrow(/contributed by both/);
	});

	it("rejects unsupported versions, unsafe paths, and filesystem collisions", () => {
		expect(() =>
			validateFrontendPlugins([plugin({ contractVersion: 1 as 2 }).manifest]),
		).toThrow(/contract version/);
		expect(() =>
			validateFrontendPlugins([
				plugin({ navItems: [{ label: "Bad", href: "relative" }] }).manifest,
			]),
		).toThrow(/must be absolute/);
		expect(() =>
			validateFrontendPlugins([plugin().manifest], ["/admin/example"]),
		).toThrow(/filesystem route/);
	});
});
