import { describe, expect, it } from "vitest";
import { Axis, Gridline } from "../atoms.js";
import { linearScale, sparkPlot } from "../geometry.js";

type ElementLike = { type?: unknown; props?: { children?: unknown } & Record<string, unknown> };

// Collect every element of a given SVG tag (a rendered component is a plain
// object tree, so no DOM is needed) with its props and text child. Recurses into
// child arrays — an axis <g> holds one array of y labels and one of x labels.
function byTag(node: unknown, tag: string): { props: Record<string, unknown>; text: string }[] {
	if (Array.isArray(node)) return node.flatMap((child) => byTag(child, tag));
	if (!node || typeof node !== "object") return [];
	const el = node as ElementLike;
	const self =
		el.type === tag && el.props
			? [{ props: el.props, text: typeof el.props.children === "string" ? el.props.children : "" }]
			: [];
	return self.concat(byTag(el.props?.children, tag));
}

const plot = sparkPlot(120);

describe("Gridline", () => {
	it("draws one rule per y position", () => {
		const lines = byTag(Gridline({ plot, ys: [10, 40, 80] }), "line");
		expect(lines).toHaveLength(3);
		for (const line of lines) {
			expect(line.props.x1).toBe(plot.left);
			expect(line.props.x2).toBe(plot.right);
		}
	});
});

describe("Axis", () => {
	it("labels each y tick with the formatted value", () => {
		const scale = linearScale(0, 10, plot.bottom, plot.top);
		const labels = byTag(Axis({ plot, y: { ticks: [0, 5, 10], scale } }), "text").map((t) => t.text);
		expect(labels).toEqual(["0", "5", "10"]);
	});

	it("time-formats ISO bucket keys on the x axis", () => {
		const keys = ["2026-09-01T00:00:00+00", "2026-09-02T00:00:00+00"];
		const labels = byTag(Axis({ plot, x: { keys } }), "text").map((t) => t.text);
		expect(labels).toEqual(["Sep 1", "Sep 2"]);
	});

	// A dense series can't show every bucket without labels colliding; the axis
	// strides to a readable subset but must always keep the final bucket.
	it("strides x labels for a dense series and keeps the last", () => {
		const keys = Array.from({ length: 20 }, (_, i) => `cat-${i}`);
		const shown = byTag(Axis({ plot, x: { keys } }), "text").map((t) => t.text);
		expect(shown.length).toBeGreaterThan(0);
		expect(shown.length).toBeLessThanOrEqual(6);
		expect(shown).toContain("cat-19");
	});
});
