// @vitest-environment happy-dom
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SortableGrid } from "../sortable-grid.js";

// happy-dom lays nothing out, so each tile gets a slot by its place in the
// list: two columns of 100×100 cells, 10px apart. a b / c d.
const CELL = 100;
const GAP = 10;

function slot(index: number) {
	const left = (index % 2) * (CELL + GAP);
	const top = Math.floor(index / 2) * (CELL + GAP);
	return { left, top, right: left + CELL, bottom: top + CELL };
}

function center(id: string) {
	const { left, top } = slot(order().indexOf(id));
	return { clientX: left + CELL / 2, clientY: top + CELL / 2 };
}

function order(): string[] {
	return screen
		.getAllByRole("listitem")
		.map((tile) => tile.dataset.sortableId ?? "");
}

function tile(id: string): HTMLElement {
	const found = screen
		.getAllByRole("listitem")
		.find((item) => item.dataset.sortableId === id);
	if (!found) throw new Error(`no tile ${id}`);
	return found;
}

const realRect = HTMLElement.prototype.getBoundingClientRect;
const realAnimate = HTMLElement.prototype.animate;
beforeEach(() => {
	HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
		const id = this.dataset.sortableId;
		const siblings = this.parentElement ? [...this.parentElement.children] : [];
		const { left, top, right, bottom } =
			id === undefined
				? { left: 0, top: 0, right: CELL, bottom: CELL }
				: slot(siblings.indexOf(this));
		return {
			x: left,
			y: top,
			left,
			top,
			right,
			bottom,
			width: right - left,
			height: bottom - top,
			toJSON: () => ({}),
		} as DOMRect;
	};
});
afterEach(async () => {
	// dnd-kit keeps swallowing clicks for 50ms after a drag ends.
	await new Promise((resolve) => setTimeout(resolve, 60));
	cleanup();
	HTMLElement.prototype.getBoundingClientRect = realRect;
	HTMLElement.prototype.animate = realAnimate;
	vi.restoreAllMocks();
});

function renderGrid(ids = ["a", "b", "c", "d"]) {
	const onSwap = vi.fn();
	const onClick = vi.fn();
	const view = render(
		<SortableGrid
			ids={ids}
			onSwap={onSwap}
			itemLabel={(id) => `Tile ${id}`}
			renderItem={(id) => (
				<button type="button" onClick={() => onClick(id)}>
					Tile {id}
				</button>
			)}
		/>,
	);
	return { onSwap, onClick, ...view };
}

// A mouse drag from the middle of `from` to a point: pressed on the tile, then
// moved in steps on the document, where dnd-kit listens once a drag begins.
function dragTo(from: string, to: { clientX: number; clientY: number }) {
	const start = center(from);
	fireEvent.mouseDown(tile(from), { ...start, button: 0 });
	for (const step of [0.1, 0.5, 1]) {
		fireEvent.mouseMove(document, {
			clientX: start.clientX + (to.clientX - start.clientX) * step,
			clientY: start.clientY + (to.clientY - start.clientY) * step,
		});
	}
	return () => fireEvent.mouseUp(document, to);
}

describe("SortableGrid", () => {
	it("renders the tiles as a list, in order", () => {
		renderGrid();
		expect(order()).toEqual(["a", "b", "c", "d"]);
	});

	it("swaps a tile dropped on another, and moves no other tile meanwhile", () => {
		const { onSwap } = renderGrid();
		// a to d is diagonal on the grid: b and c sit between them in the order.
		const drop = dragTo("a", center("d"));
		expect(tile("a").dataset.dragging).toBe("true");
		// The dragged tile's slot and the target trade places while the drag is
		// in flight...
		expect(tile("a").style.transform).toBe("translate3d(110px, 110px, 0)");
		expect(tile("d").style.transform).toBe("translate3d(-110px, -110px, 0)");
		// ...and the tiles between them stay where they are.
		expect(tile("b").style.transform).toBe("");
		expect(tile("c").style.transform).toBe("");

		drop();
		expect(onSwap).toHaveBeenCalledExactlyOnceWith("a", "d");
	});

	it("lands a drop in the gap between tiles on the nearest one", () => {
		const { onSwap } = renderGrid();
		// 3px under a, in the gap above c: a's center is the nearer.
		dragTo("b", { clientX: 40, clientY: CELL + 3 })();
		expect(onSwap).toHaveBeenCalledExactlyOnceWith("b", "a");
	});

	it("changes nothing when the drop is off the grid", () => {
		const { onSwap } = renderGrid();
		dragTo("a", { clientX: 900, clientY: 900 })();
		expect(onSwap).not.toHaveBeenCalled();
		expect(tile("a").dataset.dragging).toBeUndefined();
	});

	it("changes nothing when the tile is dropped back on its own slot", () => {
		const { onSwap } = renderGrid();
		const drop = dragTo("a", center("b"));
		fireEvent.mouseMove(document, center("a"));
		drop();
		expect(onSwap).not.toHaveBeenCalled();
	});

	it("leaves a click on a control inside a tile alone", () => {
		const { onSwap, onClick } = renderGrid();
		const button = screen.getByRole("button", { name: "Tile c" });
		fireEvent.mouseDown(button, { ...center("c"), button: 0 });
		fireEvent.mouseUp(document, center("c"));
		fireEvent.click(button);
		expect(onClick).toHaveBeenCalledWith("c");
		expect(onSwap).not.toHaveBeenCalled();
	});

	it("glides every tile from where it was drawn when the order changes", () => {
		const animate = vi.fn();
		HTMLElement.prototype.animate = animate;
		const { rerender } = renderGrid();
		rerender(
			<SortableGrid
				ids={["d", "b", "c", "a"]}
				onSwap={vi.fn()}
				renderItem={(id) => `Tile ${id}`}
			/>,
		);
		// a moved from the first slot to the last, d the other way; b and c
		// did not move, so they do not animate.
		const glides = new Map(
			animate.mock.contexts.map((element, call) => [
				(element as HTMLElement).dataset.sortableId,
				animate.mock.calls[call][0][0].transform,
			]),
		);
		expect(glides).toEqual(
			new Map([
				["d", "translate(110px, 110px)"],
				["a", "translate(-110px, -110px)"],
			]),
		);
	});

	it("does not glide for a viewer who prefers reduced motion", () => {
		const animate = vi.fn();
		HTMLElement.prototype.animate = animate;
		vi.spyOn(window, "matchMedia").mockImplementation(
			(query) =>
				({
					matches: query === "(prefers-reduced-motion: reduce)",
				}) as MediaQueryList,
		);
		const { rerender } = renderGrid();
		act(() => {
			rerender(
				<SortableGrid
					ids={["d", "b", "c", "a"]}
					onSwap={vi.fn()}
					renderItem={(id) => `Tile ${id}`}
				/>,
			);
		});
		expect(animate).not.toHaveBeenCalled();
	});
});
