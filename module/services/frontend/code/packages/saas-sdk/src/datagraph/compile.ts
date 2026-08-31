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
 * The org a compiled query is scoped to. A source metric declares only *what* to
 * measure — it carries no org of its own — so the org is bound here from the
 * render context, and every query a spec compiles to reads only the viewer's own
 * org. A blank context org would compile to an org-unscoped (org-wide) audit
 * read, so it fails closed: a user- or chat-authored spec can never widen a query
 * past the viewer's org, even if a caller forgets to withhold resolution until
 * the org is known. Together with the audit client exposing only the read-only
 * aggregate RPC, this is the data-plane invariant — a hostile spec is inert.
 */
function scopedOrgId(context: MetricContext): string {
	if (context.orgId.trim() === "") {
		throw new Error(
			"cannot compile an org-scoped audit query without a viewer org",
		);
	}
	return context.orgId;
}

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
		orgId: scopedOrgId(context),
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
