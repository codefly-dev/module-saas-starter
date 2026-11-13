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
	pagePlugins,
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
// it meets the administrator gate its manifest names, a member holding none
// does not, and a super administrator's gate yields to nobody. Red before:
// a viewer of the page who administers nothing had no entry and no route.
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
	const member = { isAuthenticated: true, orgRole: "member" as const };

	it("is the plugin bound to the page's module", () => {
		expect([...pagePlugins(["matrix-module"], bindings)]).toEqual(["matrix"]);
		expect(pagePlugins([], bindings).size).toBe(0);
	});

	it("is presented, entry and route, to a member holding a role on it", () => {
		const holder = {
			...member,
			heldPlugins: pagePlugins(["matrix-module"], bindings),
		};
		expect(
			selectNavigation(config, "sidebar", holder, features).map(
				(item) => item.label,
			),
		).toEqual(["Admin"]);
		expect(Boolean(selectRoute(config, "/admin/matrix", holder))).toBe(true);
		expect(
			canPresent({ requiredRole: "admin", plugin: "matrix" }, holder),
		).toBe(true);
		expect(Boolean(selectRoute(config, "/admin/matrix/super", holder))).toBe(
			false,
		);
	});

	it("is not presented to a member holding a role on another page, or signed out", () => {
		const other = {
			...member,
			heldPlugins: pagePlugins(["other-module"], bindings),
		};
		expect(selectNavigation(config, "sidebar", other, features)).toEqual([]);
		expect(Boolean(selectRoute(config, "/admin/matrix", other))).toBe(false);
		const signedOut = {
			isAuthenticated: false,
			heldPlugins: pagePlugins(["matrix-module"], bindings),
		};
		expect(Boolean(selectRoute(config, "/admin/matrix", signedOut))).toBe(
			false,
		);
	});
});
