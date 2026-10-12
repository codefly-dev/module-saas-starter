import { afterEach, describe, expect, it, vi } from "vitest";
import { defineDataGraph, runDashboard } from "../src/datagraph/dashboard.js";
import { fakeAuditClient } from "./support.js";

const context = { orgId: "org_1" };

const graph = defineDataGraph({
	events: [{ name: "signed_in", type: "user.signed_in.v1" }],
	metrics: [
		{
			id: "events_by_type",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "event_type",
			aggregation: "count",
		},
		{
			id: "events_over_time",
			kind: "source",
			filter: { event: "signed_in" },
			groupBy: "time",
			bucket: "day",
			aggregation: "count",
		},
	],
	dashboards: [
		{
			id: "activity",
			title: "Activity",
			layout: "grid",
			widgets: [
				{
					id: "total",
					metric: "events_by_type",
					visualization: "number",
					title: "Events",
				},
				{ id: "trend", metric: "events_over_time", visualization: "line" },
			],
		},
	],
});

afterEach(() => vi.useRealTimers());

describe("runDashboard", () => {
	it("produces render-ready widget data bound to each metric", async () => {
		// Today is the last day returned, so the series is exactly those days.
		vi.useFakeTimers({ toFake: ["Date"] });
		vi.setSystemTime(new Date("2026-01-02T12:00:00Z"));
		const { client } = fakeAuditClient((request) =>
			request.groupBy === "time"
				? [
						{ key: "2026-01-01", count: 2 },
						{ key: "2026-01-02", count: 5 },
					]
				: [{ key: "user.signed_in.v1", count: 7 }],
		);

		const data = await runDashboard(client, graph, "activity", context);

		expect(data.id).toBe("activity");
		expect(data.layout).toBe("grid");
		expect(data.widgets).toEqual([
			{
				id: "total",
				visualization: "number",
				title: "Events",
				metricId: "events_by_type",
				series: {
					metricId: "events_by_type",
					points: [{ key: "user.signed_in.v1", value: 7 }],
					total: 7,
					coverage: "complete",
					groupBy: "event_type",
					bucket: undefined,
				},
			},
			{
				id: "trend",
				visualization: "line",
				title: undefined,
				metricId: "events_over_time",
				series: {
					metricId: "events_over_time",
					points: [
						{ key: "2026-01-01", value: 2 },
						{ key: "2026-01-02", value: 5 },
					],
					total: 7,
					coverage: "complete",
					groupBy: "time",
					bucket: "day",
				},
			},
		]);
	});

	it("exposes typed per-metric accessors", async () => {
		const { client } = fakeAuditClient(() => [
			{ key: "user.signed_in.v1", count: 7 },
		]);

		const data = await runDashboard(client, graph, "activity", context);

		// `byMetric` is keyed by the declared metric ids — a typo here is a
		// compile error, which is the "typed metric accessors" guarantee.
		expect(data.byMetric.events_by_type.total).toBe(7);
		expect(data.byMetric.events_over_time.metricId).toBe("events_over_time");
	});

	it("carries each widget's metric format, and none for a metric without one", async () => {
		const formatted = defineDataGraph({
			...graph,
			metrics: [{ ...graph.metrics[0], format: "percent" }, graph.metrics[1]],
		});
		const { client } = fakeAuditClient(() => [
			{ key: "user.signed_in.v1", count: 7 },
		]);

		const data = await runDashboard(client, formatted, "activity", context);

		expect(data.widgets.map((widget) => widget.format)).toEqual([
			"percent",
			undefined,
		]);
	});

	// The one conversion boundary: a data graph's `percent` reads a value as a
	// share, the kit's reads it as percentage points, and runDashboard is where
	// the one becomes the other — once, so 0.4 is drawn "40%", never "0.4%".
	it("resolves a percent metric's shares into percentage points, and leaves byMetric as shares", async () => {
		const shares = defineDataGraph({
			events: [{ name: "closed", type: "deal.closed.v1" }],
			metrics: [
				{
					id: "closed",
					kind: "source",
					filter: { event: "closed" },
					groupBy: "event_type",
					aggregation: "count",
				},
				{
					id: "won",
					kind: "source",
					filter: { event: "closed", actor: "winner" },
					groupBy: "event_type",
					aggregation: "count",
				},
				{
					id: "win_rate",
					kind: "derived",
					operation: "ratio",
					inputs: ["won", "closed"],
					format: "percent",
				},
				{
					id: "per_close",
					kind: "derived",
					operation: "ratio",
					inputs: ["won", "closed"],
				},
			],
			dashboards: [
				{
					id: "deals",
					layout: "grid",
					widgets: [
						{ id: "rate", metric: "win_rate", visualization: "number" },
						{ id: "plain", metric: "per_close", visualization: "number" },
					],
				},
			],
		});
		const { client } = fakeAuditClient((request) => [
			{ key: "all", count: request.actorId === "winner" ? 2 : 5 },
		]);

		const data = await runDashboard(client, shares, "deals", context);
		const [rate, plain] = data.widgets;

		expect(rate.format).toBe("percent");
		expect(rate.series.points.map((point) => point.value)).toEqual([40]);
		expect(rate.series.total).toBe(40);
		expect(data.byMetric.win_rate.total).toBe(0.4);
		expect(plain.format).toBeUndefined();
		expect(plain.series.total).toBe(0.4);
	});

	it("scales a shared percent metric once per widget, never twice", async () => {
		const shared = defineDataGraph({
			events: [{ name: "signed_in", type: "user.signed_in.v1" }],
			metrics: [
				{
					id: "won",
					kind: "source",
					filter: { event: "signed_in", actor: "won" },
					groupBy: "event_type",
					aggregation: "count",
				},
				{
					id: "all",
					kind: "source",
					filter: { event: "signed_in", actor: "all" },
					groupBy: "event_type",
					aggregation: "count",
				},
				{
					id: "rate",
					kind: "derived",
					operation: "ratio",
					inputs: ["won", "all"],
					format: "percent",
				},
			],
			dashboards: [
				{
					id: "d",
					layout: "grid",
					widgets: [
						{ id: "a", metric: "rate", visualization: "number" },
						{ id: "b", metric: "rate", visualization: "table" },
					],
				},
			],
		});
		const { client } = fakeAuditClient((request) => [
			{ key: "all", count: request.actorId === "won" ? 2 : 5 },
		]);
		const data = await runDashboard(client, shared, "d", context);
		expect(data.widgets.map((widget) => widget.series.total)).toEqual([40, 40]);
		expect(data.widgets.map((widget) => widget.format)).toEqual([
			"percent",
			"percent",
		]);
		expect(data.byMetric.rate.total).toBeCloseTo(0.4);
	});

	it("carries the dashboard's sections and each widget's section", async () => {
		const sectioned = defineDataGraph({
			...graph,
			dashboards: [
				{
					...graph.dashboards[0],
					sections: [{ id: "totals", title: "Totals" }],
					widgets: graph.dashboards[0].widgets.map((widget) => ({
						...widget,
						section: "totals",
					})),
				},
			],
		});
		const { client } = fakeAuditClient(() => [
			{ key: "user.signed_in.v1", count: 7 },
		]);

		const data = await runDashboard(client, sectioned, "activity", context);

		expect(data.sections).toEqual([{ id: "totals", title: "Totals" }]);
		expect(data.widgets.map((widget) => widget.section)).toEqual(
			data.widgets.map(() => "totals"),
		);
	});

	it("rejects an unknown dashboard id", async () => {
		const { client } = fakeAuditClient(() => []);
		await expect(
			runDashboard(client, graph, "missing", context),
		).rejects.toThrow(/unknown dashboard: missing/);
	});

	it("rejects a widget bound to an undeclared metric", async () => {
		const { client } = fakeAuditClient(() => []);
		const broken = defineDataGraph({
			events: [{ name: "signed_in", type: "user.signed_in.v1" }],
			metrics: [
				{
					id: "known",
					kind: "source",
					filter: { event: "signed_in" },
					groupBy: "event_type",
					aggregation: "count",
				},
			],
			dashboards: [
				{
					id: "d",
					layout: "stack",
					widgets: [{ id: "w", metric: "unknown", visualization: "number" }],
				},
			],
		});

		await expect(runDashboard(client, broken, "d", context)).rejects.toThrow(
			/unknown metric: unknown/,
		);
	});

	it("does not resolve metrics belonging only to other dashboards", async () => {
		const { client, calls } = fakeAuditClient(() => [
			{ key: "user.signed_in.v1", count: 3 },
		]);
		const multi = defineDataGraph({
			events: [{ name: "signed_in", type: "user.signed_in.v1" }],
			metrics: [
				{
					id: "shown",
					kind: "source",
					filter: { event: "signed_in" },
					groupBy: "event_type",
					aggregation: "count",
				},
				// Used only by dashboard "b"; if dashboard "a" resolved the whole
				// graph this metric would issue a second, unrelated RPC.
				{
					id: "unshown",
					kind: "source",
					filter: { event: "signed_in" },
					groupBy: "event_type",
					aggregation: "count_distinct",
					field: "actor_id",
				},
			],
			dashboards: [
				{
					id: "a",
					layout: "grid",
					widgets: [{ id: "w", metric: "shown", visualization: "number" }],
				},
				{
					id: "b",
					layout: "grid",
					widgets: [{ id: "w", metric: "unshown", visualization: "number" }],
				},
			],
		});

		const data = await runDashboard(client, multi, "a", context);

		expect(data.byMetric.shown.total).toBe(3);
		expect(data.byMetric).not.toHaveProperty("unshown");
		expect(calls).toHaveLength(1);
	});
});
