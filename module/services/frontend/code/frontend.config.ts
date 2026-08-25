import { defineReactFrontend } from "@codefly/saas-plugin-react";
import { FRONTEND_ROUTES } from "@/gen/saas/frontend/v1/plugin_catalog";
import {
	contributedPlugins,
	contributedServiceBindings,
} from "@/generated/frontend-contributions";
import { publicSiteConfig } from "@/generated/public-site-config";
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
	...contributedPlugins,
] as const;

/**
 * Application-owned logical service wiring (FP-007).
 * Each installed plugin service requirement must map to one Codefly module and
 * service. Protocol comes from the plugin requirement; URLs and credentials are
 * deliberately inexpressible here.
 */
export const serviceBindings = contributedServiceBindings;

const frontendConfig = defineReactFrontend({
	branding: {
		name: publicSiteConfig.company.productName,
		mark: publicSiteConfig.brand.mark,
		title: `${publicSiteConfig.company.productName} application`,
		description: publicSiteConfig.company.shortDescription,
		favicon: publicSiteConfig.brand.favicon,
	},
	appearance: {
		fontSans: publicSiteConfig.brand.typography.sans,
		fontHeading: publicSiteConfig.brand.typography.heading,
		light: {
			primary: publicSiteConfig.brand.colors.primary,
			background: publicSiteConfig.brand.colors.background,
			foreground: publicSiteConfig.brand.colors.foreground,
			accent: publicSiteConfig.brand.colors.accent,
		},
	},
	plugins: installedPlugins,
	filesystemRoutes: FRONTEND_ROUTES.map((route) => route.path),
});

export default frontendConfig;
