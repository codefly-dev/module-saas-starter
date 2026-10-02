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

/**
 * Audit dimension a source metric groups its counts by: a fixed audit column,
 * or a payload field addressed as `payload:<key>`.
 */
export type MetricGroupBy =
	| "event_type"
	| "category"
	| "actor"
	| "time"
	| `payload:${string}`;

/** Time grain applied when a metric groups by time. */
export type MetricBucket = "day" | "week" | "month";

/** How a source metric reduces the audit events its filter matches. */
export type MetricAggregation =
	| "count"
	| "count_distinct"
	| "sum"
	| "avg"
	| "min"
	| "max"
	| "percentile";

/** How a derived metric combines the metrics it references. */
export type MetricOperation = "sum" | "ratio" | "difference";

/** How a widget renders the metric it is bound to. */
export type WidgetVisualization = "line" | "bar" | "area" | "number" | "table";

/** How a dashboard arranges its widgets. */
export type DashboardLayout = "grid" | "stack";

/**
 * The kind of one field of a solution-declared audit event payload: the host
 * audit registry's own kinds. `number` is finite, `int` is whole, and `enum`
 * names its allowed values.
 */
export type EventFieldKind =
	| "string"
	| "uuid"
	| "int"
	| "number"
	| "bool"
	| "enum"
	| "string_array";

/** One typed payload field of a solution-declared audit event. */
export interface EventFieldDeclaration {
	name: string;
	kind: EventFieldKind;
	/** The allowed values; required for, and only for, `kind: "enum"`. */
	values?: readonly string[];
	/**
	 * A personally identifying field: the host strips it from every path that
	 * sends an event outside its audit store. Once admitted as pii it stays so.
	 */
	pii?: boolean;
}

/**
 * A named audit event a metric can filter on. `name` is graph-local; `type` is
 * the audit event type it binds to, e.g. `acme.item.created`.
 *
 * Without `fields` the event binds a type that already exists. With `fields`
 * (even an empty list) it declares `type` as one the solution owns, which the
 * host admits into its audit registry when the solution registers — into a
 * namespace the operator bound to the solution. `@codefly/saas-plugin-manifest`
 * holds the validation; this is its shape.
 */
export interface EventDeclaration {
	name: string;
	type: string;
	description?: string;
	/**
	 * The declared type's retention class: `security` keeps its full details
	 * for the compliance window, `content` (the default) for the shorter content
	 * window. Only with `fields`; it only ever grows.
	 */
	retention?: "security" | "content";
	fields?: readonly EventFieldDeclaration[];
}

/** Narrows the audit events a source metric counts. `event` names a declared event. */
export interface MetricFilter {
	event: string;
	actor?: string;
	resource?: string;
	/** Exact resource boundary; requires resource and current read access. */
	resourceId?: string;
	collectionId?: string;
	/** Exact string payload predicates, e.g. run_id or outcome. */
	payloadContains?: Record<string, string>;
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
	/**
	 * Column or `payload:<key>` the aggregation reads. Required for every op
	 * except `count`; the numeric ops (sum/avg/min/max/percentile) need a
	 * `payload:<key>`.
	 */
	field?: string;
	/** Quantile in (0,1] for `aggregation: "percentile"` (0.95 → p95). */
	percentile?: number;
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
