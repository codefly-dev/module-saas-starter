"use client";

/**
 * ActivityFeed — humanized view of recent audit events for the user's
 * primary org. Drops in anywhere on the dashboard. Reads the same
 * `audit_events` rows that drive the admin audit log; the difference
 * is presentation: each row becomes a one-line "X did Y to Z"
 * sentence rather than a structured table.
 *
 * Designed as a card with N rows + "View all" link. Empty state hides
 * the card entirely so brand-new accounts don't see a sad-looking
 * "nothing here yet" panel.
 */

import {
	Activity,
	Building2,
	CreditCard,
	Key,
	type LucideIcon,
	ShieldCheck,
	UserPlus,
	Webhook,
} from "lucide-react";
import Link from "next/link";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { auditEventAction } from "@/features/audit/model/transforms";
import type { AuditEvent } from "@/features/audit/model/types";
import { useAuditLog } from "@/features/audit/service/queries";
import { useAuth } from "@/lib/auth";

// Keys are registered audit event types (pkg/business/audit_registry.go), so a
// name that is not in the catalog is a dead entry that can never render.
export const ACTION_ICONS: Record<string, LucideIcon> = {
	"saas.user.registered": UserPlus,
	"saas.user.suspended": UserPlus,
	"saas.org.created": Building2,
	"saas.org.member_added": UserPlus,
	"saas.api_key.created": Key,
	"saas.api_key.revoked": Key,
	"saas.webhook.created": Webhook,
	"saas.webhook.replayed": Webhook,
	"saas.webhook.secret_rotated": Webhook,
	"saas.auth.login": ShieldCheck,
	"saas.billing.checkout_started": CreditCard,
	"saas.billing.portal_opened": CreditCard,
};

export function ActivityFeed({
	orgId,
	limit = 8,
}: {
	orgId?: string;
	limit?: number;
}) {
	const { user, organizationId } = useAuth();
	const resolvedOrgId = orgId ?? organizationId ?? "";
	const { data, isLoading } = useAuditLog(
		{
			orgId: resolvedOrgId,
			pageSize: limit,
		},
		{
			enabled: resolvedOrgId !== "",
		},
	);

	const events: AuditEvent[] = data?.events ?? [];

	if (!isLoading && events.length === 0) return null;

	return (
		<Card>
			<CardHeader className="flex flex-row items-center justify-between py-3">
				<CardTitle className="text-base flex items-center gap-2">
					<Activity className="h-4 w-4 text-muted-foreground" />
					Recent activity
				</CardTitle>
				{resolvedOrgId && (
					<Link
						href="/admin/audit-log"
						className="text-xs text-muted-foreground hover:text-foreground"
					>
						View all →
					</Link>
				)}
			</CardHeader>
			<CardContent className="pt-0">
				{isLoading ? (
					<div className="space-y-2">
						<Skeleton className="h-9 w-full" />
						<Skeleton className="h-9 w-full" />
						<Skeleton className="h-9 w-full" />
						<Skeleton className="h-9 w-full" />
					</div>
				) : (
					<ul className="space-y-2.5">
						{events.map((e) => {
							const Icon = ACTION_ICONS[e.eventType] ?? Activity;
							const isYou = !!user?.id && e.actorId === user.id;
							return (
								<li key={e.id} className="flex items-start gap-3 text-sm">
									<div className="mt-0.5 h-7 w-7 rounded-md border bg-background flex items-center justify-center shrink-0">
										<Icon className="h-3.5 w-3.5 text-muted-foreground" />
									</div>
									<div className="flex-1 min-w-0">
										<div className="truncate">
											<span className="font-medium">
												{isYou ? "You" : "Someone"}
											</span>{" "}
											<span className="text-muted-foreground">
												{humanize(e.eventType)}
											</span>
											{e.resource && (
												<span className="text-muted-foreground">
													{" "}
													({e.resource})
												</span>
											)}
										</div>
										<div className="text-xs text-muted-foreground">
											{relativeTime(e.createdAt)}
										</div>
									</div>
								</li>
							);
						})}
					</ul>
				)}
			</CardContent>
		</Card>
	);
}

// The verb phrase each registered event type renders as. Exported alongside
// ACTION_ICONS so both maps are gated against the registry's naming law.
export const ACTION_PHRASES: Record<string, string> = {
	"saas.user.registered": "registered an account",
	"saas.user.suspended": "was suspended",
	"saas.user.unsuspended": "was unsuspended",
	"saas.user.deleted": "deleted their account",
	"saas.org.created": "created an organization",
	"saas.org.member_added": "joined the organization",
	"saas.org.member_removed": "left the organization",
	"saas.team.created": "created a team",
	"saas.team.member_added": "joined a team",
	"saas.api_key.created": "created an API key",
	"saas.api_key.revoked": "revoked an API key",
	"saas.webhook.created": "subscribed to a webhook",
	"saas.webhook.deleted": "removed a webhook",
	"saas.webhook.replayed": "replayed a webhook delivery",
	"saas.webhook.secret_rotated": "rotated a webhook secret",
	"saas.auth.login": "signed in",
	"saas.billing.checkout_started": "started a checkout",
	"saas.billing.portal_opened": "opened the billing portal",
	"saas.billing.free_plan_selected": "selected the free plan",
	"saas.role.assigned": "received a role",
	"saas.role.revoked": "had a role revoked",
};

// humanize transforms a registered event type (`saas.user.registered`) into a
// readable verb phrase (`registered an account`). Falls back to the
// namespace-stripped key when unmapped — better than "did unknown".
function humanize(action: string): string {
	return (
		ACTION_PHRASES[action] ?? auditEventAction(action).replace(/[._]/g, " ")
	);
}

// `createdAt` is already normalized to an ISO-8601 string at the query boundary
// (`toAuditEvent`, which converts the wire google.protobuf.Timestamp via
// `timestampDate(...).toISOString()`), so this consumer only ever sees a string
// or `undefined`. Guard `Date.parse` returning NaN so an absent or malformed
// value renders blank rather than the literal "Invalid Date".
function relativeTime(t?: string): string {
	if (!t) return "";
	const ms = Date.parse(t);
	if (Number.isNaN(ms)) return "";
	const delta = (Date.now() - ms) / 1000;
	if (delta < 60) return "just now";
	if (delta < 3600) return `${Math.floor(delta / 60)}m ago`;
	if (delta < 86400) return `${Math.floor(delta / 3600)}h ago`;
	if (delta < 604800) return `${Math.floor(delta / 86400)}d ago`;
	return new Date(ms).toLocaleDateString();
}
