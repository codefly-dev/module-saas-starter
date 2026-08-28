import { describe, expect, it } from "vitest";

import { assertDataGraph, type DataGraph } from "../src/index.js";

function graph(): DataGraph {
	return {
		events: [
			{ name: "guardrail_triggered", type: "guardrail.triggered.v1" },
			{ name: "user_created", type: "identity.user.created.v1" },
		],
		metrics: [
			{
				id: "triggers_over_time",
				kind: "source",
				filter: { event: "guardrail_triggered" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
			{
				id: "signups_over_time",
				kind: "source",
				filter: { event: "user_created" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
			{
				id: "trigger_rate",
				kind: "derived",
				operation: "ratio",
				inputs: ["triggers_over_time", "signups_over_time"],
			},
		],
		dashboards: [
			{
				id: "overview",
				layout: "grid",
				widgets: [
					{ id: "trend", metric: "triggers_over_time", visualization: "line" },
					{ id: "rate", metric: "trigger_rate", visualization: "number" },
				],
			},
		],
	};
}

const rec = (value: unknown): Record<string, unknown> =>
	value as Record<string, unknown>;
const arr = (value: unknown): Record<string, unknown>[] =>
	value as Record<string, unknown>[];

// The parsed graph is untyped at the boundary. Each mutation breaks exactly one
// rule on a deep clone of the reference graph, so a single valid baseline drives
// every negative case.
function mutated(mutate: (g: Record<string, unknown>) => void): unknown {
	const clone = JSON.parse(JSON.stringify(graph())) as Record<string, unknown>;
	mutate(clone);
	return clone;
}

const metric = (g: Record<string, unknown>, index: number) =>
	rec(arr(g.metrics)[index]);
const widget = (g: Record<string, unknown>, index: number) =>
	arr(rec(arr(g.dashboards)[0]).widgets)[index];

describe("assertDataGraph", () => {
	it("accepts a well-formed data graph", () => {
		expect(() => assertDataGraph(graph())).not.toThrow();
	});

	it("accepts the empty graph", () => {
		expect(() =>
			assertDataGraph({ events: [], metrics: [], dashboards: [] }),
		).not.toThrow();
	});

	it("rejects an unknown top-level section", () => {
		expect(() => assertDataGraph(mutated((g) => (g.panels = [])))).toThrow(
			/unknown field 'panels'/,
		);
	});

	it("rejects a duplicate event name", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => (rec(arr(g.events)[1]).name = "guardrail_triggered")),
			),
		).toThrow(/event name 'guardrail_triggered' is declared more than once/);
	});

	it("rejects an unversioned event type", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => (rec(arr(g.events)[0]).type = "guardrail.triggered")),
			),
		).toThrow(/must be namespaced and versioned/);
	});

	it("rejects a metric filtering an undeclared event", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => (rec(metric(g, 0).filter).event = "missing")),
			),
		).toThrow(/filters unknown event 'missing'/);
	});

	it("accepts a percentile over a payload field", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => {
					const m = metric(g, 0);
					m.aggregation = "percentile";
					m.field = "payload:duration_ms";
					m.percentile = 0.95;
				}),
			),
		).not.toThrow();
	});

	it("accepts count_distinct over a column and a payload group dimension", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => {
					const m = metric(g, 0);
					m.groupBy = "payload:plan";
					delete m.bucket;
					m.aggregation = "count_distinct";
					m.field = "actor_id";
				}),
			),
		).not.toThrow();
	});

	it("rejects a non-count aggregation with no field", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => (metric(g, 0).aggregation = "count_distinct")),
			),
		).toThrow(/aggregation 'count_distinct' needs a field/);
	});

	it("rejects a numeric aggregation whose field is not a payload key", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => {
					const m = metric(g, 0);
					m.aggregation = "sum";
					m.field = "actor_id";
				}),
			),
		).toThrow(/needs a payload:<key> field/);
	});

	it("rejects a count metric that declares a field", () => {
		expect(() =>
			assertDataGraph(mutated((g) => (metric(g, 0).field = "payload:x"))),
		).toThrow(/count aggregation takes no field/);
	});

	it("rejects a percentile out of range", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => {
					const m = metric(g, 0);
					m.aggregation = "percentile";
					m.field = "payload:duration_ms";
					m.percentile = 1.5;
				}),
			),
		).toThrow(/percentile must be a quantile in \(0, 1\]/);
	});

	it("rejects a percentile on a non-percentile aggregation", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => {
					const m = metric(g, 0);
					m.aggregation = "count_distinct";
					m.field = "actor_id";
					m.percentile = 0.95;
				}),
			),
		).toThrow(/percentile is only valid for the percentile aggregation/);
	});

	it("rejects a time metric with no bucket", () => {
		expect(() =>
			assertDataGraph(mutated((g) => delete metric(g, 0).bucket)),
		).toThrow(/groups by time and needs a bucket/);
	});

	it("rejects a non-time metric that declares a bucket", () => {
		expect(() =>
			assertDataGraph(mutated((g) => (metric(g, 0).groupBy = "actor"))),
		).toThrow(/declares a bucket but does not group by time/);
	});

	it("rejects a derived metric referencing an undeclared metric", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => (metric(g, 2).inputs = ["triggers_over_time", "ghost"])),
			),
		).toThrow(/derives from unknown metric 'ghost'/);
	});

	it("rejects a ratio with the wrong input count", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => (metric(g, 2).inputs = ["triggers_over_time"])),
			),
		).toThrow(/'ratio' needs exactly two inputs/);
	});

	it("rejects a metric that derives from itself", () => {
		expect(() =>
			assertDataGraph(
				mutated(
					(g) => (metric(g, 2).inputs = ["trigger_rate", "signups_over_time"]),
				),
			),
		).toThrow(/cannot derive from itself/);
	});

	it("rejects a derivation cycle across two metrics", () => {
		expect(() =>
			assertDataGraph(
				mutated((g) => {
					arr(g.metrics)[0] = {
						id: "a",
						kind: "derived",
						operation: "sum",
						inputs: ["b", "signups_over_time"],
					};
					arr(g.metrics)[1] = {
						id: "b",
						kind: "derived",
						operation: "sum",
						inputs: ["a", "signups_over_time"],
					};
					arr(g.metrics)[2] = {
						id: "signups_over_time",
						kind: "source",
						filter: { event: "user_created" },
						groupBy: "time",
						bucket: "day",
						aggregation: "count",
					};
					rec(arr(g.dashboards)[0]).widgets = [
						{ id: "trend", metric: "a", visualization: "line" },
					];
				}),
			),
		).toThrow(/derivation cycle/);
	});

	it("rejects a widget binding an undeclared metric", () => {
		expect(() =>
			assertDataGraph(mutated((g) => (widget(g, 0).metric = "nope"))),
		).toThrow(/binds unknown metric 'nope'/);
	});

	it("rejects duplicate widget ids within a dashboard", () => {
		expect(() =>
			assertDataGraph(mutated((g) => (widget(g, 1).id = "trend"))),
		).toThrow(
			/widget id in dashboard 'overview' 'trend' is declared more than once/,
		);
	});

	it("rejects a dashboard with no widgets", () => {
		expect(() =>
			assertDataGraph(mutated((g) => (rec(arr(g.dashboards)[0]).widgets = []))),
		).toThrow(/must declare at least one widget/);
	});
});
