import { describe, expect, it } from "vitest";
import {
	areaPath,
	axisPositions,
	type ChartSeries,
	linearScale,
	linePath,
	niceTicks,
	resolveSeries,
	stackedExtent,
	stackSeries,
	unionLabels,
	valuesExtent,
} from "../metric-geometry.js";

describe("unionLabels", () => {
	it("returns a single series' labels in order", () => {
		const series: ChartSeries[] = [
			{
				name: "a",
				data: [
					{ label: "Mon", value: 1 },
					{ label: "Tue", value: 2 },
				],
			},
		];
		expect(unionLabels(series)).toEqual(["Mon", "Tue"]);
	});

	it("unions differing bucket sets in first-seen order", () => {
		const series: ChartSeries[] = [
			{
				name: "a",
				data: [
					{ label: "Mon", value: 1 },
					{ label: "Tue", value: 2 },
				],
			},
			{
				name: "b",
				data: [
					{ label: "Mon", value: 3 },
					{ label: "Wed", value: 4 },
				],
			},
		];
		expect(unionLabels(series)).toEqual(["Mon", "Tue", "Wed"]);
	});
});

describe("resolveSeries", () => {
	it("aligns each series to the shared axis by label, gapping missing ones", () => {
		const series: ChartSeries[] = [
			{
				name: "a",
				data: [
					{ label: "Mon", value: 1 },
					{ label: "Tue", value: 2 },
				],
			},
			{
				name: "b",
				data: [
					{ label: "Mon", value: 3 },
					{ label: "Wed", value: 4 },
				],
			},
		];
		const labels = unionLabels(series);
		expect(resolveSeries(series, labels)).toEqual([
			{ name: "a", values: [1, 2, null] },
			{ name: "b", values: [3, null, 4] },
		]);
	});

	it("does not confuse a real zero with a missing bucket", () => {
		const series: ChartSeries[] = [
			{ name: "a", data: [{ label: "Mon", value: 0 }] },
		];
		expect(resolveSeries(series, ["Mon", "Tue"])).toEqual([
			{ name: "a", values: [0, null] },
		]);
	});

	it("drops non-finite values to null at the boundary", () => {
		const series: ChartSeries[] = [
			{
				name: "a",
				data: [
					{ label: "Mon", value: Number.NaN },
					{ label: "Tue", value: Number.POSITIVE_INFINITY },
					{ label: "Wed", value: 5 },
				],
			},
		];
		expect(resolveSeries(series, ["Mon", "Tue", "Wed"])).toEqual([
			{ name: "a", values: [null, null, 5] },
		]);
	});
});

describe("valuesExtent", () => {
	it("always anchors the domain at zero for positive data", () => {
		expect(valuesExtent([{ name: "a", values: [4, 9] }])).toEqual([0, 9]);
	});

	it("extends below zero for negative values", () => {
		expect(valuesExtent([{ name: "a", values: [-3, 5] }])).toEqual([-3, 5]);
	});

	it("spans every series and ignores gaps", () => {
		expect(
			valuesExtent([
				{ name: "a", values: [2, null] },
				{ name: "b", values: [null, 11] },
			]),
		).toEqual([0, 11]);
	});

	it("gives an all-zero or all-gap series a non-degenerate range", () => {
		expect(valuesExtent([{ name: "a", values: [0, null] }])).toEqual([0, 1]);
	});
});

describe("niceTicks", () => {
	it("produces rounded, ordered ticks bracketing the range", () => {
		const ticks = niceTicks(0, 93);
		expect(ticks[0]).toBe(0);
		expect(ticks[ticks.length - 1]).toBeGreaterThanOrEqual(93);
		for (let i = 1; i < ticks.length; i++) {
			expect(ticks[i]).toBeGreaterThan(ticks[i - 1]);
		}
	});

	it("lands on 1/2/5 · 10ⁿ boundaries", () => {
		expect(niceTicks(0, 100)).toEqual([0, 20, 40, 60, 80, 100]);
	});

	it("does not leave floating-point dust on the ticks", () => {
		for (const tick of niceTicks(0, 3)) {
			expect(Number.isInteger(tick * 1000)).toBe(true);
		}
	});

	it("collapses a zero-width range to a single tick", () => {
		expect(niceTicks(5, 5)).toEqual([5]);
	});
});

