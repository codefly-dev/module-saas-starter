import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import { defineReactPlugin } from "@codefly/saas-plugin-react";
import { lazy } from "react";

const manifest = definePlugin({
	contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
	name: "reference-product",
	navigation: { label: "Reference", placement: "primary" },
	navItems: [
		{
			label: "Reference overview",
			href: "/admin/reference",
			requiredPermission: "reference.console:read",
		},
	],
	routes: [
		{
			id: "overview",
			path: "/admin/reference",
			requiredPermission: "reference.console:read",
		},
	],
	widgets: [
		{
			id: "status",
			slot: "dashboard",
			requiredPermission: "reference.console:read",
		},
	],
	services: [
		{
			alias: "api",
			protocol: "rest",
			routePrefix: "/api/v1/reference",
			compatibility: { contract: "reference.api", major: 1 },
		},
	],
});

export const referenceFrontendPlugin = defineReactPlugin({
	manifest,
	routes: [
		{ id: "overview", component: lazy(() => import("./reference-route.js")) },
	],
	widgets: [
		{ id: "status", component: lazy(() => import("./reference-widget.js")) },
	],
});
