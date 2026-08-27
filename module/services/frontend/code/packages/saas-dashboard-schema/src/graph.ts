import { compileMetric, type MetricFetcher, resolveMetric } from "./compile.js";
import type {
	AggregateQuery,
	Dashboard,
	DataGraphInput,
	EventDeclaration,
	Metric,
	MetricPoint,
	SourceMetric,
} from "./types.js";

/** Thrown when a data graph declaration is internally inconsistent. */
export class DataGraphError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "DataGraphError";
	}
}

/**
 * A validated data graph: the immutable spine a dashboard reads from. Built by
 * {@link defineDataGraph}, which rejects any inconsistency (dangling
 * references, duplicate ids, cycles, malformed time buckets) up front so
 * consumers never resolve against a graph that can't hold.
 */
export interface DataGraph {
	readonly events: readonly EventDeclaration[];
	readonly metrics: readonly Metric[];
	readonly dashboards: readonly Dashboard[];
	/** Look up a metric by id; throws if absent. */
	metric(id: string): Metric;
	/** Compile a source metric to its audit query; throws for derived metrics. */
	compile(id: string): AggregateQuery;
	/** Resolve a metric to its series, running source queries through `fetcher`. */
	resolve(id: string, fetcher: MetricFetcher): Promise<MetricPoint[]>;
	/** Metric ids in dependency order (every upstream precedes its dependents). */
	order(): string[];
}

function requireUnique(ids: string[], what: string): void {
	const seen = new Set<string>();
	for (const id of ids) {
		if (seen.has(id)) {
			throw new DataGraphError(`duplicate ${what} id '${id}'`);
		}
		seen.add(id);
	}
}

function validateSource(
	metric: SourceMetric,
	events: Map<string, EventDeclaration>,
	categories: Set<string>,
): void {
	if (metric.groupBy === "time" && metric.bucket === undefined) {
		throw new DataGraphError(
			`source metric '${metric.id}' groups by time but declares no bucket`,
		);
	}
	if (metric.groupBy !== "time" && metric.bucket !== undefined) {
		throw new DataGraphError(
			`source metric '${metric.id}' declares a bucket but does not group by time`,
		);
	}
	const event = metric.filter?.event;
	if (event !== undefined && !events.has(event)) {
		throw new DataGraphError(
			`source metric '${metric.id}' filters on undeclared event '${event}'`,
		);
	}
	const category = metric.filter?.category;
	if (category !== undefined && !categories.has(category)) {
		throw new DataGraphError(
			`source metric '${metric.id}' filters on undeclared category '${category}'`,
		);
	}
}

/**
 * Depth-first topological order over the derived-metric dependency edges,
 * detecting cycles in the same pass. Source metrics are leaves.
 */
function topoOrder(metrics: Map<string, Metric>): string[] {
	const order: string[] = [];
	const done = new Set<string>();
	const onPath = new Set<string>();

	const visit = (id: string): void => {
		if (done.has(id)) return;
		if (onPath.has(id)) {
			throw new DataGraphError(`metric dependency cycle through '${id}'`);
		}
		onPath.add(id);
		const metric = metrics.get(id);
		if (metric?.kind === "derived") {
			for (const dep of metric.from) visit(dep);
		}
		onPath.delete(id);
		done.add(id);
		order.push(id);
	};

	for (const id of metrics.keys()) visit(id);
	return order;
}

/**
 * Validate a declaration and return an immutable {@link DataGraph}. Every
 * failure mode is a `DataGraphError` with a message naming the offending id, so
 * a mis-declared dashboard fails at authoring time rather than at render.
 */
export function defineDataGraph(input: DataGraphInput): DataGraph {
	requireUnique(
		input.events.map((e) => e.name),
		"event",
	);
	requireUnique(
		input.metrics.map((m) => m.id),
		"metric",
	);

	const events = new Map(input.events.map((e) => [e.name, e]));
	const categories = new Set(
		input.events
			.map((e) => e.category)
			.filter((c): c is string => c !== undefined),
	);
	const metrics = new Map(input.metrics.map((m) => [m.id, m]));

	for (const metric of input.metrics) {
		if (metric.kind === "source") {
			validateSource(metric, events, categories);
			continue;
		}
		if (typeof metric.compute !== "function") {
			throw new DataGraphError(
				`derived metric '${metric.id}' has no compute function`,
			);
		}
		if (metric.from.length === 0) {
			throw new DataGraphError(
				`derived metric '${metric.id}' has no upstream metrics`,
			);
		}
		for (const dep of metric.from) {
			if (dep === metric.id) {
				throw new DataGraphError(
					`derived metric '${metric.id}' depends on itself`,
				);
			}
			if (!metrics.has(dep)) {
				throw new DataGraphError(
					`derived metric '${metric.id}' depends on undeclared metric '${dep}'`,
				);
			}
		}
	}

	const order = topoOrder(metrics);

	const dashboards = input.dashboards ?? [];
	requireUnique(
		dashboards.map((d) => d.id),
		"dashboard",
	);
	for (const dashboard of dashboards) {
		requireUnique(
			dashboard.widgets.map((w) => w.id),
			`widget in dashboard '${dashboard.id}'`,
		);
		for (const widget of dashboard.widgets) {
			if (!metrics.has(widget.metric)) {
				throw new DataGraphError(
					`widget '${widget.id}' in dashboard '${dashboard.id}' binds undeclared metric '${widget.metric}'`,
				);
			}
		}
	}

	const metric = (id: string): Metric => {
		const found = metrics.get(id);
		if (!found) throw new DataGraphError(`unknown metric '${id}'`);
		return found;
	};

	return Object.freeze({
		events: Object.freeze(input.events.slice()),
		metrics: Object.freeze(input.metrics.slice()),
		dashboards: Object.freeze(dashboards.slice()),
		metric,
		compile(id: string): AggregateQuery {
			const found = metric(id);
			if (found.kind !== "source") {
				throw new DataGraphError(
					`metric '${id}' is derived and has no audit query`,
				);
			}
			return compileMetric(found);
		},
		resolve(id: string, fetcher: MetricFetcher): Promise<MetricPoint[]> {
			return resolveMetric(metric(id), metric, fetcher);
		},
		order: () => order.slice(),
	});
}
