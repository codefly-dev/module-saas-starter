"use client";

import { PluginContributionBoundary } from "@/components/plugin-contribution-boundary";
import { useAuth } from "@/lib/auth";
import { selectWidgets } from "@/lib/plugins/presentation";
import { useFrontendConfig } from "@/lib/providers";

export function Slot({ name }: { name: string }) {
	const config = useFrontendConfig();
	const { isAuthenticated, platformRole, orgRole } = useAuth();
	const widgets = selectWidgets(config, name, {
		isAuthenticated,
		platformRole,
		orgRole,
	});

	return (
		<>
			{widgets.map((widget) => (
				<PluginContributionBoundary
					key={`${widget.plugin}/${widget.id}`}
					plugin={widget.plugin}
					contributionId={widget.id}
					kind="widget"
					services={config.metadata.services.filter(
						(service) => service.plugin === widget.plugin,
					)}
				>
					<widget.component />
				</PluginContributionBoundary>
			))}
		</>
	);
}
