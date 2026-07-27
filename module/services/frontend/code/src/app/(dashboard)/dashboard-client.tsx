"use client";

import {
	Activity,
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
import { Sparkline } from "@/components/sparkline";
import {
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/components/ui/card";
import { useAuth } from "@/lib/auth";

/**
 * Main dashboard — the hosting app's home. Saas-starter is a module,
 * not the main app: everything the module adds (users, orgs, teams,
 * billing, audit) lives under /admin and is only reachable if the
 * user is admin-tier.
 *
 * Layout:
 *   - Hero header with the user's email + a subtle gradient strip
 *     for visual personality.
 *   - Three personal-account utility cards (Security / Notifications
 *     / Data & Privacy) — visible to every authenticated user.
 *   - Admin tile gated by <RoleGate require="admin">. Shown with a
 *     mini sparkline (placeholder until we wire real audit-volume
 *     data) so the surface looks alive instead of static.
 */
export default function DashboardClient() {
	const { user, platformRole, orgRole } = useAuth();
	const greetingName =
		user?.email?.split("@")[0]?.replace(/[._-]/g, " ") ?? "there";

	return (
		<div className="space-y-10">
			{/* ── HERO ─────────────────────────────────────────────── */}
			<header className="relative overflow-hidden rounded-2xl border bg-gradient-to-br from-primary/15 via-primary/5 to-accent p-8">
				<div
					aria-hidden
					className="pointer-events-none absolute inset-0 opacity-[0.05]"
					style={{
						backgroundImage:
							"linear-gradient(to right, currentColor 1px, transparent 1px), linear-gradient(to bottom, currentColor 1px, transparent 1px)",
						backgroundSize: "24px 24px",
					}}
				/>
				<div className="relative flex flex-col gap-2">
					<h1 className="text-3xl font-bold tracking-tight capitalize">
						Welcome back, {greetingName}
					</h1>
					<p className="text-muted-foreground">
						Here&apos;s an overview of your account.{" "}
						<span className="hidden sm:inline">
							Press{" "}
							<kbd className="px-1.5 py-0.5 rounded border bg-background text-[11px] font-mono">
								⌘K
							</kbd>{" "}
							to jump anywhere.
						</span>
					</p>
					{(platformRole || orgRole) && (
						<div className="mt-3 flex items-center gap-2 text-xs">
							{platformRole && (
								<span className="inline-flex items-center gap-1 rounded-full bg-primary/10 px-2.5 py-1 font-medium text-primary ring-1 ring-primary/20">
									<Shield className="h-3 w-3" />
									{platformRole}
								</span>
							)}
							{orgRole && (
								<span className="inline-flex items-center gap-1 rounded-full bg-primary/10 px-2.5 py-1 font-medium text-primary ring-1 ring-primary/20">
									org / {orgRole}
								</span>
							)}
						</div>
					)}
				</div>
			</header>

			{/* ── PERSONAL CARDS ─────────────────────────────────── */}
			<div className="grid gap-4 md:grid-cols-3">
				<DashboardCard
					href="/settings/mfa"
					icon={Shield}
					title="Security"
					description="Manage MFA and security settings"
					accent="from-primary/10 to-accent"
				/>
				<DashboardCard
					href="/settings/notifications"
					icon={Bell}
					title="Notifications"
					description="Configure notification preferences"
					accent="from-primary/10 to-accent"
				/>
				<DashboardCard
					href="/settings/data"
					icon={FileText}
					title="Data & Privacy"
					description="Export data or manage your account"
					accent="from-primary/10 to-accent"
				/>
			</div>

			{/* ── ADMIN ─────────────────────────────────────────────
          Gated so only admin+ sessions see it. The mini sparkline
          is a placeholder — wire to ListAuditEvents once the
          aggregate-by-day query lands.
          ─────────────────────────────────────────────────────── */}
			{/* ── ACTIVITY FEED ───────────────────────────────────
          Read-only view of recent audit events scoped to the user's
          primary org. Hides itself when there are no events so a
          fresh account doesn't see "nothing here yet". */}
			<ActivityFeed />

			<Slot name="dashboard.widgets" />

			<RoleGate require="admin">
				<div>
					<h2 className="text-lg font-semibold tracking-tight mb-3">
						Administration
					</h2>
					<div className="grid gap-4 md:grid-cols-2">
						<Link href="/admin" className="group">
							<Card className="relative overflow-hidden hover:border-primary/50 transition-all hover:shadow-md hover:-translate-y-0.5 cursor-pointer">
								<div
									aria-hidden
									className="absolute inset-0 bg-gradient-to-br from-primary/10 to-accent opacity-0 group-hover:opacity-100 transition-opacity"
								/>
								<CardHeader className="flex flex-row items-center gap-3 pb-2">
									<div className="h-9 w-9 rounded-lg bg-primary/10 flex items-center justify-center ring-1 ring-primary/20">
										<ShieldCheck className="h-5 w-5 text-primary" />
									</div>
									<div className="flex-1">
										<CardTitle className="text-sm font-medium">
											Admin panel
										</CardTitle>
									</div>
									<ArrowUpRight className="h-4 w-4 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
								</CardHeader>
								<CardContent className="relative space-y-3">
									<CardDescription>
										Users, organizations, teams, permissions, API keys, audit
										log
									</CardDescription>
									<div className="flex items-end justify-between pt-2">
										<div>
											<div className="text-xs text-muted-foreground flex items-center gap-1.5">
												<Activity className="h-3 w-3" />
												Audit activity (7d)
											</div>
										</div>
										<Sparkline
											points={[3, 5, 4, 8, 6, 12, 9]}
											className="text-primary/70"
										/>
									</div>
								</CardContent>
							</Card>
						</Link>
					</div>
				</div>
			</RoleGate>
		</div>
	);
}

interface DashboardCardProps {
	href: string;
	icon: React.ComponentType<{ className?: string }>;
	title: string;
	description: string;
	accent: string;
}

function DashboardCard({
	href,
	icon: Icon,
	title,
	description,
	accent,
}: DashboardCardProps) {
	return (
		<Link href={href} className="group">
			<Card className="relative overflow-hidden hover:border-primary/50 transition-all hover:shadow-md hover:-translate-y-0.5 cursor-pointer h-full">
				<div
					aria-hidden
					className={`absolute inset-0 bg-gradient-to-br ${accent} opacity-0 group-hover:opacity-100 transition-opacity`}
				/>
				<CardHeader className="flex flex-row items-center gap-3 pb-2">
					<div className="h-9 w-9 rounded-lg bg-foreground/[0.04] flex items-center justify-center ring-1 ring-border">
						<Icon className="h-4 w-4 text-muted-foreground" />
					</div>
					<CardTitle className="text-sm font-medium flex-1">{title}</CardTitle>
					<ArrowUpRight className="h-4 w-4 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
				</CardHeader>
				<CardContent className="relative">
					<CardDescription>{description}</CardDescription>
				</CardContent>
			</Card>
		</Link>
	);
}
