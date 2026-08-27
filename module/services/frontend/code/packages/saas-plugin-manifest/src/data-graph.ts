/**
 * The data-graph declaration a plugin ships under `dashboard`: named audit
 * events, metrics computed over them, and dashboards that lay those metrics out
 * as widgets. It is a pure declaration — every id is stable and JSON-safe, and
 * the whole graph compiles to `AuditService` queries at runtime, so it never
 * carries data, credentials, or deployment addresses.
 *
 * The graph has three node kinds forming a directed acyclic reference chain:
 * an event is named and bound to an audit event type; a source metric filters
 * one event and compiles to an `AggregateAuditLog` query; a derived metric
 * combines other metrics; a dashboard binds widgets to metrics. Referential
 * integrity — every reference resolves and the derived-metric graph is
 * acyclic — is what makes this a graph rather than three flat lists.
 */

/**
 * Audit dimension a source metric groups its counts by. Mirrors the
 * `group_by` domain of `AuditService.AggregateAuditLog`.
 */
export type MetricGroupBy = "event_type" | "category" | "actor" | "time";

/**
 * Time grain applied when a metric groups by time. Mirrors the audit RPC's
 * `bucket` domain.
 */
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
 * A named audit event a metric can filter on. `name` is graph-local (metrics
 * reference it); `type` is the audit event type it binds to, e.g.
 * `user.signed_in.v1`.
 */
export interface EventDeclaration {
	name: string;
	type: string;
	description?: string;
}

/**
 * Narrows the audit events a source metric counts. `event` names a declared
 * event; the optional facets pass straight through to the audit query.
 */
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
	/**
	 * Ids of the metrics this one combines. Each must be declared in the same
	 * graph, none may be this metric itself, and the reference graph must stay
	 * acyclic. `ratio` and `difference` take exactly two inputs; `sum` takes at
	 * least two.
	 */
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

const LOGICAL_ID = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;
const EVENT_TYPE = /^[a-z][a-z0-9]*(?:\.[a-z0-9]+)*\.v[1-9][0-9]*$/;
const GROUP_BY: readonly MetricGroupBy[] = [
	"event_type",
	"category",
	"actor",
	"time",
];
const BUCKET: readonly MetricBucket[] = ["day", "week", "month"];
const AGGREGATION: readonly MetricAggregation[] = ["count", "count_distinct"];
const OPERATION: readonly MetricOperation[] = ["sum", "ratio", "difference"];
const VISUALIZATION: readonly WidgetVisualization[] = [
	"line",
	"bar",
	"area",
	"number",
	"table",
];
const LAYOUT: readonly DashboardLayout[] = ["grid", "stack"];

function assertGraph(condition: unknown, message: string): asserts condition {
	if (!condition) throw new Error(`Invalid data graph: ${message}`);
}

