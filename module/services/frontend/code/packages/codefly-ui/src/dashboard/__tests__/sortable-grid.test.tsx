// @vitest-environment happy-dom
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
} from "@testing-library/react";
import { createPortal } from "react-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SortableBoard, SortableGrid } from "../sortable-grid.js";

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

	it("offers no slot to move a tile to, since it takes no onMove", () => {
		renderGrid();
		const drop = dragTo("a", center("d"));
		expect(screen.queryByText("Move here")).toBeNull();
		drop();
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

	it("starts no drag from a panel a tile opens elsewhere on the page", () => {
		const onSwap = vi.fn();
		render(
			<SortableGrid
				ids={["a", "b"]}
				onSwap={onSwap}
				renderItem={(id) => (
					<>
						Tile {id}
						{id === "a" && createPortal(<p>Text to select</p>, document.body)}
					</>
				)}
			/>,
		);
		const panel = screen.getByText("Text to select");
		fireEvent.mouseDown(panel, { clientX: 10, clientY: 10, button: 0 });
		fireEvent.mouseMove(document, { clientX: 160, clientY: 50 });
		expect(tile("a").dataset.dragging).toBeUndefined();
		fireEvent.mouseUp(document, { clientX: 160, clientY: 50 });
		expect(onSwap).not.toHaveBeenCalled();
	});

	it("draws the floating copy with renderOverlay when one is given", () => {
		render(
			<SortableGrid
				ids={["a", "b", "c", "d"]}
				onSwap={vi.fn()}
				renderItem={(id) => `Tile ${id}`}
				renderOverlay={(id) => `Copy of ${id}`}
			/>,
		);
		expect(screen.queryByText("Copy of a")).toBeNull();
		const drop = dragTo("a", center("d"));
		expect(screen.getByText("Copy of a")).toBeTruthy();
		drop();
	});

	it("draws the floating copy with renderItem by default", () => {
		renderGrid();
		const drop = dragTo("a", center("d"));
		expect(screen.getAllByRole("button", { name: "Tile a" })).toHaveLength(2);
		drop();
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

describe("SortableBoard", () => {
	// Each group is a band of the same two-column cells, GROUP_GAP below the
	// one before it, so tiles in different groups never overlap. A group's
	// slot is laid out like the tile after its last.
	const GROUP_GAP = 400;
	beforeEach(() => {
		HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
			const list = this.parentElement;
			const group = list?.parentElement;
			const inBoard =
				this.tagName === "LI" && group?.dataset.sortableGroup !== undefined;
			const { left, top, right, bottom } = inBoard
				? slot([...(list?.children ?? [])].indexOf(this))
				: { left: 0, top: 0, right: CELL, bottom: CELL };
			const offset =
				inBoard && group?.parentElement
					? [...group.parentElement.children].indexOf(group) * GROUP_GAP
					: 0;
			return {
				x: left,
				y: top + offset,
				left,
				top: top + offset,
				right,
				bottom: bottom + offset,
				width: right - left,
				height: bottom - top,
				toJSON: () => ({}),
			} as DOMRect;
		};
	});

	// Group one holds a b c, group two x y.
	const GROUPS = [
		{ id: "one", ids: ["a", "b", "c"] },
		{ id: "two", ids: ["x", "y"] },
	];

	function board(groups: { id: string; ids: string[] }[], props = {}) {
		return (
			<SortableBoard
				groups={groups.map((group) => ({
					...group,
					label: `Group ${group.id}`,
					header: <p>Group {group.id}</p>,
				}))}
				onSwap={vi.fn()}
				itemLabel={(id) => `Tile ${id}`}
				renderItem={(id) => `Tile ${id}`}
				{...props}
			/>
		);
	}

	function renderBoard(groups = GROUPS) {
		const onSwap = vi.fn();
		const onMove = vi.fn();
		const view = render(board(groups, { onSwap, onMove }));
		return { onSwap, onMove, ...view };
	}

	const middle = (element: HTMLElement) => {
		const { left, top } = element.getBoundingClientRect();
		return { clientX: left + CELL / 2, clientY: top + CELL / 2 };
	};
	const slots = () => screen.queryAllByText("Move here");
	const slotOf = (group: string) => {
		const found = slots().find(
			(element) => element.dataset.sortableSlot === group,
		);
		if (!found) throw new Error(`no slot in group ${group}`);
		return found;
	};

	// Picks a tile up, past the 5px a mouse drag needs to start. Returns the
	// moves and the drop, so a test can aim at a slot the drag brought up.
	function pickUp(id: string) {
		const start = middle(tile(id));
		fireEvent.mouseDown(tile(id), { ...start, button: 0 });
		fireEvent.mouseMove(document, {
			clientX: start.clientX + 10,
			clientY: start.clientY + 10,
		});
		let at = start;
		return {
			moveTo(to: { clientX: number; clientY: number }) {
				fireEvent.mouseMove(document, {
					clientX: (at.clientX + to.clientX) / 2,
					clientY: (at.clientY + to.clientY) / 2,
				});
				fireEvent.mouseMove(document, to);
				at = to;
			},
			drop() {
				fireEvent.mouseUp(document, at);
			},
		};
	}

	const announced = () =>
		document.querySelector('[id^="DndLiveRegion"]')?.textContent ?? "";

	it("draws each group's header above its tiles, in order", () => {
		renderBoard();
		expect(screen.getByText("Group one")).toBeTruthy();
		expect(order()).toEqual(["a", "b", "c", "x", "y"]);
		const two = tile("x").closest("[data-sortable-group]");
		expect(two?.textContent).toBe("Group twoTile xTile y");
	});

	it("swaps a tile dropped on a tile in another group, and names that group", () => {
		const { onSwap, onMove } = renderBoard();
		const drag = pickUp("a");
		drag.moveTo(middle(tile("y")));
		// The two trade places across the groups while the drag is in flight.
		expect(tile("a").style.transform).toBe("translate3d(110px, 400px, 0)");
		expect(tile("y").style.transform).toBe("translate3d(-110px, -400px, 0)");
		expect(tile("b").style.transform).toBe("");
		expect(announced()).toContain(
			"Tile a will swap with Tile y and move to Group two.",
		);

		drag.drop();
		expect(onSwap).toHaveBeenCalledExactlyOnceWith("a", "y");
		expect(onMove).not.toHaveBeenCalled();
	});

	it("shows a slot at the end of every group only while a drag is in flight", () => {
		renderBoard();
		expect(slots()).toEqual([]);
		const drag = pickUp("b");
		expect(slots().map((element) => element.dataset.sortableSlot)).toEqual([
			"one",
			"two",
		]);
		// Each slot comes after its group's last tile.
		expect(slotOf("two").previousElementSibling).toBe(tile("y"));
		drag.drop();
		expect(slots()).toEqual([]);
	});

	it("moves a tile dropped on a group's slot to the end of that group", () => {
		const { onSwap, onMove } = renderBoard();
		const drag = pickUp("a");
		drag.moveTo(middle(slotOf("two")));
		expect(announced()).toContain("Tile a will move to the end of Group two.");
		drag.drop();
		expect(onMove).toHaveBeenCalledExactlyOnceWith("a", "two");
		expect(onSwap).not.toHaveBeenCalled();
	});

	it("changes nothing when the last tile of a group is dropped on that group's slot", () => {
		const { onSwap, onMove } = renderBoard();
		const drag = pickUp("y");
		drag.moveTo(middle(slotOf("two")));
		drag.drop();
		expect(onMove).not.toHaveBeenCalled();
		expect(onSwap).not.toHaveBeenCalled();
	});

	it("lands a drop in the gap between a group's tiles on its nearest one", () => {
		const { onSwap } = renderBoard();
		const drag = pickUp("a");
		// 3px right of x, in the gap before y: x's center is the nearer.
		drag.moveTo({ clientX: CELL + 3, clientY: GROUP_GAP + 40 });
		drag.drop();
		expect(onSwap).toHaveBeenCalledExactlyOnceWith("a", "x");
	});

	it("changes nothing when the drop is between two groups, or off the board", () => {
		const { onSwap, onMove } = renderBoard();
		const between = pickUp("x");
		// Below group one's slot and above group two: on neither group.
		between.moveTo({ clientX: 50, clientY: GROUP_GAP - 50 });
		between.drop();
		const off = pickUp("x");
		off.moveTo({ clientX: 900, clientY: 2000 });
		off.drop();
		expect(onSwap).not.toHaveBeenCalled();
		expect(onMove).not.toHaveBeenCalled();
	});

	it("glides a tile that moved to another group from where it was drawn", () => {
		const animate = vi.fn();
		HTMLElement.prototype.animate = animate;
		const { rerender } = renderBoard();
		rerender(
			board([
				{ id: "one", ids: ["b", "c"] },
				{ id: "two", ids: ["x", "y", "a"] },
			]),
		);
		const glides = new Map(
			animate.mock.contexts.map((element, call) => [
				(element as HTMLElement).dataset.sortableId,
				animate.mock.calls[call][0][0].transform,
			]),
		);
		// a left the first cell of group one for the third of group two; b and
		// c each moved up a place behind it; x and y did not move.
		expect(glides).toEqual(
			new Map([
				["b", "translate(110px, 0px)"],
				["c", "translate(-110px, 110px)"],
				["a", "translate(0px, -510px)"],
			]),
		);
	});
});

describe("SortableBoard, moving groups", () => {
	// The board starts TOP down the page, under whatever the page draws above
	// it. Each group is a band BAND tall, GROUP_GAP below the one before it,
	// with a header HEADER tall at its top; its tiles are the same two-column
	// cells, under the header. Heights below are from the board's top: group
	// one's middle is at 150, two's at 550.
	const TOP = 200;
	const GROUP_GAP = 400;
	const BAND = 300;
	const HEADER = 30;
	const WIDTH = 2 * CELL + GAP;
	const bandOf = (group: Element | null | undefined) =>
		group?.parentElement ? [...group.parentElement.children].indexOf(group) : 0;
	beforeEach(() => {
		HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
			let rect = { left: 0, top: 0, right: CELL, bottom: CELL };
			if (this.dataset.sortableGroup !== undefined) {
				const top = TOP + bandOf(this) * GROUP_GAP;
				rect = { left: 0, top, right: WIDTH, bottom: top + BAND };
			} else if (this.dataset.sortableGroupHeader !== undefined) {
				const top = TOP + bandOf(this.parentElement) * GROUP_GAP;
				rect = { left: 0, top, right: WIDTH, bottom: top + HEADER };
			} else if (this.dataset.sortableId !== undefined) {
				const list = this.parentElement;
				const cell = slot([...(list?.children ?? [])].indexOf(this));
				const top = TOP + bandOf(list?.parentElement) * GROUP_GAP + HEADER;
				rect = { ...cell, top: cell.top + top, bottom: cell.bottom + top };
			}
			return {
				...rect,
				x: rect.left,
				y: rect.top,
				width: rect.right - rect.left,
				height: rect.bottom - rect.top,
				toJSON: () => ({}),
			} as DOMRect;
		};
	});

	const GROUPS = [
		{ id: "one", ids: ["a", "b"] },
		{ id: "two", ids: ["x"] },
		{ id: "three", ids: ["y"] },
	];

	// Each group's header holds a grip it is dragged by.
	function board(
		groups: { id: string; ids: string[]; fixed?: boolean }[],
		props: Partial<Parameters<typeof SortableBoard>[0]> = {},
	) {
		return (
			<SortableBoard
				groups={groups.map((group) => ({
					...group,
					label: `Group ${group.id}`,
					header: (handle) => (
						<button type="button" {...handle}>
							Grip {group.id}
						</button>
					),
					preview: <p>Preview {group.id}</p>,
				}))}
				onSwap={vi.fn()}
				itemLabel={(id) => `Tile ${id}`}
				renderItem={(id) => `Tile ${id}`}
				{...props}
			/>
		);
	}

	function renderBoard(
		groups: { id: string; ids: string[]; fixed?: boolean }[] = GROUPS,
	) {
		const onSwap = vi.fn();
		const onMove = vi.fn();
		const onMoveGroup = vi.fn();
		const view = render(board(groups, { onSwap, onMove, onMoveGroup }));
		return { onSwap, onMove, onMoveGroup, ...view };
	}

	const group = (id: string) => {
		const found = document.querySelector<HTMLElement>(
			`[data-sortable-group="${id}"]`,
		);
		if (!found) throw new Error(`no group ${id}`);
		return found;
	};
	const line = () =>
		document.querySelector<HTMLElement>("[data-sortable-line]");
	const announced = () =>
		document.querySelector('[id^="DndLiveRegion"]')?.textContent ?? "";

	// Picks a group up by its grip, past the 5px a mouse drag needs, and aims it
	// at a height from the board's top (negative is above the board), and
	// optionally off to one side.
	function pickUpGroup(id: string) {
		const band = GROUPS.findIndex((g) => g.id === id);
		let at = { clientX: 20, clientY: TOP + band * GROUP_GAP + 10 };
		fireEvent.mouseDown(screen.getByRole("button", { name: `Grip ${id}` }), {
			...at,
			button: 0,
		});
		fireEvent.mouseMove(document, {
			clientX: at.clientX,
			clientY: at.clientY + 10,
		});
		return {
			moveTo(height: number, clientX = at.clientX) {
				const clientY = TOP + height;
				fireEvent.mouseMove(document, {
					clientX: (at.clientX + clientX) / 2,
					clientY: (at.clientY + clientY) / 2,
				});
				at = { clientX, clientY };
				fireEvent.mouseMove(document, at);
			},
			drop() {
				fireEvent.mouseUp(document, at);
			},
		};
	}

	it("puts a group dropped on the top half of another before it", () => {
		const { onMoveGroup, onSwap } = renderBoard();
		const drag = pickUpGroup("three");
		// Only the dragged group's header follows the pointer.
		expect(group("three").dataset.dragging).toBe("true");
		expect(screen.getByText("Preview three")).toBeTruthy();
		drag.moveTo(50);
		expect(line()?.dataset.sortableLine).toBe("before");
		expect(line()?.parentElement).toBe(group("one"));
		expect(announced()).toContain("Group three will move before Group one.");
		drag.drop();
		expect(onMoveGroup).toHaveBeenCalledExactlyOnceWith("three", 0);
		expect(onSwap).not.toHaveBeenCalled();
		expect(line()).toBeNull();
	});

	it("puts a group dropped on the bottom half of another after it", () => {
		const { onMoveGroup } = renderBoard();
		const drag = pickUpGroup("one");
		drag.moveTo(GROUP_GAP + 250);
		expect(line()?.dataset.sortableLine).toBe("after");
		expect(line()?.parentElement).toBe(group("two"));
		expect(announced()).toContain("Group one will move after Group two.");
		drag.drop();
		expect(onMoveGroup).toHaveBeenCalledExactlyOnceWith("one", 1);
	});

	it("lands a group dropped between two others on that place", () => {
		const { onMoveGroup } = renderBoard();
		const drag = pickUpGroup("one");
		// In the gap under group two, nearer its bottom half than three's top.
		drag.moveTo(GROUP_GAP + BAND + 20);
		drag.drop();
		expect(onMoveGroup).toHaveBeenCalledExactlyOnceWith("one", 1);
	});

	it("changes nothing when a group is dropped where it already is", () => {
		const { onMoveGroup } = renderBoard();
		// Two after one is where two is.
		const drag = pickUpGroup("two");
		drag.moveTo(250);
		expect(line()).toBeNull();
		expect(announced()).toContain("Group two will stay where it is.");
		drag.drop();
		// And so is two over its own place.
		const own = pickUpGroup("two");
		own.moveTo(GROUP_GAP + 50);
		own.drop();
		expect(onMoveGroup).not.toHaveBeenCalled();
	});

	it("puts a group dropped above the board first, below it last, and goes by height alone", () => {
		const { onMoveGroup } = renderBoard();
		// Over what the page draws above the board: before the first group.
		const above = pickUpGroup("three");
		above.moveTo(-100);
		expect(line()?.dataset.sortableLine).toBe("before");
		expect(line()?.parentElement).toBe(group("one"));
		expect(announced()).toContain("Group three will move before Group one.");
		above.drop();
		expect(onMoveGroup).toHaveBeenLastCalledWith("three", 0);
		// Past the last group: after it.
		const below = pickUpGroup("one");
		below.moveTo(3000);
		below.drop();
		expect(onMoveGroup).toHaveBeenLastCalledWith("one", 2);
		// Off to one side, over two's top half: before two.
		const aside = pickUpGroup("three");
		aside.moveTo(GROUP_GAP + 50, 900);
		aside.drop();
		expect(onMoveGroup).toHaveBeenLastCalledWith("three", 1);
		expect(onMoveGroup).toHaveBeenCalledTimes(3);
	});

	it("makes no tile a drop target and shows no slot while a group is dragged", () => {
		const { onMoveGroup, onSwap, onMove } = renderBoard();
		const drag = pickUpGroup("two");
		expect(screen.queryByText("Move here")).toBeNull();
		// Over tile a, which is on group one's top half.
		drag.moveTo(HEADER + 50);
		expect(tile("a").style.transform).toBe("");
		expect(tile("x").style.transform).toBe("");
		drag.drop();
		expect(onSwap).not.toHaveBeenCalled();
		expect(onMove).not.toHaveBeenCalled();
		expect(onMoveGroup).toHaveBeenCalledExactlyOnceWith("two", 0);
	});

	it("still swaps tiles on a board that moves groups", () => {
		const { onSwap, onMoveGroup } = renderBoard();
		const { left, top } = tile("a").getBoundingClientRect();
		fireEvent.mouseDown(tile("a"), {
			clientX: left + 50,
			clientY: top + 50,
			button: 0,
		});
		const target = tile("y").getBoundingClientRect();
		for (const step of [0.1, 0.5, 1]) {
			fireEvent.mouseMove(document, {
				clientX: left + 50 + (target.left - left) * step,
				clientY: top + 50 + (target.top - top) * step,
			});
		}
		expect(line()).toBeNull();
		fireEvent.mouseUp(document, {
			clientX: target.left + 50,
			clientY: target.top + 50,
		});
		expect(onSwap).toHaveBeenCalledExactlyOnceWith("a", "y");
		expect(onMoveGroup).not.toHaveBeenCalled();
	});

	it("never moves a fixed group, nor puts one before it", () => {
		const { onMoveGroup } = renderBoard([
			{ ...GROUPS[0], fixed: true },
			GROUPS[1],
			GROUPS[2],
		]);
		const fixed = pickUpGroup("one");
		expect(group("one").dataset.dragging).toBeUndefined();
		fixed.moveTo(GROUP_GAP * 2 + 250);
		fixed.drop();
		expect(onMoveGroup).not.toHaveBeenCalled();
		// Over the fixed first group, or above it, a group goes right after it.
		const onto = pickUpGroup("three");
		onto.moveTo(-50);
		expect(announced()).toContain("Group three will move before Group two.");
		onto.drop();
		expect(onMoveGroup).toHaveBeenCalledExactlyOnceWith("three", 1);
	});

	it("drags no group on a board that does not move groups", () => {
		const onSwap = vi.fn();
		render(board(GROUPS, { onSwap }));
		const drag = pickUpGroup("one");
		expect(group("one").dataset.dragging).toBeUndefined();
		drag.moveTo(GROUP_GAP + 250);
		drag.drop();
		expect(onSwap).not.toHaveBeenCalled();
	});

	it("glides a moved group, and its tiles with it rather than on their own", () => {
		const animate = vi.fn();
		HTMLElement.prototype.animate = animate;
		const { rerender } = renderBoard();
		rerender(board([GROUPS[1], GROUPS[0], GROUPS[2]]));
		const glides = new Map(
			animate.mock.contexts.map((element, call) => [
				(element as HTMLElement).dataset.sortableGroup ??
					(element as HTMLElement).dataset.sortableId,
				animate.mock.calls[call][0][0].transform,
			]),
		);
		// One and two traded bands; three, and every tile, stayed put within
		// its group.
		expect(glides).toEqual(
			new Map([
				["two", "translate(0px, 400px)"],
				["one", "translate(0px, -400px)"],
			]),
		);
	});
});
