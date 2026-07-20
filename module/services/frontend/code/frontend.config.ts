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
	appearance: {
		defaultTheme: "system",
		radius: "0.75rem",
		light: {
			primary: "oklch(0.52 0.22 270)",
			primaryForeground: "oklch(0.985 0 0)",
			ring: "oklch(0.52 0.22 270)",
			sidebarPrimary: "oklch(0.52 0.22 270)",
			sidebarPrimaryForeground: "oklch(0.985 0 0)",
			chart1: "oklch(0.52 0.22 270)",
		},
		dark: {
			primary: "oklch(0.72 0.17 270)",
			primaryForeground: "oklch(0.16 0.03 270)",
			ring: "oklch(0.72 0.17 270)",
			sidebarPrimary: "oklch(0.72 0.17 270)",
			sidebarPrimaryForeground: "oklch(0.16 0.03 270)",
			chart1: "oklch(0.72 0.17 270)",
		},
	},
	plugins: installedPlugins,
	filesystemRoutes: FRONTEND_ROUTES.map((route) => route.path),
});

export default frontendConfig;
