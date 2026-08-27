import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AreaChart, BarChart, chartSeriesColor, LineChart } from "./chart";
import type { ChartSeries } from "./geometry";

const single: ChartSeries[] = [
	{
		name: "Events",
		data: [
			{ label: "Mon", value: 12 },
			{ label: "Tue", value: 30 },
			{ label: "Wed", value: 18 },
		],
	},
];

const multi: ChartSeries[] = [
	{
		name: "Sign-ups",
		data: [
			{ label: "Jan", value: 5 },
			{ label: "Feb", value: 8 },
		],
	},
	{
		name: "Churn",
		data: [
			{ label: "Jan", value: 2 },
			{ label: "Feb", value: 3 },
		],
	},
];

describe.each([
	["LineChart", LineChart],
	["AreaChart", AreaChart],
	["BarChart", BarChart],
])("%s", (_name, Chart) => {
	it("exposes every value in an accessible data table", () => {
		render(<Chart series={single} title="Events over time" />);
		const table = screen.getByRole("table", { name: "Events over time" });
		// Exact values are reachable without hovering the SVG.
		expect(within(table).getByText("12")).toBeTruthy();
		expect(within(table).getByText("30")).toBeTruthy();
		expect(within(table).getByRole("row", { name: /Mon/ })).toBeTruthy();
	});

	it("labels the figure and hides the decorative SVG from the a11y tree", () => {
		const { container } = render(
			<Chart series={single} title="Events over time" />,
		);
		expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe(
			"true",
		);
	});

	it("keys each series in a legend when there are two or more", () => {
		render(<Chart series={multi} title="Growth" />);
		const legend = screen.getByRole("list");
		expect(within(legend).getByText("Sign-ups")).toBeTruthy();
		expect(within(legend).getByText("Churn")).toBeTruthy();
	});

	it("omits the legend for a single series", () => {
		render(<Chart series={single} title="Events over time" />);
		expect(screen.queryByRole("list")).toBeNull();
	});

	it("renders an empty state instead of an axis when there is no data", () => {
		const { container } = render(
			<Chart series={[]} title="Events over time" />,
		);
		expect(screen.getByText("No data")).toBeTruthy();
		expect(container.querySelector("svg")).toBeNull();
	});

	it("formats values through a custom formatter", () => {
		render(
			<Chart
				series={single}
				title="Revenue"
				formatValue={(value) => `$${value}`}
			/>,
		);
		expect(screen.getByRole("cell", { name: "$12" })).toBeTruthy();
	});
});

describe("chartSeriesColor", () => {
	it("assigns the palette in fixed order", () => {
		expect(chartSeriesColor(0)).toBe("var(--chart-1)");
		expect(chartSeriesColor(4)).toBe("var(--chart-5)");
	});

	it("wraps a sixth series back to the first token", () => {
		expect(chartSeriesColor(5)).toBe("var(--chart-1)");
	});
});

function svgTexts(container: HTMLElement): SVGTextElement[] {
	return Array.from(container.querySelectorAll("svg text"));
}

describe("bar-chart x-axis alignment", () => {
	it("centers each x-axis label under its band, not on the point scale", () => {
		const { container } = render(
			<BarChart
				series={[
					{
						name: "Events",
						data: [
							{ label: "Mon", value: 12 },
							{ label: "Tue", value: 30 },
							{ label: "Wed", value: 18 },
						],
					},
				]}
				title="Events"
			/>,
		);
		const bars = Array.from(
			container.querySelectorAll<SVGPathElement>("svg path"),
		);
		const firstBar = bars[0].getAttribute("d") ?? "";
		const xs = Array.from(firstBar.matchAll(/[ML]([\d.]+),/g)).map((m) =>
			Number(m[1]),
		);
		const barCenter = (Math.min(...xs) + Math.max(...xs)) / 2;

		const monLabel = svgTexts(container).find((t) => t.textContent === "Mon");
		const labelX = Number(monLabel?.getAttribute("x"));

		// The bug plotted the label at the point-scale origin (plotLeft = 48)
		// while the bar sat at the band centre (144); they must now coincide.
		expect(labelX).not.toBeCloseTo(48, 0);
		expect(labelX).toBeCloseTo(barCenter, 1);
	});
});

describe.each([
	["LineChart", LineChart],
	["AreaChart", AreaChart],
	["BarChart", BarChart],
])("%s with mismatched series", (_name, Chart) => {
	const ragged: ChartSeries[] = [
		{ name: "a", data: [{ label: "Mon", value: 1 }] },
		{
			name: "b",
			data: [
				{ label: "Mon", value: 2 },
				{ label: "Tue", value: 3 },
			],
		},
	];

	it("does not crash and never emits NaN geometry", () => {
		const { container } = render(<Chart series={ragged} title="Ragged" />);
		expect(container.querySelector("svg")).toBeTruthy();
		expect(container.innerHTML).not.toContain("NaN");
	});

	it("plots each value under its own label instead of shifting by index", () => {
		render(<Chart series={ragged} title="Ragged" />);
		const table = screen.getByRole("table", { name: "Ragged" });
		const monRow = within(table).getByRole("row", { name: /Mon/ });
		expect(within(monRow).getByText("1")).toBeTruthy();
		expect(within(monRow).getByText("2")).toBeTruthy();
		// "b" has a Tue point, "a" does not — the gap is marked, not faked as 0.
		const tueRow = within(table).getByRole("row", { name: /Tue/ });
		expect(within(tueRow).getByText("3")).toBeTruthy();
		expect(within(tueRow).getByText("—")).toBeTruthy();
	});
});

describe("non-finite values", () => {
	it("drops NaN/Infinity to a gap rather than rendering NaN coordinates", () => {
		const { container } = render(
			<LineChart
				series={[
					{
						name: "rate",
						data: [
							{ label: "Mon", value: 5 },
							{ label: "Tue", value: Number.NaN },
							{ label: "Wed", value: Number.POSITIVE_INFINITY },
							{ label: "Thu", value: 8 },
						],
					},
				]}
				title="Rate"
			/>,
		);
		expect(container.innerHTML).not.toContain("NaN");
		const table = screen.getByRole("table", { name: "Rate" });
		expect(within(table).getAllByText("—")).toHaveLength(2);
	});
});

describe("axis formatting", () => {
	it("labels the value axis with the same formatter as the tooltip and table", () => {
		const { container } = render(
			<LineChart
				series={[
					{
						name: "Revenue",
						data: [
							{ label: "Mon", value: 10 },
							{ label: "Tue", value: 20 },
						],
					},
				]}
				title="Revenue"
				formatValue={(value) => `$${value}`}
			/>,
		);
		const tickLabels = svgTexts(container).map((t) => t.textContent ?? "");
		expect(tickLabels.some((label) => label.startsWith("$"))).toBe(true);
	});
});
