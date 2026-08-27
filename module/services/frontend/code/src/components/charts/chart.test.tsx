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
