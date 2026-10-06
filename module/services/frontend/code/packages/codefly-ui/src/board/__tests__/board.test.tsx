// @vitest-environment happy-dom
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Board, type BoardColumn } from "../board.js";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

interface Row {
	ref: string;
	name: string;
	stage: string;
	owner: string;
}

const COLUMNS: BoardColumn[] = [
	{ id: "open", label: "Open", empty: "Nothing open" },
	{ id: "accepted", label: "Accepted" },
	{ id: "rejected", label: "Rejected" },
];

const ROWS: Row[] = [
	{ ref: "a", name: "Ask one", stage: "open", owner: "jane" },
	{ ref: "b", name: "Ask two", stage: "open", owner: "sam" },
	{ ref: "c", name: "Settled thing", stage: "accepted", owner: "jane" },
];

function board(props: Partial<Parameters<typeof Board<Row>>[0]> = {}) {
	return render(
		<Board<Row>
			items={ROWS}
			columns={COLUMNS}
			columnOf={(row) => row.stage}
			idOf={(row) => row.ref}
			labelOf={(row) => row.name}
			renderCard={(row) => <span>{row.name}</span>}
			{...props}
		/>,
	);
}

const column = (label: string) =>
	screen.getByRole("region", { name: label }) ??
	(screen.getByLabelText(label) as HTMLElement);

/** base-ui opens its menu through effects; let them flush. */
async function open(trigger: HTMLElement) {
	await act(async () => {
		fireEvent.click(trigger);
	});
}

// happy-dom lays nothing out, so every rect is zero and dnd-kit's pointer
// collision finds no column whatever the drag does — a drag test would pass
// against a board that never moved anything. So the columns and cards are given
// a real geometry: three 300px columns side by side, each card at the top of its
// own. This is the layout the assertions below are about.
const RECTS: Record<string, [number, number, number, number]> = {
	"column:open": [0, 0, 300, 400],
	"column:accepted": [320, 0, 300, 400],
	"column:rejected": [640, 0, 300, 400],
	"card:a": [10, 40, 280, 80],
	"card:b": [10, 130, 280, 80],
	"card:c": [330, 40, 280, 80],
};

function layOut() {
	vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(
		function (this: Element) {
			const key =
				(this.getAttribute?.("data-board-column") !== null
					? `column:${this.getAttribute("data-board-column")}`
					: undefined) ??
				(this.getAttribute?.("data-board-card") !== null
					? `card:${this.getAttribute("data-board-card")}`
					: undefined);
			const [x, y, width, height] = RECTS[key ?? ""] ?? [0, 0, 1000, 600];
			return {
				x,
				y,
				left: x,
				top: y,
				width,
				height,
				right: x + width,
				bottom: y + height,
				toJSON: () => ({}),
			} as DOMRect;
		},
	);
}

/** A pointer drag from the card to a point on the page, as a mouse makes it. */
async function dragCard(id: string, to: [number, number]) {
	const card = document.querySelector(
		`[data-board-card="${id}"]`,
	) as HTMLElement;
	const [x, y] = RECTS[`card:${id}`] ?? [0, 0];
	const from: [number, number] = [x + 20, y + 20];
	const move = async (at: [number, number]) => {
		await act(async () => {
			document.dispatchEvent(
				new MouseEvent("mousemove", {
					bubbles: true,
					clientX: at[0],
					clientY: at[1],
				}),
			);
		});
	};
	await act(async () => {
		card.dispatchEvent(
			new MouseEvent("mousedown", {
				bubbles: true,
				button: 0,
				clientX: from[0],
				clientY: from[1],
			}),
		);
	});
	// The sensor waits 5px before a drag begins, so a click on a control inside a
	// card still lands.
	await move([from[0] + 10, from[1] + 10]);
	await move(to);
	await act(async () => {
		document.dispatchEvent(
			new MouseEvent("mouseup", {
				bubbles: true,
				clientX: to[0],
				clientY: to[1],
			}),
		);
	});
}

