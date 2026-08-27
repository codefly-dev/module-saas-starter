import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { SourceMetric } from "../schema.js";
import type { AuditAggregateQuery, MetricContext } from "./types.js";

/** Resolves a graph-local event name to the audit event type it binds to. */
export type EventTypeResolver = (eventName: string) => string;

/**
 * Turn a source metric plus its render context into the bound audit query. The
 * metric declares *what* to measure — its filter names an event, which the
 * resolver maps to the registered audit event type — and the context binds it
 * to *whose* data (org) and *when* (time window).
 */
export function compileMetric(
	metric: SourceMetric,
	resolveEventType: EventTypeResolver,
	context: MetricContext,
): AuditAggregateQuery {
	if (metric.aggregation === "count_distinct") {
		// The audit RPC counts rows only; distinct-count is part of the RPC
		// extension (#280) and has no bound query to compile to yet.
		throw new Error(
			`metric '${metric.id}' uses count_distinct, which the audit RPC does not support yet`,
		);
	}
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
	};
}
