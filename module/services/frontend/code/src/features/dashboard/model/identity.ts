// The identity of a rendered metric — what makes two panels "the same panel".
// It is derived from the fields that distinguish a metric as displayed, not its
// array position (reorder-fragile) nor its title alone (two panels can share a
// title). This is the single source of truth for that notion: <Dashboard> uses
// it as each card's React key, and any producer (the driver, a JSON editor)
// uses it to avoid emitting two metrics that would collide on that key.

import type { MetricDef } from "./schema";

export function metricIdentity(metric: MetricDef): string {
	return [
		metric.title,
		// description is the card's rendered subtitle, so two cards that differ
		// only in it are distinct panels and must not share a key.
		metric.description ?? "",
		metric.chart,
		Array.isArray(metric.groupBy) ? metric.groupBy.join(",") : metric.groupBy,
		metric.bucket ?? "",
		metric.event?.type ?? "",
		metric.category ?? "",
		metric.from ?? "",
		metric.to ?? "",
		metric.span ?? "",
		// limit ranks a categorical series to its top N, so a top-5 and a top-10
		// of the same metric render as different cards and need different keys.
		metric.limit ?? "",
		JSON.stringify(metric.value ?? metric.ratio ?? 0),
	].join("|");
}
