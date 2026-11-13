import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly-dev/saas-plugin-contract";
import {
	defineReactFrontend,
	defineReactPlugin,
} from "@codefly-dev/ui/plugin-host";
import { lazy } from "react";
import { describe, expect, it } from "vitest";
import {
	canPresent,
	pageEntries,
	selectNavigation,
	selectRoute,
	selectWidgets,
} from "../presentation";

const Empty = lazy(async () => ({ default: () => null }));
const matrixManifest = definePlugin({
	contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
	name: "matrix",
	navigation: { label: "Matrix", placement: "admin" },
	navItems: [
		{ label: "Admin", href: "/admin/matrix", requiredRole: "admin" },
		{
			label: "Super",
			href: "/admin/matrix/super",
			requiredRole: "super_admin",
		},
	],
	routes: [
		{
			id: "admin",
			path: "/admin/matrix",
			requiredRole: "admin",
		},
		{
			id: "super",
			path: "/admin/matrix/super",
			requiredRole: "super_admin",
		},
	],
	widgets: [
		{ id: "matrix.admin", requiredRole: "admin" },
		{ id: "matrix.super", requiredRole: "super_admin" },
	],
});

const config = defineReactFrontend({
	branding: { name: "Test", mark: "T", title: "Test", description: "Test" },
	plugins: [
		defineReactPlugin({
			manifest: matrixManifest,
			routes: [
				{ id: "admin", component: Empty },
				{ id: "super", component: Empty },
			],
			widgets: [
				{ id: "matrix.admin", component: Empty },
				{ id: "matrix.super", component: Empty },
			],
		}),
	],
});

const principals = [
	{
		name: "anonymous",
		principal: { isAuthenticated: false },
		admin: false,
		super: false,
	},
	{
		name: "member",
		principal: { isAuthenticated: true, orgRole: "member" as const },
		admin: false,
		super: false,
	},
	{
		name: "org admin",
		principal: { isAuthenticated: true, orgRole: "admin" as const },
		admin: true,
		super: false,
	},
	{
		name: "support",
		principal: { isAuthenticated: true, platformRole: "support" as const },
		admin: true,
		super: false,
	},
	{
		name: "super admin",
		principal: { isAuthenticated: true, platformRole: "super_admin" as const },
		admin: true,
		super: true,
	},
];

describe("shared presentation authorization", () => {
	it.each(principals)(
		"gives routes, navigation, widgets, commands, and tiles identical results for $name",
		({ principal, admin, super: superAllowed }) => {
			expect(canPresent({ requiredRole: "admin" }, principal)).toBe(admin);
			expect(canPresent({ requiredRole: "super_admin" }, principal)).toBe(
				superAllowed,
			);

			for (const surface of [
				"sidebar",
				"command_palette",
				"plugin_registry",
			] as const) {
				expect(
					selectNavigation(config, surface, principal, {
						subscriptions: false,
						sso: false,
						entitlements: false,
					}).map((item) => item.label),
				).toEqual([
					...(admin ? ["Admin"] : []),
					...(superAllowed ? ["Super"] : []),
				]);
			}
			expect(
				selectWidgets(config, "dashboard.widgets", principal).map(
					(item) => item.id,
				),
			).toEqual([
				...(admin ? ["matrix.admin"] : []),
				...(superAllowed ? ["matrix.super"] : []),
			]);
			expect(Boolean(selectRoute(config, "/admin/matrix", principal))).toBe(
				admin,
			);
			expect(
				Boolean(selectRoute(config, "/admin/matrix/super", principal)),
			).toBe(superAllowed);
		},
	);
});

