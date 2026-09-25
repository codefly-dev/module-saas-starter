import type { DataGraph } from "@codefly/saas-plugin-manifest";
import { describe, expect, it } from "vitest";
import {
	ACTIVITY_DASHBOARD,
	activityGraph,
	activityRange,
	describeMetric,
	formatDay,
} from "../metric-info";

const graph: DataGraph = {
	events: [
		{
			name: "login",
			type: "saas.auth.login",
			description: "Someone signed in.",
		},
		{ name: "deal", type: "example.deal.created" },
		{ name: "run", type: "example.run.finished" },
	],
	metrics: [
		{
			id: "people",
			kind: "source",
			title: "People",
			description: "Everyone who signed in.",
			filter: { event: "login" },
			groupBy: "event_type",
			aggregation: "count_distinct",
			field: "actor_id",
		},
		{
			id: "people_per_day",
			kind: "source",
			filter: { event: "login" },
			groupBy: "time",
			bucket: "week",
			aggregation: "count_distinct",
			field: "actor_id",
		},
		{
			id: "deals",
			kind: "source",
			title: "Deals",
			filter: { event: "deal" },
			groupBy: "category",
			aggregation: "count_distinct",
			field: "payload:deal_id",
		},
		{
			id: "slow_runs",
			kind: "source",
			filter: {
				event: "run",
				resource: "pipeline",
				payloadContains: { outcome: "failed" },
			},
			groupBy: "payload:region",
			aggregation: "percentile",
			field: "payload:duration_ms",
			percentile: 0.95,
		},
		{
			id: "deals_per_person",
			kind: "derived",
			operation: "ratio",
			inputs: ["deals", "people"],
		},
		{
			id: "everything",
			kind: "derived",
			operation: "sum",
			inputs: ["deals", "people", "deals_per_person"],
		},
	],
	dashboards: [],
};

describe("describeMetric", () => {
	it("says what a source metric counts, and from which event", () => {
		expect(describeMetric(graph, "people")).toEqual({
			counts: "Distinct people (actor_id)",
			sources: [{ type: "saas.auth.login", declared: "Someone signed in." }],
			note: "Everyone who signed in.",
		});
	});

	it("names the time grouping, but not one that splits a single event type", () => {
		expect(describeMetric(graph, "people_per_day").counts).toBe(
			"Distinct people (actor_id), per week",
		);
		// Grouping one event type by category splits nothing.
		expect(describeMetric(graph, "deals").counts).toBe(
			"Distinct deal_id values",
		);
	});

	it("leaves the grouping out for a tile that shows one number", () => {
		expect(describeMetric(graph, "people_per_day", "number").counts).toBe(
			"Distinct people (actor_id)",
		);
	});

	it("reads a numeric aggregation, a payload grouping and every filter", () => {
		expect(describeMetric(graph, "slow_runs").counts).toBe(
			"95th percentile of duration_ms, by region, only where the resource is pipeline and outcome is failed",
		);
	});

	it("describes a derived metric by its inputs, and lists every event behind it", () => {
		expect(describeMetric(graph, "deals_per_person")).toEqual({
			counts: "Deals divided by People",
			sources: [
				{ type: "example.deal.created", declared: undefined },
				{ type: "saas.auth.login", declared: "Someone signed in." },
			],
			note: undefined,
		});
		// A metric reached twice lists its event once.
		expect(describeMetric(graph, "everything")).toMatchObject({
			counts: "Deals plus People plus deals_per_person",
			sources: [{ type: "example.deal.created" }, { type: "saas.auth.login" }],
		});
	});
});

describe("activityGraph", () => {
	it("counts per day the events behind each source, with its filter unchanged", () => {
		const activity = activityGraph(graph, "deals_per_person");
		expect(activity.metrics).toEqual([
			{
				id: "activity_0",
				kind: "source",
				filter: { event: "deal" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
			{
				id: "activity_1",
				kind: "source",
				filter: { event: "login" },
				groupBy: "time",
				bucket: "day",
				aggregation: "count",
			},
		]);
		expect(activity.dashboards.map((d) => d.id)).toEqual([ACTIVITY_DASHBOARD]);
		expect(activity.dashboards[0].widgets.map((w) => w.metric)).toEqual([
			"activity_0",
			"activity_1",
		]);
	});
});

describe("activityRange", () => {
	it("spans the first to the last day with events across every source", () => {
		// Keys as the audit service writes them, with Postgres' bare-hour offset.
		const range = activityRange([
			{
				points: [
					{ key: "2026-09-03T00:00:00+00" },
					{ key: "2026-09-20T00:00:00+00" },
				],
			},
			{
				points: [
					{ key: "2026-08-26T00:00:00+00" },
					{ key: "2026-09-24T00:00:00+00" },
				],
			},
		]);
		expect(range?.first.toISOString()).toBe("2026-08-26T00:00:00.000Z");
		expect(range?.last.toISOString()).toBe("2026-09-24T00:00:00.000Z");
	});

	it("is null when nothing matched", () => {
		expect(activityRange([{ points: [] }, { points: [] }])).toBeNull();
	});
});

it("formats a day bucket as its UTC calendar day", () => {
	// 00:00 UTC is still the previous evening west of UTC; the day must not shift.
	expect(formatDay(new Date("2026-09-24T00:00:00Z"), "en-US")).toBe(
		"Sep 24, 2026",
	);
});
