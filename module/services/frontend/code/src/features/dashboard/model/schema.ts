// Declarative data-graph for audit-driven dashboards. A consumer wires three
// nodes — an event names an audit event type, a metric aggregates it over the
// audit RPC, a dashboard groups metrics — then hands the dashboard to
// <Dashboard data={…}/>. The helpers below are typed identity functions: they
// exist so a declaration reads as data and gets full inference without a class
// or builder ceremony.

import { assertDashboardSpec } from "./validate";

// GroupBy mirrors the audit AggregateAuditLog RPC's group_by dimension.
export type GroupBy = "event_type" | "category" | "actor" | "time";

// Bucket sizes the time grain; only meaningful when groupBy === "time".
export type Bucket = "day" | "week" | "month";

// ChartKind selects how a metric renders: a time-series line, a ranked bar
// list (top-N), or a single KPI tile.
export type ChartKind = "line" | "bar" | "stat";

// An event names an audit event type the dashboard reads (e.g. "auth.login").
export interface EventDef {
	type: string;
}

export function event(type: string): EventDef {
	return { type };
}

// A metric is one node in the data graph: an aggregation over the audit trail.
// `event` restricts to a single event type, `category` to a whole category;
// omit both to aggregate everything.
export interface MetricDef {
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
	// layout; ignored by a stack. It clamps to the grid's column count on
	// narrower breakpoints so a wide card never overflows its row.
	span?: 1 | 2 | 3 | 4;
}

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
