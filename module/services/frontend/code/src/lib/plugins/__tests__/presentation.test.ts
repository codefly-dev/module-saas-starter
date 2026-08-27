import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import {
	defineReactFrontend,
	defineReactPlugin,
} from "@codefly/ui/plugin-host";
import { lazy } from "react";
import { describe, expect, it } from "vitest";
import {
	canPresent,
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
					selectNavigation(config, surface, principal).map(
						(item) => item.label,
					),
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
