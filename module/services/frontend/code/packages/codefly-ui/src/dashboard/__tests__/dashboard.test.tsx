// @vitest-environment happy-dom
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Dashboard } from "../dashboard.js";
import type {
	DashboardView,
	DashboardWidgetView,
	WidgetSeries,
} from "../types.js";

afterEach(cleanup);

// Two daily buckets: a time-bucketed series, so it earns a trend.
const daily: WidgetSeries = {
	points: [
		{ key: "2026-09-01T00:00:00Z", value: 3 },
		{ key: "2026-09-02T00:00:00Z", value: 5 },
	],
	total: 8,
};

// The same counts grouped by a category: an ordered line through these would
// assert a direction the data does not have.
const byCategory: WidgetSeries = {
	points: [
		{ key: "identity", value: 3 },
		{ key: "security", value: 5 },
	],
	total: 8,
};

const empty: WidgetSeries = { points: [], total: 0, coverage: "empty" };

function view(...widgets: DashboardWidgetView[]): DashboardView {
	return { widgets };
}

function show(...widgets: DashboardWidgetView[]) {
	return render(<Dashboard data={view(...widgets)} />);
}

describe("Dashboard", () => {
	it("renders its header through Section and each series widget through Card", () => {
		const { container } = render(
			<Dashboard
				data={{
					title: "Traffic",
					description: "last 7 days",
					widgets: [
						{ id: "w1", title: "Visits", visualization: "line", series: daily },
					],
				}}
			/>,
		);

		// Section owns the header markup — its <section> root and heading.
		expect(container.querySelector("section")).not.toBeNull();
		expect(
			screen.getByRole("heading", { name: "Traffic", level: 2 }),
		).toBeTruthy();
		expect(screen.getByText("last 7 days")).toBeTruthy();

		// Card owns the surface — one card per series widget, painted by the
		// primitive, with the widget's title as its heading.
		expect(
			container.querySelector(
				".rounded-lg.border.bg-card.p-4.text-card-foreground.shadow-sm",
			),
		).not.toBeNull();
		expect(
			screen.getByRole("heading", { name: "Visits", level: 3 }),
		).toBeTruthy();
	});

	it("scopes the accent override onto the Section root", () => {
		const { container } = render(
			<Dashboard
				data={{
					accent: "hotpink",
					widgets: [{ id: "w1", visualization: "number", series: daily }],
				}}
			/>,
		);
		const section = container.querySelector("section") as HTMLElement;
		expect(section.style.getPropertyValue("--primary")).toBe("hotpink");
	});

	it("says so, rather than drawing an empty grid, when the view has no widgets", () => {
		show();
		expect(screen.getByText("No widgets.")).toBeTruthy();
	});
});

// Each cartesian visualization is drawn by the kit's METRIC charts, not the
// older basic tier. The telling difference is the accessible representation:
// a metric chart hides its SVG from the a11y tree and publishes every exact
// value in a visually-hidden data table captioned with the chart's title. The
// basic tier had neither, which is what made a solution's dashboard read
// plainer than the host's own pages.
describe.each(["line", "area", "bar"] as const)(
	"a %s widget",
	(visualization) => {
		it("draws a metric chart with an accessible data table", () => {
			const { container } = render(
				<Dashboard
					data={view({
						id: "w",
						title: "Events",
						visualization,
						series: byCategory,
					})}
				/>,
			);
			const table = screen.getByRole("table", { name: "Events" });
			expect(within(table).getByRole("row", { name: /identity/ })).toBeTruthy();
			expect(within(table).getByText("3")).toBeTruthy();
			expect(within(table).getByText("5")).toBeTruthy();
			expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe(
				"true",
			);
		});

		it("draws the chart's own empty state instead of an axis with nothing on it", () => {
			const { container } = render(
				<Dashboard
					data={view({
						id: "w",
						title: "Events",
						visualization,
						series: empty,
					})}
				/>,
			);
			expect(
				container.querySelector("figure")?.getAttribute("aria-label"),
			).toBe("Events: no data");
			expect(container.querySelector("svg")).toBeNull();
			// Said twice on purpose, and in two registers: the card badges the
			// coverage beside its title, and the plot says it where the geometry
			// would have been.
			expect(screen.getAllByText("No data")).toHaveLength(2);
		});

		it("badges a partial series on the card, from `series.coverage`", () => {
			render(
				<Dashboard
					data={view({
						id: "w",
						title: "Events",
						visualization,
						series: { ...byCategory, total: null, coverage: "partial" },
					})}
				/>,
			);
			expect(screen.getByText("Partial data")).toBeTruthy();
			// A partial series keeps its observed points: the chart still draws.
			expect(screen.getByRole("table", { name: "Events" })).toBeTruthy();
		});
	},
);

