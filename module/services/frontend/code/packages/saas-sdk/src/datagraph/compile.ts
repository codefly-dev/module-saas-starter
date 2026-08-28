import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { SourceMetric } from "../schema.js";
import type { AuditAggregateQuery, MetricContext } from "./types.js";

/** Resolves a graph-local event name to the audit event type it binds to. */
export type EventTypeResolver = (eventName: string) => string;

/**
 * The alias a non-count metric's value is stored under in each bucket's metrics
 * map. A plain count has no metrics entry and is read from the bucket's own
 * COUNT(*); {@link runMetric} reads this alias for every other op.
 */
export const METRIC_VALUE_ALIAS = "value";

/**
 * Turn a source metric plus its render context into the bound audit query. The
 * metric declares *what* to measure — its filter names an event, which the
 * resolver maps to the registered audit event type, and its aggregation selects
 * the op computed per group — and the context binds it to *whose* data (org)
 * and *when* (time window). A plain count stays a metrics-free COUNT(*) query;
 * any other op compiles to one aliased `AuditMetric`.
 */
export function compileMetric(
	metric: SourceMetric,
	resolveEventType: EventTypeResolver,
	context: MetricContext,
): AuditAggregateQuery {
	return {
		orgId: context.orgId,
		eventType: resolveEventType(metric.filter.event),
		category: "",
		actorId: metric.filter.actor ?? "",
		resource: metric.filter.resource ?? "",
		from: context.from ? timestampFromDate(context.from) : undefined,
		to: context.to ? timestampFromDate(context.to) : undefined,
		groupBy: metric.groupBy,
		bucket: metric.groupBy === "time" ? (metric.bucket ?? "day") : "",
		metrics:
			metric.aggregation === "count"
				? []
				: [
						{
							op: metric.aggregation,
							field: metric.field ?? "",
							percentile: metric.percentile ?? 0,
							alias: METRIC_VALUE_ALIAS,
						},
					],
	};
}
