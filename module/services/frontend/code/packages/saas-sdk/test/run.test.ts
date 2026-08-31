import { describe, expect, it } from "vitest";
import { runDataGraph, runMetric } from "../src/datagraph/run.js";
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
		expect(series.total).toBe(4.2);
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

	it("sums and differences point-wise on the union of keys", async () => {
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

		expect(resolved.total.points).toEqual([
			{ key: "mon", value: 1 },
			{ key: "tue", value: 7 },
		]);
		expect(resolved.gap.points).toEqual([
			{ key: "mon", value: 1 },
			{ key: "tue", value: 1 },
		]);
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
		const graph = graphWith({
			id: "logins",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "event_type",
			aggregation: "count",
		});

		// A spec that would otherwise reach the audit trail is inert without a
		// bound org: it rejects before any request is sent, so it can never widen
		// past the viewer's org into an org-wide read.
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
