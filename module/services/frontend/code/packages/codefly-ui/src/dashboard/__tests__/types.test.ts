import { describe, expect, it } from "vitest";
import { fromDashboardData } from "../types.js";

// The one seam a consumer crosses between @codefly-dev/saas-sdk's data runtime and
// this component kit: runDashboard's result → the renderer's view model.
describe("fromDashboardData", () => {
	it("maps a resolved data-graph dashboard to the view model", () => {
		const view = fromDashboardData(
			{
				title: "Activity",
				layout: "grid",
				widgets: [
					{
						id: "trend",
						visualization: "line",
						title: "Logins over time",
						series: { points: [{ key: "2026-01-01", value: 3 }], total: 3 },
					},
				],
			},
			{ description: "from a data graph", columns: 3, accent: "#7c3aed" },
		);
		expect(view.title).toBe("Activity");
		expect(view.layout).toBe("grid");
		expect(view.columns).toBe(3);
		expect(view.accent).toBe("#7c3aed");
		expect(view.widgets).toHaveLength(1);
		expect(view.widgets[0]).toMatchObject({
			id: "trend",
			visualization: "line",
		});
		// `fromDashboardData` bridges an ALREADY-resolved result, so the series
		// it produces is never the `null` the view model allows for a widget
		// still being resolved.
		expect(view.widgets[0].series?.total).toBe(3);
	});

	it("defaults layout to grid when the source omits it", () => {
		const view = fromDashboardData({ widgets: [] });
		expect(view.layout).toBe("grid");
		expect(view.widgets).toEqual([]);
	});
});

describe("fromDashboardData's sections", () => {
	it("carries the sections and each widget's section through", () => {
		const view = fromDashboardData({
			sections: [{ id: "totals", title: "Totals", description: "All time" }],
			widgets: [
				{
					id: "total",
					visualization: "number",
					series: { points: [{ key: "all", value: 1 }], total: 1 },
					section: "totals",
				},
			],
		});
		expect(view.sections).toEqual([
			{ id: "totals", title: "Totals", description: "All time" },
		]);
		expect(view.widgets[0].section).toBe("totals");
	});

	it("forwards a percent widget's values as they are: the producer has scaled them", () => {
		const view = fromDashboardData({
			widgets: [
				{
					id: "rate",
					visualization: "number",
					series: { points: [{ key: "all", value: 40 }], total: 40 },
					format: "percent",
				},
			],
		});
		expect(view.widgets[0]).toMatchObject({ format: "percent" });
		expect(view.widgets[0].series?.total).toBe(40);
	});
});
