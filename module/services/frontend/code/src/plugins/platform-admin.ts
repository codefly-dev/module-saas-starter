import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import { defineReactPlugin } from "@codefly/saas-plugin-react";
import { FRONTEND_NAVIGATION } from "@/gen/saas/frontend/v1/plugin_catalog";

export const platformAdminPlugin = defineReactPlugin({
	manifest: definePlugin({
		contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
		name: "platform-admin",
		navigation: { label: "Platform", placement: "admin", priority: 20 },
		navItems: FRONTEND_NAVIGATION.filter(
			(item) => item.plugin === "platform-admin",
		),
	}),
});
