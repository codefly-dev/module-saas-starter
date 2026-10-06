import {
	Activity,
	Building2,
	CreditCard,
	Flag,
	Layers,
	ListChecks,
	ShieldCheck,
	Workflow,
} from "lucide-react";
import Link from "next/link";
import { isProductRouteEnabled } from "@/lib/product-features";
import { readPublicRuntimeConfig } from "@/lib/read-public-runtime-config";
import { Card, CardDescription, CardHeader, CardTitle } from "@/shared/ui";

const sections = [
	{
		title: "Catalogue",
		description:
			"Every deployed module and solution: what it declares, what runs, where it is installed.",
		href: "/admin/platform/catalogue",
		icon: Layers,
	},
	{
		title: "Organizations",
		description:
			"Find any organization and manage it: members, roles, name, deletion.",
		href: "/admin/platform/organizations",
		icon: Building2,
	},
	{
		title: "Platform Admins",
		description: "Manage platform-level admin roles and permissions.",
		href: "/admin/platform/admins",
		icon: ShieldCheck,
	},
	{
		title: "Feature Flags",
		description: "Review the read-only legacy inventory awaiting migration.",
		href: "/admin/platform/feature-flags",
		icon: Flag,
	},
	{
		title: "Sessions",
		description: "View and manage active user sessions.",
		href: "/admin/sessions",
		icon: Activity,
	},
	{
		title: "Entitlements",
		description: "View and override organization entitlements and plan limits.",
		href: "/admin/entitlements",
		icon: CreditCard,
	},
	{
		title: "Job Operations",
		description: "Inspect durable queues, lifecycle history, and dead letters.",
		href: "/admin/platform/jobs",
		icon: ListChecks,
	},
	{
		title: "Event Operations",
		description:
			"Inspect the domain-event catalog, subscribers, relay lag, and dead letters.",
		href: "/admin/platform/events",
		icon: Workflow,
	},
];

export default async function PlatformPage() {
	const { productFeatures } = await readPublicRuntimeConfig();
	return (
		<div className="space-y-6">
			<div>
				<h1 data-slot="page-title" className="type-page-title">
					Platform Management
				</h1>
				<p className="text-muted-foreground">
					Manage platform-wide settings and monitoring.
				</p>
			</div>
			<div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
				{sections
					.filter((s) => isProductRouteEnabled(s.href, productFeatures))
					.map((s) => {
						const Icon = s.icon;
						return (
							<Link key={s.href} href={s.href}>
								<Card className="hover:bg-accent/50 transition-colors cursor-pointer">
									<CardHeader>
										<div className="flex items-center gap-3">
											<Icon className="h-5 w-5 text-muted-foreground" />
											<div>
												<CardTitle className="text-base">{s.title}</CardTitle>
												<CardDescription>{s.description}</CardDescription>
											</div>
										</div>
									</CardHeader>
								</Card>
							</Link>
						);
					})}
			</div>
		</div>
	);
}
