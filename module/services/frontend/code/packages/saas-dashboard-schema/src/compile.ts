import type {
	AggregateQuery,
	Metric,
	MetricPoint,
	SourceMetric,
} from "./types.js";

/**
 * Compile a source metric to the AggregateAuditLog query it draws from. A
 * time-grouped metric carries its bucket; every other grouping sends an empty
 * bucket, matching the RPC's field shape. `defineDataGraph` guarantees the
 * time/bucket invariant, so no branch on grouping is needed here.
 */
export function compileMetric(metric: SourceMetric): AggregateQuery {
	return {
		eventType: metric.filter?.event ?? "",
		category: metric.filter?.category ?? "",
		groupBy: metric.groupBy,
		bucket: metric.bucket ?? "",
	};
}

/** Fetches a source metric's series by running its compiled query. */
export type MetricFetcher = (query: AggregateQuery) => Promise<MetricPoint[]>;

/**
 * Resolve a metric to its series: a source metric runs its query through
 * `fetcher`; a derived metric resolves its upstreams first, then applies its
 * pure `compute`. `memo` dedupes shared upstreams within one resolution — a
 * metric fanned into by several others runs its query once. The graph is
 * acyclic by construction (validated in `defineDataGraph`), so this recursion
 * terminates.
 */
export function resolveMetric(
	metric: Metric,
	lookup: (id: string) => Metric,
	fetcher: MetricFetcher,
	memo: Map<string, Promise<MetricPoint[]>> = new Map(),
): Promise<MetricPoint[]> {
	const cached = memo.get(metric.id);
	if (cached) return cached;

	const series =
		metric.kind === "source"
			? fetcher(compileMetric(metric))
			: Promise.all(
					metric.from.map((id) =>
						resolveMetric(lookup(id), lookup, fetcher, memo),
					),
				).then((inputs) => metric.compute(inputs));

	memo.set(metric.id, series);
	return series;
}
