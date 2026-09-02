import { describe, expect, it } from "vitest";
import { areaPath, linearScale, linePath, niceTicks, scaleX, sparkPlot } from "../geometry.js";
import type { SeriesPoint } from "../types.js";

// Path coordinates are emitted as "M x y L x y …"; pull them out as alternating
// x,y pairs so the tests can assert what actually gets drawn.
function coords(d: string): { xs: number[]; ys: number[] } {
	const nums = (d.match(/-?\d+(?:\.\d+)?/g) ?? []).map(Number);
	return {
		xs: nums.filter((_, i) => i % 2 === 0),
		ys: nums.filter((_, i) => i % 2 === 1),
	};
}

const pts = (...values: number[]): SeriesPoint[] => values.map((value, i) => ({ key: String(i), value }));

describe("linePath", () => {
	// Regression: scaleY used to close over the constant full viewBox height, so a
	// sparkline drawn at height 40 produced y-coordinates up to ~114 that spilled
	// out of its 40-tall viewBox and got clipped. The scale must follow the plot.
	it("scales into the passed plot, not a constant viewBox", () => {
		const d = linePath(pts(2, 9, 4, 7), sparkPlot(40));
		for (const y of coords(d).ys) {
			expect(y).toBeGreaterThanOrEqual(0);
			expect(y).toBeLessThanOrEqual(40);
		}
	});

	it("keeps a full-height line inside its viewBox", () => {
		const d = linePath(pts(0, 100), sparkPlot(120));
		for (const y of coords(d).ys) {
			expect(y).toBeGreaterThanOrEqual(0);
			expect(y).toBeLessThanOrEqual(120);
		}
	});

	// Regression: a single point used to emit a lone "M x y" that SVG draws as
	// nothing, so a one-bucket series rendered a blank card. It must draw a real
	// flat line across the width instead.
	it("draws a single point as a flat line across the width", () => {
		const d = linePath(pts(5), sparkPlot(120));
		expect(d).toContain("L");
		const { xs, ys } = coords(d);
		expect(Math.min(...xs)).toBeLessThan(10);
		expect(Math.max(...xs)).toBeGreaterThan(290);
		expect(new Set(ys).size).toBe(1);
	});

	it("returns an empty path for no points", () => {
		expect(linePath([], sparkPlot(120))).toBe("");
	});

	// The axed charts pass a tick-based scale so the line meets the gridlines: a
	// value on a tick must land exactly on that tick's y, not on the raw-extent y.
	it("honors an explicit y scale", () => {
		const p = sparkPlot(120);
		const y = linearScale(0, 10, p.bottom, p.top);
		const { ys } = coords(linePath(pts(0, 10), p, y));
		expect(Math.max(...ys)).toBeCloseTo(p.bottom, 1);
		expect(Math.min(...ys)).toBeCloseTo(p.top, 1);
	});

	// A lone axed point must sit at its own value's y (via `singleY`), not the
	// centred sparkline default — otherwise a one-bucket line reads as ~half its
	// value against the axis it's now drawn beside.
	it("places a single point at the supplied singleY, not mid", () => {
		const p = sparkPlot(120);
		const y = linearScale(0, 10, p.bottom, p.top);
		const { ys } = coords(linePath(pts(8), p, y, y(8)));
		expect(new Set(ys).size).toBe(1);
		expect(ys[0]).toBeCloseTo(y(8), 1);
		expect(ys[0]).not.toBeCloseTo((p.top + p.bottom) / 2, 1);
	});
});

describe("areaPath", () => {
	// Regression: the single-point area used to close on a zero-width sliver. It
	// must be a closed, non-degenerate band spanning the width.
	it("fills a single-point series as a closed band across the width", () => {
		const d = areaPath(pts(5), sparkPlot(120));
		expect(d).toContain("L");
		expect(d.trimEnd().endsWith("Z")).toBe(true);
		const { xs } = coords(d);
		expect(Math.max(...xs) - Math.min(...xs)).toBeGreaterThan(280);
	});

	it("returns an empty path for no points", () => {
		expect(areaPath([], sparkPlot(120))).toBe("");
	});
});

describe("linearScale", () => {
	it("maps the domain onto the range", () => {
		const s = linearScale(0, 10, 100, 0);
		expect(s(0)).toBe(100);
		expect(s(10)).toBe(0);
		expect(s(5)).toBe(50);
	});

	// A flat series has a zero-width domain; collapsing to the range start beats
	// dividing by zero into NaN coordinates.
	it("collapses a zero-width domain to the range start", () => {
		expect(linearScale(4, 4, 20, 80)(4)).toBe(20);
	});
});

describe("scaleX", () => {
	it("spreads indices across the plot and centers a lone point", () => {
		const p = sparkPlot(120);
		expect(scaleX(0, 3, p)).toBeCloseTo(p.left, 5);
		expect(scaleX(2, 3, p)).toBeCloseTo(p.right, 5);
		expect(scaleX(0, 1, p)).toBeCloseTo((p.left + p.right) / 2, 5);
	});
});

describe("niceTicks", () => {
	it("produces rounded, ascending ticks covering the range", () => {
		const ticks = niceTicks(0, 95);
		expect(ticks[0]).toBe(0);
		expect(ticks[ticks.length - 1]).toBeGreaterThanOrEqual(95);
		for (let i = 1; i < ticks.length; i++) expect(ticks[i]).toBeGreaterThan(ticks[i - 1]);
	});

	// A degenerate span (all-zero series, or a single point anchored at its own
	// value) still needs an axis to draw against.
	it("falls back to a 0..1 axis for a zero span", () => {
		expect(niceTicks(0, 0)).toEqual([0, 1]);
	});
});
