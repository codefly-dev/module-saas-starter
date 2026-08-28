import { useMemo } from "react";
import {
	type AuditAggregateBucket,
	type AuditAggregateParams,
	useAuditAggregate,
} from "@/features/audit/service/queries";
import type { Dimension, MetricDef } from "../model/schema";

export interface MetricPoint {
	key: string;
	value: number;
}

// A metric is either still resolving (query pending — includes the pre-org
// window where it's disabled), failed, or ready with data. Kept distinct so the
// card never renders a failed or not-yet-loaded query as "no events".
export type MetricStatus = "loading" | "error" | "ready";

export interface MetricSeries {
	points: MetricPoint[];
	total: number;
	status: MetricStatus;
}

// The alias the plotted value is stored under in each bucket's metrics map.
// Count-only metrics have no metrics entry and read the bucket's own COUNT(*).
const VALUE_ALIAS = "value";

export interface CompiledMetric {
	params: AuditAggregateParams;
	// The metrics-map alias holding each bucket's plotted value, or null when the
	// metric is a plain count read straight off the bucket.
	valueAlias: string | null;
}

// Lower a declared metric to a bound audit query. The simple form (no `value`
// or `ratio`) stays a COUNT(*) query read off the bucket; a `value` compiles to
// one aliased aggregation; a `ratio` compiles to its two operands plus the
// derived ratio — all under the same alias the reader plots.
export function compileMetricQuery(
	metric: MetricDef,
	orgId: string,
): CompiledMetric {
	let groupBy: Dimension | undefined;
	let groupBys: Dimension[] | undefined;
	if (Array.isArray(metric.groupBy)) {
		groupBys = metric.groupBy;
	} else {
		groupBy = metric.groupBy;
	}

	const params: AuditAggregateParams = {
		orgId,
		eventType: metric.event?.type,
		category: metric.category,
		groupBy,
		groupBys,
		bucket: metric.bucket,
		from: metric.from ? new Date(metric.from) : undefined,
		to: metric.to ? new Date(metric.to) : undefined,
	};

	if (metric.ratio) {
		params.metrics = [
			{ ...metric.ratio.numerator, alias: "numerator" },
			{ ...metric.ratio.denominator, alias: "denominator" },
		];
		params.derived = [
			{
				alias: VALUE_ALIAS,
				numerator: "numerator",
				denominator: "denominator",
			},
		];
		return { params, valueAlias: VALUE_ALIAS };
	}

	if (metric.value) {
		params.metrics = [{ ...metric.value, alias: VALUE_ALIAS }];
		return { params, valueAlias: VALUE_ALIAS };
	}

	return { params, valueAlias: null };
}

// A multi-dimensional group keys on the tuple of its dimension values; a
// single-dimension one on the first (and only) key.
function pointKey(bucket: AuditAggregateBucket): string {
	return bucket.keys.length > 1 ? bucket.keys.join(" · ") : bucket.key;
}

// The plotted value for a bucket, or undefined when the RPC left the aggregate
// undefined for this group (min/avg/max/percentile over no numeric values, or a
// ratio whose denominator is 0). Absence means "no data", not zero — the audit
// RPC omits the alias deliberately, so the bucket is dropped rather than plotted
// as a phantom zero. A plain count is always present on the bucket.
function pointValue(
	bucket: AuditAggregateBucket,
	alias: string | null,
): number | undefined {
	return alias === null ? bucket.count : bucket.metrics[alias];
}

// count and sum accumulate across groups, so a stat over them is their sum.
// count_distinct, avg, min, max, percentile, and ratios do not — summing
// per-group percentiles or ratios is meaningless — so a stat reports their
// mean, which collapses to the single value for a one-group metric.
function isAdditive(metric: MetricDef): boolean {
	if (metric.ratio) return false;
	const op = metric.value?.op ?? "count";
	return op === "count" || op === "sum";
}

// shapeMetricSeries turns a metric's raw aggregate buckets into its ordered,
// render-ready series: it reads each bucket's plotted value under `valueAlias`
// (dropping groups the RPC left undefined), orders a time series
// chronologically and a categorical one by value (honoring the top-N limit),
// and totals additive ops by sum and the rest by mean. It is the single source
// of truth for series shape, so an imperative preview renders identically to a
// mounted card.
export function shapeMetricSeries(
	buckets: readonly AuditAggregateBucket[],
	metric: MetricDef,
	valueAlias: string | null,
): { points: MetricPoint[]; total: number } {
	const shaped: MetricPoint[] = [];
	for (const bucket of buckets) {
		const value = pointValue(bucket, valueAlias);
		if (value === undefined) continue;
		shaped.push({ key: pointKey(bucket), value });
	}

	let points: MetricPoint[];
	if (metric.groupBy === "time") {
		points = shaped.sort((a, b) => a.key.localeCompare(b.key));
	} else {
		const ranked = shaped.sort((a, b) => b.value - a.value);
		points = metric.limit ? ranked.slice(0, metric.limit) : ranked;
	}

	if (points.length === 0) return { points, total: 0 };
	const sum = points.reduce((acc, p) => acc + p.value, 0);
	return { points, total: isAdditive(metric) ? sum : sum / points.length };
}

// useMetric resolves a MetricDef against the audit AggregateAuditLog RPC and
// shapes the buckets for its chart via shapeMetricSeries.
export function useMetric(metric: MetricDef, orgId: string): MetricSeries {
	const { params, valueAlias } = useMemo(
		() => compileMetricQuery(metric, orgId),
		[metric, orgId],
	);

	const { data, isPending, isError } = useAuditAggregate(params, {
		enabled: orgId !== "",
	});

	const { points, total } = useMemo(
		() => shapeMetricSeries(data ?? [], metric, valueAlias),
		[data, metric, valueAlias],
	);

	// isPending stays true while the query is disabled (orgId not resolved yet),
	// so a metric awaiting its org context reads as loading, never as empty.
	const status: MetricStatus = isError
		? "error"
		: isPending
			? "loading"
			: "ready";

	return { points, total, status };
}
