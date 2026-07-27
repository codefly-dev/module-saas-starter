import { type FrontendServiceBinding } from "@codefly/saas-plugin-contract";
import { defineReactFrontend } from "@codefly/saas-plugin-react";
import { FRONTEND_ROUTES } from "@/gen/saas/frontend/v1/plugin_catalog";
import { auditPlugin } from "@/plugins/audit";
import { coreUsersPlugin } from "@/plugins/core-users";
import { platformAdminPlugin } from "@/plugins/platform-admin";

/**
 * Application-owned frontend composition root (FP-003).
 * Product packages are imported and registered explicitly here. There is no
 * directory scanning, implicit side-effect registration, or product logic in
 * generic starter source.
 */
export const installedPlugins = [
	auditPlugin,
	coreUsersPlugin,
	platformAdminPlugin,
] as const;

/**
 * Application-owned logical service wiring (FP-007).
 * Each installed plugin service requirement must map to one Codefly module and
 * service. Protocol comes from the plugin requirement; URLs and credentials are
 * deliberately inexpressible here.
 */
export const serviceBindings =
	[] as const satisfies readonly FrontendServiceBinding[];

const frontendConfig = defineReactFrontend({
	branding: {
		name: "SaaS Application",
		mark: "S",
		title: "SaaS Application",
		description: "A secure multi-tenant application.",
	},
	plugins: installedPlugins,
	filesystemRoutes: FRONTEND_ROUTES.map((route) => route.path),
});

export default frontendConfig;
