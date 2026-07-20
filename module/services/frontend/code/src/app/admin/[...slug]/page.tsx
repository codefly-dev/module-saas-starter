"use client";

/**
 * Catch-all renderer for PLUGIN-PROVIDED admin pages.
 *
 * Plugins declare route metadata via `FrontendPlugin.routes` and bind lazy
 * components separately through `defineReactPlugin`. Next's filesystem router
 * can't see product package files outside `app/`, so this catch-all matches the
 * current path to a validated React route registration.
 *
 * Specific filesystem routes (users, billing, …) are matched by Next BEFORE this
 * catch-all, so only unmatched paths fall through here. The (admin) layout
 * already enforces auth + admin-tier; a route's optional `requiredRole` is an
 * additional, finer gate.
 */

import { notFound, usePathname } from "next/navigation";

import { PluginContributionBoundary } from "@/components/plugin-contribution-boundary";
import { useAuth } from "@/lib/auth";
import { selectRoute } from "@/lib/plugins/presentation";
import { useFrontendConfig } from "@/lib/providers";

export default function PluginRoutePage() {
	const pathname = usePathname();
	const config = useFrontendConfig();
	const { isAuthenticated, platformRole, orgRole } = useAuth();
	const route = selectRoute(config, pathname, {
		isAuthenticated,
		platformRole,
		orgRole,
	});

	if (!route) {
		notFound();
	}

	const Component = route.component;
	return (
		<PluginContributionBoundary
			plugin={route.plugin}
			contributionId={route.id}
			kind="route"
			services={config.metadata.services.filter(
				(service) => service.plugin === route.plugin,
			)}
		>
			<Component />
		</PluginContributionBoundary>
	);
}
