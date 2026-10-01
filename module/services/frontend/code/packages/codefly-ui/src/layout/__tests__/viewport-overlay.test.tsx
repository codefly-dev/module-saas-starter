import { cleanup, render } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, expect, it } from "vitest";
import { ViewportOverlay } from "../viewport-overlay.js";

afterEach(cleanup);
it("paints every rectangle and polygon using supplied geometry and unsplit CSS colors", () => {
	const stroke = "color-mix(in srgb, var(--primary) 70%, transparent)";
	const fill = "var(--highlight)";
	const { container } = render(
		<ViewportOverlay
			attributes={{ "data-viewport": "" }}
			items={[
				{
					id: "first",
					regions: [
						{ kind: "rect", x: 10, y: 20, width: 30, height: 40 },
						{
							kind: "polygon",
							points: [
								[1, 2],
								[9, 3],
								[8, 7],
							],
						},
					],
					stroke,
					fill,
					strokeWidth: 3,
					dashed: true,
					attributes: { "data-mark": "a" },
				},
				{
					id: "second",
					regions: [
						{ kind: "rect", x: 90, y: 80, width: 70, height: 60 },
						{
							kind: "polygon",
							points: [
								[11, 12],
								[19, 13],
								[18, 17],
							],
						},
					],
					stroke: "red",
					fill: "blue",
					attributes: { "data-mark": "b" },
				},
			]}
		/>,
	);
	const overlay = container.firstElementChild as HTMLElement;
	expect(overlay.getAttribute("aria-hidden")).toBe("true");
	expect(overlay.style.pointerEvents).toBe("none");
	expect(overlay.style.inset).toBe("0");
	expect(overlay.style.position).toBe("absolute");
	expect(overlay.children).toHaveLength(4);
	const rect = overlay.querySelector<HTMLElement>('div[data-mark="a"]');
	if (!rect) throw new Error("Missing rectangle");
	expect([
		rect.style.left,
		rect.style.top,
		rect.style.width,
		rect.style.height,
	]).toEqual(["10px", "20px", "30px", "40px"]);
	expect(rect.style.outlineWidth).toBe("3px");
	expect(rect.style.outlineStyle).toBe("dashed");
	expect(rect.style.background).toBe(fill);
	const polygon = overlay.querySelector('svg[data-mark="a"] polygon');
	if (!polygon) throw new Error("Missing first polygon");
	expect(polygon.getAttribute("points")).toBe("1,2 9,3 8,7");
	expect(polygon.getAttribute("stroke")).toBe(stroke);
	expect(polygon.getAttribute("fill")).toBe(fill);
	expect(polygon.getAttribute("stroke-width")).toBe("3");
	expect(polygon.getAttribute("stroke-dasharray")).toBe("6 4");
	const second = overlay.querySelector('svg[data-mark="b"] polygon');
	if (!second) throw new Error("Missing second polygon");
	expect(second.getAttribute("points")).toBe("11,12 19,13 18,17");
	expect(second.getAttribute("stroke")).toBe("red");
	expect(second.getAttribute("stroke-width")).toBe("2");
	expect(second.hasAttribute("stroke-dasharray")).toBe(false);
});
it("keeps an empty decoration inert", () => {
	const { container } = render(<ViewportOverlay items={[]} />);
	expect(container.firstElementChild?.children).toHaveLength(0);
	expect(container.firstElementChild?.getAttribute("aria-hidden")).toBe("true");
});

it("preserves the complete rectangle stroke expression in the rendered style", () => {
	const html = renderToStaticMarkup(
		<ViewportOverlay
			items={[
				{
					id: "rectangle",
					regions: [{ kind: "rect", x: 1, y: 2, width: 3, height: 4 }],
					stroke: "color-mix(in srgb, red 50%, blue)",
					fill: "transparent",
				},
			]}
		/>,
	);
	expect(html).toContain("outline-color:color-mix(in srgb, red 50%, blue)");
});
