import {
	Activity,
	CreditCard,
	Flag,
	ListChecks,
	ShieldCheck,
} from "lucide-react";
import Link from "next/link";
import { Card, CardDescription, CardHeader, CardTitle } from "@/shared/ui";

const sections = [
	{
		title: "Platform Admins",
		description: "Manage platform-level admin roles and permissions.",
		href: "/admin/platform/admins",
		icon: ShieldCheck,
	},
	{
		title: "Feature Flags",
		description: "Create and toggle feature flags for gradual rollouts.",
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
];

export default function PlatformPage() {
	return (
		<div className="space-y-6">
			<div>
				<h1 className="text-2xl font-bold tracking-tight">
					Platform Management
				</h1>
				<p className="text-muted-foreground">
					Manage platform-wide settings and monitoring.
				</p>
			</div>
			<div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
				{sections.map((s) => {
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
