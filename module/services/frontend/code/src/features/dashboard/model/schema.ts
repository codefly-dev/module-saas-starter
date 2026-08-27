// Declarative data-graph for audit-driven dashboards. A consumer wires three
// nodes — an event names an audit event type, a metric aggregates it over the
// audit RPC, a dashboard groups metrics — then hands the dashboard to
// <Dashboard data={…}/>. The helpers below are typed identity functions: they
// exist so a declaration reads as data and gets full inference without a class
// or builder ceremony.

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
	label?: string;
}

export function event(type: string, label?: string): EventDef {
	return { type, label };
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
}

export function metric(def: MetricDef): MetricDef {
	return def;
}

export interface DashboardDef {
	title?: string;
	description?: string;
	metrics: MetricDef[];
}

export function dashboard(def: DashboardDef): DashboardDef {
	return def;
}
