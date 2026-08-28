// Declarative data-graph for audit-driven dashboards. A consumer wires three
// nodes — an event names an audit event type, a metric aggregates it over the
// audit RPC, a dashboard groups metrics — then hands the dashboard to
// <Dashboard data={…}/>. The helpers below are typed identity functions: they
// exist so a declaration reads as data and gets full inference without a class
// or builder ceremony.

import { assertDashboardSpec } from "./validate";

// A group dimension mirrors the audit AggregateAuditLog RPC's group_by: one of
// the fixed audit columns, or a payload field addressed as `payload:<key>`.
export type Dimension =
	| "event_type"
	| "category"
	| "actor"
	| "time"
	| `payload:${string}`;

// GroupBy is a single dimension, or a list for multi-dimensional grouping (each
// bucket keyed on the tuple of dimension values).
export type GroupBy = Dimension | Dimension[];

// Bucket sizes the time grain; only meaningful when a metric groups by time.
export type Bucket = "day" | "week" | "month";

// ChartKind selects how a metric renders: a time-series line, a ranked bar
// list (top-N), or a single KPI tile.
export type ChartKind = "line" | "bar" | "stat";

// MetricOp mirrors the audit RPC's per-group aggregation functions.
export type MetricOp =
	| "count"
	| "count_distinct"
	| "sum"
	| "avg"
	| "min"
	| "max"
	| "percentile";

// A single aggregation computed per group. The op determines what else is
// required, so the illegal states are unrepresentable: `count` takes no field;
// count_distinct and the numeric ops (sum/avg/min/max) read a `field` — a
// `payload:<key>` for the numeric ops, or a column such as "actor_id" for
// count_distinct; `percentile` additionally names its quantile in (0,1]
// (0.95 → p95).
export type MetricValue =
	| { op: "count" }
	| { op: "count_distinct" | "sum" | "avg" | "min" | "max"; field: string }
	| { op: "percentile"; field: string; percentile: number };

// A per-group ratio of two aggregations, computed by the RPC (e.g. an error
// rate). The ratio is omitted for a group where the denominator is 0.
export interface MetricRatio {
	numerator: MetricValue;
	denominator: MetricValue;
}

// An event names an audit event type the dashboard reads (e.g. "auth.login").
export interface EventDef {
	type: string;
}

export function event(type: string): EventDef {
	return { type };
}

// The shared shape of every metric node, before its plotted value is chosen.
interface MetricBase {
	title: string;
	description?: string;
	event?: EventDef;
	category?: string;
	groupBy: GroupBy;
	bucket?: Bucket;
	chart: ChartKind;
	// limit caps the ranked bars for a categorical `chart: "bar"` metric.
	limit?: number;
	// span sets how many grid columns this metric's card occupies in a grid
	// layout; ignored by a stack. A span wider than the layout's column count is
	// clamped down to it, so a card never spans more columns than the grid has.
	span?: 1 | 2 | 3 | 4;
	// from/to bound the audit window this metric reads, as ISO-8601 timestamps
	// so the spec stays JSON-serializable; omit for all-time.
	from?: string;
	to?: string;
}

// A metric is one node in the data graph: an aggregation over the audit trail.
// `event` restricts to a single event type, `category` to a whole category;
// omit both to aggregate everything. A card renders one series, so `value` and
// `ratio` are mutually exclusive: a bare metric counts events; `value` plots a
// single aggregation; `ratio` plots a per-group ratio of two.
export type MetricDef = MetricBase &
	(
		| {
				// value selects the aggregation plotted per group; defaults to a
				// plain count.
				value?: MetricValue;
				ratio?: never;
		  }
		| {
				// ratio plots a per-group ratio of two aggregations instead of a
				// single value.
				ratio: MetricRatio;
				value?: never;
		  }
	);

export function metric(def: MetricDef): MetricDef {
	return def;
}

// The current dashboard-spec schema version. It is a discriminant, not a
// range: a spec stamped with any other value is from a schema this build does
// not understand and is rejected on load rather than coerced.
export const DASHBOARD_SPEC_VERSION = 1;

// Layout arranges a dashboard's metric cards: a responsive column `grid` or a
// single-column `stack`. `columns` sizes the grid (default 2) and is ignored by
// a stack.
export interface LayoutDef {
	kind: "grid" | "stack";
	columns?: 1 | 2 | 3 | 4;
}

// Theme applies a local accent to a dashboard's charts and cards: `accent` (any
// CSS color) overrides the primary token for this dashboard's subtree only, so
// every series line, sparkline, and bar fill picks it up. In-browser theming
// only for now — full skin-token integration is deferred.
export interface ThemeDef {
	accent?: string;
}

// A dashboard spec is serializable data, not code: it round-trips through
// JSON.stringify/parse so it can live in app state and localStorage, be edited
// at runtime, and survive a reload. `version` pins the schema it was authored
// against.
export interface DashboardDef {
	version: typeof DASHBOARD_SPEC_VERSION;
	title?: string;
	description?: string;
	layout?: LayoutDef;
	theme?: ThemeDef;
	metrics: MetricDef[];
}

// dashboard() stamps the current spec version and validates the result, so an
// authored literal need not repeat the version and is held to exactly the same
// coherence rules as a spec restored from storage or set at runtime. There is
// one notion of a valid spec, not one for authored dashboards and a stricter
// one for drafts; an incoherent literal (e.g. a time metric without a bucket,
// which the types permit) fails here at author time rather than at render.
export function dashboard(def: Omit<DashboardDef, "version">): DashboardDef {
	const spec = { version: DASHBOARD_SPEC_VERSION, ...def };
	assertDashboardSpec(spec);
	return spec;
}
