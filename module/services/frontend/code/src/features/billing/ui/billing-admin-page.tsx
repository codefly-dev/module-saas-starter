"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useMutation, useQuery } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import {
  CreditCard,
  ExternalLink,
  TrendingUp,
  FileText,
  Download,
} from "lucide-react";
import Link from "next/link";
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

  const { data: invoicesResp, isLoading: invoicesLoading } = useQuery({
    queryKey: ["billing", "invoices", orgId],
    queryFn: () => billingClient.listInvoices({ orgId, limit: 12 }),
    enabled: !!orgId,
  });
  const invoices = invoicesResp?.invoices ?? [];

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
        <div className="space-y-6">
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

          {/* ── Invoices card ── */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <FileText className="h-4 w-4" />
                Invoices
              </CardTitle>
              <CardDescription>
                Last 12 invoices from Stripe. Click an invoice number to view
                the hosted detail page or download the PDF.
              </CardDescription>
            </CardHeader>
            <CardContent>
              {invoicesLoading ? (
                <div className="space-y-2">
                  <Skeleton className="h-10 w-full" />
                  <Skeleton className="h-10 w-full" />
                  <Skeleton className="h-10 w-full" />
                </div>
              ) : invoices.length === 0 ? (
                <div className="text-sm text-muted-foreground py-6 text-center">
                  No invoices yet — start a checkout to enable billing.
                </div>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="border-b text-xs text-muted-foreground">
                        <th className="text-left py-2 font-medium">Number</th>
                        <th className="text-left py-2 font-medium">Date</th>
                        <th className="text-left py-2 font-medium">Status</th>
                        <th className="text-right py-2 font-medium">Amount</th>
                        <th className="py-2"></th>
                      </tr>
                    </thead>
                    <tbody>
                      {invoices.map((inv) => (
                        <tr key={inv.id} className="border-b last:border-0">
                          <td className="py-2.5 font-mono text-xs">
                            {inv.hostedInvoiceUrl ? (
                              <Link
                                href={inv.hostedInvoiceUrl}
                                target="_blank"
                                className="underline underline-offset-2 hover:text-foreground"
                              >
                                {inv.number || inv.id.slice(0, 12)}
                              </Link>
                            ) : (
                              <span>{inv.number || inv.id.slice(0, 12)}</span>
                            )}
                          </td>
                          <td className="py-2.5 text-muted-foreground">
                            {inv.created
                              ? new Date(
                                  Number(inv.created.seconds) * 1000,
                                ).toLocaleDateString()
                              : "—"}
                          </td>
                          <td className="py-2.5">
                            <InvoiceStatusBadge status={inv.status} />
                          </td>
                          <td className="py-2.5 text-right font-mono text-xs">
                            {formatMoney(Number(inv.amountPaid || inv.amountDue), inv.currency)}
                          </td>
                          <td className="py-2.5 text-right">
                            {inv.invoicePdf && (
                              <Link
                                href={inv.invoicePdf}
                                target="_blank"
                                aria-label="Download PDF"
                                className="inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
                              >
                                <Download className="h-3.5 w-3.5" />
                              </Link>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  );
}

function InvoiceStatusBadge({ status }: { status: string }) {
  const variant: "default" | "secondary" | "destructive" | "outline" = (() => {
    switch (status) {
      case "paid":
        return "default";
      case "open":
        return "secondary";
      case "void":
      case "uncollectible":
        return "destructive";
      default:
        return "outline";
    }
  })();
  return (
    <Badge variant={variant} className="text-[10px] capitalize">
      {status || "draft"}
    </Badge>
  );
}

function formatMoney(amount: number, currency: string): string {
  // Stripe amounts are in the smallest currency unit (cents for USD/EUR).
  const major = amount / 100;
  try {
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: (currency || "usd").toUpperCase(),
    }).format(major);
  } catch {
    return `${major.toFixed(2)} ${currency.toUpperCase()}`;
  }
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