describe("Board groups a collection by a field", () => {
	it("puts every item on exactly one column, and counts it there", () => {
		board();
		expect(within(column("Open")).getAllByRole("listitem")).toHaveLength(2);
		expect(within(column("Open")).getByText("2")).toBeTruthy();
		expect(within(column("Accepted")).getByText("1")).toBeTruthy();
		expect(screen.getAllByText(/^Ask |^Settled/)).toHaveLength(3);
	});

	it("says what an empty column would hold, in the consumer's words", () => {
		board({ items: [] });
		expect(within(column("Open")).getByText("Nothing open")).toBeTruthy();
		// A column that gave no words still says something rather than nothing.
		expect(within(column("Rejected")).getByText("Nothing here")).toBeTruthy();
	});

	it("shows no search box without something to search", () => {
		board();
		expect(screen.queryByRole("searchbox")).toBeNull();
	});

	it("searches what the consumer says is searchable, and nothing else", () => {
		board({
			searchText: (row) => `${row.name} ${row.owner}`,
			searchLabel: "Search by name or owner",
		});
		fireEvent.change(
			screen.getByRole("searchbox", { name: "Search by name or owner" }),
			{ target: { value: "sam" } },
		);
		expect(screen.getByText("Ask two")).toBeTruthy();
		expect(screen.queryByText("Ask one")).toBeNull();
		// A field the consumer did not offer is not searched.
		fireEvent.change(screen.getByRole("searchbox"), {
			target: { value: "accepted" },
		});
		expect(screen.queryByText("Settled thing")).toBeNull();
	});

	it("orders cards inside a column the way the consumer asks", () => {
		board({ compare: (a, b) => a.name.localeCompare(b.name) });
		const names = within(column("Open"))
			.getAllByRole("listitem")
			.map((row) => row.textContent);
		expect(names[0]).toContain("Ask one");
	});
});

describe("a card is reachable without a pointer", () => {
	it("opens from the keyboard, with a name that says which card", () => {
		const onOpen = vi.fn();
		board({ onOpen });
		const first = screen.getByRole("button", { name: "Open Ask one" });
		first.focus();
		expect(document.activeElement).toBe(first);
		fireEvent.keyDown(first, { key: "Enter" });
		fireEvent.click(first);
		expect(onOpen).toHaveBeenCalledTimes(1);
		expect(onOpen.mock.calls[0]?.[0]).toMatchObject({ ref: "a" });
	});

	it("is not a control at all when there is nothing to open", () => {
		board();
		expect(screen.queryByRole("button", { name: /^Open / })).toBeNull();
		expect(screen.getByText("Ask one")).toBeTruthy();
	});

	it("offers a move to every other column, by name", async () => {
		const onMove = vi.fn();
		board({ onMove });
		await open(screen.getByRole("button", { name: "Move Ask one" }));
		const offered = screen
			.getAllByRole("menuitem")
			.map((item) => item.textContent);
		// Its own column is not a move.
		expect(offered).toEqual(["Accepted", "Rejected"]);
	});

	it("reports a move and changes nothing itself", async () => {
		const onMove = vi.fn();
		board({ onMove });
		await open(screen.getByRole("button", { name: "Move Ask one" }));
		await act(async () => {
			fireEvent.click(screen.getByRole("menuitem", { name: "Accepted" }));
		});
		expect(onMove).toHaveBeenCalledTimes(1);
		expect(onMove.mock.calls[0]?.[0]).toMatchObject({ ref: "a" });
		expect(onMove.mock.calls[0]?.[1]).toMatchObject({ id: "accepted" });
		// The consumer owns the write: the card is still where it was.
		expect(within(column("Open")).getByText("Ask one")).toBeTruthy();
		expect(within(column("Accepted")).queryByText("Ask one")).toBeNull();
	});
});

describe("a board the consumer did not open to moves does not offer them", () => {
	it("carries no move control without onMove", () => {
		board();
		expect(screen.queryByRole("button", { name: /^Move / })).toBeNull();
	});

	it("drops a column canMove refuses, and the card with no target at all", async () => {
		const onMove = vi.fn();
		board({
			onMove,
			canMove: (row, target) =>
				row.owner === "jane" && target.id === "accepted",
		});
		// "Ask two" is sam's: no column accepts it, so it offers no move.
		expect(screen.queryByRole("button", { name: "Move Ask two" })).toBeNull();
		await open(screen.getByRole("button", { name: "Move Ask one" }));
		expect(
			screen.getAllByRole("menuitem").map((item) => item.textContent),
		).toEqual(["Accepted"]);
	});
});

