// Pure reshaping between what AggregateAuditLog returns and what a chart or a
// tile renders. No React, no fetching: the page composes these, the tests pin
// them, and the server's aggregate stays the only place a count is computed.

import type { AuditAggregateBucket } from "../service/queries";

export type AuditRangePreset = "7d" | "30d" | "90d";
export type AuditBucket = "day" | "week" | "month";

export const AUDIT_RANGE_PRESETS: readonly AuditRangePreset[] = [
	"7d",
	"30d",
	"90d",
];
export const AUDIT_BUCKETS: readonly AuditBucket[] = ["day", "week", "month"];

const RANGE_DAYS: Record<AuditRangePreset, number> = {
	"7d": 7,
	"30d": 30,
	"90d": 90,
};

export interface AuditWindow {
	from: Date;
	to: Date;
}

/**
 * The window a preset names, ending now, and the equal-length window before
 * it. A tile's delta compares the two; a series is drawn over the first.
 */
export function auditWindows(
	preset: AuditRangePreset,
	now: Date = new Date(),
): { current: AuditWindow; previous: AuditWindow } {
	const days = RANGE_DAYS[preset];
	const to = new Date(now);
	const from = new Date(to.getTime() - days * 86_400_000);
	const previousTo = new Date(from);
	const previousFrom = new Date(previousTo.getTime() - days * 86_400_000);
	return {
		current: { from, to },
		previous: { from: previousFrom, to: previousTo },
	};
}

/** The bucket a preset defaults to, so 90 days does not draw 90 day bars. */
export function defaultBucketFor(preset: AuditRangePreset): AuditBucket {
	return preset === "90d" ? "week" : "day";
}

/**
 * Relative change from the previous window to the current one, as a fraction
 * (0.12 → +12%). Undefined when the previous window had nothing to compare
 * against: a delta from zero is not a percentage, and a tile shows none.
 */
export function relativeChange(
	current: number,
	previous: number,
): number | undefined {
	if (previous === 0) return undefined;
	return (current - previous) / previous;
}

/** Sum of every bucket's count — the total a single-dimension aggregate holds. */
export function totalCount(buckets: readonly AuditAggregateBucket[]): number {
	return buckets.reduce((sum, bucket) => sum + bucket.count, 0);
}

/** How many distinct groups an aggregate returned (actors, types, …). */
export function distinctCount(
	buckets: readonly AuditAggregateBucket[],
): number {
	return buckets.length;
}

export interface StackedSeries {
	/** Time buckets in ascending order; the shared x axis. */
	labels: string[];
	/** One series per group, each aligned to `labels` by index, zero-filled. */
	series: { name: string; data: { label: string; value: number }[] }[];
}

/**
 * Pivot a two-dimensional `time × group` aggregate into aligned series.
 *
 * The server returns one bucket per (time, group) pair that has events; a
 * group with no events in some time bucket has no row there. A chart aligns
 * series by index, so every series must carry every time bucket, zero-filled.
 * Series are ordered by their total, largest first, so the biggest band sits at
 * the bottom of a stack and the legend reads top-down by weight.
 */
export function pivotByTime(
	buckets: readonly AuditAggregateBucket[],
	limit = 8,
): StackedSeries {
	const labels = Array.from(
		new Set(buckets.map((b) => b.keys[0] ?? b.key)),
	).sort();
	const byGroup = new Map<string, Map<string, number>>();
	for (const bucket of buckets) {
		const time = bucket.keys[0] ?? bucket.key;
		const group = bucket.keys[1] ?? "";
		let row = byGroup.get(group);
		if (!row) {
			row = new Map();
			byGroup.set(group, row);
		}
		row.set(time, (row.get(time) ?? 0) + bucket.count);
	}
	const ranked = Array.from(byGroup.entries())
		.map(([name, row]) => ({
			name,
			total: Array.from(row.values()).reduce((a, b) => a + b, 0),
			row,
		}))
		.sort((a, b) => b.total - a.total);

	// Everything past the limit folds into one band rather than vanishing, so
	// the stack still sums to the true total.
	const kept = ranked.slice(0, limit);
	const rest = ranked.slice(limit);
	const series = kept.map(({ name, row }) => ({
		name,
		data: labels.map((label) => ({ label, value: row.get(label) ?? 0 })),
	}));
	if (rest.length > 0) {
		series.push({
			name: "other",
			data: labels.map((label) => ({
				label,
				value: rest.reduce((sum, { row }) => sum + (row.get(label) ?? 0), 0),
			})),
		});
	}
	return { labels, series };
}

/** The top N groups of a one-dimensional aggregate, largest first. */
export function topGroups(
	buckets: readonly AuditAggregateBucket[],
	limit: number,
): { key: string; count: number }[] {
	return buckets
		.slice()
		.sort((a, b) => b.count - a.count)
		.slice(0, limit)
		.map(({ key, count }) => ({ key, count }));
}

/**
 * Event types that mean a person joined the tenant, by their registered names.
 * The server's registry is the authority; this list is checked against it, not
 * trusted over it: `newUserEventTypes` keeps only the names the registry
 * actually advertises, so a rename on the server empties the tile visibly
 * ("not registered") instead of counting nothing and calling it zero.
 */
export const NEW_USER_EVENT_TYPES: readonly string[] = [
	"saas.user.created",
	"saas.user.registered",
];

/** The new-user names the loaded registry still knows. */
export function newUserEventTypes(
	registry: readonly { name: string }[],
): string[] {
	const known = new Set(registry.map((t) => t.name));
	return NEW_USER_EVENT_TYPES.filter((name) => known.has(name));
}

/** Count of the given event types in a by-event-type aggregate. */
export function countEventTypes(
	byType: readonly AuditAggregateBucket[],
	names: readonly string[],
): number {
	return byType
		.filter((b) => names.includes(b.key))
		.reduce((sum, b) => sum + b.count, 0);
}

/**
 * The page's headline aggregate is ONE request per window, grouped by
 * category then event type and never filtered by category on the server. Every
 * tile slices that result here: the category is a group dimension, so slicing
 * client-side is exact, and the security tile can read its own category while
 * the viewer drills into another. Four aggregates per window became one.
 */
export function sliceCategory(
	byCategoryAndType: readonly AuditAggregateBucket[],
	category: string | undefined,
): AuditAggregateBucket[] {
	if (category === undefined) return byCategoryAndType.slice();
	return byCategoryAndType.filter((b) => b.keys[0] === category);
}

/** Fold a category × event-type aggregate down to one bucket per event type. */
export function byEventType(
	byCategoryAndType: readonly AuditAggregateBucket[],
): AuditAggregateBucket[] {
	const totals = new Map<string, number>();
	for (const b of byCategoryAndType) {
		const type = b.keys[1] ?? b.key;
		totals.set(type, (totals.get(type) ?? 0) + b.count);
	}
	return Array.from(totals, ([key, count]) => ({
		key,
		count,
		keys: [key],
		metrics: { count },
	}));
}

/**
 * The registry's category for security-relevant events (MFA, sessions,
 * suspicious sign-ins). One of seven typed categories on the server; there is
 * deliberately no "denied" or "failure" category there, because an audit
 * event records what happened, not whether a caller liked the outcome. A tile
 * counting a category that does not exist would read zero forever and look
 * like good news.
 */
export const SECURITY_CATEGORY = "security";