describe("linearScale", () => {
	it("maps the domain onto an inverted pixel range", () => {
		const scale = linearScale(0, 10, 100, 0);
		expect(scale(0)).toBe(100);
		expect(scale(10)).toBe(0);
		expect(scale(5)).toBe(50);
	});

	it("never divides by zero on a flat domain", () => {
		const scale = linearScale(4, 4, 0, 100);
		expect(Number.isFinite(scale(4))).toBe(true);
	});
});

describe("axisPositions", () => {
	it("puts the first and last point on the edges of a point scale", () => {
		const xs = axisPositions(3, 0, 100);
		expect(xs).toEqual([0, 50, 100]);
	});

	it("centers a single point", () => {
		expect(axisPositions(1, 0, 100)).toEqual([50]);
	});

	it("insets band centers by half a slot", () => {
		expect(axisPositions(2, 0, 100, true)).toEqual([25, 75]);
	});
});

describe("path builders", () => {
	it("threads a move then line commands through the points", () => {
		expect(
			linePath([
				{ x: 0, y: 0 },
				{ x: 10, y: 5 },
			]),
		).toBe("M0.00,0.00 L10.00,5.00");
	});

	it("closes an area down to the baseline", () => {
		const path = areaPath(
			[
				{ x: 0, y: 0 },
				{ x: 10, y: 5 },
			],
			20,
		);
		expect(path.startsWith("M0.00,0.00")).toBe(true);
		expect(path.endsWith("Z")).toBe(true);
		expect(path).toContain("L10.00,20.00");
		expect(path).toContain("L0.00,20.00");
	});
});

describe("stackSeries", () => {
	const resolved = (name: string, values: (number | null)[]) => ({
		name,
		values,
	});

	it("stacks each series on the running total beneath it", () => {
		const [a, b, c] = stackSeries([
			resolved("a", [1, 2, 3]),
			resolved("b", [10, 10, 10]),
			resolved("c", [100, 0, 100]),
		]);
		expect(a.base).toEqual([0, 0, 0]);
		expect(a.top).toEqual([1, 2, 3]);
		expect(b.base).toEqual([1, 2, 3]);
		expect(b.top).toEqual([11, 12, 13]);
		expect(c.top).toEqual([111, 12, 113]);
	});

	// A tooltip and the accessible table report what a series CONTRIBUTED,
	// never the height it is drawn at; the draw height is `top`, not `values`.
	it("keeps each series' own values untouched", () => {
		const [, b] = stackSeries([resolved("a", [5, 5]), resolved("b", [1, 2])]);
		expect(b.values).toEqual([1, 2]);
	});

	// A gap is "no data", not "fell to zero": the band stays flat at its base
	// and the series above are not pulled down.
	it("treats a gap as contributing nothing", () => {
		const [a, b] = stackSeries([
			resolved("a", [4, null, 4]),
			resolved("b", [1, 1, 1]),
		]);
		expect(a.top).toEqual([4, 0, 4]);
		expect(b.base).toEqual([4, 0, 4]);
		expect(b.top).toEqual([5, 1, 5]);
	});

	it("scales a stacked chart to its tallest column, from zero", () => {
		const stacked = stackSeries([
			resolved("a", [1, 50]),
			resolved("b", [60, 1]),
		]);
		expect(stackedExtent(stacked)).toEqual([0, 61]);
	});

	it("handles no series", () => {
		expect(stackSeries([])).toEqual([]);
		expect(stackedExtent([])).toEqual([0, 0]);
	});
});
