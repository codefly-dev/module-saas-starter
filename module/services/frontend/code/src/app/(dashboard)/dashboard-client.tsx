"use client";

import {
	ArrowUpRight,
	Bell,
	FileText,
	Shield,
	ShieldCheck,
} from "lucide-react";
import Link from "next/link";
import { ActivityFeed } from "@/components/activity-feed";
import { RoleGate } from "@/components/auth/role-gate";
import { Slot } from "@/components/slot";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import {
	Badge,
	Card,
	CardDescription,
	CardHeader,
	CardTitle,
	Grid,
	Page as PageBody,
	PageHeader,
	Section,
} from "@/shared/ui";

/**
 * Main dashboard — the hosting app's home. Saas-starter is a module,
 * not the main app: everything the module adds (users, orgs, teams,
 * billing, audit) lives under /admin and is only reachable if the
 * user is admin-tier.
 *
 * Layout:
 *   - The page header greets the person and names the roles they hold.
 *   - Three personal-account cards (Security / Notifications / Data &
 *     privacy) — visible to every authenticated user.
 *   - Admin card gated by <RoleGate require="admin">.
 */
export default function DashboardClient() {
	const { user, platformRole, orgRole } = useAuth();
	const greetingName = greetingNameOf(user);

	return (
		<PageBody>
			<PageHeader
				title={`Welcome back, ${greetingName}`}
				description={
					<>
						Here&apos;s an overview of your account.{" "}
						<span className="hidden sm:inline">
							Press{" "}
							<kbd className="rounded border bg-muted px-1.5 py-0.5 font-mono text-xs">
								⌘K
							</kbd>{" "}
							to jump anywhere.
						</span>
					</>
				}
			>
				{(platformRole || orgRole) && (
					<div className="flex flex-wrap items-center gap-2 pt-2">
						{platformRole && (
							<Badge variant="secondary">
								<Shield />
								{platformRole}
							</Badge>
						)}
						{orgRole && (
							<Badge variant="secondary">Organization {orgRole}</Badge>
						)}
					</div>
				)}
			</PageHeader>

			<Grid cols={3}>
				<DashboardCard
					href="/settings/mfa"
					icon={Shield}
					title="Security"
					description="Manage how you sign in and protect your account"
				/>
				<DashboardCard
					href="/settings/notifications"
					icon={Bell}
					title="Notifications"
					description="Choose which notifications you receive"
				/>
				<DashboardCard
					href="/settings/data"
					icon={FileText}
					title="Data & privacy"
					description="Export your data or delete your account"
				/>
			</Grid>

			{/* Read-only view of recent audit events scoped to the user's
			    primary org. Hides itself when there are no events so a
			    fresh account doesn't see "nothing here yet". */}
			<RoleGate requirePermission={PERMISSIONS.AUDIT_READ}>
				<ActivityFeed />
			</RoleGate>

			<Slot name="dashboard.widgets" />

			<RoleGate require="admin">
				<Section title="Administration">
					<Grid cols={3}>
						<DashboardCard
							href="/admin"
							icon={ShieldCheck}
							title="Admin panel"
							description="Users, organizations, teams, permissions, API keys, audit log"
						/>
					</Grid>
				</Section>
			</RoleGate>
		</PageBody>
	);
}

/**
 * The name the greeting uses: the person's first name when the session carries
 * one, else their e-mail's local part with each word capitalised. The heading
 * used to capitalise itself whole ("Welcome Back, Maya") and never read the
 * name the person has.
 */
export function greetingNameOf(
	user: { email?: string; name?: string } | null | undefined,
): string {
	const first = user?.name?.trim().split(/\s+/)[0];
	if (first) return first;
	const local = user?.email
		?.split("@")[0]
		?.replace(/[._-]+/g, " ")
		.trim();
	if (!local) return "there";
	return local.replace(/\b\p{L}/gu, (c) => c.toUpperCase());
}

interface DashboardCardProps {
	href: string;
	icon: React.ComponentType<{ className?: string }>;
	title: string;
	description: string;
}

function DashboardCard({
	href,
	icon: Icon,
	title,
	description,
}: DashboardCardProps) {
	return (
		<Link href={href} className="group rounded-xl">
			<Card className="h-full transition-colors group-hover:bg-muted/50">
				<CardHeader>
					<div className="flex items-center gap-2">
						<Icon className="size-4 shrink-0 text-muted-foreground" />
						<CardTitle className="flex-1">{title}</CardTitle>
						<ArrowUpRight className="size-4 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" />
					</div>
					<CardDescription>{description}</CardDescription>
				</CardHeader>
			</Card>
		</Link>
	);
}
