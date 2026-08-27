import { describe, expect, it } from "vitest";
import { runDataGraph, runMetric } from "../src/datagraph/run.js";
import type { DataGraph, SourceMetric } from "../src/schema.js";
import { fakeAuditClient } from "./support.js";

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
		});
		expect(calls[0]?.eventType).toBe("user.signed_in.v1");
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
});
