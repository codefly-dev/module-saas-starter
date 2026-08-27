import type {
	DataGraph,
	DerivedMetric,
	Metric,
	MetricBucket,
	MetricGroupBy,
	SourceMetric,
} from "../schema.js";
import { compileMetric, type EventTypeResolver } from "./compile.js";
import type {
	AuditAggregateClient,
	MetricContext,
	MetricPoint,
	MetricSeries,
} from "./types.js";

function toSeries(
	metricId: string,
	points: MetricPoint[],
	groupBy: MetricGroupBy,
	bucket: MetricBucket | undefined,
): MetricSeries {
	return {
		metricId,
		points,
		total: points.reduce((sum, point) => sum + point.value, 0),
		groupBy,
		bucket,
	};
}

/** Resolve a single source metric against the audit RPC. */
export async function runMetric(
	client: AuditAggregateClient,
	metric: SourceMetric,
	resolveEventType: EventTypeResolver,
	context: MetricContext,
): Promise<MetricSeries> {
	const response = await client.aggregateAuditLog(
		compileMetric(metric, resolveEventType, context),
	);
	return toSeries(
		metric.id,
		response.buckets.map((bucket) => ({
			key: bucket.key,
			value: Number(bucket.count),
		})),
		metric.groupBy,
		metric.bucket,
	);
}

// Combine the resolved series of a derived metric's inputs. Inputs are aligned
// on the union of their point keys (first-seen order), a key missing from an
// input counting as 0. Combining series grouped by different dimensions is
// meaningless — the keyspaces don't line up — so it is rejected rather than
// silently producing a series of stray values.
function combineDerived(
	metric: DerivedMetric,
	inputs: MetricSeries[],
): MetricSeries {
	if (metric.operation === "sum") {
		if (inputs.length < 2) {
			throw new Error(
				`derived metric '${metric.id}' operation 'sum' needs at least two inputs`,
			);
		}
	} else if (inputs.length !== 2) {
		throw new Error(
			`derived metric '${metric.id}' operation '${metric.operation}' needs exactly two inputs`,
		);
	}

	const dimension = inputs[0];
	for (const input of inputs) {
		if (
			input.groupBy !== dimension.groupBy ||
			input.bucket !== dimension.bucket
		) {
			throw new Error(
				`derived metric '${metric.id}' combines metrics with different group-by dimensions`,
			);
		}
	}

	const keys: string[] = [];
	const seen = new Set<string>();
	for (const input of inputs) {
		for (const point of input.points) {
			if (!seen.has(point.key)) {
				seen.add(point.key);
				keys.push(point.key);
			}
		}
	}
	const valueAt = (input: MetricSeries, key: string): number =>
		input.points.find((point) => point.key === key)?.value ?? 0;

	let points: MetricPoint[];
	switch (metric.operation) {
		case "sum":
			points = keys.map((key) => ({
				key,
				value: inputs.reduce((sum, input) => sum + valueAt(input, key), 0),
			}));
			break;
		case "difference":
			points = keys.map((key) => ({
				key,
				value: valueAt(inputs[0], key) - valueAt(inputs[1], key),
			}));
			break;
		case "ratio":
			points = keys.map((key) => {
				const denominator = valueAt(inputs[1], key);
				return {
					key,
					value: denominator === 0 ? 0 : valueAt(inputs[0], key) / denominator,
				};
			});
			break;
		default:
			throw new Error(
				`derived metric '${metric.id}' has unsupported operation '${metric.operation}'`,
			);
	}
	return toSeries(metric.id, points, dimension.groupBy, dimension.bucket);
}

function indexMetrics(metrics: readonly Metric[]): Map<string, Metric> {
	const byId = new Map<string, Metric>();
	for (const metric of metrics) {
		if (byId.has(metric.id)) {
			throw new Error(`duplicate metric id: ${metric.id}`);
		}
		byId.set(metric.id, metric);
	}
	return byId;
}

// Depth-first walk from the roots collecting every metric that must resolve
// (roots plus the transitive derived inputs they reach), detecting derivation
// cycles up front. Detecting cycles synchronously here is what lets the async
// resolver below memoize promises without a cycle deadlocking on itself. An
// input naming no declared metric is left for the resolver to report at run
// time, so it is skipped rather than treated as a leaf error here.
function reachableMetrics(
	byId: Map<string, Metric>,
	roots: Iterable<string>,
): Set<string> {
	const done = new Set<string>();
	const onStack = new Set<string>();
	const visit = (id: string): void => {
		if (done.has(id)) return;
		const metric = byId.get(id);
		if (!metric) return;
		if (onStack.has(id)) {
			throw new Error(`metric dependency cycle through: ${id}`);
		}
		onStack.add(id);
		if (metric.kind === "derived") {
			for (const input of metric.inputs) visit(input);
		}
		onStack.delete(id);
		done.add(id);
	};
	for (const id of roots) visit(id);
	return done;
}

/**
 * Resolve the metrics reachable from `roots` (the roots plus their transitive
 * derived inputs) into one series each. Source metrics run against the audit RPC
 * concurrently; each metric is computed at most once and shared by every
 * dependent. Unrelated metrics elsewhere in the graph are never fetched.
 */
export async function resolveMetrics(
	client: AuditAggregateClient,
	graph: DataGraph,
	context: MetricContext,
	roots: Iterable<string>,
): Promise<Record<string, MetricSeries>> {
	const byId = indexMetrics(graph.metrics);
	const rootIds = [...roots];
	const reachable = reachableMetrics(byId, rootIds);

	const eventTypes = new Map(
		graph.events.map((event) => [event.name, event.type]),
	);
	const resolveEventType: EventTypeResolver = (name) => {
		const type = eventTypes.get(name);
		if (type === undefined) {
			throw new Error(`metric filters unknown event: ${name}`);
		}
		return type;
	};

	// Cache one promise per metric id so a shared metric runs once and
	// independent source metrics fetch in parallel. `reachableMetrics` proved the
	// graph acyclic, so a metric's promise is cached before its inputs are
	// awaited and no promise can ever await itself.
	const pending = new Map<string, Promise<MetricSeries>>();
	const resolve = (id: string): Promise<MetricSeries> => {
		const cached = pending.get(id);
		if (cached) return cached;
		const metric = byId.get(id);
		if (!metric) {
			throw new Error(`metric references unknown input: ${id}`);
		}
		const series =
			metric.kind === "source"
				? runMetric(client, metric, resolveEventType, context)
				: Promise.all(metric.inputs.map(resolve)).then((inputs) =>
						combineDerived(metric, inputs),
					);
		pending.set(id, series);
		return series;
	};

	const entries = await Promise.all(
		[...reachable].map(async (id) => [id, await resolve(id)] as const),
	);
	return Object.fromEntries(entries);
}

/**
 * Resolve a data graph into one series per metric: source metrics run against
 * the audit RPC (concurrently), derived metrics combine their inputs once those
 * are known. A metric that references an undeclared event or input, a derivation
 * cycle, or a duplicate metric id throws — the graph's own referential-integrity
 * validation lives with its declaration.
 */
export function runDataGraph(
	client: AuditAggregateClient,
	graph: DataGraph,
	context: MetricContext,
): Promise<Record<string, MetricSeries>> {
	return resolveMetrics(
		client,
		graph,
		context,
		graph.metrics.map((metric) => metric.id),
	);
}
