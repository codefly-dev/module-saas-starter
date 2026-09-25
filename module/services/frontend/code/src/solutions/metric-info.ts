import type {
	DataGraph,
	MetricFilter,
	SourceMetric,
	WidgetVisualization,
} from "@codefly/saas-plugin-manifest";
import { parseTimeKey } from "@codefly-dev/ui/dashboard";

/**
 * What a tile's ⓘ says about where its number comes from. Pure: it reads only
 * the solution's declaration and the audit results handed in, so every
 * solution's dashboard gets it with no solution code.
 */

export interface MetricSource {
	/** The audit event type, e.g. `saas.auth.login`. */
	type: string;
	/** What the solution's declaration says about the event, if anything. */
	declared?: string;
}

export interface MetricDescription {
	/** What the number counts, in words: the aggregation, grouping and filter. */
	counts: string;
	/** Every audit event type the number reads, derived inputs included. */
	sources: MetricSource[];
	/** The solution's own description of the metric. */
	note?: string;
}

function fieldName(field: string | undefined): string {
	if (!field) return "value";
	return field.startsWith("payload:") ? field.slice("payload:".length) : field;
}

const ORDINAL_SUFFIX: Partial<Record<number, string>> = {
	1: "st",
	2: "nd",
	3: "rd",
};

function ordinal(n: number): string {
	const tens = n % 100;
	if (tens >= 11 && tens <= 13) return `${n}th`;
	return `${n}${ORDINAL_SUFFIX[n % 10] ?? "th"}`;
}

function measure(metric: SourceMetric): string {
	const field = fieldName(metric.field);
	switch (metric.aggregation) {
		case "count":
			return "Number of events";
		case "count_distinct":
			return metric.field === "actor_id"
				? "Distinct people (actor_id)"
				: `Distinct ${field} values`;
		case "sum":
			return `Total ${field}`;
		case "avg":
			return `Average ${field}`;
		case "min":
			return `Smallest ${field}`;
		case "max":
			return `Largest ${field}`;
		case "percentile":
			return `${ordinal(Math.round((metric.percentile ?? 0) * 100))} percentile of ${field}`;
	}
}

// A metric reads one event type, which has one category, so grouping by either
// splits nothing and goes unsaid. A number tile shows one total, so its
// grouping goes unsaid too.
function grouping(
	metric: SourceMetric,
	visualization: WidgetVisualization | undefined,
): string {
	const { groupBy } = metric;
	if (visualization === "number") return "";
	if (groupBy === "time") return `, per ${metric.bucket ?? "day"}`;
	if (groupBy === "actor") return ", by person";
	if (groupBy === "event_type" || groupBy === "category") return "";
	return `, by ${fieldName(groupBy)}`;
}

function narrowing(filter: MetricFilter): string {
	const parts = [
		filter.actor && `the actor is ${filter.actor}`,
		filter.resource && `the resource is ${filter.resource}`,
		filter.resourceId && `the resource id is ${filter.resourceId}`,
		filter.collectionId && `the collection is ${filter.collectionId}`,
		...Object.entries(filter.payloadContains ?? {}).map(
			([key, value]) => `${key} is ${value}`,
		),
	].filter(Boolean);
	return parts.length > 0 ? `, only where ${parts.join(" and ")}` : "";
}

// The source metrics a metric reads, itself included, derived inputs followed
// through to their sources.
export function sourceMetrics(
	graph: DataGraph,
	metricId: string,
	visited = new Set<string>(),
): SourceMetric[] {
	const metric = graph.metrics.find((m) => m.id === metricId);
	if (!metric || visited.has(metricId)) return [];
	visited.add(metricId);
	if (metric.kind === "source") return [metric];
	return metric.inputs.flatMap((input) => sourceMetrics(graph, input, visited));
}

export function describeMetric(
	graph: DataGraph,
	metricId: string,
	visualization?: WidgetVisualization,
): MetricDescription {
	const metric = graph.metrics.find((m) => m.id === metricId);
	const sources: MetricSource[] = [];
	for (const source of sourceMetrics(graph, metricId)) {
		const event = graph.events.find((e) => e.name === source.filter.event);
		if (!event || sources.some((s) => s.type === event.type)) continue;
		sources.push({ type: event.type, declared: event.description });
	}
	if (!metric) return { counts: "Unknown metric", sources };

	let counts: string;
	if (metric.kind === "source") {
		counts =
			measure(metric) +
			grouping(metric, visualization) +
			narrowing(metric.filter);
	} else {
		const names = metric.inputs.map(
			(id) => graph.metrics.find((m) => m.id === id)?.title ?? id,
		);
		counts =
			metric.operation === "ratio"
				? `${names[0]} divided by ${names[1]}`
				: metric.operation === "difference"
					? `${names[0]} minus ${names[1]}`
					: names.join(" plus ");
	}
	return { counts, sources, note: metric.description };
}

/** The dashboard id `activityGraph` declares. */
export const ACTIVITY_DASHBOARD = "activity";

/**
 * A graph that counts, per day, the events behind a metric: one metric per
 * source it reads, with that source's filter unchanged, so the days line up
 * exactly with what the metric counts. The audit service returns only days
 * that have events, oldest first.
 */
export function activityGraph(graph: DataGraph, metricId: string): DataGraph {
	const metrics = sourceMetrics(graph, metricId).map(
		(source, i): SourceMetric => ({
			id: `activity_${i}`,
			kind: "source",
			filter: source.filter,
			groupBy: "time",
			bucket: "day",
			aggregation: "count",
		}),
	);
	return {
		events: graph.events,
		metrics,
		dashboards: [
			{
				id: ACTIVITY_DASHBOARD,
				layout: "grid",
				widgets: metrics.map((m) => ({
					id: m.id,
					metric: m.id,
					visualization: "line",
				})),
			},
		],
	};
}

/** The first and last day with events across the series, or null for none. */
export function activityRange(
	series: readonly { points: readonly { key: string }[] }[],
): { first: Date; last: Date } | null {
	const days = series
		.flatMap((s) => s.points.map((p) => parseTimeKey(p.key)))
		.filter((day): day is Date => day !== null)
		.sort((a, b) => a.getTime() - b.getTime());
	if (days.length === 0) return null;
	return { first: days[0], last: days[days.length - 1] };
}

/** A day bucket as the audit service keys it: a UTC calendar day. */
export function formatDay(day: Date, locale?: string): string {
	return day.toLocaleDateString(locale, {
		month: "short",
		day: "numeric",
		year: "numeric",
		timeZone: "UTC",
	});
}
