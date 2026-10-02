import type {
	DataGraph,
	DerivedMetric,
	Metric,
	MetricAggregation,
	MetricBucket,
	MetricGroupBy,
	SourceMetric,
} from "../schema.js";
import {
	compileMetric,
	type EventTypeResolver,
	METRIC_VALUE_ALIAS,
} from "./compile.js";
import type {
	AuditAggregateClient,
	MetricContext,
	MetricPoint,
	MetricSeries,
} from "./types.js";

/** Refuse scoped results from servers that silently ignore new request fields. */
export function assertAuditScopeContract(
	request: {
		resourceId?: string;
		collectionId?: string;
		payloadContains?: unknown;
	},
	response: { scopeContractVersion?: number },
): void {
	if (
		(request.resourceId ||
			request.collectionId ||
			(request.payloadContains &&
				Object.keys(request.payloadContains).length > 0)) &&
		(response.scopeContractVersion ?? 0) < 1
	) {
		throw new Error(
			"Audit server does not acknowledge scope contract version 1",
		);
	}
}

function toSeries(
	metricId: string,
	points: MetricPoint[],
	groupBy: MetricGroupBy,
	bucket: MetricBucket | undefined,
	additive = true,
	partial = false,
): MetricSeries {
	return {
		metricId,
		points,
		// An additive total (count, sum) is the sum of what was actually observed,
		// so partiality annotates it via `coverage` rather than withholding it:
		// optional payload fields such as `result_count` are absent by design, and
		// suppressing on partial blanked the stat tile of every metric over one.
		// A non-additive scalar (avg, min, max, percentile, ratio) is only defined
		// for a single complete group — if any group was dropped, the surviving
		// one is not the series' value, so it stays null.
		total:
			points.length === 0
				? null
				: additive
					? points.reduce((sum, point) => sum + point.value, 0)
					: partial || points.length !== 1
						? null
						: points[0].value,
		coverage: partial ? "partial" : points.length === 0 ? "empty" : "complete",
		groupBy,
		bucket,
	};
}

// The aggregations whose value over no events is 0. An avg, min, max or
// percentile has no value without events, so a bucket with none stays out.
const ZERO_OVER_NO_EVENTS: readonly MetricAggregation[] = [
	"count",
	"count_distinct",
	"sum",
];

// A time bucket's key: the date the server truncated to, then whatever follows
// it ("T00:00:00+00"), which a filled bucket copies so its key reads the same.
const TIME_KEY = /^(\d{4})-(\d{2})-(\d{2})(.*)$/;

// A filled series stops growing here: years of days is a few thousand
// buckets, and two keys far apart must not balloon one series.
const MAX_FILLED_BUCKETS = 100_000;

// The zeros put on buckets the RPC returned none for: no event matched there.
// A derived metric tells them apart from a returned bucket by this mark.
const noEvents = new WeakSet<MetricPoint>();

function bucketDate(key: string): { date: Date; rest: string } | null {
	const match = TIME_KEY.exec(key);
	if (!match) return null;
	const [, year, month, day, rest] = match;
	const date = new Date(Date.UTC(Number(year), Number(month) - 1, Number(day)));
	// Date.UTC rolls an impossible date over (Feb 30 is Mar 2), so a date that
	// does not read back the same is not one.
	return Number.isNaN(date.getTime()) ||
		date.toISOString().slice(0, 10) !== `${year}-${month}-${day}`
		? null
		: { date, rest };
}

function nextBucket(date: Date, bucket: MetricBucket): Date {
	const year = date.getUTCFullYear();
	const month = date.getUTCMonth();
	const day = date.getUTCDate();
	switch (bucket) {
		case "week":
			return new Date(Date.UTC(year, month, day + 7));
		case "month":
			return new Date(Date.UTC(year, month + 1, day));
		default:
			return new Date(Date.UTC(year, month, day + 1));
	}
}

/**
 * The audit RPC returns no bucket for a time bucket with no events, and a
 * chart spaces its points evenly, so a day with none vanished and the line ran
 * straight over it. This puts a 0 at every bucket between the first and the
 * last one the RPC returned that it did not return. A bucket it did return,
 * whose value was dropped as unknown, stays out: that is not a bucket with no
 * events. A key that does not read as a date, or one off the bucket's step
 * from the first, leaves the points as they are.
 */
export function fillEmptyBuckets(
	points: readonly MetricPoint[],
	returnedKeys: readonly string[],
	bucket: MetricBucket,
): MetricPoint[] {
	const unchanged = [...points];
	const returned = returnedKeys.map(bucketDate);
	if (returned.length === 0 || returned.includes(null)) return unchanged;
	const parsed = returned as { date: Date; rest: string }[];
	const day = (date: Date) => date.toISOString().slice(0, 10);
	const seen = new Set(parsed.map(({ date }) => day(date)));
	const times = parsed.map(({ date }) => date.getTime());
	const first = parsed[times.indexOf(Math.min(...times))];
	const last = Math.max(...times);

	const grid: string[] = [];
	for (let at = first.date; at.getTime() <= last; at = nextBucket(at, bucket)) {
		if (grid.length === MAX_FILLED_BUCKETS) return unchanged;
		grid.push(day(at));
	}
	const onGrid = new Set(grid);
	if ([...seen].some((date) => !onGrid.has(date))) return unchanged;

	const valueAt = new Map<string, MetricPoint>();
	for (const point of points) {
		const at = bucketDate(point.key);
		if (!at) return unchanged;
		valueAt.set(day(at.date), point);
	}
	if (valueAt.size !== points.length) return unchanged;

	return grid.flatMap((date): MetricPoint[] => {
		const point = valueAt.get(date);
		if (point) return [point];
		if (seen.has(date)) return [];
		const zero = { key: `${date}${first.rest}`, value: 0 };
		noEvents.add(zero);
		return [zero];
	});
}

