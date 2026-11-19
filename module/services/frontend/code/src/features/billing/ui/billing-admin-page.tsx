"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Progress } from "@codefly-dev/ui/layout";
import { createClient } from "@connectrpc/connect";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { CreditCard, Download, ExternalLink, FileText } from "lucide-react";
import Link from "next/link";
import { useMemo } from "react";
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
import {
	BillingService,
	type Invoice,
} from "@/gen/saas/accounts/v1/billing_pb";
import {
	UsageAggregation,
	type UsageMeterSnapshot,
} from "@/gen/saas/accounts/v1/usage_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	Badge,
	Banner,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Skeleton,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { billingMutations } from "../service/mutations";

const billingClient = createClient(BillingService, apiTransport);

export function BillingAdminPage() {
	const { organizationId: orgId = "", getToken } = useAuth();

	const {
		data: entitlements,
		isLoading,
		isError: planFailed,
		error: planError,
	} = useOrgEntitlements(orgId || null);
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
				toast.error(failureText("open the billing portal", undefined));
				return;
			}
			window.location.href = resp.url;
		},
		onError: (error) =>
			toast.error(failureText("open the billing portal", error)),
	});
	const freePlan = useMutation({
		mutationFn: () => billingMutations.selectFreePlan(getToken()),
		onSuccess: () => toast.success("Free plan activated"),
		onError: (error) =>
			toast.error(failureText("activate the free plan", error)),
	});
	const checkout = useMutation({
		mutationFn: () => billingMutations.startCheckout(getToken(), "pro"),
		onSuccess: (url) => {
			window.location.href = url;
		},
		onError: (error) => toast.error(failureText("start the checkout", error)),
	});

	return (
		<PageBody className="max-w-4xl">
			<PageHeader
				title="Subscription"
				description="Your plan, what you have used, your invoices and how you pay."
				actions={<OrgSelector />}
			/>

			{!orgId ? (
				<EmptyState
					icon={CreditCard}
					title="Select an organization"
					description="Each organization has its own subscription. Pick one above to see its current plan, usage, invoices and payment settings."
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
									Manage your subscription, payment method and invoices in the
									billing portal.
								</CardDescription>
							</CardHeader>
							<CardContent className="space-y-3">
								{isLoading ? (
									<Skeleton className="h-10 w-full" />
								) : planFailed ? (
									<ErrorState
										title="Couldn't load the current plan"
										detail={
											readOutcome(true, planError) === "forbidden"
												? "You don't have permission to see this organization's plan. Ask one of its administrators for access."
												: "Whether a plan is active is unknown. Reload the page; if it keeps failing, tell your administrator."
										}
									/>
								) : (
									<div className="text-sm text-muted-foreground">
										{entitlements?.entitlements.length
											? `${entitlements.entitlements.length} feature${entitlements.entitlements.length === 1 ? "" : "s"} included`
											: "No active plan"}
									</div>
								)}
								<div className="grid gap-2 pt-2 sm:grid-cols-2">
									<Button
										variant="outline"
										onClick={() => freePlan.mutate()}
										disabled={freePlan.isPending || checkout.isPending}
									>
										{freePlan.isPending ? "Activating…" : "Use Free plan"}
									</Button>
									<Button
										onClick={() => checkout.mutate()}
										disabled={freePlan.isPending || checkout.isPending}
									>
										{checkout.isPending ? "Opening checkout…" : "Choose Pro"}
									</Button>
									<Button
										className="w-full sm:col-span-2"
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
								Your last 12 invoices. Select an invoice number to open it, or
								download it as a PDF.
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
								<Banner tone="info" title="Billing isn't set up yet">
									Invoices and subscription management become available once the
									people who run this application connect a payment provider.
									Your plan and usage are shown without it.
								</Banner>
							) : invoicesError ? (
								// A failed read is not "no invoices": whether there are any
								// is unknown, and saying so keeps a person from concluding
								// they were never billed.
								<ErrorState
									title="Couldn't load the invoices"
									detail={
										readOutcome(true, invoicesError) === "forbidden"
											? "You don't have permission to see this organization's invoices. Ask one of its administrators for access."
											: "Whether there are any is unknown. Reload the page; if it keeps failing, tell your administrator."
									}
								/>
							) : invoices.length === 0 ? (
								<EmptyState
									icon={FileText}
									title="No invoices yet"
									description="Invoices appear here once you choose a paid plan."
								/>
							) : (
								<InvoiceTable invoices={invoices} />
							)}
						</CardContent>
					</Card>
				</div>
			)}
		</PageBody>
	);
}

function InvoiceTable({ invoices }: { invoices: Invoice[] }) {
	const columns = useMemo(() => {
		const column = createColumnHelper<Invoice>();
		return [
			column.accessor("number", {
				header: "Invoice",
				cell: ({ row }) =>
					row.original.hostedInvoiceUrl ? (
						<Link
							href={row.original.hostedInvoiceUrl}
							target="_blank"
							rel="noopener noreferrer"
							className="underline underline-offset-2"
						>
							{row.original.number || "View invoice"}
						</Link>
					) : (
						row.original.number || "Invoice"
					),
			}),
			column.accessor("created", {
				header: "Date",
				cell: ({ getValue }) => {
					const date = getValue();
					return date
						? timestampDate(date).toLocaleDateString()
						: "Date unavailable";
				},
			}),
			column.accessor("status", {
				header: "Status",
				cell: ({ getValue }) => <InvoiceStatusBadge status={getValue()} />,
			}),
			column.display({
				id: "amount",
				header: "Amount",
				cell: ({ row }) =>
					formatMoney(
						Number(row.original.amountPaid || row.original.amountDue),
						row.original.currency,
					),
			}),
			column.display({
				id: "download",
				header: "Download",
				cell: ({ row }) =>
					row.original.invoicePdf ? (
						<Button
							variant="ghost"
							size="sm"
							nativeButton={false}
							aria-label={`Download invoice ${row.original.number || "PDF"}`}
							render={
								<Link
									href={row.original.invoicePdf}
									target="_blank"
									rel="noopener noreferrer"
								/>
							}
						>
							<Download aria-hidden="true" /> PDF
						</Button>
					) : (
						"Unavailable"
					),
			}),
		];
	}, []);
	const table = useReactTable({
		data: invoices,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
		initialState: { pagination: { pageSize: 10 } },
	});
	return <DataTable table={table} />;
}

function InvoiceStatusBadge({ status }: { status: string }) {
	const tone: "success" | "info" | "danger" | "neutral" = (() => {
		switch (status) {
			case "paid":
				return "success";
			case "open":
				return "info";
			case "void":
			case "uncollectible":
				return "danger";
			default:
				return "neutral";
		}
	})();
	return (
		<Badge tone={tone} className="capitalize">
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
	const tone = (
		{ critical: "danger", warning: "warning", healthy: "success" } as const
	)[usageTone(used, limit)];
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
				<Progress
					value={percent}
					tone={tone}
					label={`${meter?.displayName || "Plan"} usage`}
					valueText={`${used.toLocaleString()} of ${limitLabel} used`}
				/>
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