describe("the board stays generic", () => {
	it("works with two columns and a field that is not a status", () => {
		const onMove = vi.fn();
		render(
			<Board<Row>
				items={ROWS}
				columns={[
					{ id: "jane", label: "Jane" },
					{ id: "sam", label: "Sam" },
				]}
				columnOf={(row) => row.owner}
				idOf={(row) => row.ref}
				labelOf={(row) => row.name}
				renderCard={(row) => <span>{row.name}</span>}
				onMove={onMove}
			/>,
		);
		expect(within(column("Jane")).getAllByRole("listitem")).toHaveLength(2);
		expect(within(column("Sam")).getAllByRole("listitem")).toHaveLength(1);
	});

	it("shows no card for a column it was not given", () => {
		board({ columns: [COLUMNS[0] as BoardColumn] });
		expect(screen.getByText("Ask one")).toBeTruthy();
		expect(screen.queryByText("Settled thing")).toBeNull();
	});
});

describe("dragging a card onto a column", () => {
	it("reports the move and leaves the board exactly as it was", async () => {
		layOut();
		const onMove = vi.fn();
		board({ onMove });
		await dragCard("a", [450, 200]);
		expect(onMove).toHaveBeenCalledTimes(1);
		expect(onMove.mock.calls[0]?.[0]).toMatchObject({ ref: "a" });
		expect(onMove.mock.calls[0]?.[1]).toMatchObject({ id: "accepted" });
		expect(within(column("Open")).getByText("Ask one")).toBeTruthy();
		expect(within(column("Accepted")).queryByText("Ask one")).toBeNull();
	});

	it("reports nothing for a drop back on the card's own column", async () => {
		layOut();
		const onMove = vi.fn();
		board({ onMove });
		await dragCard("a", [150, 300]);
		expect(onMove).not.toHaveBeenCalled();
	});

	it("reports nothing for a drop on no column at all", async () => {
		layOut();
		const onMove = vi.fn();
		board({ onMove });
		await dragCard("a", [900, 900]);
		expect(onMove).not.toHaveBeenCalled();
	});

	it("reports nothing for a drop on a column canMove refuses", async () => {
		layOut();
		const onMove = vi.fn();
		board({ onMove, canMove: (_row, target) => target.id === "rejected" });
		await dragCard("a", [450, 200]);
		expect(onMove).not.toHaveBeenCalled();
		await dragCard("a", [760, 200]);
		expect(onMove).toHaveBeenCalledTimes(1);
		expect(onMove.mock.calls[0]?.[1]).toMatchObject({ id: "rejected" });
	});

	it("reports nothing on a read-only board", async () => {
		layOut();
		const onOpen = vi.fn();
		board({ onOpen });
		await dragCard("a", [450, 200]);
		expect(onOpen).not.toHaveBeenCalled();
	});

	it("says what the drop would do, for a reader who cannot see the card", async () => {
		layOut();
		board({ onMove: vi.fn() });
		await dragCard("a", [450, 200]);
		expect(document.body.textContent).toContain(
			"Ask one was dropped on Accepted.",
		);
	});

	it("tells a screen reader about the Move control, not about arrow keys it has no sensor for", () => {
		board({ onMove: vi.fn() });
		const text = document.body.textContent ?? "";
		expect(text).toContain("also carries a Move control");
		expect(text).not.toContain("arrow keys");
	});

	it("offers the drag cursor only on a card that may actually move", () => {
		const { container } = board({
			onMove: vi.fn(),
			canMove: (row) => row.ref === "a",
		});
		const style = (id: string) =>
			(container.querySelector(`[data-board-card="${id}"]`) as HTMLElement)
				.style.cursor;
		expect(style("a")).toBe("grab");
		expect(style("b")).toBe("");
	});
});
