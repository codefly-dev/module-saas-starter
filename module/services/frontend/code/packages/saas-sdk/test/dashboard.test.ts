import { describe, expect, it } from "vitest";
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

describe("runDashboard", () => {
	it("produces render-ready widget data bound to each metric", async () => {
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
});
