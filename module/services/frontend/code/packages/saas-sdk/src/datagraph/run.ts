import type {
	DataGraph,
	DerivedMetric,
	Metric,
	MetricOperation,
	SourceMetric,
} from "../schema.js";
import { compileMetric, type EventTypeResolver } from "./compile.js";
import type {
	AuditAggregateClient,
	MetricContext,
	MetricPoint,
	MetricSeries,
} from "./types.js";

function toSeries(metricId: string, points: MetricPoint[]): MetricSeries {
	return {
		metricId,
		points,
		total: points.reduce((sum, point) => sum + point.value, 0),
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
	);
}

// Align inputs on the union of their point keys (first-seen order) so an
// operation combines like keys and treats a key missing from one input as 0.
function combine(metric: DerivedMetric, inputs: MetricSeries[]): MetricPoint[] {
	const requireBinary = (operation: MetricOperation) => {
		if (inputs.length !== 2) {
			throw new Error(
				`derived metric '${metric.id}' operation '${operation}' needs exactly two inputs`,
			);
		}
	};

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

	switch (metric.operation) {
		case "sum":
			return keys.map((key) => ({
				key,
				value: inputs.reduce((sum, input) => sum + valueAt(input, key), 0),
			}));
		case "difference":
			requireBinary("difference");
			return keys.map((key) => ({
				key,
				value: valueAt(inputs[0], key) - valueAt(inputs[1], key),
			}));
		case "ratio": {
			requireBinary("ratio");
			return keys.map((key) => {
				const denominator = valueAt(inputs[1], key);
				return {
					key,
					value: denominator === 0 ? 0 : valueAt(inputs[0], key) / denominator,
				};
			});
		}
	}
}

/**
 * Resolve a data graph into one series per metric: source metrics run against
 * the audit RPC (concurrently), derived metrics combine their inputs once those
 * are known. A metric that references an undeclared event or input throws — the
 * graph's own referential-integrity validation lives with its declaration.
 */
export async function runDataGraph(
	client: AuditAggregateClient,
	graph: DataGraph,
	context: MetricContext,
): Promise<Record<string, MetricSeries>> {
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

	const byId = new Map<string, Metric>(graph.metrics.map((m) => [m.id, m]));
	const resolved: Record<string, MetricSeries> = {};
	const inProgress = new Set<string>();

	const resolve = async (id: string): Promise<MetricSeries> => {
		const existing = resolved[id];
		if (existing) return existing;

		const metric = byId.get(id);
		if (!metric) {
			throw new Error(`metric references unknown input: ${id}`);
		}
		if (inProgress.has(id)) {
			throw new Error(`metric dependency cycle through: ${id}`);
		}
		inProgress.add(id);

		let series: MetricSeries;
		if (metric.kind === "source") {
			series = await runMetric(client, metric, resolveEventType, context);
		} else {
			const inputs: MetricSeries[] = [];
			for (const dep of metric.inputs) inputs.push(await resolve(dep));
			series = toSeries(metric.id, combine(metric, inputs));
		}

		inProgress.delete(id);
		resolved[id] = series;
		return series;
	};

	for (const metric of graph.metrics) await resolve(metric.id);
	return resolved;
}
