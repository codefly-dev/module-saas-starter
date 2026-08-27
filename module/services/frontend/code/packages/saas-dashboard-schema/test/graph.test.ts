import { describe, expect, it } from "vitest";
import {
	DataGraphError,
	defineDataGraph,
	type MetricPoint,
} from "../src/index.js";

// A small but complete graph modelled on audit-page.tsx: events over time and
// top event types, plus a derived total — the shape a reference dashboard binds.
const validInput = {
	events: [
		{ name: "user.login", category: "auth" },
		{ name: "user.logout", category: "auth" },
		{ name: "org.member.invited", category: "org" },
	],
	metrics: [
		{
			id: "logins-over-time",
			kind: "source" as const,
			filter: { event: "user.login" },
			groupBy: "time" as const,
			bucket: "day" as const,
		},
		{
			id: "events-by-type",
			kind: "source" as const,
			groupBy: "event_type" as const,
		},
		{
			id: "total-logins",
			kind: "derived" as const,
			from: ["logins-over-time"],
			compute: (inputs: MetricPoint[][]) => [
				{
					key: "total",
					value: inputs[0].reduce((sum, p) => sum + p.value, 0),
				},
			],
		},
	],
	dashboards: [
		{
			id: "activity",
			title: "Activity",
			widgets: [
				{ id: "w1", metric: "logins-over-time", type: "line" as const },
				{ id: "w2", metric: "events-by-type", type: "bar" as const },
				{ id: "w3", metric: "total-logins", type: "stat" as const },
			],
		},
	],
};

describe("defineDataGraph", () => {
	it("accepts a consistent declaration and exposes lookups", () => {
		const graph = defineDataGraph(validInput);
		expect(graph.events).toHaveLength(3);
		expect(graph.metrics).toHaveLength(3);
		expect(graph.dashboards).toHaveLength(1);
		expect(graph.metric("events-by-type").kind).toBe("source");
	});

	it("orders metrics so every upstream precedes its dependents", () => {
		const order = defineDataGraph(validInput).order();
		expect(order.indexOf("logins-over-time")).toBeLessThan(
			order.indexOf("total-logins"),
		);
	});

	it("compiles a bound source metric but refuses a derived one", () => {
		const graph = defineDataGraph(validInput);
		expect(graph.compile("logins-over-time").groupBy).toBe("time");
		expect(() => graph.compile("total-logins")).toThrow(DataGraphError);
	});

	it("rejects duplicate metric ids", () => {
		expect(() =>
			defineDataGraph({
				events: [],
				metrics: [
					{ id: "dup", kind: "source", groupBy: "event_type" },
					{ id: "dup", kind: "source", groupBy: "category" },
				],
			}),
		).toThrow(/duplicate metric id 'dup'/);
	});

	it("rejects a time grouping without a bucket", () => {
		expect(() =>
			defineDataGraph({
				events: [],
				metrics: [{ id: "m", kind: "source", groupBy: "time" }],
			}),
		).toThrow(/groups by time but declares no bucket/);
	});

	it("rejects a bucket on a non-time grouping", () => {
		expect(() =>
			defineDataGraph({
				events: [],
				metrics: [
					{ id: "m", kind: "source", groupBy: "event_type", bucket: "day" },
				],
			}),
		).toThrow(/does not group by time/);
	});

	it("rejects a filter on an undeclared event", () => {
		expect(() =>
			defineDataGraph({
				events: [{ name: "user.login" }],
				metrics: [
					{
						id: "m",
						kind: "source",
						filter: { event: "user.ghost" },
						groupBy: "event_type",
					},
				],
			}),
		).toThrow(/undeclared event 'user.ghost'/);
	});

	it("rejects a filter on an undeclared category", () => {
		expect(() =>
			defineDataGraph({
				events: [{ name: "user.login", category: "auth" }],
				metrics: [
					{
						id: "m",
						kind: "source",
						filter: { category: "billing" },
						groupBy: "event_type",
					},
				],
			}),
		).toThrow(/undeclared category 'billing'/);
	});

	it("rejects a derived metric with no compute function", () => {
		expect(() =>
			defineDataGraph({
				events: [],
				metrics: [
					{ id: "s", kind: "source", groupBy: "event_type" },
					{
						id: "d",
						kind: "derived",
						from: ["s"],
						compute: undefined as unknown as (
							i: MetricPoint[][],
						) => MetricPoint[],
					},
				],
			}),
		).toThrow(/has no compute function/);
	});

	it("rejects a derived metric with an unknown upstream", () => {
		expect(() =>
			defineDataGraph({
				events: [],
				metrics: [
					{ id: "d", kind: "derived", from: ["missing"], compute: (i) => i[0] },
				],
			}),
		).toThrow(/depends on undeclared metric 'missing'/);
	});

	it("rejects a self-dependent derived metric", () => {
		expect(() =>
			defineDataGraph({
				events: [],
				metrics: [
					{ id: "d", kind: "derived", from: ["d"], compute: (i) => i[0] },
				],
			}),
		).toThrow(/depends on itself/);
	});

	it("detects a dependency cycle", () => {
		expect(() =>
			defineDataGraph({
				events: [],
				metrics: [
					{ id: "a", kind: "derived", from: ["b"], compute: (i) => i[0] },
					{ id: "b", kind: "derived", from: ["a"], compute: (i) => i[0] },
				],
			}),
		).toThrow(/cycle/);
	});

	it("rejects a widget bound to an undeclared metric", () => {
		expect(() =>
			defineDataGraph({
				events: [],
				metrics: [{ id: "m", kind: "source", groupBy: "event_type" }],
				dashboards: [
					{
						id: "d",
						widgets: [{ id: "w", metric: "ghost", type: "line" }],
					},
				],
			}),
		).toThrow(/binds undeclared metric 'ghost'/);
	});

	it("rejects duplicate widget ids within a dashboard", () => {
		expect(() =>
			defineDataGraph({
				events: [],
				metrics: [{ id: "m", kind: "source", groupBy: "event_type" }],
				dashboards: [
					{
						id: "d",
						widgets: [
							{ id: "w", metric: "m", type: "line" },
							{ id: "w", metric: "m", type: "bar" },
						],
					},
				],
			}),
		).toThrow(/duplicate widget in dashboard 'd' id 'w'/);
	});
});
