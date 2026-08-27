// Contract-first types for the Template Dashboard data graph: a declarative
// description of events -> metrics -> dashboards that compiles to audit-RPC
// queries. React-free and serializable so both the TS SDK and the dashboard
// component consume the same graph.

export const DATA_GRAPH_SCHEMA_VERSION = "1.0.0";

/** How audit rows are bucketed into a single metric series. */
export type GroupBy = "event_type" | "category" | "actor" | "time";

/** Time granularity; only meaningful when a source metric groups by "time". */
export type Bucket = "day" | "week" | "month";

/**
 * Reduction applied within each group. COUNT is the only aggregation the audit
 * RPC supports today (#280 lifts this ceiling); the type is kept as a union so a
 * graph can be authored ahead of the RPC without a breaking change later.
 */
export type Aggregation = "count";

/** A declared audit event the data graph may draw from. */
export interface EventDeclaration {
	/** Fully-qualified audit event type, e.g. "user.login". */
	name: string;
	/** Registry category, e.g. "auth". Used for grouping and filtering. */
	category?: string;
}

/** Narrows which audit rows a source metric aggregates over. */
export interface EventFilter {
	/** Exact audit event type name. */
	event?: string;
	/** Audit registry category. */
	category?: string;
}

/** A single point in a resolved metric series. */
export interface MetricPoint {
	key: string;
	value: number;
}

/** The AggregateAuditLog query a source metric compiles to. */
export interface AggregateQuery {
	eventType: string;
	category: string;
	groupBy: GroupBy;
	bucket: string;
}

/** A metric computed directly from the audit RPC. */
export interface SourceMetric {
	id: string;
	kind: "source";
	filter?: EventFilter;
	groupBy: GroupBy;
	bucket?: Bucket;
	aggregation?: Aggregation;
}

/**
 * A metric computed from the resolved series of other metrics — the "metrics
 * derive from other metrics" case. `compute` is a pure reducer over the
 * upstream series, passed in the order listed by {@link DerivedMetric.from}.
 */
export interface DerivedMetric {
	id: string;
	kind: "derived";
	from: string[];
	compute: (inputs: MetricPoint[][]) => MetricPoint[];
}

export type Metric = SourceMetric | DerivedMetric;

/** The visualization a widget renders a metric series with. */
export type WidgetType = "line" | "bar" | "stat" | "table";

/** A visualization bound to a metric within a dashboard. */
export interface MetricWidget {
	id: string;
	/** Id of the metric whose series this widget renders. */
	metric: string;
	type: WidgetType;
	title?: string;
}

/** A dashboard: a titled collection of metric-bound widgets. */
export interface Dashboard {
	id: string;
	title?: string;
	/** Grid-column hint for the default layout; layout primitives are #283. */
	columns?: number;
	widgets: MetricWidget[];
}

/** The raw declaration passed to {@link defineDataGraph}. */
export interface DataGraphInput {
	events: EventDeclaration[];
	metrics: Metric[];
	dashboards?: Dashboard[];
}
