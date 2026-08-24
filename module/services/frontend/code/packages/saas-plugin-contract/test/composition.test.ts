import { describe, expect, it } from "vitest";

import {
	defineFrontend,
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
	type FrontendPlugin,
	type FrontendServiceRequirement,
	validateFrontendPlugins,
} from "../src/index.js";

function plugin(overrides: Partial<FrontendPlugin> = {}): FrontendPlugin {
	return {
		contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
		name: "example",
		navigation: { label: "Example", placement: "primary" },
		navItems: [
			{ label: "Example", href: "/admin/example", requiredRole: "admin" },
		],
		routes: [{ id: "overview", path: "/admin/example", requiredRole: "admin" }],
		widgets: [{ id: "example.widget", slot: "dashboard.widgets" }],
		...overrides,
	};
}

function service(
	overrides: Partial<FrontendServiceRequirement> = {},
): FrontendServiceRequirement {
	return {
		alias: "control-plane",
		protocol: "rest",
		routePrefix: "/api/v1/example",
		compatibility: { contract: "example.control-plane", major: 1 },
		...overrides,
	};
}

describe("public frontend plugin composition", () => {
	it("defines one plugin with runtime diagnostics and preserved literals", () => {
		const definition = {
			contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
			name: "literal-product",
			services: [
				{
					alias: "literal-api",
					protocol: "rest",
					routePrefix: "/api/v1/literal",
					compatibility: { contract: "literal.api", major: 1 },
				},
			],
		} as const;
		const defined = definePlugin(definition);
		const name: "literal-product" = defined.name;
		const alias: "literal-api" = defined.services[0].alias;
		const protocol: "rest" = defined.services[0].protocol;

		expect(defined).toBe(definition);
		expect({ name, alias, protocol }).toEqual({
			name: "literal-product",
			alias: "literal-api",
			protocol: "rest",
		});
		expect(() =>
			definePlugin({ ...plugin(), unexpected: true } as FrontendPlugin),
		).toThrow("plugin 'example' has unknown field 'unexpected'");
		expect(() => definePlugin(plugin({ name: "" }))).toThrow(
			"plugin name cannot be empty",
		);
		expect(() => definePlugin(plugin({ name: "../private" }))).toThrow(
			"plugin name '../private' is unsafe",
		);
	});

	it("composes a product-neutral fixture through only the package root", () => {
		const config = defineFrontend({
			branding: {
				name: "Example",
				mark: "E",
				title: "Example",
				description: "Example",
			},
			appearance: {
				defaultTheme: "dark",
				radius: "0.75rem",
				spacing: "0.2rem",
				sidebarWidth: "18rem",
				shadowStrength: "1.5",
				light: { primary: "#3344ff" },
				dark: { primary: "#99aaff" },
			},
			plugins: [plugin()],
		});
		expect(config.plugins.map((item) => item.name)).toEqual(["example"]);
		expect(config.navItems).toHaveLength(1);
		expect(config.routes).toHaveLength(1);
		expect(config.widgets).toHaveLength(1);
		expect(JSON.parse(JSON.stringify(config.plugins))).toEqual(config.plugins);
		expect(JSON.parse(JSON.stringify(config.routes))).toEqual(config.routes);
		expect(JSON.parse(JSON.stringify(config.widgets))).toEqual(config.widgets);
		expect(config.appearance.defaultTheme).toBe("dark");
		expect(config.appearance.light.primary).toBe("#3344ff");
		expect(config.appearance.light.background).toBe("oklch(1 0 0)");
		expect(config.appearance.spacing).toBe("0.2rem");
		expect(config.appearance.sidebarWidth).toBe("18rem");
		expect(config.appearance.shadowStrength).toBe("1.5");
		// Omitted structural tokens inherit the neutral defaults.
		expect(config.appearance.fontSizeBase).toBe("1rem");
		expect(config.appearance.borderWidth).toBe("1px");
		expect(Object.isFrozen(config.appearance)).toBe(true);
		expect(Object.isFrozen(config.appearance.light)).toBe(true);
	});

	it("rejects unsafe or unknown application appearance fields", () => {
		const base = {
			branding: {
				name: "Example",
				mark: "E",
				title: "Example",
				description: "Example",
			},
			plugins: [plugin()],
		};
		expect(() =>
			defineFrontend({
				...base,
				appearance: { light: { primary: "red; color: transparent" } },
			}),
		).toThrow(/safe non-empty CSS value/);
		expect(() =>
			defineFrontend({
				...base,
				appearance: { palette: "unsafe" } as never,
			}),
		).toThrow(/unknown field 'palette'/);
		expect(() =>
			defineFrontend({
				...base,
				appearance: { spacing: "0.25foo" },
			}),
		).toThrow(/spacing must be 0 or a px\/rem\/em length/);
		expect(() =>
			defineFrontend({
				...base,
				appearance: { shadowStrength: "3" },
			}),
		).toThrow(/shadowStrength must be a unitless number between 0 and 2/);
	});

	it("rejects an incompatible contract major with an actionable diagnostic", () => {
		const incompatible = plugin({ contractVersion: 1 as 2 });
		expect(() => validateFrontendPlugins([incompatible])).toThrow(
			"plugin 'example' uses contract version 1; expected 2",
		);
	});

	it("rejects React handles and other non-metadata fields", () => {
		expect(() =>
			definePlugin({
				...plugin(),
				routes: [
					{
						id: "overview",
						path: "/admin/example",
						component: () => null,
					},
				],
			} as unknown as FrontendPlugin),
		).toThrow("route has unknown field 'component'");
	});

	it("rejects duplicate product contributions before rendering", () => {
		expect(() =>
			validateFrontendPlugins([
				plugin(),
				plugin({ name: "other", navItems: [], widgets: [] }),
			]),
		).toThrow(
			"route '/admin/example' is contributed by both 'example' and 'other'",
		);
	});

	it("composes a deterministic serializable service inventory", () => {
		const config = defineFrontend({
			branding: {
				name: "Example",
				mark: "E",
				title: "Example",
				description: "Example",
			},
			plugins: [
				plugin({
					services: [
						service({ alias: "telemetry", routePrefix: "/api/v1/telemetry" }),
						service(),
					],
				}),
			],
		});

		expect(config.services).toEqual([
			{ plugin: "example", ...service() },
			{
				plugin: "example",
				...service({ alias: "telemetry", routePrefix: "/api/v1/telemetry" }),
			},
		]);
		expect(JSON.parse(JSON.stringify(config.services))).toEqual(
			config.services,
		);
		expect(Object.isFrozen(config.services)).toBe(true);
		expect(Object.isFrozen(config.services[0])).toBe(true);
		expect(Object.isFrozen(config.services[0]?.compatibility)).toBe(true);
	});

	it.each([
		["unsafe alias", service({ alias: "../private" }), /service alias.*unsafe/],
		[
			"unsupported protocol",
			service({ protocol: "grpc" as "rest" }),
			/unsupported protocol/,
		],
		["root route", service({ routePrefix: "/" }), /route prefix.*unsafe/],
		[
			"traversal route",
			service({ routePrefix: "/api/../private" }),
			/route prefix.*unsafe/,
		],
		[
			"encoded route",
			service({ routePrefix: "/api/%2fprivate" }),
			/route prefix.*unsafe/,
		],
		[
			"missing compatibility",
			{
				...service(),
				compatibility: undefined,
			} as unknown as FrontendServiceRequirement,
			/missing compatibility metadata/,
		],
		[
			"unsafe contract",
			service({ compatibility: { contract: "Example API", major: 1 } }),
			/compatibility contract.*unsafe/,
		],
		[
			"invalid major",
			service({
				compatibility: { contract: "example.control-plane", major: 0 },
			}),
			/positive integer/,
		],
		[
			"unknown field",
			{
				...service(),
				endpoint: "https://example.invalid",
			} as FrontendServiceRequirement,
			/unknown field 'endpoint'/,
		],
	])(
		"rejects a service requirement with %s",
		(_case, requirement, diagnostic) => {
			expect(() =>
				validateFrontendPlugins([plugin({ services: [requirement] })]),
			).toThrow(diagnostic);
		},
	);

	it("accepts a safe product-owned REST capability probe path", () => {
		const config = defineFrontend({
			branding: {
				name: "Example",
				mark: "E",
				title: "Example",
				description: "Example",
			},
			plugins: [
				plugin({
					services: [
						service({
							compatibility: {
								contract: "example.api",
								major: 1,
								probePath: "/api/v1/plugins/example/capabilities",
							},
						}),
					],
				}),
			],
		});
		expect(config.services[0]?.compatibility.probePath).toBe(
			"/api/v1/plugins/example/capabilities",
		);
	});

	it.each([
		["relative", "api/v1/capabilities"],
		["traversal", "/api/v1/../capabilities"],
		["encoded separator", "/api/v1/%2fprivate"],
	])("rejects an unsafe capability probe path: %s", (_case, probePath) => {
		expect(() =>
			validateFrontendPlugins([
				plugin({
					services: [
						service({
							compatibility: {
								contract: "example.api",
								major: 1,
								probePath,
							},
						}),
					],
				}),
			]),
		).toThrow(/probe path.*unsafe/);
	});

	it("rejects duplicate aliases and routes within one plugin", () => {
		expect(() =>
			validateFrontendPlugins([
				plugin({
					services: [service(), service({ routePrefix: "/api/v1/other" })],
				}),
			]),
		).toThrow(/service alias/);
		expect(() =>
			validateFrontendPlugins([
				plugin({ services: [service(), service({ alias: "other" })] }),
			]),
		).toThrow(/service route/);
	});

	it("rejects a non-array service declaration", () => {
		expect(() =>
			validateFrontendPlugins([
				plugin({
					services:
						service() as unknown as readonly FrontendServiceRequirement[],
				}),
			]),
		).toThrow(/service requirements must be an array/);
	});
});
