import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import {
	defineReactPlugin,
	type FrontendReactPlugin,
} from "@codefly/saas-plugin-react";
import {
	type PluginServiceTransport,
	usePluginService,
} from "@codefly/saas-plugin-react/runtime";

/** Minimal product-neutral metadata fixture proving the React-free contract. */
export const referenceFrontendPluginManifest = definePlugin({
	contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
	name: "reference-product",
	navigation: { label: "Reference", placement: "primary" },
	navItems: [
		{
			label: "Reference overview",
			href: "/admin/reference",
			requiredRole: "admin",
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

/** Public React package binds render contributions separately from metadata. */
export const referenceFrontendPlugin: FrontendReactPlugin = defineReactPlugin({
	manifest: referenceFrontendPluginManifest,
});

/** Product-style hook proving runtime use without a starter-private import. */
export function useReferenceService(): PluginServiceTransport {
	return usePluginService(referenceFrontendPlugin.manifest.name, "api");
}
