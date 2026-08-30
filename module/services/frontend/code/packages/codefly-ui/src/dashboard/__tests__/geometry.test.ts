import { describe, expect, it } from "vitest";
import { areaPath, linePath } from "../geometry.js";
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
	// out of its 40-tall viewBox and got clipped. The scale must follow `height`.
	it("scales into the passed height, not a constant viewBox", () => {
		const d = linePath(pts(2, 9, 4, 7), 40);
		for (const y of coords(d).ys) {
			expect(y).toBeGreaterThanOrEqual(0);
			expect(y).toBeLessThanOrEqual(40);
		}
	});

	it("keeps a full-height line inside its viewBox", () => {
		const d = linePath(pts(0, 100), 120);
		for (const y of coords(d).ys) {
			expect(y).toBeGreaterThanOrEqual(0);
			expect(y).toBeLessThanOrEqual(120);
		}
	});

	// Regression: a single point used to emit a lone "M x y" that SVG draws as
	// nothing, so a one-bucket series rendered a blank card. It must draw a real
	// flat line across the width instead.
	it("draws a single point as a flat line across the width", () => {
		const d = linePath(pts(5), 120);
		expect(d).toContain("L");
		const { xs, ys } = coords(d);
		expect(Math.min(...xs)).toBeLessThan(10);
		expect(Math.max(...xs)).toBeGreaterThan(290);
		expect(new Set(ys).size).toBe(1);
	});

	it("returns an empty path for no points", () => {
		expect(linePath([], 120)).toBe("");
	});
});

describe("areaPath", () => {
	// Regression: the single-point area used to close on a zero-width sliver. It
	// must be a closed, non-degenerate band spanning the width.
	it("fills a single-point series as a closed band across the width", () => {
		const d = areaPath(pts(5), 120);
		expect(d).toContain("L");
		expect(d.trimEnd().endsWith("Z")).toBe(true);
		const { xs } = coords(d);
		expect(Math.max(...xs) - Math.min(...xs)).toBeGreaterThan(280);
	});

	it("returns an empty path for no points", () => {
		expect(areaPath([], 120)).toBe("");
	});
});