describe("a number widget", () => {
	it("is a stat tile showing the series total in the widget's format", () => {
		show({
			id: "w",
			title: "Revenue",
			visualization: "number",
			series: { points: [{ key: "all", value: 4_200_000 }], total: 4_200_000 },
			format: "currency",
		});
		expect(screen.getByText("Revenue")).toBeTruthy();
		expect(screen.getByText("$4.2M")).toBeTruthy();
	});

	it("appends the widget's unit", () => {
		show({
			id: "w",
			title: "Throughput",
			visualization: "number",
			series: { points: [{ key: "all", value: 42 }], total: 42 },
			unit: "req/s",
		});
		expect(screen.getByText("req/s")).toBeTruthy();
	});

	it("sparklines a time-bucketed metric and deltas its last bucket", () => {
		const { container } = show({
			id: "w",
			title: "Logins",
			visualization: "number",
			series: daily,
		});
		expect(screen.getByText("8")).toBeTruthy();
		expect(container.querySelector("svg")).not.toBeNull();
		// 3 → 5 between the last two buckets.
		expect(screen.getByText(/66\.7%/).textContent).toContain(
			"vs previous bucket",
		);
	});

	it("draws neither a trend nor a delta for a metric grouped by category", () => {
		// A sparkline over "identity, security" would read as a direction, and a
		// delta between them as a change over time. Neither is true of a
		// categorical grouping, whose order is the aggregate's, not time's.
		const { container } = show({
			id: "w",
			title: "Logins",
			visualization: "number",
			series: byCategory,
		});
		expect(screen.getByText("8")).toBeTruthy();
		expect(container.querySelector("svg")).toBeNull();
		expect(screen.queryByText(/%/)).toBeNull();
	});

	it("gives no delta for a rise from zero, which is not a percentage", () => {
		show({
			id: "w",
			visualization: "number",
			series: {
				points: [
					{ key: "2026-09-01T00:00:00Z", value: 0 },
					{ key: "2026-09-02T00:00:00Z", value: 5 },
				],
				total: 5,
			},
		});
		expect(screen.queryByText(/%/)).toBeNull();
	});

	it("shows a dash and says no total, never a 0, when the total is withheld", () => {
		// `total: null` means the pipeline cannot name one number — a distinct
		// count or an average across groups. Rendering it as 0 would report
		// "nothing happened" where the answer is "cannot say".
		show({
			id: "w",
			title: "Distinct actors",
			visualization: "number",
			series: { ...byCategory, total: null },
		});
		expect(screen.getByText("—")).toBeTruthy();
		expect(screen.getByText("No total")).toBeTruthy();
		expect(screen.queryByText("0")).toBeNull();
	});

	it("badges a partial series, which is why its total is withheld", () => {
		show({
			id: "w",
			title: "Logins",
			visualization: "number",
			series: { ...byCategory, total: null, coverage: "partial" },
		});
		expect(screen.getByText("Partial data")).toBeTruthy();
		expect(screen.getByText("—")).toBeTruthy();
		expect(screen.queryByText("No total")).toBeNull();
	});

	it("reads an empty series as no data, not as a zero", () => {
		// The audit aggregate omits a bucket rather than emitting a zero one, so
		// a series with no points means nothing matched.
		show({ id: "w", title: "Logins", visualization: "number", series: empty });
		expect(screen.getByText("No data")).toBeTruthy();
		expect(screen.getByText("—")).toBeTruthy();
		expect(screen.queryByText("0")).toBeNull();
	});

	it("keeps a real zero a zero", () => {
		show({
			id: "w",
			title: "Failures",
			visualization: "number",
			series: { points: [{ key: "all", value: 0 }], total: 0 },
		});
		expect(screen.getByText("0")).toBeTruthy();
		expect(screen.queryByText("—")).toBeNull();
	});
});

describe("a table widget", () => {
	it("lists each point's key and value", () => {
		show({
			id: "w",
			title: "By type",
			visualization: "table",
			series: byCategory,
		});
		const table = screen.getByRole("table", { name: "By type" });
		expect(within(table).getByText("identity")).toBeTruthy();
		expect(within(table).getByText("5")).toBeTruthy();
	});

	it("says there is nothing yet for an empty series", () => {
		show({ id: "w", title: "By type", visualization: "table", series: empty });
		expect(screen.getByText("No data yet.")).toBeTruthy();
	});
});

