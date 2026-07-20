import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import { defineReactPlugin } from "@codefly/saas-plugin-react";
import { FRONTEND_NAVIGATION } from "@/gen/saas/frontend/v1/plugin_catalog";

export const auditPlugin = defineReactPlugin({
	manifest: definePlugin({
		contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
		name: "audit",
		navigation: { label: "Operations", placement: "admin", priority: 30 },
		navItems: FRONTEND_NAVIGATION.filter((item) => item.plugin === "audit"),
	}),
});