// A page the host bundles (its plugin bound to the page's module) is its role
// holders' to see and open, as a remote page is: a member holding a role on
// it meets the administrator gate its entry names, a member holding none
// does not, and a super administrator's gate yields to nobody. Red before:
// a viewer of the page who administers nothing had no entry and no route.
// The page is its entry alone: the plugin's other admin routes and items keep
// their gate, and a plugin also bound to another module is no page's.
describe("a page the host bundles", () => {
	const features = { subscriptions: false, sso: false, entitlements: false };
	const bindings = [
		{
			plugin: "matrix",
			alias: "api",
			target: { module: "matrix-module", service: "api" },
		},
		{
			plugin: "other",
			alias: "api",
			target: { module: "other-module", service: "api" },
		},
	];
	const plugins = [matrixManifest];
	const member = { isAuthenticated: true, orgRole: "member" as const };
	const holder = {
		...member,
		heldEntries: pageEntries(["matrix-module"], bindings, plugins),
	};

	it("is the entry its plugin's first navigation item opens", () => {
		expect([...pageEntries(["matrix-module"], bindings, plugins)]).toEqual([
			["matrix", "/admin/matrix"],
		]);
		expect(pageEntries([], bindings, plugins).size).toBe(0);
	});

	it("is no page of a plugin also bound to another module", () => {
		const shared = [
			...bindings,
			{
				plugin: "matrix",
				alias: "admin",
				target: { module: "host", service: "accounts" },
			},
		];
		expect(pageEntries(["matrix-module"], shared, plugins).size).toBe(0);
	});

	it("is presented, entry and route, to a member holding a role on it", () => {
		expect(
			selectNavigation(config, "sidebar", holder, features).map(
				(item) => item.label,
			),
		).toEqual(["Admin"]);
		expect(Boolean(selectRoute(config, "/admin/matrix", holder))).toBe(true);
		expect(
			canPresent(
				{ requiredRole: "admin", plugin: "matrix", href: "/admin/matrix" },
				holder,
			),
		).toBe(true);
		expect(Boolean(selectRoute(config, "/admin/matrix/super", holder))).toBe(
			false,
		);
	});

	it("opens none of the plugin's other admin routes, items or widgets", () => {
		const settings = defineReactFrontend({
			branding: { name: "Test", mark: "T", title: "Test", description: "Test" },
			plugins: [
				defineReactPlugin({
					manifest: definePlugin({
						contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
						name: "matrix",
						navigation: { label: "Matrix", placement: "admin" },
						navItems: [
							{ label: "Admin", href: "/admin/matrix", requiredRole: "admin" },
							{
								label: "Settings",
								href: "/admin/matrix/settings",
								requiredRole: "admin",
							},
						],
						routes: [
							{ id: "admin", path: "/admin/matrix", requiredRole: "admin" },
							{
								id: "settings",
								path: "/admin/matrix/settings",
								requiredRole: "admin",
							},
						],
						widgets: [{ id: "matrix.admin", requiredRole: "admin" }],
					}),
					routes: [
						{ id: "admin", component: Empty },
						{ id: "settings", component: Empty },
					],
					widgets: [{ id: "matrix.admin", component: Empty }],
				}),
			],
		});
		const viewer = {
			...member,
			heldEntries: pageEntries(["matrix-module"], bindings, settings.plugins),
		};
		expect(
			selectNavigation(settings, "sidebar", viewer, features).map(
				(item) => item.label,
			),
		).toEqual(["Admin"]);
		expect(Boolean(selectRoute(settings, "/admin/matrix", viewer))).toBe(true);
		expect(
			Boolean(selectRoute(settings, "/admin/matrix/settings", viewer)),
		).toBe(false);
		expect(selectWidgets(settings, "dashboard.widgets", viewer)).toEqual([]);
	});

	it("is not presented to a member holding a role on another page, or signed out", () => {
		const other = {
			...member,
			heldEntries: pageEntries(["other-module"], bindings, plugins),
		};
		expect(selectNavigation(config, "sidebar", other, features)).toEqual([]);
		expect(Boolean(selectRoute(config, "/admin/matrix", other))).toBe(false);
		const signedOut = {
			isAuthenticated: false,
			heldEntries: pageEntries(["matrix-module"], bindings, plugins),
		};
		expect(Boolean(selectRoute(config, "/admin/matrix", signedOut))).toBe(
			false,
		);
	});
});
