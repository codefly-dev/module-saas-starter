"use client";

import Link from "next/link";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAuth } from "@/lib/auth";
import { getNavigationIcon } from "@/lib/navigation-icons";
import { groupNavigation, selectNavigation } from "@/lib/plugins/presentation";
import { useFrontendConfig } from "@/lib/providers";

export default function AdminOverviewClient() {
	const config = useFrontendConfig();
	const { isAuthenticated, platformRole, orgRole } = useAuth();
	const groups = groupNavigation(
		selectNavigation(config, "plugin_registry", {
			isAuthenticated,
			platformRole,
			orgRole,
		}),
	);

	return (
		<div className="space-y-8">
			<div>
				<h1 className="text-3xl font-bold tracking-tight">Admin</h1>
				<p className="text-muted-foreground mt-1">
					Manage application and platform settings.
				</p>
			</div>

			{groups.map((group) => (
				<section key={group.group} className="space-y-3">
					<h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wider">
						{group.group}
					</h2>
					<div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
						{group.items.map((item) => {
							const Icon = getNavigationIcon(item.icon);
							return (
								<Link key={item.href} href={item.href}>
									<Card className="hover:border-primary/50 transition-colors cursor-pointer h-full">
										<CardHeader className="flex flex-row items-center gap-3 pb-2">
											<Icon className="h-5 w-5 text-muted-foreground" />
											<CardTitle className="text-sm font-medium">
												{item.label}
											</CardTitle>
										</CardHeader>
										<CardContent className="text-sm text-muted-foreground">
											Open {item.label.toLocaleLowerCase()}.
										</CardContent>
									</Card>
								</Link>
							);
						})}
					</div>
				</section>
			))}
		</div>
	);
}
