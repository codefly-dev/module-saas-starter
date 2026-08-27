import { describe, expect, it } from "vitest";
import {
	areaPath,
	axisPositions,
	type ChartSeries,
	linearScale,
	linePath,
	niceTicks,
	valueExtent,
} from "./geometry";

describe("valueExtent", () => {
	it("always anchors the domain at zero for positive data", () => {
		const series: ChartSeries[] = [
			{
				name: "a",
				data: [
					{ label: "x", value: 4 },
					{ label: "y", value: 9 },
				],
			},
		];
		expect(valueExtent(series)).toEqual([0, 9]);
	});

	it("extends below zero for negative values", () => {
		const series: ChartSeries[] = [
			{
				name: "a",
				data: [
					{ label: "x", value: -3 },
					{ label: "y", value: 5 },
				],
			},
		];
		expect(valueExtent(series)).toEqual([-3, 5]);
	});

	it("spans every series", () => {
		const series: ChartSeries[] = [
			{ name: "a", data: [{ label: "x", value: 2 }] },
			{ name: "b", data: [{ label: "x", value: 11 }] },
		];
		expect(valueExtent(series)).toEqual([0, 11]);
	});

	it("gives an all-zero series a non-degenerate range", () => {
		const series: ChartSeries[] = [
			{
				name: "a",
				data: [
					{ label: "x", value: 0 },
					{ label: "y", value: 0 },
				],
			},
		];
		expect(valueExtent(series)).toEqual([0, 1]);
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
