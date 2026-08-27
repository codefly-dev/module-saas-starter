import { describe, expect, it, vi } from "vitest";
import {
	type AggregateQuery,
	compileMetric,
	defineDataGraph,
	type MetricPoint,
	type SourceMetric,
} from "../src/index.js";

describe("compileMetric", () => {
	it("maps an event filter and grouping to an audit query", () => {
		const metric: SourceMetric = {
			id: "logins-by-day",
			kind: "source",
			filter: { event: "user.login" },
			groupBy: "time",
			bucket: "day",
		};
		expect(compileMetric(metric)).toEqual({
			eventType: "user.login",
			category: "",
			groupBy: "time",
			bucket: "day",
		});
	});

	it("sends an empty bucket for non-time groupings", () => {
		const metric: SourceMetric = {
			id: "by-type",
			kind: "source",
			groupBy: "event_type",
		};
		expect(compileMetric(metric)).toEqual({
			eventType: "",
			category: "",
			groupBy: "event_type",
			bucket: "",
		});
	});

	it("carries a category filter", () => {
		const metric: SourceMetric = {
			id: "auth-by-actor",
			kind: "source",
			filter: { category: "auth" },
			groupBy: "actor",
		};
		expect(compileMetric(metric).category).toBe("auth");
	});
});

describe("resolve", () => {
	const graph = defineDataGraph({
		events: [
			{ name: "user.login", category: "auth" },
			{ name: "user.logout", category: "auth" },
		],
		metrics: [
			{
				id: "logins-by-day",
				kind: "source",
				filter: { event: "user.login" },
				groupBy: "time",
				bucket: "day",
			},
			{
				id: "total-logins",
				kind: "derived",
				from: ["logins-by-day"],
				compute: ([series]) => [
					{ key: "total", value: series.reduce((sum, p) => sum + p.value, 0) },
				],
			},
		],
	});

	it("runs a source metric's compiled query through the fetcher", async () => {
		const fetcher = vi.fn(
			async (query: AggregateQuery): Promise<MetricPoint[]> => {
				expect(query.eventType).toBe("user.login");
				return [
					{ key: "2026-08-01", value: 3 },
					{ key: "2026-08-02", value: 5 },
				];
			},
		);
		const series = await graph.resolve("logins-by-day", fetcher);
		expect(series).toEqual([
			{ key: "2026-08-01", value: 3 },
			{ key: "2026-08-02", value: 5 },
		]);
	});

	it("computes a derived metric from its upstream series", async () => {
		const fetcher = async (): Promise<MetricPoint[]> => [
			{ key: "2026-08-01", value: 3 },
			{ key: "2026-08-02", value: 5 },
		];
		const series = await graph.resolve("total-logins", fetcher);
		expect(series).toEqual([{ key: "total", value: 8 }]);
	});

	it("fetches a shared upstream once per resolution", async () => {
		const fanOut = defineDataGraph({
			events: [{ name: "user.login" }],
			metrics: [
				{ id: "src", kind: "source", groupBy: "event_type" },
				{
					id: "a",
					kind: "derived",
					from: ["src"],
					compute: ([s]) => s,
				},
				{
					id: "b",
					kind: "derived",
					from: ["src", "a"],
					compute: ([s]) => s,
				},
			],
		});
		const fetcher = vi.fn(
			async (): Promise<MetricPoint[]> => [{ key: "user.login", value: 1 }],
		);
		await fanOut.resolve("b", fetcher);
		expect(fetcher).toHaveBeenCalledTimes(1);
	});
});
