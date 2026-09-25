import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { DataGraph } from "@codefly/saas-plugin-manifest";
import { describe, expect, it } from "vitest";
import {
	ACTIVITY_DASHBOARD,
	activityGraph,
	activitySummary,
	describeMetric,
	formatDay,
	formatMoment,
	newestEvents,
	recentEventsQueries,
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

describe("reading each event once", () => {
	const closes: DataGraph = {
		events: [{ name: "closed", type: "example.deal.closed" }],
		metrics: [
			{
				id: "won",
				kind: "source",
				filter: { event: "closed", payloadContains: { outcome: "won" } },
				groupBy: "category",
				aggregation: "count",
			},
			{
				id: "closed",
				kind: "source",
				filter: { event: "closed" },
				groupBy: "category",
				aggregation: "count",
			},
			{
				id: "lost",
				kind: "source",
				filter: { event: "closed", payloadContains: { outcome: "lost" } },
				groupBy: "category",
				aggregation: "count",
			},
			{
				id: "win_rate",
				kind: "derived",
				operation: "ratio",
				inputs: ["won", "closed"],
			},
			{
				id: "won_vs_lost",
				kind: "derived",
				operation: "difference",
				inputs: ["won", "lost"],
			},
			{
				id: "closed_twice",
				kind: "derived",
				operation: "sum",
				inputs: ["closed", "closed"],
			},
		],
		dashboards: [],
	};

	it("drops a source another one covers, so a win rate counts each close once", () => {
		expect(
			activityGraph(closes, "win_rate").metrics.map(
				(m) => m.kind === "source" && m.filter,
			),
		).toEqual([{ event: "closed" }]);
		expect(recentEventsQueries(closes, "win_rate")).toHaveLength(1);
	});

	it("keeps sources that do not cover each other", () => {
		expect(
			activityGraph(closes, "won_vs_lost").metrics.map(
				(m) => m.kind === "source" && m.filter.payloadContains,
			),
		).toEqual([{ outcome: "won" }, { outcome: "lost" }]);
	});

	it("reads one input twice only once", () => {
		expect(activityGraph(closes, "closed_twice").metrics).toHaveLength(1);
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

describe("activitySummary", () => {
	it("spans the first to the last day with events, and counts them all", () => {
		// Keys as the audit service writes them, with Postgres' bare-hour offset.
		const summary = activitySummary([
			{
				points: [
					{ key: "2026-09-03T00:00:00+00", value: 4 },
					{ key: "2026-09-20T00:00:00+00", value: 1 },
				],
			},
			{
				points: [
					{ key: "2026-08-26T00:00:00+00", value: 2 },
					{ key: "2026-09-24T00:00:00+00", value: 5 },
				],
			},
		]);
		expect(summary?.first.toISOString()).toBe("2026-08-26T00:00:00.000Z");
		expect(summary?.last.toISOString()).toBe("2026-09-24T00:00:00.000Z");
		expect(summary?.events).toBe(12);
	});

	it("is null when nothing matched", () => {
		expect(activitySummary([{ points: [] }, { points: [] }])).toBeNull();
	});
});

describe("recentEventsQueries", () => {
	it("searches the audit log with each source's own filter", () => {
		expect(recentEventsQueries(graph, "slow_runs")).toEqual([
			{
				eventType: "example.run.finished",
				actorId: undefined,
				resource: "pipeline",
				resourceId: undefined,
				payloadContains: { outcome: "failed" },
			},
		]);
		expect(
			recentEventsQueries(graph, "deals_per_person")?.map((q) => q.eventType),
		).toEqual(["example.deal.created", "saas.auth.login"]);
	});

	it("gives up on a metric narrowed by collection, which the search cannot do", () => {
		const byCollection: DataGraph = {
			...graph,
			metrics: [
				{
					id: "filed",
					kind: "source",
					filter: { event: "deal", collectionId: "col-1" },
					groupBy: "time",
					bucket: "day",
					aggregation: "count",
				},
			],
		};
		expect(recentEventsQueries(byCollection, "filed")).toBeNull();
	});
});

describe("newestEvents", () => {
	const at = (iso: string) => timestampFromDate(new Date(iso));

	it("lists the newest events across every search, each once", () => {
		const newest = newestEvents(
			[
				[
					{ id: "a", actorId: "p1", createdAt: at("2026-09-24T14:32:00Z") },
					{ id: "b", actorId: "p2", createdAt: at("2026-09-20T09:00:00Z") },
				],
				[
					{ id: "c", actorId: "p3", createdAt: at("2026-09-22T11:00:00Z") },
					{ id: "a", actorId: "p1", createdAt: at("2026-09-24T14:32:00Z") },
				],
			],
			2,
		);
		expect(newest.map((e) => e.id)).toEqual(["a", "c"]);
		expect(newest[0].at.toISOString()).toBe("2026-09-24T14:32:00.000Z");
	});
});

it("formats a day bucket as its UTC calendar day", () => {
	// 00:00 UTC is still the previous evening west of UTC; the day must not shift.
	expect(formatDay(new Date("2026-09-24T00:00:00Z"), "en-US")).toBe(
		"Sep 24, 2026",
	);
});

it("formats an event's time in UTC, with or without the year", () => {
	const at = new Date("2026-09-24T14:32:00Z");
	expect(formatMoment(at, { locale: "en-US" })).toBe(
		"Sep 24, 2026, 2:32 PM UTC",
	);
	expect(formatMoment(at, { year: false, locale: "en-US" })).toBe(
		"Sep 24, 2:32 PM UTC",
	);
});