describe("a widget still being resolved, or that failed", () => {
	it("waits on a null series rather than drawing it as empty", () => {
		const { container } = show(
			{ id: "chart", title: "Logins", visualization: "line", series: null },
			{ id: "tile", title: "Total", visualization: "number", series: null },
		);
		expect(container.querySelectorAll('[data-slot="skeleton"]')).toHaveLength(
			2,
		);
		expect(screen.queryByText("No data")).toBeNull();
	});

	it("says a failed widget failed, and does not wait forever on its missing series", () => {
		// A failed resolution leaves no series behind, so a renderer that tested
		// for the series first would draw every failure as an endless wait.
		const { container } = show(
			{
				id: "chart",
				title: "Logins",
				visualization: "line",
				series: null,
				failed: true,
			},
			{
				id: "tile",
				title: "Total",
				visualization: "number",
				series: null,
				failed: true,
			},
		);
		expect(screen.getByText("Unable to load.")).toBeTruthy();
		expect(screen.getByText("Provider unavailable")).toBeTruthy();
		expect(container.querySelector('[data-slot="skeleton"]')).toBeNull();
	});
});

describe("the default layout", () => {
	it("gathers adjacent scalars into one KPI row and keeps declared order", () => {
		const { container } = show(
			{ id: "a", title: "A", visualization: "number", series: daily },
			{ id: "b", title: "B", visualization: "number", series: daily },
			{ id: "c", title: "C", visualization: "line", series: daily },
			{ id: "d", title: "D", visualization: "number", series: daily },
		);
		const grid = container.querySelector(".grid.items-start");
		expect(grid?.className).toContain("lg:grid-cols-4");
		const tiles = [...container.querySelectorAll("[data-dashboard-tile]")];
		expect(tiles).toHaveLength(4);
		expect(tiles[2].className).toContain("sm:col-span-2");
		expect(tiles[2].className).toContain("col-start-1");
		expect(tiles[3].className).toContain("col-start-1");
		expect(
			[...container.querySelectorAll('[data-slot="metric-label"], h3')].map(
				(node) => node.textContent,
			),
		).toEqual(["A", "B", "C", "D"]);
	});

	it("stacks every widget in a row of its own for a stack layout", () => {
		const { container } = render(
			<Dashboard
				data={{
					layout: "stack",
					widgets: [
						{ id: "a", title: "A", visualization: "number", series: daily },
						{ id: "b", title: "B", visualization: "number", series: daily },
					],
				}}
			/>,
		);
		expect(container.querySelector(".flex.flex-col")?.className).toContain(
			"flex flex-col",
		);
		expect(container.querySelector('[class*="grid-cols-4"]')).toBeNull();
	});
});

// The seams a host composes its own features through. They are what let the
// host's solution dashboard keep its reorderable grid, its + menu and its
// per-widget queries while this renderer still does the drawing.
describe("slots", () => {
	const widgets: DashboardWidgetView[] = [
		{ id: "a", title: "A", visualization: "line", series: daily },
		{ id: "b", title: "B", visualization: "number", series: daily },
	];

	it("puts actions in the dashboard's own header", () => {
		render(
			<Dashboard
				data={{ title: "Activity", widgets }}
				slots={{ actions: <button type="button">Add</button> }}
			/>,
		);
		expect(screen.getByRole("button", { name: "Add" })).toBeTruthy();
	});

	it("hands every widget to renderWidget, in the view's order", () => {
		render(
			<Dashboard
				data={{ widgets }}
				slots={{ renderWidget: (widget) => <li>{widget.id}</li> }}
			/>,
		);
		expect(
			screen.getAllByRole("listitem").map((item) => item.textContent),
		).toEqual(["a", "b"]);
	});

	it("hands the tiles, each with its widget, to a layout of the host's own", () => {
		render(
			<Dashboard
				data={{ widgets }}
				slots={{
					layout: (tiles) => (
						<ul>
							{tiles.map((tile) => (
								<li
									key={tile.id}
									data-visualization={tile.widget.visualization}
								>
									{tile.node}
								</li>
							))}
						</ul>
					),
				}}
			/>,
		);
		const items = screen.getAllByRole("listitem");
		expect(items.map((item) => item.dataset.visualization)).toEqual([
			"line",
			"number",
		]);
		// The nodes are the kit's own cards: the chart's data table is in them.
		expect(within(items[0]).getByRole("table", { name: "A" })).toBeTruthy();
	});

	it("shows the host's own empty state in place of the layout", () => {
		render(
			<Dashboard
				data={{ widgets: [] }}
				slots={{ empty: <p>Add one back with +.</p> }}
			/>,
		);
		expect(screen.getByText("Add one back with +.")).toBeTruthy();
		expect(screen.queryByText("No widgets.")).toBeNull();
	});
});
