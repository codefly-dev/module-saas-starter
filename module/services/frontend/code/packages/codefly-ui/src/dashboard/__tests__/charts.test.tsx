import { describe, expect, it } from "vitest";
import { AreaChart, LineChart } from "../charts.js";
import type { SeriesPoint } from "../types.js";

type ElementLike = { type?: unknown; props?: { children?: unknown } & Record<string, unknown> };

// Walk a React element tree (no DOM needed — a rendered component is a plain
// object) collecting every <path>'s props, so a test can assert what color the
// chart draws with.
function pathProps(node: unknown): Record<string, unknown>[] {
	if (!node || typeof node !== "object") return [];
	const el = node as ElementLike;
	const children = el.props?.children;
	const list = Array.isArray(children) ? children : children != null ? [children] : [];
	const self = el.type === "path" && el.props ? [el.props] : [];
	return self.concat(list.flatMap(pathProps));
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
