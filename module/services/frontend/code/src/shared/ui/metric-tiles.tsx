import { ArrowDown, ArrowUp, Minus } from "lucide-react";
import type { ComponentProps } from "react";
import {
	MetricProvenance,
	type MetricState,
	MetricStateBadge,
} from "@/components/metric-state";
import { Sparkline } from "@/components/sparkline";
import {
	Card,
	CardContent,
	CardFooter,
	CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/shared/lib/utils";

export type MetricFormat = "number" | "compact" | "currency" | "percent";

/**
 * The presentational value a dashboard value-widget binds to — the leaf of the
 * data graph: a single computed number plus the freshness/provenance the
 * audit-driven pipeline attaches to it. The declarative metric *definition*
 * (event filter, group_by, aggregation) is a separate contract; this is only
 * what a tile needs to render.
 */
export interface Metric {
	label: string;
	value: number;
	format?: MetricFormat;
	/** Suffix rendered after the value, e.g. "req/s". */
	unit?: string;
	/**
	 * Signed relative change vs the comparison period, as a fraction
	 * (0.12 → +12%). Colored by direction and `higherIsBetter`.
	 */
	delta?: number;
	/** Names the comparison period, e.g. "vs last week". */
	deltaLabel?: string;
	/** When false a rising delta reads as bad (e.g. error rate). Defaults true. */
	higherIsBetter?: boolean;
	/** Trend points for a sparkline; omitted or too short hides the trend. */
	series?: number[];
	/** Freshness/availability — drives the state badge and value fallback. */
	state?: MetricState;
	provenance?: ComponentProps<typeof MetricProvenance>;
}

// States for which no meaningful number exists, so the value renders as a dash.
const valuelessStates: MetricState[] = [
	"no_data",
	"not_configured",
	"provider_unavailable",
];

export function formatMetricValue(
	value: number,
	format: MetricFormat = "number",
): string {
	switch (format) {
		case "compact":
			return new Intl.NumberFormat("en-US", {
				notation: "compact",
				maximumFractionDigits: 1,
			}).format(value);
		case "currency":
			return new Intl.NumberFormat("en-US", {
				style: "currency",
				currency: "USD",
				notation: "compact",
				maximumFractionDigits: 1,
			}).format(value);
		case "percent":
			return new Intl.NumberFormat("en-US", {
				style: "percent",
				maximumFractionDigits: 1,
			}).format(value);
		default:
			return value.toLocaleString("en-US");
	}
}

function MetricValue({
	metric,
	className,
}: {
	metric: Metric;
	className?: string;
}) {
	if (metric.state === "loading") {
		return <Skeleton className={cn("h-8 w-24", className)} />;
	}
	const valueless = !!metric.state && valuelessStates.includes(metric.state);
	// Standalone figures use the font's proportional digits — tabular-nums is
	// for columns that must align, and looks loose at display sizes.
	return (
		<span className={cn("text-2xl font-semibold tracking-tight", className)}>
			{valueless ? "—" : formatMetricValue(metric.value, metric.format)}
			{!valueless && metric.unit && (
				<span className="ml-1 text-sm font-normal text-muted-foreground">
					{metric.unit}
				</span>
			)}
		</span>
	);
}

function MetricDelta({
	delta,
	deltaLabel,
	higherIsBetter = true,
}: Pick<Metric, "delta" | "deltaLabel" | "higherIsBetter">) {
	if (delta === undefined) return null;

	const magnitude = new Intl.NumberFormat("en-US", {
		style: "percent",
		maximumFractionDigits: 1,
	}).format(Math.abs(delta));

	if (delta === 0) {
		return (
			<span className="inline-flex items-center gap-0.5 text-xs font-medium text-muted-foreground">
				<Minus className="size-3" aria-hidden />
				{magnitude}
				{deltaLabel && <span className="font-normal">{deltaLabel}</span>}
			</span>
		);
	}

	const up = delta > 0;
	const good = up === higherIsBetter;
	const Icon = up ? ArrowUp : ArrowDown;
	return (
		<span
			className={cn(
				"inline-flex items-center gap-0.5 text-xs font-medium",
				good ? "text-emerald-600 dark:text-emerald-500" : "text-destructive",
			)}
		>
			<Icon className="size-3" aria-hidden />
			{up ? "+" : "−"}
			{magnitude}
			{deltaLabel && (
				<span className="font-normal text-muted-foreground">{deltaLabel}</span>
			)}
		</span>
	);
}

function hasTrend(series: number[] | undefined): series is number[] {
	return !!series && series.length > 1;
}

/**
 * A single headline number — label, value, optional delta and trend sparkline,
 * with a freshness badge when the metric isn't `ready`. The compact building
 * block of a {@link KPIRow}.
 */
export function StatTile({
	metric,
	className,
}: {
	metric: Metric;
	className?: string;
}) {
	return (
		<div
			className={cn(
				"flex flex-col gap-1.5 rounded-xl bg-card p-4 ring-1 ring-foreground/10",
				className,
			)}
		>
			<div className="flex items-center justify-between gap-2">
				<span className="text-sm text-muted-foreground">{metric.label}</span>
				{metric.state && <MetricStateBadge state={metric.state} />}
			</div>
			<div className="flex items-end justify-between gap-3">
				<div className="flex flex-col gap-1">
					<MetricValue metric={metric} />
					<MetricDelta
						delta={metric.delta}
						deltaLabel={metric.deltaLabel}
						higherIsBetter={metric.higherIsBetter}
					/>
				</div>
				{hasTrend(metric.series) && (
					<Sparkline
						points={metric.series}
						className="shrink-0 text-muted-foreground/70"
					/>
				)}
			</div>
		</div>
	);
}

/**
 * A richer single-metric card: {@link StatTile}'s content plus a wider trend and
 * a {@link MetricProvenance} footer (source/freshness/owner). Use it when the
 * metric's provenance matters at a glance, not just its value.
 */
export function MetricCard({
	metric,
	className,
}: {
	metric: Metric;
	className?: string;
}) {
	return (
		<Card className={className}>
			<CardHeader className="flex flex-row items-center justify-between gap-2 space-y-0">
				<span className="text-sm font-medium text-muted-foreground">
					{metric.label}
				</span>
				{metric.state && <MetricStateBadge state={metric.state} />}
			</CardHeader>
			<CardContent className="flex items-end justify-between gap-3">
				<div className="flex flex-col gap-1">
					<MetricValue metric={metric} className="text-3xl" />
					<MetricDelta
						delta={metric.delta}
						deltaLabel={metric.deltaLabel}
						higherIsBetter={metric.higherIsBetter}
					/>
				</div>
				{hasTrend(metric.series) && (
					<Sparkline
						points={metric.series}
						width={128}
						height={40}
						className="shrink-0 text-muted-foreground/70"
					/>
				)}
			</CardContent>
			{metric.provenance && (
				<CardFooter>
					<MetricProvenance {...metric.provenance} />
				</CardFooter>
			)}
		</Card>
	);
}

/**
 * A responsive row of {@link StatTile}s — the handful of headline numbers a
 * dashboard leads with.
 */
export function KPIRow({
	metrics,
	className,
}: {
	metrics: Metric[];
	className?: string;
}) {
	return (
		<div className={cn("grid gap-4 sm:grid-cols-2 lg:grid-cols-4", className)}>
			{metrics.map((metric) => (
				<StatTile key={metric.label} metric={metric} />
			))}
		</div>
	);
}
