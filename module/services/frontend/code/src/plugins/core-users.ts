import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import { defineReactPlugin } from "@codefly/saas-plugin-react";
import { FRONTEND_NAVIGATION } from "@/gen/saas/frontend/v1/plugin_catalog";

export const coreUsersPlugin = defineReactPlugin({
	manifest: definePlugin({
		contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
		name: "core-users",
		navigation: { label: "Users & access", placement: "admin", priority: 10 },
		navItems: FRONTEND_NAVIGATION.filter(
			(item) => item.plugin === "core-users",
		),
	}),
});
