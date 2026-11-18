"use client";

import Link from "next/link";
import { ArrowUpRight } from "lucide-react";

import { Card, CardContent } from "@/components/ui/card";
import { useAuth } from "@/lib/auth";
import { getNavigationIcon } from "@/lib/navigation-icons";
import { groupNavigation, selectNavigation } from "@/lib/plugins/presentation";
import { useFrontendConfig } from "@/lib/providers";
import { usePublicRuntimeConfig } from "@/lib/public-runtime-config-provider";
import { Page as PageBody, PageHeader, Section, Grid } from "@/shared/ui";
import { ADMIN_CARD_DESCRIPTIONS } from "./admin-overview-descriptions";

export default function AdminOverviewClient() {
	const config = useFrontendConfig();
	const { isAuthenticated, platformRole, orgRole } = useAuth();
	const { productFeatures } = usePublicRuntimeConfig();
	const groups = groupNavigation(
		selectNavigation(
			config,
			"plugin_registry",
			{ isAuthenticated, platformRole, orgRole },
			productFeatures,
		),
	);

	return (
		<PageBody className="space-y-8">
			<PageHeader
				title="Administration"
				description="Manage your people, connected services, and workspace settings."
			/>

			{groups.map((group) => (
				<Section key={group.group} title={group.group}>
					<Grid cols={2}>
						{group.items.map((item) => {
							const Icon = getNavigationIcon(item.icon);
							const description = item.id ? ADMIN_CARD_DESCRIPTIONS[item.id] : undefined;
							return (
								<Link key={item.href} href={item.href} className="group rounded-xl outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2">
									<Card className="h-full transition-colors group-hover:bg-muted/50 group-focus-visible:bg-muted/50">
										<CardContent className="flex items-start gap-4 py-5">
											<span className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground"><Icon className="size-5" aria-hidden="true" /></span>
											<div className="min-w-0 flex-1 space-y-1">
												<h3 className="text-sm font-semibold text-foreground">{item.label}</h3>
												{description && <p className="text-sm leading-relaxed text-muted-foreground">{description}</p>}
											</div>
											<ArrowUpRight className="mt-1 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
										</CardContent>
									</Card>
								</Link>
							);
						})}
					</Grid>
				</Section>
			))}
		</PageBody>
	);
}