function isObject(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

function assertExactKeys(
	value: Record<string, unknown>,
	allowed: readonly string[],
	context: string,
): void {
	const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
	assertGraph(
		unknown.length === 0,
		`${context} has unknown field '${unknown[0]}'`,
	);
}

function assertLogicalId(
	value: unknown,
	context: string,
): asserts value is string {
	assertGraph(
		typeof value === "string" && LOGICAL_ID.test(value),
		`${context} '${String(value)}' is not a valid logical id`,
	);
}

function assertOptionalText(value: unknown, context: string): void {
	assertGraph(
		value === undefined ||
			(typeof value === "string" && value.trim().length > 0),
		`${context} must be a non-empty string`,
	);
}

function assertUnique(values: readonly string[], kind: string): void {
	const seen = new Set<string>();
	for (const value of values) {
		assertGraph(
			!seen.has(value),
			`${kind} '${value}' is declared more than once`,
		);
		seen.add(value);
	}
}

function validateEvent(value: unknown): asserts value is EventDeclaration {
	assertGraph(isObject(value), "event must be an object");
	assertExactKeys(value, ["name", "type", "description"], "event");
	assertLogicalId(value.name, "event name");
	assertGraph(
		typeof value.type === "string" && EVENT_TYPE.test(value.type),
		`event '${String(value.name)}' type '${String(value.type)}' must be namespaced and versioned`,
	);
	assertOptionalText(
		value.description,
		`event '${String(value.name)}' description`,
	);
}

function validateSourceMetric(value: Record<string, unknown>): void {
	assertExactKeys(
		value,
		[
			"id",
			"kind",
			"title",
			"description",
			"filter",
			"groupBy",
			"bucket",
			"aggregation",
		],
		`metric '${String(value.id)}'`,
	);
	const context = `metric '${String(value.id)}'`;
	const filter = value.filter;
	assertGraph(isObject(filter), `${context} filter must be an object`);
	assertExactKeys(filter, ["event", "actor", "resource"], `${context} filter`);
	assertLogicalId(filter.event, `${context} filter event`);
	assertGraph(
		filter.actor === undefined ||
			(typeof filter.actor === "string" && filter.actor.trim().length > 0),
		`${context} filter actor must be a non-empty string`,
	);
	assertGraph(
		filter.resource === undefined ||
			(typeof filter.resource === "string" &&
				filter.resource.trim().length > 0),
		`${context} filter resource must be a non-empty string`,
	);
	assertGraph(
		GROUP_BY.includes(value.groupBy as MetricGroupBy),
		`${context} groupBy '${String(value.groupBy)}' is unsupported`,
	);
	assertGraph(
		AGGREGATION.includes(value.aggregation as MetricAggregation),
		`${context} aggregation '${String(value.aggregation)}' is unsupported`,
	);
	if (value.groupBy === "time") {
		assertGraph(
			BUCKET.includes(value.bucket as MetricBucket),
			`${context} groups by time and needs a bucket of ${BUCKET.join(", ")}`,
		);
	} else {
		assertGraph(
			value.bucket === undefined,
			`${context} declares a bucket but does not group by time`,
		);
	}
	assertOptionalText(value.title, `${context} title`);
	assertOptionalText(value.description, `${context} description`);
}

function validateDerivedMetric(value: Record<string, unknown>): void {
	assertExactKeys(
		value,
		["id", "kind", "title", "description", "operation", "inputs"],
		`metric '${String(value.id)}'`,
	);
	const context = `metric '${String(value.id)}'`;
	assertGraph(
		OPERATION.includes(value.operation as MetricOperation),
		`${context} operation '${String(value.operation)}' is unsupported`,
	);
	assertGraph(
		Array.isArray(value.inputs),
		`${context} inputs must be an array`,
	);
	const inputs = value.inputs as unknown[];
	for (const input of inputs) assertLogicalId(input, `${context} input`);
	assertGraph(
		!(inputs as string[]).includes(value.id as string),
		`${context} cannot derive from itself`,
	);
	assertUnique(inputs as string[], `${context} input`);
	if (value.operation === "sum") {
		assertGraph(
			inputs.length >= 2,
			`${context} 'sum' needs at least two inputs`,
		);
	} else {
		assertGraph(
			inputs.length === 2,
			`${context} '${String(value.operation)}' needs exactly two inputs`,
		);
	}
	assertOptionalText(value.title, `${context} title`);
	assertOptionalText(value.description, `${context} description`);
}

function validateMetric(value: unknown): asserts value is Metric {
	assertGraph(isObject(value), "metric must be an object");
	assertLogicalId(value.id, "metric id");
	assertGraph(
		value.kind === "source" || value.kind === "derived",
		`metric '${String(value.id)}' kind '${String(value.kind)}' must be 'source' or 'derived'`,
	);
	if (value.kind === "source") validateSourceMetric(value);
	else validateDerivedMetric(value);
}

function validateWidget(
	value: unknown,
	dashboardId: string,
): asserts value is MetricWidget {
	assertGraph(
		isObject(value),
		`dashboard '${dashboardId}' widget must be an object`,
	);
	assertExactKeys(
		value,
		["id", "metric", "visualization", "title"],
		`dashboard '${dashboardId}' widget`,
	);
	assertLogicalId(value.id, `dashboard '${dashboardId}' widget id`);
	assertLogicalId(
		value.metric,
		`dashboard '${dashboardId}' widget '${String(value.id)}' metric`,
	);
	assertGraph(
		VISUALIZATION.includes(value.visualization as WidgetVisualization),
		`dashboard '${dashboardId}' widget '${String(value.id)}' visualization '${String(value.visualization)}' is unsupported`,
	);
	assertOptionalText(
		value.title,
		`dashboard '${dashboardId}' widget '${String(value.id)}' title`,
	);
}

function validateDashboard(value: unknown): asserts value is Dashboard {
	assertGraph(isObject(value), "dashboard must be an object");
	assertExactKeys(value, ["id", "title", "layout", "widgets"], "dashboard");
	assertLogicalId(value.id, "dashboard id");
	assertGraph(
		LAYOUT.includes(value.layout as DashboardLayout),
		`dashboard '${String(value.id)}' layout '${String(value.layout)}' is unsupported`,
	);
	assertGraph(
		Array.isArray(value.widgets),
		`dashboard '${String(value.id)}' widgets must be an array`,
	);
	assertGraph(
		value.widgets.length > 0,
		`dashboard '${String(value.id)}' must declare at least one widget`,
	);
	for (const widget of value.widgets)
		validateWidget(widget, value.id as string);
	assertUnique(
		(value.widgets as MetricWidget[]).map((widget) => widget.id),
		`widget id in dashboard '${String(value.id)}'`,
	);
	assertOptionalText(value.title, `dashboard '${String(value.id)}' title`);
}

/**
 * A derived metric cannot, through any chain of inputs, depend on itself. The
 * inputs are already known to name declared metrics, so a depth-first walk over
 * the derived-metric edges that finds a node twice on the current stack proves
 * a cycle.
 */
function assertAcyclic(metrics: readonly Metric[]): void {
	const derived = new Map<string, readonly string[]>();
	for (const metric of metrics) {
		if (metric.kind === "derived") derived.set(metric.id, metric.inputs);
	}
	const state = new Map<string, "visiting" | "done">();
	const walk = (id: string): void => {
		const status = state.get(id);
		assertGraph(
			status !== "visiting",
			`metric '${id}' is part of a derivation cycle`,
		);
		if (status === "done") return;
		state.set(id, "visiting");
		for (const input of derived.get(id) ?? []) walk(input);
		state.set(id, "done");
	};
	for (const id of derived.keys()) walk(id);
}

/**
 * Validates a parsed data graph and narrows it to `DataGraph`. Beyond the
 * per-node field rules, it enforces the cross-references that make the
 * declaration a graph: every metric filter names a declared event, every
 * derived-metric input and every widget names a declared metric, and the
 * derived-metric reference graph is acyclic.
 */
export function assertDataGraph(value: unknown): asserts value is DataGraph {
	assertGraph(isObject(value), "dashboard must be an object");
	assertExactKeys(value, ["events", "metrics", "dashboards"], "dashboard");
	assertGraph(Array.isArray(value.events), "dashboard events must be an array");
	assertGraph(
		Array.isArray(value.metrics),
		"dashboard metrics must be an array",
	);
	assertGraph(
		Array.isArray(value.dashboards),
		"dashboard dashboards must be an array",
	);

	for (const event of value.events) validateEvent(event);
	const eventNameList = (value.events as EventDeclaration[]).map(
		(event) => event.name,
	);
	assertUnique(eventNameList, "event name");
	const eventNames = new Set(eventNameList);

	for (const metric of value.metrics) validateMetric(metric);
	const metricIdList = (value.metrics as Metric[]).map((metric) => metric.id);
	assertUnique(metricIdList, "metric id");
	const metricIds = new Set(metricIdList);

	for (const metric of value.metrics as Metric[]) {
		if (metric.kind === "source") {
			assertGraph(
				eventNames.has(metric.filter.event),
				`metric '${metric.id}' filters unknown event '${metric.filter.event}'`,
			);
		} else {
			for (const input of metric.inputs) {
				assertGraph(
					metricIds.has(input),
					`metric '${metric.id}' derives from unknown metric '${input}'`,
				);
			}
		}
	}
	assertAcyclic(value.metrics as Metric[]);

	for (const dashboard of value.dashboards) validateDashboard(dashboard);
	assertUnique(
		(value.dashboards as Dashboard[]).map((dashboard) => dashboard.id),
		"dashboard id",
	);
	for (const dashboard of value.dashboards as Dashboard[]) {
		for (const widget of dashboard.widgets) {
			assertGraph(
				metricIds.has(widget.metric),
				`dashboard '${dashboard.id}' widget '${widget.id}' binds unknown metric '${widget.metric}'`,
			);
		}
	}
}