/** Resolve a single source metric against the audit RPC. */
export async function runMetric(
	client: AuditAggregateClient,
	metric: SourceMetric,
	resolveEventType: EventTypeResolver,
	context: MetricContext,
): Promise<MetricSeries> {
	const request = compileMetric(metric, resolveEventType, context);
	const response = await client.aggregateAuditLog(request);
	assertAuditScopeContract(request, response);
	// A plain count reads the bucket's own COUNT(*); every other op is computed
	// under METRIC_VALUE_ALIAS in the bucket's metrics map. The RPC omits that
	// alias for a group whose aggregate is undefined (min/avg/max/percentile over
	// no numeric values) — absence means "no data", not zero — so such a bucket
	// is dropped rather than plotted as a phantom zero.
	const readValue =
		metric.aggregation === "count"
			? (bucket: (typeof response.buckets)[number]) => Number(bucket.count)
			: (bucket: (typeof response.buckets)[number]) =>
					bucket.metrics[METRIC_VALUE_ALIAS];
	let partial = false;
	const points: MetricPoint[] = [];
	for (const bucket of response.buckets) {
		const value = readValue(bucket);
		if (value === undefined || !Number.isFinite(value)) {
			partial = true;
			continue;
		}
		if (
			metric.aggregation !== "count" &&
			(bucket.samples[METRIC_VALUE_ALIAS] === undefined ||
				bucket.samples[METRIC_VALUE_ALIAS] < bucket.count)
		)
			partial = true;
		points.push({ key: bucket.key, value });
	}
	return toSeries(
		metric.id,
		metric.groupBy === "time" &&
			ZERO_OVER_NO_EVENTS.includes(metric.aggregation)
			? fillEmptyBuckets(
					points,
					response.buckets.map((bucket) => bucket.key),
					metric.bucket ?? "day",
				)
			: points,
		metric.groupBy,
		metric.bucket,
		metric.aggregation === "count" || metric.aggregation === "sum",
		partial,
	);
}

// Combine the resolved series of a derived metric's inputs. Inputs are aligned
// on the union of their point keys (first-seen order). Missing operands
// remain unknown and are never substituted with zero. A key no input has an
// event on (each operand there is a filled zero or missing) is not missing
// telemetry: before the zeros, no input returned it. Its zeros still sum, to a
// filled zero in turn, and a ratio of them has no value. Combining series
// grouped by different dimensions is meaningless — the keyspaces don't line
// up — so it is rejected rather than silently producing a series of stray
// values.
function combineDerived(
	metric: DerivedMetric,
	inputs: MetricSeries[],
	additive: boolean,
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
	const pointAt = (input: MetricSeries, key: string): MetricPoint | undefined =>
		input.points.find((point) => point.key === key);
	let partial = inputs.some((input) => input.coverage === "partial");
	const points: MetricPoint[] = [];
	for (const key of keys) {
		const operands = inputs.map((input) => pointAt(input, key));
		const empty = operands.every(
			(point) => point === undefined || noEvents.has(point),
		);
		const values = operands.map((point) => point?.value);
		if (values.some((value) => value === undefined)) {
			if (!empty) partial = true;
			continue;
		}
		const present = values as number[];
		let value: number;
		switch (metric.operation) {
			case "sum":
				value = present.reduce((sum, v) => sum + v, 0);
				break;
			case "difference":
				value = present[0] - present[1];
				break;
			case "ratio":
				if (present[1] === 0) {
					if (!empty) partial = true;
					continue;
				}
				value = present[0] / present[1];
				break;
			default:
				throw new Error(
					`derived metric '${metric.id}' has unsupported operation '${metric.operation}'`,
				);
		}
		if (!Number.isFinite(value)) {
			partial = true;
			continue;
		}
		const point = { key, value };
		if (empty) noEvents.add(point);
		points.push(point);
	}

	return toSeries(
		metric.id,
		points,
		dimension.groupBy,
		dimension.bucket,
		additive,
		partial,
	);
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
	const additive = new Map<string, boolean>();
	const isAdditive = (id: string): boolean => {
		const cached = additive.get(id);
		if (cached !== undefined) return cached;
		const metric = byId.get(id);
		const result =
			metric !== undefined &&
			(metric.kind === "source"
				? metric.aggregation === "count" || metric.aggregation === "sum"
				: metric.operation !== "ratio" && metric.inputs.every(isAdditive));
		additive.set(id, result);
		return result;
	};
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
						combineDerived(metric, inputs, isAdditive(metric.id)),
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
