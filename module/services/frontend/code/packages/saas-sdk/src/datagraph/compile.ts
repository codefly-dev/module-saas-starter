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
 * The org a compiled query is bound to. A source metric carries no org of its
 * own — the org comes only from the trusted render context — so this guard owns
 * the context side of that contract: refuse to compile when no viewer org is
 * present, rather than emit an org-unscoped (org-wide) audit read. A caller that
 * forgets to withhold resolution until the org is known therefore fails closed
 * instead of widening the read.
 *
 * A spec cannot inject an org to begin with: the metric shape has no org field
 * and the graph validator rejects unknown ones — that structural check, not this
 * function, is what makes a hostile spec inert. This guard is the complementary
 * context-side check.
 *
 * The org is coerced before trimming so a missing/blank context org fails closed
 * with this message rather than a raw `TypeError`, and the trimmed value is what
 * gets emitted — a padded org must not be judged present here yet miss the
 * server's exact-id org lookup.
 */
function scopedOrgId(context: MetricContext): string {
	const orgId = context.orgId?.trim() ?? "";
	if (orgId === "") {
		throw new Error(
			"cannot compile an org-scoped audit query without a viewer org",
		);
	}
	return orgId;
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
