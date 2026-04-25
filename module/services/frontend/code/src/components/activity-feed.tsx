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

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuditLog } from "@/features/audit/service/queries";
import { useAuth } from "@/lib/auth";
import {
  Activity,
  UserPlus,
  Building2,
  Key,
  ShieldCheck,
  Webhook,
  CreditCard,
  type LucideIcon,
} from "lucide-react";
import Link from "next/link";

interface RawEvent {
  id: string;
  action: string;
  actorId: string;
  resource: string;
  resourceId: string;
  createdAt?: { seconds: bigint };
  metadata?: Record<string, string>;
}

const ACTION_ICONS: Record<string, LucideIcon> = {
  "user.registered": UserPlus,
  "user.suspended": UserPlus,
  "org.created": Building2,
  "org.member_added": UserPlus,
  "api_key.created": Key,
  "api_key.revoked": Key,
  "webhook.created": Webhook,
  "webhook.replayed": Webhook,
  "webhook.secret_rotated": Webhook,
  "auth.login": ShieldCheck,
  "billing.subscription_created": CreditCard,
  "billing.subscription_updated": CreditCard,
};

export function ActivityFeed({ orgId, limit = 8 }: { orgId?: string; limit?: number }) {
  const { user } = useAuth();
  const { data, isLoading } = useAuditLog({
    orgId: orgId ?? "",
    pageSize: limit,
  });

  const events: RawEvent[] = (data?.events as RawEvent[] | undefined) ?? [];

  if (!isLoading && events.length === 0) return null;

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between py-3">
        <CardTitle className="text-base flex items-center gap-2">
          <Activity className="h-4 w-4 text-muted-foreground" />
          Recent activity
        </CardTitle>
        {orgId && (
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
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-9 w-full" />
            ))}
          </div>
        ) : (
          <ul className="space-y-2.5">
            {events.map((e) => {
              const Icon = ACTION_ICONS[e.action] ?? Activity;
              const isYou = !!user?.id && e.actorId === user.id;
              return (
                <li
                  key={e.id}
                  className="flex items-start gap-3 text-sm"
                >
                  <div className="mt-0.5 h-7 w-7 rounded-md border bg-background flex items-center justify-center shrink-0">
                    <Icon className="h-3.5 w-3.5 text-muted-foreground" />
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="truncate">
                      <span className="font-medium">
                        {isYou ? "You" : "Someone"}
                      </span>{" "}
                      <span className="text-muted-foreground">
                        {humanize(e.action)}
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

// humanize transforms an action key (`user.registered`) into a readable
// verb phrase (`registered an account`). Handles the common cases; falls
// back to the raw key when unmapped — better than "did unknown".
function humanize(action: string): string {
  const map: Record<string, string> = {
    "user.registered": "registered an account",
    "user.suspended": "was suspended",
    "user.unsuspended": "was unsuspended",
    "user.deleted": "deleted their account",
    "org.created": "created an organization",
    "org.member_added": "joined the organization",
    "org.member_removed": "left the organization",
    "team.created": "created a team",
    "team.member_added": "joined a team",
    "api_key.created": "created an API key",
    "api_key.revoked": "revoked an API key",
    "webhook.created": "subscribed to a webhook",
    "webhook.deleted": "removed a webhook",
    "webhook.replayed": "replayed a webhook delivery",
    "webhook.secret_rotated": "rotated a webhook secret",
    "auth.login": "signed in",
    "billing.subscription_created": "started a subscription",
    "billing.subscription_updated": "updated their subscription",
    "billing.subscription_canceled": "canceled their subscription",
    "role.granted": "received a role",
    "role.revoked": "had a role revoked",
  };
  return map[action] ?? action.replace(/[._]/g, " ");
}

function relativeTime(t?: { seconds: bigint }): string {
  if (!t) return "";
  const sec = Number(t.seconds);
  const delta = Date.now() / 1000 - sec;
  if (delta < 60) return "just now";
  if (delta < 3600) return `${Math.floor(delta / 60)}m ago`;
  if (delta < 86400) return `${Math.floor(delta / 3600)}h ago`;
  if (delta < 604800) return `${Math.floor(delta / 86400)}d ago`;
  return new Date(sec * 1000).toLocaleDateString();
}
