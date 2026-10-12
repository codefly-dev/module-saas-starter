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

	it("reaches the token the metric charts actually paint with its accent", () => {
		// The tier change moved where a mark's colour comes from: a metric
		// chart colours its first series `var(--chart-1)`, not `--primary`,
		// so an accent that set only `--primary` stopped colouring the charts
		// it exists for. Asserted on the rendered mark, not just the variable.
		const { container } = render(
			<Dashboard
				data={{
					accent: "hotpink",
					widgets: [{ id: "line", visualization: "line", series: byCategory }],
				}}
			/>,
		);
		expect(
			container.querySelector('path[stroke="var(--chart-1)"]'),
		).not.toBeNull();
		const section = container.querySelector("section") as HTMLElement;
		expect(section.style.getPropertyValue("--chart-1")).toBe("hotpink");
		expect(section.style.getPropertyValue("--primary")).toBe("hotpink");
	});

	it("reaches a scalar's own trend with the accent, not just the charts", () => {
		// A declared accent that coloured the line charts but not the
		// sparklines beside them drew one dashboard in two palettes.
		const { container } = render(
			<Dashboard
				data={{
					accent: "hotpink",
					widgets: [{ id: "total", visualization: "number", series: daily }],
				}}
			/>,
		);
		// The delta draws a lucide arrow, so the sparkline is not the first svg.
		const [trend] = [...container.querySelectorAll("svg")].filter(
			(svg) => !svg.classList.contains("lucide"),
		);
		expect((trend as SVGElement).style.color).toBe("var(--chart-1)");
		expect(
			(
				container.querySelector("section") as HTMLElement
			).style.getPropertyValue("--chart-1"),
		).toBe("hotpink");
	});

	it("keeps a titled scalar reachable by heading navigation", () => {
		// It was a Card with an <h3>; a StatTile's label is a span, so the
		// title left the document outline when the tier changed.
		render(
			<Dashboard
				data={view({
					id: "total",
					title: "Total events",
					visualization: "number",
					series: { points: [{ key: "all", value: 5 }], total: 5 },
				})}
			/>,
		);
		expect(
			screen.getByRole("heading", { name: "Total events", level: 3 }),
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
		// A line or area chart's one series colours from the palette's last token.
		expect(section.style.getPropertyValue("--chart-5")).toBe("hotpink");
	});

	it("draws a line widget's series in the palette's last colour, which the accent sets", () => {
		const { container } = render(
			<Dashboard
				data={{
					accent: "hotpink",
					widgets: [
						{
							id: "w1",
							title: "Visits",
							visualization: "line",
							series: {
								points: [
									{ key: "2026-09-01", value: 1 },
									{ key: "2026-09-02", value: 3 },
								],
								total: 4,
							},
						},
					],
				}}
			/>,
		);
		expect(
			container.querySelector('path[stroke="var(--chart-5)"]'),
		).toBeTruthy();
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

	it("deltas the two latest buckets whatever order the series arrives in", () => {
		// The resolver appends one point per bucket in the order the aggregate
		// returned them, with no sort, and a delta's sign is drawn as a colour
		// — so reading "previous" and "latest" off the last two array positions
		// would paint a decline green for a series delivered newest-first.
		const forward = [
			{ key: "2026-09-01T00:00:00Z", value: 10 },
			{ key: "2026-09-02T00:00:00Z", value: 5 },
		];
		const reading = (points: typeof forward) => {
			const { unmount } = show({
				id: "w",
				title: "Logins",
				visualization: "number",
				series: { points, total: 15 },
			});
			const text = screen.getByText(/%/).textContent;
			unmount();
			return text;
		};
		const ordered = reading(forward);
		expect(ordered).toContain("50%");
		// A halving, drawn as a fall, not a doubling drawn as a rise.
		expect(ordered).toContain("−");
		expect(reading([...forward].reverse())).toBe(ordered);
	});

	it("orders the sparkline by time too, so the trend and the delta agree", () => {
		const { container } = show({
			id: "w",
			visualization: "number",
			series: {
				points: [
					{ key: "2026-09-03T00:00:00Z", value: 9 },
					{ key: "2026-09-01T00:00:00Z", value: 1 },
					{ key: "2026-09-02T00:00:00Z", value: 5 },
				],
				total: 15,
			},
		});
		// The sparkline's path rises monotonically: 1, 5, 9 in time order, not
		// 9, 1, 5 in array order.
		const paths = [...container.querySelectorAll("svg path")];
		const d = paths[paths.length - 1]?.getAttribute("d") ?? "";
		const ys = [...d.matchAll(/[ML][\d.]+,([\d.]+)/g)].map((m) => Number(m[1]));
		expect(ys).toHaveLength(3);
		// Smaller y is higher on screen, so a rising series falls in y.
		expect(ys[0]).toBeGreaterThan(ys[1]);
		expect(ys[1]).toBeGreaterThan(ys[2]);
	});

	it("reads `format: percent` as an already-scaled percentage, not a share", () => {
		// The field is bound structurally, so nothing on the wire distinguishes
		// a producer whose own `percent` means a 0-to-1 share. This pins the
		// kit's convention: a producer with the other one must scale first, and
		// finds out here rather than by shipping a hundredfold error.
		show({
			id: "w",
			title: "MFA adoption",
			visualization: "number",
			series: { points: [{ key: "all", value: 45 }], total: 45 },
			format: "percent",
		});
		expect(screen.getByText("45%")).toBeTruthy();
		expect(screen.queryByText("4,500%")).toBeNull();
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

	it("keeps a series a failed refresh left behind, and marks it stale", () => {
		// A refresh that did not land is not an absence of data. Blanking the
		// tile would throw away values that are still the best available.
		const { container } = show(
			{
				id: "chart",
				title: "Logins",
				visualization: "line",
				series: byCategory,
				failure: "failed",
			},
			{
				id: "tile",
				title: "Total",
				visualization: "number",
				series: daily,
				failure: "failed",
			},
		);
		expect(screen.queryByText("Unable to load.")).toBeNull();
		expect(screen.getAllByText("Stale")).toHaveLength(2);
		expect(screen.getByRole("table", { name: "Logins" })).toBeTruthy();
		expect(screen.getByText("8")).toBeTruthy();
		expect(container.querySelector('[data-slot="skeleton"]')).toBeNull();
	});

	it("keeps an empty series valueless through a failed refresh", () => {
		// The badge is one slot and freshness takes it, so deriving the value's
		// presence from the state turned a known-empty scalar into a reported
		// zero the moment its refresh failed.
		const widget: DashboardWidgetView = {
			id: "total",
			visualization: "number",
			series: empty,
		};
		const { rerender } = render(<Dashboard data={view(widget)} />);
		expect(screen.getByText("—")).toBeTruthy();
		expect(screen.getByText("No data")).toBeTruthy();

		rerender(<Dashboard data={view({ ...widget, failure: "failed" })} />);
		expect(screen.getByText("Stale")).toBeTruthy();
		expect(screen.getByText("—")).toBeTruthy();
		expect(screen.queryByText("0")).toBeNull();
	});

	it("keeps a partial series identifiable through a failed refresh", () => {
		show({
			id: "w",
			title: "Logins",
			visualization: "number",
			series: { ...byCategory, total: null, coverage: "partial" },
			failure: "failed",
		});
		// Freshness takes the badge, but the withheld total still reads as one.
		expect(screen.getByText("Stale")).toBeTruthy();
		expect(screen.getByText("—")).toBeTruthy();
		expect(screen.queryByText("8")).toBeNull();
	});

	it("discards a series the caller retained on a refused read", () => {
		// A number this viewer may not see is a WRONG answer, not an old one.
		// The host is expected to withhold it; the renderer that owns what a
		// refusal looks like fails closed too, so a caller that forwards
		// retained values by mistake cannot show them through this.
		const { container } = show(
			{
				id: "chart",
				title: "Logins",
				visualization: "line",
				series: byCategory,
				failure: "refused",
			},
			{
				id: "tile",
				title: "Total",
				visualization: "number",
				series: daily,
				failure: "refused",
			},
		);
		expect(screen.queryByText("8")).toBeNull();
		expect(container.querySelector("svg")).toBeNull();
		expect(screen.queryByRole("table", { name: "Logins" })).toBeNull();
		expect(screen.getAllByText("No access")).toHaveLength(2);
		expect(
			screen.getByText("You don't have permission to see this."),
		).toBeTruthy();
		expect(screen.queryByText("Unable to load.")).toBeNull();
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
				failure: "failed",
			},
			{
				id: "tile",
				title: "Total",
				visualization: "number",
				series: null,
				failure: "failed",
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
		// Two tiles, then a chart, then a tile: three runs, the KPI row's own
		// four-up geometry only around the adjacent pair.
		const rows = [
			...container.querySelectorAll('section > div > div[class*="grid"]'),
		];
		expect(rows).toHaveLength(3);
		expect(rows[0].className).toContain("lg:grid-cols-4");
		expect(rows[0].children).toHaveLength(2);
		expect(rows[1].className).toContain("sm:grid-cols-2");
		expect(rows[2].children).toHaveLength(1);
		expect(
			[...container.querySelectorAll('[data-slot="metric-label"], h3')].map(
				(node) => node.textContent,
			),
		).toEqual(["A", "B", "C", "D"]);
	});

	it("keeps a scalar that declares a span in the grid, with its span", () => {
		// A span is a request for grid width, which the KPI row's own geometry
		// cannot grant, so a scalar that declares one stays a grid item rather
		// than having the field silently dropped.
		const { container } = render(
			<Dashboard
				data={{
					columns: 2,
					widgets: [
						{
							id: "wide",
							title: "W",
							visualization: "number",
							series: daily,
							span: 2,
						},
						{ id: "plain", title: "P", visualization: "number", series: daily },
					],
				}}
			/>,
		);
		const rows = [
			...container.querySelectorAll('section > div > div[class*="grid"]'),
		];
		expect(rows).toHaveLength(2);
		// The spanning scalar is a card in the grid...
		expect(rows[0].className).toContain("sm:grid-cols-2");
		expect(rows[0].firstElementChild?.className).toContain("sm:col-span-2");
		// ...and only the one that asked for nothing becomes a headline number.
		expect(rows[1].className).toContain("lg:grid-cols-2");
	});

	it("caps the KPI row at the dashboard's declared column count", () => {
		const { container } = render(
			<Dashboard
				data={{
					columns: 2,
					widgets: [
						{ id: "a", title: "A", visualization: "number", series: daily },
						{ id: "b", title: "B", visualization: "number", series: daily },
					],
				}}
			/>,
		);
		const row = container.querySelector(
			'section > div > div[class*="grid"]',
		) as HTMLElement;
		expect(row.className).toContain("lg:grid-cols-2");
		expect(row.className).not.toContain("lg:grid-cols-4");
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
		expect(container.querySelector("section > div")?.className).toContain(
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

it("writes a percent widget's values as percentages and a plain one as a number", () => {
	render(
		<Dashboard
			data={{
				widgets: [
					{
						id: "rate",
						visualization: "number",
						format: "percent",
						series: { points: [{ key: "all", value: 0.4 }], total: 0.4 },
					},
					{
						id: "outcomes",
						visualization: "bar",
						format: "percent",
						series: {
							points: [
								{ key: "won", value: 0.25 },
								{ key: "lost", value: 0.75 },
							],
							total: 1,
						},
					},
					{
						id: "versions",
						visualization: "number",
						series: { points: [{ key: "all", value: 2.25 }], total: 2.25 },
					},
				],
			}}
		/>,
	);
	expect(screen.getByText("40%")).toBeTruthy();
	expect(screen.getByText("25%")).toBeTruthy();
	expect(screen.getByText("75%")).toBeTruthy();
	expect(screen.getByText((2.25).toLocaleString())).toBeTruthy();
	expect(screen.queryByText("0.4")).toBeNull();
});
