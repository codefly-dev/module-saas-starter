import { useMemo } from "react";
import { useAuditAggregate } from "@/features/audit/service/queries";
import type { MetricDef } from "../model/schema";

export interface MetricPoint {
	key: string;
	count: number;
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

// shapeMetricSeries orders a metric's raw buckets for its chart: time series
// stay in chronological order, categorical series rank by count and honor the
// metric's top-N limit. It is the single source of truth for series shape, so
// an imperative preview renders identically to a mounted card.
export function shapeMetricSeries(
	buckets: readonly MetricPoint[],
	metric: Pick<MetricDef, "groupBy" | "limit">,
): { points: MetricPoint[]; total: number } {
	let points: MetricPoint[];
	if (metric.groupBy === "time") {
		points = buckets.slice().sort((a, b) => a.key.localeCompare(b.key));
	} else {
		const ranked = buckets.slice().sort((a, b) => b.count - a.count);
		points = metric.limit ? ranked.slice(0, metric.limit) : ranked;
	}
	const total = points.reduce((sum, p) => sum + p.count, 0);
	return { points, total };
}

// useMetric resolves a MetricDef against the audit AggregateAuditLog RPC and
// shapes the buckets for its chart.
export function useMetric(metric: MetricDef, orgId: string): MetricSeries {
	const { data, isPending, isError } = useAuditAggregate(
		{
			orgId,
			eventType: metric.event?.type,
			category: metric.category,
			groupBy: metric.groupBy,
			bucket: metric.bucket,
		},
		{ enabled: orgId !== "" },
	);

	const { points, total } = useMemo(
		() => shapeMetricSeries(data ?? [], metric),
		[data, metric],
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
