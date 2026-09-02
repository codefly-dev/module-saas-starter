import { describe, expect, it } from "vitest";
import { Axis, Svg } from "../atoms.js";
import { AreaChart, LineChart } from "../charts.js";
import type { SeriesPoint } from "../types.js";

type ElementLike = { type?: unknown; props?: { children?: unknown } & Record<string, unknown> };

// Walk a React element tree (no DOM needed — a rendered component is a plain
// object) collecting every <path>'s props, so a test can assert what color the
// chart draws with.
function pathProps(node: unknown): Record<string, unknown>[] {
	if (Array.isArray(node)) return node.flatMap(pathProps);
	if (!node || typeof node !== "object") return [];
	const el = node as ElementLike;
	const self = el.type === "path" && el.props ? [el.props] : [];
	return self.concat(pathProps(el.props?.children));
}

// Collect every element whose `type` matches (component identity or tag).
function byType(node: unknown, type: unknown): ElementLike[] {
	if (Array.isArray(node)) return node.flatMap((child) => byType(child, type));
	if (!node || typeof node !== "object") return [];
	const el = node as ElementLike;
	const self = el.type === type ? [el] : [];
	return self.concat(byType(el.props?.children, type));
}

const pts: SeriesPoint[] = [
	{ key: "a", value: 1 },
	{ key: "b", value: 4 },
];

// Regression: line/area used to hardcode stroke="var(--primary)", which ignored
// the caller's text-color className (e.g. `text-primary/70`) — making the class
// dead and silently dropping its opacity. Coloring via currentColor is what lets
// a text-color class (and the Dashboard accent behind `text-primary`) theme it.
describe("chart color inherits via currentColor", () => {
	it("LineChart strokes with currentColor", () => {
		const strokes = pathProps(LineChart({ points: pts })).filter((p) => p.stroke && p.stroke !== "none");
		expect(strokes.length).toBeGreaterThan(0);
		for (const p of strokes) expect(p.stroke).toBe("currentColor");
	});

	it("AreaChart fills and strokes with currentColor", () => {
		const drawn = pathProps(AreaChart({ points: pts }));
		const fills = drawn.filter((p) => p.fill && p.fill !== "none");
		const strokes = drawn.filter((p) => p.stroke && p.stroke !== "none");
		expect(fills.length).toBeGreaterThan(0);
		expect(strokes.length).toBeGreaterThan(0);
		for (const p of fills) expect(p.fill).toBe("currentColor");
		for (const p of strokes) expect(p.stroke).toBe("currentColor");
	});
});

// The sparkline floor (StatChart inline, and the default) must stay axis-less; a
// dashboard widget opts into axes with the prop. The frame also switches from
// stretch (sparkline) to a uniform scale (axed) so axis text isn't distorted.
describe("axes gating", () => {
	it("draws no axis by default and stretches to fill", () => {
		const tree = LineChart({ points: pts });
		expect(tree.type).toBe(Svg);
		expect(tree.props.stretch).toBe(true);
		expect(byType(tree, Axis)).toHaveLength(0);
	});

	it("draws an axis when opted in and keeps a uniform scale", () => {
		const tree = LineChart({ points: pts, axes: true });
		expect(tree.type).toBe(Svg);
		expect(tree.props.stretch).toBe(false);
		expect(byType(tree, Axis)).toHaveLength(1);
		// The data path still colors via currentColor, not a hardcoded hue.
		const strokes = pathProps(tree).filter((p) => p.stroke && p.stroke !== "none");
		expect(strokes.length).toBeGreaterThan(0);
		for (const p of strokes) expect(p.stroke).toBe("currentColor");
	});

	it("honors per-axis toggles", () => {
		const [axis] = byType(LineChart({ points: pts, axes: { y: false } }), Axis);
		expect(axis?.props).toBeDefined();
		expect(axis?.props?.x).toBeTruthy();
		expect(axis?.props?.y).toBeUndefined();
	});
});
