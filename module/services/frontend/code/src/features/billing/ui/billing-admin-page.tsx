"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useMutation, useQuery } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import {
  CreditCard,
  ExternalLink,
  TrendingUp,
} from "lucide-react";
import { toast } from "sonner";
import {
  Badge,
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Skeleton,
} from "@/shared/ui";
import { OrgSelector } from "@/components/org-selector";
import { EmptyState } from "@/components/empty-state";
import { apiTransport } from "@/lib/connect/transport";
import { BillingService } from "@/gen/saas-starter_api_grpc_pb";
import { useOrgEntitlements } from "@/features/platform/service/queries";

const billingClient = createClient(BillingService, apiTransport);

/**
 * BillingAdminPage — current plan summary + Stripe-portal jump-off.
 * Power-user destination after the post-checkout success screen:
 * "see your plan, see your usage, change card / cancel /
 * upgrade".
 *
 * Usage cards reuse the same GetOrgEntitlements RPC the platform
 * Entitlements page hits — different framing (operator vs platform
 * admin), same data shape.
 */
export function BillingAdminPage() {
  const router = useRouter();
  const [orgId, setOrgId] = useState("");

  const { data: entitlements, isLoading } = useOrgEntitlements(orgId || null);

  const portal = useMutation({
    mutationFn: () =>
      billingClient.openPortal({
        orgId,
        returnUrl:
          typeof window !== "undefined"
            ? `${window.location.origin}/admin/billing`
            : "",
      }),
    onSuccess: (resp) => {
      if (!resp.url) {
        toast.error("Portal failed", { description: "No URL returned" });
        return;
      }
      window.location.href = resp.url;
    },
    onError: (err) =>
      toast.error("Couldn't open portal", { description: err.message }),
  });

  return (
    <div className="space-y-6 max-w-4xl">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Billing</h1>
          <p className="text-muted-foreground">
            Plan, usage, and subscription management. Powered by Stripe.
          </p>
        </div>
        <OrgSelector value={orgId} onChange={setOrgId} />
      </div>

      {!orgId ? (
        <EmptyState
          icon={CreditCard}
          title="Select an organization"
          description="Plans and billing are scoped per org. Pick an organization above to see its current plan and manage subscription."
        />
      ) : (
        <div className="grid gap-6 md:grid-cols-2">
          {/* ── Plan card ── */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center justify-between text-base">
                <span>Current plan</span>
                {entitlements?.planName && (
                  <Badge variant="default">{entitlements.planName}</Badge>
                )}
              </CardTitle>
              <CardDescription>
                Manage your subscription, payment method, and invoices through
                the Stripe-hosted billing portal.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              {isLoading ? (
                <Skeleton className="h-10 w-full" />
              ) : (
                <div className="text-sm text-muted-foreground">
                  {entitlements?.entitlements.length
                    ? `${entitlements.entitlements.length} feature${entitlements.entitlements.length === 1 ? "" : "s"} included`
                    : "No active plan"}
                </div>
              )}
              <div className="flex flex-col gap-2 pt-2">
                <Button
                  onClick={() => portal.mutate()}
                  disabled={portal.isPending}
                >
                  <ExternalLink className="mr-2 h-4 w-4" />
                  {portal.isPending
                    ? "Opening portal…"
                    : "Manage subscription"}
                </Button>
                <Button
                  variant="outline"
                  onClick={() => router.push("/pricing")}
                >
                  <TrendingUp className="mr-2 h-4 w-4" />
                  Compare plans
                </Button>
              </div>
            </CardContent>
          </Card>

          {/* ── Usage summary ── */}
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Usage this period</CardTitle>
              <CardDescription>
                Top features by approach-to-limit. See{" "}
                <a
                  href="/admin/entitlements"
                  className="underline underline-offset-2"
                >
                  Entitlements
                </a>{" "}
                for the full breakdown.
              </CardDescription>
            </CardHeader>
            <CardContent>
              {isLoading ? (
                <div className="space-y-2">
                  <Skeleton className="h-6 w-full" />
                  <Skeleton className="h-6 w-full" />
                  <Skeleton className="h-6 w-full" />
                </div>
              ) : entitlements?.entitlements.length ? (
                <div className="space-y-3">
                  {topThreeByPercentUsed(entitlements.entitlements).map((e) => (
                    <UsageRow key={e.feature} e={e} />
                  ))}
                </div>
              ) : (
                <div className="text-sm text-muted-foreground">
                  No metered features on this plan.
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  );
}

interface E {
  feature: string;
  limit: bigint;
  used: bigint;
}

function topThreeByPercentUsed<T extends E>(entitlements: T[]): T[] {
  return [...entitlements]
    .filter((e) => Number(e.limit) > 0) // skip unlimited (-1) + disabled (0)
    .sort((a, b) => pct(b) - pct(a))
    .slice(0, 3);
}

function pct(e: E): number {
  const limit = Number(e.limit);
  const used = Number(e.used);
  if (limit <= 0) return 0;
  return used / limit;
}

function UsageRow({ e }: { e: E }) {
  const used = Number(e.used);
  const limit = Number(e.limit);
  const ratio = pct(e);
  const percent = Math.min(100, Math.round(ratio * 100));
  const tone =
    ratio > 0.9
      ? "bg-destructive"
      : ratio > 0.7
        ? "bg-amber-500"
        : "bg-emerald-500";
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between text-sm">
        <span className="font-medium capitalize">
          {e.feature.replace(/_/g, " ")}
        </span>
        <span className="font-mono text-xs text-muted-foreground">
          {used.toLocaleString()} / {limit.toLocaleString()}
        </span>
      </div>
      <div className="h-2 rounded-full bg-muted">
        <div
          className={`h-2 rounded-full ${tone}`}
          style={{ width: `${percent}%` }}
        />
      </div>
    </div>
  );
}
