// @vitest-environment happy-dom
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { AreaChart, LineChart } from "../metric-chart.js";

afterEach(cleanup);

const series = [
	{
		name: "identity",
		data: [
			{ label: "2026-09-01T00:00:00Z", value: 3 },
			{ label: "2026-09-02T00:00:00Z", value: 5 },
		],
	},
	{
		name: "security",
		data: [
			{ label: "2026-09-01T00:00:00Z", value: 1 },
			{ label: "2026-09-02T00:00:00Z", value: 2 },
		],
	},
];

describe("the metric chart frame's x axis", () => {
	// The tooltip formatted its key while the axis drew the raw one, so a daily
	// series read "2026-09-20T00:0" clipped at the plot's edge on screen.
	it("time-formats ISO bucket keys, as the tooltip already did", () => {
		const { container } = render(
			<AreaChart series={series} title="Events" stacked />,
		);
		const texts = [...container.querySelectorAll("svg text")].map(
			(t) => t.textContent,
		);
		expect(texts).toContain("Sep 1");
		expect(texts).toContain("Sep 2");
		expect(texts.some((t) => t?.includes("2026-"))).toBe(false);
	});
});

describe("the metric chart's x-axis labels", () => {
	// Thirty daily buckets show every fourth label plus the last. The fourth-
	// stride label before the last (Sep 29) sat one day from it (Sep 30), and
	// the two drew over each other at the plot's right edge.
	it("never draws the last label on top of the one before it", () => {
		const data = Array.from({ length: 30 }, (_, i) => ({
			label: `2026-09-${String(1 + i).padStart(2, "0")}T00:00:00Z`,
			value: i,
		}));
		const { container } = render(
			<LineChart series={[{ name: "daily", data }]} title="Daily" />,
		);
		const texts = [...container.querySelectorAll("svg text")].map(
			(t) => t.textContent,
		);
		expect(texts).toContain("Sep 1");
		expect(texts).toContain("Sep 30");
		expect(texts).not.toContain("Sep 29");
	});
});
