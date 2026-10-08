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

it("preserves sections, columns and widget spans from the SDK", () => {
	const data = {
		columns: 3 as const,
		sections: [{ id: "summary", title: "Summary", columns: 4 as const }],
		widgets: [
			{
				id: "total",
				visualization: "number" as const,
				span: 2 as const,
				section: "summary",
				series: { points: [], total: 0 },
			},
		],
	};
	const view = fromDashboardData(data);
	expect(view.columns).toBe(3);
	expect(view.sections).toEqual(data.sections);
	expect(view.widgets[0]).toMatchObject({ span: 2, section: "summary" });
	expect(fromDashboardData(data, { columns: 1 }).columns).toBe(1);
});
