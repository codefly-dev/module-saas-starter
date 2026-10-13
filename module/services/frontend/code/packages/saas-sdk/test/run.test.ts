import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AggregateAuditLogResponseSchema } from "../generated/typescript/src/gen/saas/accounts/v1/audit_pb.js";
import {
	fillEmptyBuckets,
	runDataGraph,
	runMetric,
} from "../src/datagraph/run.js";
import type {
	DataGraph,
	MetricOperation,
	SourceMetric,
} from "../src/schema.js";
import { fakeAuditClient, gatedAuditClient } from "./support.js";

const context = { orgId: "org_1" };

describe("runMetric", () => {
	it("shapes buckets into a series and sums the total", async () => {
		const { client, calls } = fakeAuditClient(() => [
			{ key: "user.signed_in.v1", count: 3 },
			{ key: "user.signed_out.v1", count: 2 },
		]);
		const metric: SourceMetric = {
			id: "events_by_type",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "event_type",
			aggregation: "count",
		};

		const series = await runMetric(
			client,
			metric,
			(n) => `user.${n}.v1`,
			context,
		);

		expect(series).toEqual({
			metricId: "events_by_type",
			points: [
				{ key: "user.signed_in.v1", value: 3 },
				{ key: "user.signed_out.v1", value: 2 },
			],
			total: 5,
			coverage: "complete",
			groupBy: "event_type",
			bucket: undefined,
		});
		expect(calls[0]?.eventType).toBe("user.signed_in.v1");
	});

	it("reads a non-count metric's value from the bucket metrics map", async () => {
		const { client, calls } = fakeAuditClient(() => [
			{ key: "2026-01-01", count: 128, metrics: { value: 4.2 } },
		]);
		const metric: SourceMetric = {
			id: "p95_latency",
			kind: "source",
			filter: { event: "request_served" },
			groupBy: "time",
			bucket: "day",
			aggregation: "percentile",
			field: "payload:duration_ms",
			percentile: 0.95,
		};

		const series = await runMetric(client, metric, (n) => n, context);

		// The plotted value is the aliased percentile, not the group's row count.
		expect(series.points).toEqual([{ key: "2026-01-01", value: 4.2 }]);
		expect(calls[0]?.metrics).toEqual([
			{
				op: "percentile",
				field: "payload:duration_ms",
				percentile: 0.95,
				alias: "value",
			},
		]);
	});

	it("drops a bucket whose aggregate the RPC left undefined, not plots it as zero", async () => {
		const { client } = fakeAuditClient(() => [
			{ key: "2026-01-01", count: 5, metrics: { value: 4.2 } },
			// Group has events (count>0) but the percentile is undefined — the RPC
			// omits the alias. "No data", not zero.
			{ key: "2026-01-02", count: 3 },
		]);
		const metric: SourceMetric = {
			id: "p95_latency",
			kind: "source",
			filter: { event: "request_served" },
			groupBy: "time",
			bucket: "day",
			aggregation: "percentile",
			field: "payload:duration_ms",
			percentile: 0.95,
		};

		const series = await runMetric(client, metric, (n) => n, context);

		expect(series.points).toEqual([{ key: "2026-01-01", value: 4.2 }]);
		expect(series.total).toBeNull();
		expect(series.coverage).toBe("partial");
	});

	it("carries the metric's dimension onto the series", async () => {
		const { client } = fakeAuditClient(() => [{ key: "2026-01-01", count: 1 }]);
		const metric: SourceMetric = {
			id: "over_time",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "time",
			bucket: "week",
			aggregation: "count",
		};

		const series = await runMetric(client, metric, (n) => n, context);

		expect(series.groupBy).toBe("time");
		expect(series.bucket).toBe("week");
	});
});

