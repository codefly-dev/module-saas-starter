import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
	formatMetricValue,
	KPIRow,
	type Metric,
	MetricCard,
	StatTile,
} from "./metric-tiles";

describe("formatMetricValue", () => {
	it.each([
		[1284, "number", "1,284"],
		[12900, "compact", "12.9K"],
		[4_200_000, "currency", "$4.2M"],
		[0.128, "percent", "12.8%"],
	] satisfies [number, Parameters<typeof formatMetricValue>[1], string][])(
		"formats %d as %s → %s",
		(value, format, expected) => {
			expect(formatMetricValue(value, format)).toBe(expected);
		},
	);
});

describe("StatTile", () => {
	it("renders the label and formatted value", () => {
		render(
			<StatTile
				metric={{ label: "Active users", value: 12900, format: "compact" }}
			/>,
		);
		expect(screen.getByText("Active users")).toBeTruthy();
		expect(screen.getByText("12.9K")).toBeTruthy();
	});

	it("appends a unit suffix", () => {
		render(
			<StatTile metric={{ label: "Throughput", value: 42, unit: "req/s" }} />,
		);
		expect(screen.getByText("req/s")).toBeTruthy();
	});

	it("colors a rising delta green when higher is better", () => {
		render(
			<StatTile
				metric={{
					label: "Signups",
					value: 100,
					delta: 0.12,
					deltaLabel: "vs last week",
				}}
			/>,
		);
		const delta = screen.getByText(/12%/);
		expect(delta.className).toContain("text-emerald-600");
		expect(screen.getByText("vs last week")).toBeTruthy();
	});

	it("colors a rising delta red when higher is worse", () => {
		render(
			<StatTile
				metric={{
					label: "Error rate",
					value: 3,
					delta: 0.2,
					higherIsBetter: false,
				}}
			/>,
		);
		expect(screen.getByText(/20%/).className).toContain("text-destructive");
	});

	it("colors a falling delta red when higher is better", () => {
		render(<StatTile metric={{ label: "Revenue", value: 3, delta: -0.1 }} />);
		expect(screen.getByText(/10%/).className).toContain("text-destructive");
	});

	it("renders a dash for states without a value and shows the state badge", () => {
		render(
			<StatTile metric={{ label: "Latency", value: 0, state: "no_data" }} />,
		);
		expect(screen.getByText("—")).toBeTruthy();
		expect(screen.getByText("No data")).toBeTruthy();
	});

	it("shows a skeleton instead of a value while loading", () => {
		const { container } = render(
			<StatTile metric={{ label: "Latency", value: 0, state: "loading" }} />,
		);
		expect(screen.queryByText("0")).toBeNull();
		expect(container.querySelector('[data-slot="skeleton"]')).toBeTruthy();
	});

	it("draws a sparkline when a trend series is provided", () => {
		const { container } = render(
			<StatTile metric={{ label: "MAU", value: 5, series: [1, 3, 2, 5] }} />,
		);
		expect(container.querySelector("svg")).toBeTruthy();
	});

	it("omits the sparkline for a series shorter than two points", () => {
		const { container } = render(
			<StatTile metric={{ label: "MAU", value: 5, series: [5] }} />,
		);
		expect(container.querySelector("svg")).toBeNull();
	});
});

describe("MetricCard", () => {
	it("renders the provenance footer when provenance is provided", () => {
		render(
			<MetricCard
				metric={{
					label: "Accepted usage",
					value: 1000,
					format: "compact",
					provenance: {
						source: "UsageService",
						observedAt: "2026-07-28T10:00:00Z",
						owner: "product",
					},
				}}
			/>,
		);
		expect(screen.getByText("Accepted usage")).toBeTruthy();
		expect(screen.getByText("Source: UsageService")).toBeTruthy();
		expect(screen.getByText("Owner: product")).toBeTruthy();
		expect(screen.getByText(/Freshness:/)).toBeTruthy();
	});
});

describe("KPIRow", () => {
	it("renders one tile per metric", () => {
		const metrics: Metric[] = [
			{ label: "Users", value: 10 },
			{ label: "Orgs", value: 4 },
			{ label: "Revenue", value: 4_200_000, format: "currency" },
		];
		render(<KPIRow metrics={metrics} />);
		expect(screen.getByText("Users")).toBeTruthy();
		expect(screen.getByText("Orgs")).toBeTruthy();
		expect(screen.getByText("$4.2M")).toBeTruthy();
	});
});
