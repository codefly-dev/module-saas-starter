/**
 * The data-graph declaration this SDK executes: named audit events, metrics
 * computed over them, and dashboards that lay those metrics out as widgets.
 *
 * These shapes mirror the contract-first schema owned by
 * `@codefly/saas-plugin-manifest` (the `dashboard` manifest slot). They are
 * re-declared here so the SDK compiles and ships independently; once the
 * manifest schema lands they collapse to a re-export from that package. The
 * SDK's job is the *runtime* half — turning this declaration into bound audit
 * queries — which is deliberately separate from declaring and validating it.
 */

/** Audit dimension a source metric groups its counts by. */
export type MetricGroupBy = "event_type" | "category" | "actor" | "time";

/** Time grain applied when a metric groups by time. */
export type MetricBucket = "day" | "week" | "month";

/** How a source metric reduces the audit events its filter matches. */
export type MetricAggregation = "count" | "count_distinct";

/** How a derived metric combines the metrics it references. */
export type MetricOperation = "sum" | "ratio" | "difference";

/** How a widget renders the metric it is bound to. */
export type WidgetVisualization = "line" | "bar" | "area" | "number" | "table";

/** How a dashboard arranges its widgets. */
export type DashboardLayout = "grid" | "stack";

/**
 * A named audit event a metric can filter on. `name` is graph-local; `type` is
 * the audit event type it binds to, e.g. `user.signed_in.v1`.
 */
export interface EventDeclaration {
	name: string;
	type: string;
	description?: string;
}

/** Narrows the audit events a source metric counts. `event` names a declared event. */
export interface MetricFilter {
	event: string;
	actor?: string;
	resource?: string;
}

/** A metric computed directly from audit events — one `AggregateAuditLog` query. */
export interface SourceMetric {
	id: string;
	kind: "source";
	title?: string;
	description?: string;
	filter: MetricFilter;
	groupBy: MetricGroupBy;
	/** Required when `groupBy` is `time`, forbidden otherwise. */
	bucket?: MetricBucket;
	aggregation: MetricAggregation;
}

/** A metric derived by combining metrics already declared in the same graph. */
export interface DerivedMetric {
	id: string;
	kind: "derived";
	title?: string;
	description?: string;
	operation: MetricOperation;
	/** Ids of the metrics this one combines (`ratio`/`difference` take two, `sum` at least two). */
	inputs: readonly string[];
}

export type Metric = SourceMetric | DerivedMetric;

/** One dashboard widget bound to a declared metric. */
export interface MetricWidget {
	id: string;
	metric: string;
	visualization: WidgetVisualization;
	title?: string;
}

/** A layout of widgets, each rendering one metric. */
export interface Dashboard {
	id: string;
	title?: string;
	layout: DashboardLayout;
	widgets: readonly MetricWidget[];
}

/** The full data graph declared under a manifest's `dashboard` slot. */
export interface DataGraph {
	events: readonly EventDeclaration[];
	metrics: readonly Metric[];
	dashboards: readonly Dashboard[];
}