describe("runDataGraph", () => {
	function graphWith(...metrics: DataGraph["metrics"]): DataGraph {
		return {
			events: [{ name: "signed_in", type: "user.signed_in.v1" }],
			metrics,
			dashboards: [],
		};
	}

	it("resolves a ratio derived from two source metrics", async () => {
		const { client } = fakeAuditClient((request) =>
			request.actorId === "verified"
				? [{ key: "all", count: 8 }]
				: [{ key: "all", count: 10 }],
		);
		const graph = graphWith(
			{
				id: "logins",
				kind: "source",
				filter: { event: "signed_in" },
				groupBy: "event_type",
				aggregation: "count",
			},
			{
				id: "verified_logins",
				kind: "source",
				filter: { event: "signed_in", actor: "verified" },
				groupBy: "event_type",
				aggregation: "count",
			},
			{
				id: "verified_rate",
				kind: "derived",
				operation: "ratio",
				inputs: ["verified_logins", "logins"],
			},
		);

		const resolved = await runDataGraph(client, graph, context);

		expect(resolved.verified_rate.points).toEqual([{ key: "all", value: 0.8 }]);
	});

	it("preserves missing operands in sums and differences", async () => {
		const { client } = fakeAuditClient((request) =>
			request.actorId === "a"
				? [
						{ key: "mon", count: 1 },
						{ key: "tue", count: 4 },
					]
				: [{ key: "tue", count: 3 }],
		);
		const graph = graphWith(
			{
				id: "a",
				kind: "source",
				filter: { event: "signed_in", actor: "a" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
			{
				id: "b",
				kind: "source",
				filter: { event: "signed_in", actor: "b" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
			{ id: "total", kind: "derived", operation: "sum", inputs: ["a", "b"] },
			{
				id: "gap",
				kind: "derived",
				operation: "difference",
				inputs: ["a", "b"],
			},
		);

		const resolved = await runDataGraph(client, graph, context);

		expect(resolved.total.points).toEqual([{ key: "tue", value: 7 }]);
		expect(resolved.gap.points).toEqual([{ key: "tue", value: 1 }]);
	});

	it("resolves derived metrics declared before their inputs", async () => {
		const { client } = fakeAuditClient(() => [{ key: "all", count: 4 }]);
		const graph = graphWith(
			{ id: "total", kind: "derived", operation: "sum", inputs: ["x", "y"] },
			{
				id: "x",
				kind: "source",
				filter: { event: "signed_in", actor: "x" },
				groupBy: "event_type",
				aggregation: "count",
			},
			{
				id: "y",
				kind: "source",
				filter: { event: "signed_in", actor: "y" },
				groupBy: "event_type",
				aggregation: "count",
			},
		);

		const resolved = await runDataGraph(client, graph, context);

		expect(resolved.total.points).toEqual([{ key: "all", value: 8 }]);
	});

	it("fails a whole graph closed when no viewer org is bound, issuing no query", async () => {
		const { client, calls } = fakeAuditClient(() => [{ key: "all", count: 1 }]);
		const graph = graphWith(
			{
				id: "logins",
				kind: "source",
				filter: { event: "signed_in", actor: "a" },
				groupBy: "event_type",
				aggregation: "count",
			},
			{
				id: "signups",
				kind: "source",
				filter: { event: "signed_in", actor: "b" },
				groupBy: "event_type",
				aggregation: "count",
			},
		);

		// A spec that would otherwise reach the audit trail is inert without a
		// bound org: every source metric rejects before its request is sent — no
		// sibling metric fires either — so it can never widen past the viewer's org
		// into an org-wide read.
		await expect(runDataGraph(client, graph, { orgId: "" })).rejects.toThrow(
			/without a viewer org/,
		);
		expect(calls).toHaveLength(0);
	});

	it("rejects a metric filtering an undeclared event", async () => {
		const { client } = fakeAuditClient(() => []);
		const graph: DataGraph = {
			events: [],
			metrics: [
				{
					id: "m",
					kind: "source",
					filter: { event: "ghost" },
					groupBy: "event_type",
					aggregation: "count",
				},
			],
			dashboards: [],
		};

		await expect(runDataGraph(client, graph, context)).rejects.toThrow(
			/unknown event: ghost/,
		);
	});

	it("rejects a dangling derived input", async () => {
		const { client } = fakeAuditClient(() => []);
		const graph = graphWith({
			id: "orphan",
			kind: "derived",
			operation: "sum",
			inputs: ["missing", "phantom"],
		});

		await expect(runDataGraph(client, graph, context)).rejects.toThrow(
			/unknown input: missing/,
		);
	});

	it("rejects a derivation cycle", async () => {
		const { client } = fakeAuditClient(() => []);
		const graph = graphWith(
			{ id: "a", kind: "derived", operation: "sum", inputs: ["b", "b"] },
			{ id: "b", kind: "derived", operation: "sum", inputs: ["a", "a"] },
		);

		await expect(runDataGraph(client, graph, context)).rejects.toThrow(/cycle/);
	});

	it("rejects duplicate metric ids", async () => {
		const { client } = fakeAuditClient(() => []);
		const graph = graphWith(
			{
				id: "dup",
				kind: "source",
				filter: { event: "signed_in" },
				groupBy: "event_type",
				aggregation: "count",
			},
			{
				id: "dup",
				kind: "source",
				filter: { event: "signed_in" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
		);

		await expect(runDataGraph(client, graph, context)).rejects.toThrow(
			/duplicate metric id: dup/,
		);
	});

	it("rejects a sum with fewer than two inputs", async () => {
		const { client } = fakeAuditClient(() => [{ key: "all", count: 1 }]);
		const graph = graphWith(
			{
				id: "base",
				kind: "source",
				filter: { event: "signed_in" },
				groupBy: "event_type",
				aggregation: "count",
			},
			{ id: "solo", kind: "derived", operation: "sum", inputs: ["base"] },
		);

		await expect(runDataGraph(client, graph, context)).rejects.toThrow(
			/'sum' needs at least two inputs/,
		);
	});

	it("rejects a derived metric combining different group-by dimensions", async () => {
		const { client } = fakeAuditClient((request) =>
			request.groupBy === "time"
				? [{ key: "2026-01-01", count: 1 }]
				: [{ key: "user.signed_in.v1", count: 2 }],
		);
		const graph = graphWith(
			{
				id: "by_time",
				kind: "source",
				filter: { event: "signed_in" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
			{
				id: "by_type",
				kind: "source",
				filter: { event: "signed_in" },
				groupBy: "event_type",
				aggregation: "count",
			},
			{
				id: "mix",
				kind: "derived",
				operation: "difference",
				inputs: ["by_time", "by_type"],
			},
		);

		await expect(runDataGraph(client, graph, context)).rejects.toThrow(
			/different group-by dimensions/,
		);
	});

	it("rejects an unsupported derived operation", async () => {
		const { client } = fakeAuditClient(() => [{ key: "all", count: 1 }]);
		const graph = graphWith(
			{
				id: "a",
				kind: "source",
				filter: { event: "signed_in", actor: "a" },
				groupBy: "event_type",
				aggregation: "count",
			},
			{
				id: "b",
				kind: "source",
				filter: { event: "signed_in", actor: "b" },
				groupBy: "event_type",
				aggregation: "count",
			},
			// An operation outside the typed union — e.g. a graph parsed from
			// untrusted YAML — must fail loudly, not return a broken series.
			{
				id: "bad",
				kind: "derived",
				operation: "avg" as MetricOperation,
				inputs: ["a", "b"],
			},
		);

		await expect(runDataGraph(client, graph, context)).rejects.toThrow(
			/unsupported operation 'avg'/,
		);
	});

	it("fetches independent source metrics concurrently", async () => {
		const gate = gatedAuditClient(() => [{ key: "all", count: 1 }]);
		const graph = graphWith(
			{
				id: "m1",
				kind: "source",
				filter: { event: "signed_in", actor: "1" },
				groupBy: "event_type",
				aggregation: "count",
			},
			{
				id: "m2",
				kind: "source",
				filter: { event: "signed_in", actor: "2" },
				groupBy: "event_type",
				aggregation: "count",
			},
			{
				id: "m3",
				kind: "source",
				filter: { event: "signed_in", actor: "3" },
				groupBy: "event_type",
				aggregation: "count",
			},
		);

		// Resolution enters every independent source RPC in one synchronous turn;
		// serialized resolution would leave only the first in flight.
		const done = runDataGraph(gate.client, graph, context);
		expect(gate.inFlight()).toBe(3);

		gate.releaseAll();
		await done;
	});
});

it("distinguishes empty, observed zero, partial samples and non-additive totals", async () => {
	const metric: SourceMetric = {
		id: "usage",
		kind: "source",
		filter: { event: "observed" },
		groupBy: "time",
		bucket: "day",
		aggregation: "sum",
		field: "payload:usage",
	};
	const empty = await runMetric(
		fakeAuditClient(() => []).client,
		metric,
		(n) => n,
		context,
	);
	expect(empty.total).toBeNull();
	expect(empty.coverage).toBe("empty");
	const zero = await runMetric(
		fakeAuditClient(() => [{ key: "a", count: 1, metrics: { value: 0 } }])
			.client,
		metric,
		(n) => n,
		context,
	);
	expect(zero.total).toBe(0);
	expect(zero.coverage).toBe("complete");
	const partial = await runMetric(
		fakeAuditClient(() => [
			{
				key: "a",
				count: 2,
				metrics: { value: 9 },
				samples: { value: BigInt(1) },
			},
		]).client,
		metric,
		(n) => n,
		context,
	);
	expect(partial.points).toEqual([{ key: "a", value: 9 }]);
	// An additive total reports what WAS observed and flags the gap through
	// coverage. Nulling it here blanked the stat tile of every metric over an
	// optional payload field (result_count, duration_ms), which is the shape the
	// documented example dashboard uses.
	expect(partial.total).toBe(9);
	expect(partial.coverage).toBe("partial");
	const grouped = await runMetric(
		fakeAuditClient(() => [
			{ key: "a", count: 1, metrics: { value: 9 } },
			{ key: "b", count: 1, metrics: { value: 1 } },
		]).client,
		{ ...metric, aggregation: "avg" },
		(n) => n,
		context,
	);
	expect(grouped.total).toBeNull();
});

it("never turns missing or zero-denominator ratios into successful zero results", async () => {
	const graph: DataGraph = {
		events: [{ name: "observed", type: "saas.document.ingested" }],
		metrics: [
			{
				id: "a",
				kind: "source",
				filter: { event: "observed", actor: "a" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
			{
				id: "b",
				kind: "source",
				filter: { event: "observed", actor: "b" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
			{ id: "rate", kind: "derived", operation: "ratio", inputs: ["a", "b"] },
		],
		dashboards: [],
	};
	const client = fakeAuditClient((q) =>
		q.actorId === "a"
			? [
					{ key: "zero", count: 3 },
					{ key: "missing", count: 1 },
					{ key: "real", count: 0 },
				]
			: [
					{ key: "zero", count: 0 },
					{ key: "real", count: 2 },
				],
	).client;
	const result = await runDataGraph(client, graph, context);
	expect(result.rate.points).toEqual([{ key: "real", value: 0 }]);
	expect(result.rate.total).toBeNull();
	expect(result.rate.coverage).toBe("partial");
});

describe("scoped server contract", () => {
	for (const filter of [
		{ event: "read", resource: "datasource", resourceId: "source-a" },
		{ event: "read", collectionId: "collection-a" },
		{ event: "read", payloadContains: { outcome: "returned" } },
	]) {
		for (const aggregation of ["count", "avg"] as const) {
			for (const empty of [false, true]) {
				it(`rejects unacknowledged ${aggregation} response, empty=${empty}, ${JSON.stringify(filter)}`, async () => {
					const client = {
						aggregateAuditLog: async () =>
							create(AggregateAuditLogResponseSchema, {
								buckets: empty
									? []
									: [
											{
												key: "all",
												count: BigInt(900),
												metrics: { value: 900 },
											},
										],
							}),
					};
					await expect(
						runMetric(
							client,
							{
								id: "scoped",
								kind: "source",
								filter,
								groupBy: "event_type",
								aggregation,
								field: "payload:duration_ms",
							},
							() => "saas.document.read",
							context,
						),
					).rejects.toThrow("scope contract");
				});
			}
		}
	}
	it("accepts a newer scope contract version than it was built against", async () => {
		// The version only ever grows, and a later version is a superset of 1's
		// guarantees. Demanding exact equality meant the first server bump to 2
		// would throw on every scoped metric of every already-deployed client.
		const client = {
			aggregateAuditLog: async () =>
				create(AggregateAuditLogResponseSchema, {
					scopeContractVersion: 2,
					buckets: [{ key: "all", count: BigInt(3) }],
				}),
		};
		const series = await runMetric(
			client,
			{
				id: "scoped",
				kind: "source",
				filter: { event: "read", collectionId: "collection-a" },
				groupBy: "event_type",
				aggregation: "count",
			},
			() => "saas.document.read",
			context,
		);
		expect(series.total).toBe(3);
	});

	it("accepts acknowledged empty scoped responses", async () => {
		const { client } = fakeAuditClient(() => []);
		expect(
			(
				await runMetric(
					client,
					{
						id: "scoped",
						kind: "source",
						filter: { event: "read", collectionId: "collection-a" },
						groupBy: "event_type",
						aggregation: "count",
					},
					() => "saas.document.read",
					context,
				)
			).coverage,
		).toBe("empty");
	});
});

describe("derived additive totals", () => {
	for (const aggregation of [
		"count",
		"sum",
		"avg",
		"percentile",
		"count_distinct",
	] as const) {
		it(`preserves ${aggregation} semantics through nested arithmetic`, async () => {
			const { client } = fakeAuditClient(() => [
				{ key: "a", count: 2, metrics: { value: 2 } },
				{ key: "b", count: 3, metrics: { value: 3 } },
			]);
			const graph: DataGraph = {
				dashboards: [],
				events: [{ name: "read", type: "saas.document.read" }],
				metrics: [
					...["a", "b"].map(
						(id): SourceMetric => ({
							id,
							kind: "source",
							filter: { event: "read" },
							groupBy: "actor",
							aggregation,
							field: "payload:value",
							percentile: 0.95,
						}),
					),
					{
						id: "added",
						kind: "derived",
						operation: "sum",
						inputs: ["a", "b"],
					},
					{
						id: "nested",
						kind: "derived",
						operation: "difference",
						inputs: ["added", "a"],
					},
				],
			};
			const result = await runDataGraph(client, graph, context);
			const additive = aggregation === "count" || aggregation === "sum";
			expect(result.added.total).toBe(additive ? 10 : null);
			expect(result.nested.total).toBe(additive ? 5 : null);
			if (aggregation === "sum") {
				const partial = await runDataGraph(
					fakeAuditClient(() => [
						{
							key: "a",
							count: 2,
							metrics: { value: 2 },
							samples: { value: BigInt(1) },
						},
					]).client,
					graph,
					context,
				);
				expect(partial.nested.coverage).toBe("partial");
				// Same rule one level up: a derived ADDITIVE total stays a number
				// and carries the partial coverage rather than vanishing.
				expect(partial.nested.total).toBe(2);
			}
		});
	}
});

// The audit RPC returns no bucket for a day with no events. Plotted as it came
// back, a day with none vanished and the line ran straight over it.
describe("a time series with days that have no events", () => {
	// The clock stands before every day these tests return, so only the days
	// between the first and the last are filled. The days since the last one,
	// through the end of the range, have tests of their own below.
	beforeEach(() => {
		vi.useFakeTimers({ toFake: ["Date"] });
		vi.setSystemTime(new Date("2020-01-01T00:00:00Z"));
	});
	afterEach(() => vi.useRealTimers());

	const day = (date: string) => `${date}T00:00:00+00`;
	const perDay = (
		aggregation: SourceMetric["aggregation"],
		bucket: SourceMetric["bucket"] = "day",
	): SourceMetric => ({
		id: "per_day",
		kind: "source",
		filter: { event: "signed_in" },
		groupBy: "time",
		bucket,
		aggregation,
		...(aggregation === "count" ? {} : { field: "payload:amount" }),
		...(aggregation === "percentile" ? { percentile: 0.5 } : {}),
	});

	it("puts a 0 at each day between the first and the last with none", async () => {
		const { client } = fakeAuditClient(() => [
			{ key: day("2026-09-02"), count: 1 },
			{ key: day("2026-09-05"), count: 1 },
			{ key: day("2026-09-06"), count: 2 },
		]);

		const series = await runMetric(client, perDay("count"), (n) => n, context);

		expect(series.points).toEqual([
			{ key: day("2026-09-02"), value: 1 },
			{ key: day("2026-09-03"), value: 0 },
			{ key: day("2026-09-04"), value: 0 },
			{ key: day("2026-09-05"), value: 1 },
			{ key: day("2026-09-06"), value: 2 },
		]);
		expect(series.total).toBe(4);
		expect(series.coverage).toBe("complete");
	});

	it.each(["count_distinct", "sum"] as const)(
		"fills a %s, which is 0 over no events",
		async (aggregation) => {
			const { client } = fakeAuditClient(() => [
				{ key: day("2026-09-02"), count: 3, metrics: { value: 2 } },
				{ key: day("2026-09-04"), count: 1, metrics: { value: 1 } },
			]);

			const series = await runMetric(
				client,
				perDay(aggregation),
				(n) => n,
				context,
			);

			expect(series.points.map((point) => point.value)).toEqual([2, 0, 1]);
		},
	);

	it.each(["avg", "min", "max", "percentile"] as const)(
		"leaves out a day with no events for a %s, which has no value then",
		async (aggregation) => {
			const { client } = fakeAuditClient(() => [
				{ key: day("2026-09-02"), count: 3, metrics: { value: 2 } },
				{ key: day("2026-09-04"), count: 1, metrics: { value: 1 } },
			]);

			const series = await runMetric(
				client,
				perDay(aggregation),
				(n) => n,
				context,
			);

			expect(series.points.map((point) => point.key)).toEqual([
				day("2026-09-02"),
				day("2026-09-04"),
			]);
		},
	);

	it("does not put a 0 where the RPC returned a day whose value is unknown", async () => {
		const { client } = fakeAuditClient(() => [
			{ key: day("2026-09-02"), count: 3, metrics: { value: 2 } },
			// Events that day, none with the field: the RPC omits the value.
			{ key: day("2026-09-03"), count: 2, samples: { value: BigInt(0) } },
			{ key: day("2026-09-05"), count: 1, metrics: { value: 1 } },
		]);

		const series = await runMetric(client, perDay("sum"), (n) => n, context);

		expect(series.points).toEqual([
			{ key: day("2026-09-02"), value: 2 },
			{ key: day("2026-09-04"), value: 0 },
			{ key: day("2026-09-05"), value: 1 },
		]);
		expect(series.coverage).toBe("partial");
	});

	it("keeps the total unavailable when every day with events has an unknown value", async () => {
		const { client } = fakeAuditClient(() => [
			// Events on both days, none with the field: the RPC omits the value.
			{ key: day("2026-09-02"), count: 2, samples: { value: BigInt(0) } },
			{ key: day("2026-09-04"), count: 1, samples: { value: BigInt(0) } },
		]);

		const series = await runMetric(client, perDay("sum"), (n) => n, context);

		// The day between gets its 0, but that 0 observed nothing, so the sum
		// of what was observed is unavailable rather than 0.
		expect(series.points).toEqual([{ key: day("2026-09-03"), value: 0 }]);
		expect(series.total).toBeNull();
		expect(series.coverage).toBe("partial");
	});

	it("steps a week and a month bucket by a week and a month", async () => {
		const weeks = await runMetric(
			fakeAuditClient(() => [
				{ key: day("2026-08-31"), count: 1 },
				{ key: day("2026-09-14"), count: 1 },
			]).client,
			perDay("count", "week"),
			(n) => n,
			context,
		);
		const months = await runMetric(
			fakeAuditClient(() => [
				{ key: day("2026-01-01"), count: 1 },
				{ key: day("2026-04-01"), count: 1 },
			]).client,
			perDay("count", "month"),
			(n) => n,
			context,
		);

		expect(weeks.points.map((point) => point.key)).toEqual([
			day("2026-08-31"),
			day("2026-09-07"),
			day("2026-09-14"),
		]);
		expect(months.points.map((point) => [point.key, point.value])).toEqual([
			[day("2026-01-01"), 1],
			[day("2026-02-01"), 0],
			[day("2026-03-01"), 0],
			[day("2026-04-01"), 1],
		]);
	});

	it("leaves the points as they are when a key is off the bucket's step or not a date", async () => {
		const offStep = await runMetric(
			fakeAuditClient(() => [
				{ key: day("2026-08-31"), count: 1 },
				{ key: day("2026-09-10"), count: 1 },
			]).client,
			perDay("count", "week"),
			(n) => n,
			context,
		);
		const notADate = await runMetric(
			fakeAuditClient(() => [
				{ key: day("2026-09-02"), count: 1 },
				{ key: "2026-02-30", count: 1 },
			]).client,
			perDay("count"),
			(n) => n,
			context,
		);

		expect(offStep.points).toHaveLength(2);
		expect(notADate.points).toHaveLength(2);
	});

	// Before the zeros, a day no input had events on was a key no input
	// returned, so a derived metric had nothing there to combine or miss.
	it("does not call a derived metric partial over a day no input had events on", async () => {
		const byActor = (
			id: string,
			aggregation: SourceMetric["aggregation"],
		): SourceMetric => ({
			...perDay(aggregation),
			id,
			filter: { event: "signed_in", actor: id },
		});
		const graph: DataGraph = {
			events: [{ name: "signed_in", type: "user.signed_in.v1" }],
			metrics: [
				byActor("won", "count"),
				byActor("all", "count"),
				byActor("typical", "avg"),
				{
					id: "rate",
					kind: "derived",
					operation: "ratio",
					inputs: ["won", "all"],
				},
				{
					id: "spread",
					kind: "derived",
					operation: "difference",
					inputs: ["all", "typical"],
				},
			],
			dashboards: [],
		};
		// No events at all on 09-04; on 09-03 none won.
		const { client } = fakeAuditClient((request) => {
			if (request.actorId === "won")
				return [
					{ key: day("2026-09-02"), count: 1 },
					{ key: day("2026-09-05"), count: 1 },
				];
			if (request.actorId === "all")
				return [
					{ key: day("2026-09-02"), count: 2 },
					{ key: day("2026-09-03"), count: 4 },
					{ key: day("2026-09-05"), count: 4 },
				];
			return [
				{ key: day("2026-09-02"), count: 2, metrics: { value: 2 } },
				{ key: day("2026-09-03"), count: 4, metrics: { value: 3 } },
				{ key: day("2026-09-05"), count: 4, metrics: { value: 1 } },
			];
		});

		const resolved = await runDataGraph(client, graph, context);

		expect(resolved.rate.points).toEqual([
			{ key: day("2026-09-02"), value: 0.5 },
			{ key: day("2026-09-03"), value: 0 },
			{ key: day("2026-09-05"), value: 0.25 },
		]);
		expect(resolved.rate.coverage).toBe("complete");
		expect(resolved.spread.points.map((point) => point.value)).toEqual([
			0, 1, 3,
		]);
		expect(resolved.spread.coverage).toBe("complete");
	});
});

// A rate's inputs rarely start on the same day: failures begin later than
// requests, or never happen at all. A day the numerator had no events on is a
// 0, not missing telemetry, wherever it falls in the denominator's range.
describe("a derived metric over inputs with different first and last days", () => {
	beforeEach(() => {
		vi.useFakeTimers({ toFake: ["Date"] });
		vi.setSystemTime(new Date("2020-01-01T00:00:00Z"));
	});
	afterEach(() => vi.useRealTimers());

	const day = (date: string) => `${date}T00:00:00+00`;
	const perDay = (id: string, actor: string): SourceMetric => ({
		id,
		kind: "source",
		filter: { event: "signed_in", actor },
		groupBy: "time",
		bucket: "day",
		aggregation: "count",
	});
	const graph = (...derived: DataGraph["metrics"][number][]): DataGraph => ({
		events: [{ name: "signed_in", type: "user.signed_in.v1" }],
		metrics: [perDay("failed", "failed"), perDay("all", "all"), ...derived],
		dashboards: [],
	});
	const rate = {
		id: "rate",
		kind: "derived",
		operation: "ratio",
		inputs: ["failed", "all"],
	} as const;

	it("reads 0, and stays complete, on a denominator day before the numerator's first event", async () => {
		const { client } = fakeAuditClient((request) =>
			request.actorId === "failed"
				? [{ key: day("2026-09-03"), count: 1 }]
				: [
						{ key: day("2026-09-02"), count: 2 },
						{ key: day("2026-09-03"), count: 4 },
						{ key: day("2026-09-04"), count: 4 },
					],
		);
		const resolved = await runDataGraph(client, graph(rate), context);
		expect(resolved.rate.points).toEqual([
			{ key: day("2026-09-02"), value: 0 },
			{ key: day("2026-09-03"), value: 0.25 },
			{ key: day("2026-09-04"), value: 0 },
		]);
		expect(resolved.rate.coverage).toBe("complete");
	});

	it("reads 0% on every day of a ratio whose numerator has no events at all", async () => {
		const { client } = fakeAuditClient((request) =>
			request.actorId === "failed"
				? []
				: [
						{ key: day("2026-09-02"), count: 2 },
						{ key: day("2026-09-04"), count: 4 },
					],
		);
		const resolved = await runDataGraph(client, graph(rate), context);
		expect(resolved.rate.coverage).toBe("complete");
		// 3 September had no requests either: a rate of nothing has no value.
		expect(resolved.rate.points.map((point) => point.value)).toEqual([0, 0]);
	});

	it("puts the days of a later-starting input in time order, not first-seen order", async () => {
		const { client } = fakeAuditClient((request) =>
			request.actorId === "failed"
				? [{ key: day("2026-09-01"), count: 1 }]
				: [{ key: day("2026-09-03"), count: 2 }],
		);
		const resolved = await runDataGraph(
			client,
			graph({
				id: "total",
				kind: "derived",
				operation: "sum",
				inputs: ["all", "failed"],
			}),
			context,
		);
		expect(resolved.total.points).toEqual([
			{ key: day("2026-09-01"), value: 1 },
			{ key: day("2026-09-02"), value: 0 },
			{ key: day("2026-09-03"), value: 2 },
		]);
		expect(resolved.total.coverage).toBe("complete");
	});

	it("reads a sum of no events as 0 when it feeds another derived metric", async () => {
		const { client } = fakeAuditClient((request) =>
			request.actorId === "failed"
				? []
				: [
						{ key: day("2026-09-02"), count: 2 },
						{ key: day("2026-09-03"), count: 4 },
					],
		);
		const resolved = await runDataGraph(
			client,
			graph(
				{
					id: "failures",
					kind: "derived",
					operation: "sum",
					inputs: ["failed", "failed"],
				},
				{
					id: "rate",
					kind: "derived",
					operation: "ratio",
					inputs: ["failures", "all"],
				},
			),
			context,
		);
		expect(resolved.rate.coverage).toBe("complete");
		expect(resolved.rate.points.map((point) => point.value)).toEqual([0, 0]);
	});

	it("still reads a missing operand as unknown when that input is partial", async () => {
		const { client } = fakeAuditClient((request) =>
			request.actorId === "failed"
				? [{ key: "user.signed_in.v1", count: 1 }]
				: [{ key: day("2026-09-02"), count: 2 }],
		);
		const resolved = await runDataGraph(
			client,
			{
				...graph(rate),
				metrics: [
					{ ...perDay("failed", "failed"), groupBy: "time" },
					perDay("all", "all"),
					rate,
				],
			},
			context,
		);
		// A key that is not a date is never read as a day with no events.
		expect(resolved.rate.coverage).toBe("partial");
	});
});

// The query is open-ended, so its range ends today: the days since the last
// event are days with no events like the ones between, and the latest point is
// the latest day, not the last day anything happened.
describe("a time series whose range runs past its last event", () => {
	afterEach(() => vi.useRealTimers());

	const perDay = (bucket: SourceMetric["bucket"] = "day"): SourceMetric => ({
		id: "per_day",
		kind: "source",
		filter: { event: "signed_in" },
		groupBy: "time",
		bucket,
		aggregation: "count",
	});
	const days = (series: { points: { key: string }[] }) =>
		series.points.map((point) => point.key.slice(0, 10));

	it("reads 0 on each day since the last event, up to today", async () => {
		vi.useFakeTimers({ toFake: ["Date"] });
		vi.setSystemTime(new Date("2026-09-05T12:00:00Z"));
		const { client } = fakeAuditClient(() => [
			{ key: "2026-09-01T00:00:00+00", count: 3 },
			{ key: "2026-09-02T00:00:00+00", count: 5 },
		]);
		const series = await runMetric(client, perDay(), (n) => `user.${n}.v1`, {
			orgId: "org_1",
		});
		expect(days(series)).toEqual([
			"2026-09-01",
			"2026-09-02",
			"2026-09-03",
			"2026-09-04",
			"2026-09-05",
		]);
		expect(series.points.slice(2).map((point) => point.value)).toEqual([
			0, 0, 0,
		]);
		expect(series.total).toBe(8);
	});

	it("stops before an explicit end of range, which is exclusive", async () => {
		const { client } = fakeAuditClient(() => [
			{ key: "2026-09-01T00:00:00+00", count: 3 },
		]);
		const series = await runMetric(client, perDay(), (n) => `user.${n}.v1`, {
			orgId: "org_1",
			to: new Date("2026-09-04T00:00:00Z"),
		});
		expect(days(series)).toEqual(["2026-09-01", "2026-09-02", "2026-09-03"]);
	});

	it("runs a week bucket through the week holding today", async () => {
		vi.useFakeTimers({ toFake: ["Date"] });
		vi.setSystemTime(new Date("2026-09-16T12:00:00Z"));
		const { client } = fakeAuditClient(() => [
			{ key: "2026-08-31T00:00:00+00", count: 3 },
		]);
		const series = await runMetric(
			client,
			perDay("week"),
			(n) => `user.${n}.v1`,
			{ orgId: "org_1" },
		);
		expect(days(series)).toEqual(["2026-08-31", "2026-09-07", "2026-09-14"]);
	});
});

// The RPC keys UTC buckets, so every key carries "+00" and a filled day copies
// it. Keys that disagree about their offset leave a filled day with an offset
// that cannot be known from them, so nothing is invented.
describe("filling keys that carry different offsets", () => {
	it("leaves the points as they came rather than give a filled day a guessed offset", () => {
		const points = [
			{ key: "2026-03-28T00:00:00+01", value: 1 },
			{ key: "2026-04-01T00:00:00+02", value: 1 },
		];
		const filled = fillEmptyBuckets(
			points,
			points.map((point) => point.key),
			"day",
		);
		expect(filled).toEqual(points);
	});

	it("fills keys that share one offset, copying it", () => {
		const points = [
			{ key: "2026-03-28T00:00:00+00", value: 1 },
			{ key: "2026-03-30T00:00:00+00", value: 1 },
		];
		const filled = fillEmptyBuckets(
			points,
			points.map((point) => point.key),
			"day",
		);
		expect(filled.map((point) => point.key)).toEqual([
			"2026-03-28T00:00:00+00",
			"2026-03-29T00:00:00+00",
			"2026-03-30T00:00:00+00",
		]);
	});
});
