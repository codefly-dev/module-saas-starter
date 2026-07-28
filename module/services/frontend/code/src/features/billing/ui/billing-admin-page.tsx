"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { createClient } from "@connectrpc/connect";
import { useMutation, useQuery } from "@tanstack/react-query";
import { CreditCard, Download, ExternalLink, FileText } from "lucide-react";
import Link from "next/link";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { MetricProvenance, MetricStateBadge } from "@/components/metric-state";
import { OrgSelector } from "@/components/org-selector";
import { Sparkline } from "@/components/sparkline";
import {
	normalizeUsageSeries,
	projectUsage,
	usageHistoryPresentation,
	usagePercent,
	usageTone,
} from "@/features/billing/model/usage-display";
import {
	useUsageHistory,
	useUsageMeters,
} from "@/features/billing/service/usage-queries";
import { useOrgEntitlements } from "@/features/platform/service/queries";
import { BillingService } from "@/gen/saas/accounts/v1/billing_pb";
import {
	UsageAggregation,
	type UsageMeterSnapshot,
} from "@/gen/saas/accounts/v1/usage_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
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

const billingClient = createClient(BillingService, apiTransport);

export function BillingAdminPage() {
	const { organizationId: orgId = "" } = useAuth();

	const { data: entitlements, isLoading } = useOrgEntitlements(orgId || null);
	const {
		data: usage,
		isLoading: usageLoading,
		error: usageError,
	} = useUsageMeters(orgId || null);

	const {
		data: invoicesResp,
		isLoading: invoicesLoading,
		error: invoicesError,
	} = useQuery({
		queryKey: ["billing", "invoices", orgId],
		queryFn: () => billingClient.listInvoices({ orgId, limit: 12 }),
		enabled: !!orgId,
		// Don't retry on the "billing not configured" path — we'll
		// render a friendlier callout. Default 1 retry would just
		// delay that callout by 30s.
		retry: false,
	});
	const invoices = invoicesResp?.invoices ?? [];
	// The api returns "billing not configured" when STRIPE_API_KEY
	// isn't wired (typical dev / dogfood). Detect by message so we
	// can surface a helpful callout instead of a generic error.
	const stripeMissing =
		!!invoicesError &&
		/billing not configured/i.test((invoicesError as Error).message);

	const portal = useMutation({
		mutationFn: () =>
			billingClient.openPortal(
				{ orgId },
				{ headers: { "Idempotency-Key": crypto.randomUUID() } },
			),
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
					<h1 className="text-2xl font-bold tracking-tight">Subscription</h1>
					<p className="text-muted-foreground">
						Plan, usage, invoices, and payment management. Powered by Stripe.
					</p>
				</div>
				<OrgSelector />
			</div>

			{!orgId ? (
				<EmptyState
					icon={CreditCard}
					title="Select an organization"
					description="Subscriptions are scoped per organization. Pick one above to see its current plan, usage, invoices, and payment settings."
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
								<div className="pt-2">
									<Button
										className="w-full"
										onClick={() => portal.mutate()}
										disabled={portal.isPending}
									>
										<ExternalLink className="mr-2 h-4 w-4" />
										{portal.isPending
											? "Opening portal…"
											: "Manage subscription"}
									</Button>
								</div>
							</CardContent>
						</Card>

						{/* ── Usage summary ── */}
						<Card>
							<CardHeader>
								<CardTitle className="flex items-center justify-between text-base">
									<span>Billable usage this period</span>
									{usageError && (
										<MetricStateBadge state="provider_unavailable" />
									)}
								</CardTitle>
								<CardDescription>
									Accepted usage events, history, entitlement headroom, and a
									run-rate forecast. Cardinality gauges remain in{" "}
									<Link
										href="/admin/entitlements"
										className="underline underline-offset-2"
									>
										Entitlements
									</Link>
									.
								</CardDescription>
							</CardHeader>
							<CardContent className="space-y-4">
								{usageLoading ? (
									<div className="space-y-2">
										<Skeleton className="h-6 w-full" />
										<Skeleton className="h-6 w-full" />
										<Skeleton className="h-6 w-full" />
									</div>
								) : usageError ? (
									<div className="text-sm text-muted-foreground">
										Usage is temporarily unavailable. Subscription management is
										unaffected.
									</div>
								) : usage?.meters.length ? (
									<div className="space-y-5">
										{usage.meters.map((snapshot) => (
											<MeterUsageRow
												key={snapshot.meter?.key}
												orgId={orgId}
												snapshot={snapshot}
												observedAt={
													usage.observedAt
														? timestampDate(usage.observedAt).toISOString()
														: undefined
												}
											/>
										))}
									</div>
								) : (
									<div className="flex items-center justify-between text-sm text-muted-foreground">
										<span>No customer-visible meters are configured.</span>
										<MetricStateBadge state="not_configured" />
									</div>
								)}
								<MetricProvenance
									source="UsageService"
									observedAt={
										usage?.observedAt
											? timestampDate(usage.observedAt).toISOString()
											: undefined
									}
									owner="product"
								/>
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
							) : stripeMissing ? (
								<div className="rounded-md border bg-muted/30 p-4 text-sm space-y-1">
									<div className="font-medium">Stripe not configured</div>
									<div className="text-xs text-muted-foreground">
										Set <code className="font-mono">STRIPE_API_KEY</code> in
										your codefly secret to enable invoices and the Manage-
										subscription portal. Plan + usage display works without it.
									</div>
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
														{formatMoney(
															Number(inv.amountPaid || inv.amountDue),
															inv.currency,
														)}
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

function MeterUsageRow({
	orgId,
	snapshot,
	observedAt,
}: {
	orgId: string;
	snapshot: UsageMeterSnapshot;
	observedAt?: string;
}) {
	const meter = snapshot.meter;
	const used = snapshot.used;
	const limit = snapshot.limit;
	const percent = usagePercent(used, limit);
	const tone = {
		critical: "bg-destructive",
		warning: "bg-amber-500",
		healthy: "bg-emerald-500",
	}[usageTone(used, limit)];
	const fromISO = snapshot.periodStart
		? timestampDate(snapshot.periodStart).toISOString()
		: "";
	const toISO = observedAt ?? "";
	const history = useUsageHistory(orgId, meter?.key ?? "", fromISO, toISO);
	const points = normalizeUsageSeries(
		history.data?.buckets.map((bucket) => bucket.quantity) ?? [],
	);
	const historyPresentation = usageHistoryPresentation(
		history.isLoading,
		history.isError,
		points.length,
		used,
	);
	const forecast = usageForecast(snapshot, observedAt);
	const limitLabel =
		limit === BigInt(-1)
			? "unlimited"
			: limit === BigInt(0)
				? "disabled"
				: limit.toLocaleString();

	return (
		<div className="space-y-2 border-b pb-4 last:border-0 last:pb-0">
			<div className="flex items-center justify-between text-sm">
				<div>
					<div className="font-medium">
						{meter?.displayName || meter?.key || "Meter"}
					</div>
					<div className="text-[11px] text-muted-foreground">
						{meter?.reconciliationRule}
					</div>
				</div>
				<div className="flex items-center gap-3">
					{historyPresentation === "loading" ? (
						<MetricStateBadge state="loading" />
					) : historyPresentation === "partial" ? (
						<MetricStateBadge state="partial" />
					) : historyPresentation === "chart" ? (
						<Sparkline points={points} className="text-primary/70" />
					) : historyPresentation === "no_data" ? (
						<MetricStateBadge state="no_data" />
					) : null}
					<span className="font-mono text-xs text-muted-foreground">
						{used.toLocaleString()} / {limitLabel}
					</span>
				</div>
			</div>
			{limit > BigInt(0) && (
				<div className="h-2 rounded-full bg-muted">
					<div
						className={`h-2 rounded-full ${tone}`}
						style={{ width: `${percent}%` }}
					/>
				</div>
			)}
			<div className="flex justify-between text-[11px] text-muted-foreground">
				<span>
					Unit: {meter?.unit || "unit"} · Aggregation:{" "}
					{UsageAggregation[
						meter?.aggregation ?? UsageAggregation.SUM
					].toLowerCase()}
				</span>
				<span>
					{forecast === undefined
						? "Forecast unavailable"
						: `Forecast: ${forecast.toLocaleString()}`}
				</span>
			</div>
		</div>
	);
}

function usageForecast(
	snapshot: UsageMeterSnapshot,
	observedAt?: string,
): bigint | undefined {
	if (!snapshot.periodStart || !snapshot.periodEnd || !observedAt)
		return undefined;
	const start = timestampDate(snapshot.periodStart).getTime();
	const end = timestampDate(snapshot.periodEnd).getTime();
	const observed = new Date(observedAt).getTime();
	const elapsed = Math.max(observed - start, 24 * 60 * 60 * 1000);
	const period = end - start;
	return projectUsage(snapshot.used, period, elapsed);
}
