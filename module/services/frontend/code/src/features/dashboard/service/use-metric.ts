import { useMemo } from "react";
import { useAuditAggregate } from "@/features/audit/service/queries";
import type { MetricDef } from "../model/schema";

export interface MetricPoint {
	key: string;
	count: number;
}

export interface MetricSeries {
	points: MetricPoint[];
	total: number;
	isLoading: boolean;
}

// useMetric resolves a MetricDef against the audit AggregateAuditLog RPC and
// shapes the buckets for its chart: time series stay in chronological order,
// categorical series rank by count and honor the metric's top-N limit.
export function useMetric(metric: MetricDef, orgId: string): MetricSeries {
	const { data, isLoading } = useAuditAggregate(
		{
			orgId,
			eventType: metric.event?.type,
			category: metric.category,
			groupBy: metric.groupBy,
			bucket: metric.bucket,
		},
		{ enabled: orgId !== "" },
	);

	const points = useMemo<MetricPoint[]>(() => {
		const buckets = data ?? [];
		if (metric.groupBy === "time") {
			return buckets.slice().sort((a, b) => a.key.localeCompare(b.key));
		}
		const ranked = buckets.slice().sort((a, b) => b.count - a.count);
		return metric.limit ? ranked.slice(0, metric.limit) : ranked;
	}, [data, metric.groupBy, metric.limit]);

	const total = useMemo(
		() => points.reduce((sum, p) => sum + p.count, 0),
		[points],
	);

	return { points, total, isLoading };
}
