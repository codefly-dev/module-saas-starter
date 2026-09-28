import type { Dashboard, DataGraph } from "@codefly/saas-plugin-manifest";
import { describe, expect, it } from "vitest";
import {
	addableTiles,
	addSection,
	addTile,
	canRemoveSection,
	defaultLayout,
	type Layout,
	layoutKey,
	moveBy,
	moveSection,
	moveSectionBy,
	moveTileToSection,
	NEW_SECTION_TITLE,
	newSectionId,
	parseLayout,
	readSavedLayout,
	removeSection,
	removeTile,
	renameSection,
	sectionTitle,
	serializeLayout,
	swapTiles,
	tileWidget,
	UNSECTIONED,
	writeSavedLayout,
} from "../dashboard-layout";

// A graph with one metric on no dashboard (`per_day`), one drawn only on the
// second dashboard (`returns`), and one derived from two others (`net`). Only
// the first dashboard's own widgets can ever be on it.
const graph: DataGraph = {
	events: [
		{ name: "created", type: "example.item.created.v1" },
		{ name: "returned", type: "example.item.returned.v1" },
	],
	metrics: [
		{
			id: "items",
			kind: "source",
			title: "Items",
			filter: { event: "created" },
			groupBy: "event_type",
			aggregation: "count",
		},
		{
			id: "returns",
			kind: "source",
			title: "Returns",
			filter: { event: "returned" },
			groupBy: "event_type",
			aggregation: "count",
		},
		{
			id: "per_day",
			kind: "source",
			title: "Items per day",
			filter: { event: "created" },
			groupBy: "time",
			bucket: "day",
			aggregation: "count",
		},
		{
			id: "net",
			kind: "derived",
			operation: "difference",
			inputs: ["items", "returns"],
		},
	],
	dashboards: [
		{
			id: "main",
			layout: "grid",
			widgets: [
				{
					id: "w_items",
					metric: "items",
					visualization: "number",
					title: "Items made",
				},
				{ id: "w_net", metric: "net", visualization: "number" },
			],
		},
		{
			id: "other",
			layout: "stack",
			widgets: [
				{
					id: "w_returns",
					metric: "returns",
					visualization: "bar",
					title: "Returns by type",
				},
			],
		},
	],
};
const main = graph.dashboards[0];

function withWidgets(widgets: Dashboard["widgets"]): Dashboard {
	return { ...main, widgets };
}

function memoryStorage(initial: Record<string, string> = {}) {
	const data = new Map(Object.entries(initial));
	return {
		getItem: (key: string) => data.get(key) ?? null,
		setItem: (key: string, value: string) => {
			data.set(key, value);
		},
	};
}

const blocked = {
	getItem(): string | null {
		throw new Error("SecurityError");
	},
	setItem(): void {
		throw new Error("QuotaExceededError");
	},
};

// The one untitled section of a dashboard that declares none.
function flat(...tiles: string[]): Layout {
	return [{ id: UNSECTIONED, tiles }];
}

// `main` split into an overview band (the net number) above a detail band
// (the items made), and a section no widget is in yet.
const sectioned: Dashboard = {
	...main,
	sections: [
		{ id: "overview", title: "Overview", description: "The headline read." },
		{ id: "detail", title: "Detail" },
		{ id: "later", title: "Later" },
	],
	widgets: [
		{ ...main.widgets[0], section: "detail" },
		{ ...main.widgets[1], section: "overview" },
	],
};

function bands(overview: string[], detail: string[], later: string[] = []) {
	return [
		{ id: "overview", tiles: overview },
		{ id: "detail", tiles: detail },
		{ id: "later", tiles: later },
	];
}

// Ids of sections a viewer added, shaped as newSectionId makes them.
const MINE = "custom:0123456789abcdef";
const YOURS = "custom:fedcba9876543210";

describe("dashboard layout", () => {
	it("starts from the declared widgets in declared order", () => {
		expect(defaultLayout(main)).toEqual(flat("w_items", "w_net"));
		expect(parseLayout(null, main)).toEqual(flat("w_items", "w_net"));
	});

	it("resolves a declared widget as declared", () => {
		expect(tileWidget(main, "w_items")).toBe(main.widgets[0]);
		expect(tileWidget(main, "w_returns")).toBeUndefined();
		expect(tileWidget(main, "metric:per_day")).toBeUndefined();
		expect(tileWidget(main, "gone")).toBeUndefined();
	});

	it("offers only the declared widgets the layout does not show, in declared order", () => {
		// Metrics this dashboard does not draw (returns, per_day) are never offered.
		expect(addableTiles(graph, main, defaultLayout(main))).toEqual([]);
		const layout = removeTile(defaultLayout(main), "w_items");
		expect(layout).toEqual(flat("w_net"));
		expect(addableTiles(graph, main, layout)).toEqual([
			{ id: "w_items", label: "Items made" },
		]);
		const back = addTile(layout, main, "w_items", UNSECTIONED);
		expect(back).toEqual(flat("w_net", "w_items"));
		expect(addableTiles(graph, main, back)).toEqual([]);
		expect(addTile(back, main, "w_net", UNSECTIONED)).toEqual(back);
		expect(addableTiles(graph, main, flat()).map((tile) => tile.id)).toEqual([
			"w_items",
			"w_net",
		]);
	});

	it("never adds a tile the dashboard does not declare", () => {
		for (const id of ["w_returns", "metric:per_day", "gone"]) {
			expect(addTile(flat("w_net"), main, id, UNSECTIONED), id).toEqual(
				flat("w_net"),
			);
		}
	});

	it("labels an offered widget by its title, else its metric", () => {
		const layout = removeTile(defaultLayout(main), "w_net");
		const offered = addableTiles(graph, main, layout);
		expect(offered.find((tile) => tile.id === "w_net")?.label).toBe("net");
		expect(
			addableTiles(graph, main, flat()).find((tile) => tile.id === "w_items")
				?.label,
		).toBe("Items made");
	});

	it("moves a tile one place with the keyboard, stopping at either end", () => {
		const layout = flat("a", "b", "c");
		expect(moveBy(layout, "b", -1)).toEqual(flat("b", "a", "c"));
		expect(moveBy(layout, "b", 1)).toEqual(flat("a", "c", "b"));
		expect(moveBy(layout, "a", -1)).toEqual(layout);
		expect(moveBy(layout, "c", 1)).toEqual(layout);
		expect(moveBy(layout, "x", 1)).toEqual(layout);
	});

	it("swaps a dropped tile with the tile it is dropped on, moving no other", () => {
		const layout = flat("a", "b", "c", "d");
		expect(swapTiles(layout, "a", "b")).toEqual(flat("b", "a", "c", "d"));
		// Two rows down a two-column grid: b and d stay where they are.
		expect(swapTiles(layout, "a", "c")).toEqual(flat("c", "b", "a", "d"));
		expect(swapTiles(layout, "d", "a")).toEqual(flat("d", "b", "c", "a"));
		expect(swapTiles(layout, "a", "a")).toEqual(layout);
		expect(swapTiles(layout, "a", "gone")).toEqual(layout);
		expect(swapTiles(layout, "gone", "a")).toEqual(layout);
	});

	it("keys a layout by solution and dashboard", () => {
		expect(layoutKey("example", "main")).toBe(
			"solution-dashboard:layout:example:main",
		);
	});

	it("writes version 2: sections with their tiles, and what the dashboard declared", () => {
		expect(
			JSON.parse(serializeLayout(bands(["w_net"], ["w_items"]), sectioned)),
		).toEqual({
			version: 2,
			sections: bands(["w_net"], ["w_items"]),
			seen: ["w_items", "w_net"],
			seenSections: ["overview", "detail", "later"],
		});
		expect(JSON.parse(serializeLayout(flat("w_net"), main))).toEqual({
			version: 2,
			sections: flat("w_net"),
			seen: ["w_items", "w_net"],
			seenSections: [UNSECTIONED],
		});
	});

	it("round-trips a saved layout", () => {
		const storage = memoryStorage();
		const layout = flat("w_net", "w_items");
		expect(writeSavedLayout(storage, "k", serializeLayout(layout, main))).toBe(
			true,
		);
		expect(parseLayout(readSavedLayout(storage, "k"), main)).toEqual(layout);
		expect(readSavedLayout(storage, "other")).toBeNull();
	});

	it("keeps a saved empty layout empty", () => {
		expect(parseLayout(serializeLayout(flat(), main), main)).toEqual(flat());
	});

	it("drops tiles the dashboard does not declare, and repeats", () => {
		const raw = JSON.stringify({
			version: 2,
			sections: [
				{
					id: UNSECTIONED,
					tiles: ["w_net", "w_gone", "metric:gone", "w_net", "w_items"],
				},
			],
			seen: ["w_items", "w_net"],
			seenSections: [UNSECTIONED],
		});
		expect(parseLayout(raw, main)).toEqual(flat("w_net", "w_items"));
	});

	it("appends a widget declared after the save, but not one the viewer removed", () => {
		const before = withWidgets([main.widgets[0]]);
		const raw = serializeLayout(flat(), before);
		expect(parseLayout(raw, main)).toEqual(flat("w_net"));
	});

	it("reads the flat version-1 layout saved before sections", () => {
		// What the host wrote before sections: a list of widget ids and the
		// widgets declared then. w_items was removed; nothing brings it back.
		const raw = JSON.stringify({
			version: 1,
			tiles: ["w_net"],
			seen: ["w_items", "w_net"],
		});
		expect(parseLayout(raw, main)).toEqual(flat("w_net"));
		const reordered = JSON.stringify({
			version: 1,
			tiles: ["w_net", "w_items"],
			seen: ["w_items", "w_net"],
		});
		expect(parseLayout(reordered, main)).toEqual(flat("w_net", "w_items"));
	});

	it("drops a stored metric tile or another dashboard's widget from a version-1 layout", () => {
		// A version-1 layout from before ADR 0007 was applied, or one edited by
		// hand, may hold ids this dashboard does not declare; none of them is
		// drawn, and each repeat is dropped.
		const raw = JSON.stringify({
			version: 1,
			tiles: [
				"metric:per_day",
				"w_returns",
				"w_items",
				"metric:items",
				"w_items",
			],
			seen: ["w_items", "w_net"],
		});
		expect(parseLayout(raw, main)).toEqual(flat("w_items"));
	});

	it("falls back to the default for an unreadable or untrusted entry", () => {
		const fallback = defaultLayout(main);
		for (const raw of [
			"not json",
			"null",
			"[]",
			JSON.stringify({ version: 3, sections: [], seen: [], seenSections: [] }),
			JSON.stringify({ version: 2, tiles: [], seen: [], seenSections: [] }),
			JSON.stringify({
				version: 2,
				sections: [{ id: "a" }],
				seen: [],
				seenSections: [],
			}),
			JSON.stringify({
				version: 2,
				sections: [{ tiles: [] }],
				seen: [],
				seenSections: [],
			}),
			JSON.stringify({
				version: 2,
				sections: [null],
				seen: [],
				seenSections: [],
			}),
			JSON.stringify({ version: 2, sections: [], seenSections: [] }),
			// Version 2 always records the sections it was made against.
			JSON.stringify({
				version: 2,
				sections: [{ id: UNSECTIONED, tiles: ["w_net"] }],
				seen: ["w_items", "w_net"],
			}),
			JSON.stringify({ version: 1, tiles: "w_net", seen: [] }),
			JSON.stringify({ version: 1, tiles: ["w_net"] }),
			JSON.stringify({ version: 1, tiles: [1], seen: [] }),
		]) {
			expect(parseLayout(raw, main), raw).toEqual(fallback);
		}
	});

	describe("in sections", () => {
		it("starts each declared widget in its declared section, sections in declared order", () => {
			expect(defaultLayout(sectioned)).toEqual(bands(["w_net"], ["w_items"]));
			expect(parseLayout(null, sectioned)).toEqual(
				bands(["w_net"], ["w_items"]),
			);
		});

		it("swaps two tiles across sections, moving no other", () => {
			const layout = bands(["a", "b"], ["c", "d"]);
			expect(swapTiles(layout, "a", "d")).toEqual(
				bands(["d", "b"], ["c", "a"]),
			);
		});

		it("moves a tile to the end of a section, its own or another", () => {
			const layout = bands(["a", "b"], ["c", "d"]);
			expect(moveTileToSection(layout, "a", "detail")).toEqual(
				bands(["b"], ["c", "d", "a"]),
			);
			expect(moveTileToSection(layout, "a", "overview")).toEqual(
				bands(["b", "a"], ["c", "d"]),
			);
			expect(moveTileToSection(layout, "a", "later")).toEqual(
				bands(["b"], ["c", "d"], ["a"]),
			);
			expect(moveTileToSection(layout, "a", "gone")).toEqual(layout);
			expect(moveTileToSection(layout, "gone", "detail")).toEqual(layout);
		});

		it("puts a removed widget back at the end of the section whose + was used", () => {
			const layout = removeTile(defaultLayout(sectioned), "w_items");
			expect(layout).toEqual(bands(["w_net"], []));
			// Only the removed widget is offered, whichever section asks.
			expect(addableTiles(graph, sectioned, layout)).toEqual([
				{ id: "w_items", label: "Items made" },
			]);
			expect(addTile(layout, sectioned, "w_items", "overview")).toEqual(
				bands(["w_net", "w_items"], []),
			);
			expect(addTile(layout, sectioned, "w_items", "later")).toEqual(
				bands(["w_net"], [], ["w_items"]),
			);
			// A tile already shown stays where it is.
			expect(addTile(layout, sectioned, "w_net", "later")).toEqual(layout);
		});

		it("crosses into the neighbouring section with the keyboard at a section's end", () => {
			const layout = bands(["a", "b"], ["c"]);
			// The last of a section moves down into the next, as its first...
			expect(moveBy(layout, "b", 1)).toEqual(bands(["a"], ["b", "c"]));
			// ...and the first of a section up into the one before, as its last.
			expect(moveBy(layout, "c", -1)).toEqual(bands(["a", "b", "c"], []));
			// An empty section is a place too.
			expect(moveBy(layout, "c", 1)).toEqual(bands(["a", "b"], [], ["c"]));
			expect(moveBy(layout, "a", -1)).toEqual(layout);
			expect(moveBy(bands([], [], ["x"]), "x", 1)).toEqual(
				bands([], [], ["x"]),
			);
		});

		it("round-trips each tile's section", () => {
			const layout = bands(["w_items"], [], ["w_net"]);
			expect(
				parseLayout(serializeLayout(layout, sectioned), sectioned),
			).toEqual(layout);
		});

		it("reads a version-1 layout by putting each tile in its declared section", () => {
			const raw = JSON.stringify({
				version: 1,
				tiles: ["metric:per_day", "w_items", "w_net"],
				seen: ["w_items", "w_net"],
			});
			expect(parseLayout(raw, sectioned)).toEqual(
				bands(["w_net"], ["w_items"]),
			);
		});

		it("sends a tile whose section is gone to its declared section, and drops the section", () => {
			const raw = JSON.stringify({
				version: 2,
				sections: [
					{ id: "retired", tiles: ["w_net", "metric:per_day"] },
					{ id: "detail", tiles: ["w_items"] },
				],
				seen: ["w_items", "w_net"],
				seenSections: ["retired", "detail"],
			});
			expect(parseLayout(raw, sectioned)).toEqual(
				bands(["w_net"], ["w_items"]),
			);
		});

		it("appends a widget declared after the save to its declared section", () => {
			const before: Dashboard = {
				...sectioned,
				widgets: [sectioned.widgets[0]],
			};
			const raw = serializeLayout(bands(["w_items"], []), before);
			expect(parseLayout(raw, sectioned)).toEqual(
				bands(["w_items", "w_net"], []),
			);
		});

		it("does not bring back a widget the viewer removed", () => {
			const raw = serializeLayout(bands([], ["w_items"]), sectioned);
			expect(parseLayout(raw, sectioned)).toEqual(bands([], ["w_items"]));
		});

		it("moves a layout saved without sections into the sections declared since", () => {
			const raw = serializeLayout(flat("w_items"), main);
			expect(parseLayout(raw, sectioned)).toEqual(bands([], ["w_items"]));
		});
	});

	describe("the viewer's own sections", () => {
		// A section the viewer added, with its tiles.
		const own = (id: string, title: string, tiles: string[] = []) => ({
			id,
			title,
			tiles,
		});
		const read = (layout: Layout, dashboard = sectioned) =>
			parseLayout(serializeLayout(layout, dashboard), dashboard);

		it("adds an empty section after the last, never under a declared id", () => {
			const layout = bands(["a"], ["b"]);
			const id = newSectionId(layout);
			expect(id).toMatch(/^custom:[0-9a-f]{16}$/);
			expect(addSection(layout, id)).toEqual([
				...layout,
				own(id, NEW_SECTION_TITLE),
			]);
			// A declared id, one not shaped as a viewer's, or one the layout
			// already has, adds nothing.
			expect(addSection(layout, "overview")).toEqual(layout);
			expect(addSection(layout, "custom:a1")).toEqual(layout);
			const added = addSection(layout, MINE);
			expect(addSection(added, MINE)).toEqual(added);
			expect(newSectionId(added)).not.toBe(MINE);
		});

		it("adds no tile, so the page offers nothing new", () => {
			const layout = addSection(defaultLayout(sectioned), MINE);
			expect(layout.at(-1)).toEqual(own(MINE, NEW_SECTION_TITLE));
			expect(addableTiles(graph, sectioned, layout)).toEqual([]);
			// It is filled by dragging a tile in.
			expect(moveTileToSection(layout, "w_items", MINE).at(-1)).toEqual(
				own(MINE, NEW_SECTION_TITLE, ["w_items"]),
			);
		});

		it("renames a section, trimmed and cut to 60 characters", () => {
			const layout = [...bands(["a"], ["b"]), own(MINE, "Mine")];
			expect(
				renameSection(layout, sectioned, MINE, "  Watch list  ")[3],
			).toEqual(own(MINE, "Watch list"));
			const long = renameSection(layout, sectioned, "detail", "x".repeat(70));
			expect(long[1]).toEqual({
				id: "detail",
				title: "x".repeat(60),
				tiles: ["b"],
			});
			expect(sectionTitle(sectioned, long[1])).toBe("x".repeat(60));
			// Untouched, a declared section goes by its declared title.
			expect(sectionTitle(sectioned, long[0])).toBe("Overview");
		});

		it("keeps the title a blank rename would clear", () => {
			const layout = [...bands(["a"], []), own(MINE, "Mine")];
			expect(renameSection(layout, sectioned, MINE, "   ")).toEqual(layout);
			expect(renameSection(layout, sectioned, "overview", "")).toEqual(layout);
		});

		it("follows the declared title again once a section is renamed back to it", () => {
			const renamed = renameSection(
				bands(["a"], []),
				sectioned,
				"overview",
				"Headlines",
			);
			expect(renamed[0]).toEqual({
				id: "overview",
				title: "Headlines",
				tiles: ["a"],
			});
			expect(
				renameSection(renamed, sectioned, "overview", " Overview ")[0],
			).toEqual({ id: "overview", tiles: ["a"] });
		});

		it("never renames or removes the untitled section", () => {
			const layout = addSection(flat("a"), MINE);
			expect(renameSection(layout, main, UNSECTIONED, "Top")).toEqual(layout);
			expect(canRemoveSection(layout, UNSECTIONED)).toBe(false);
			expect(removeSection(layout, UNSECTIONED)).toEqual(layout);
			expect(sectionTitle(main, layout[0])).toBeUndefined();
		});

		it("moves a removed section's tiles to the end of the section above", () => {
			const layout = [...bands(["a"], ["b", "c"]), own(MINE, "Mine", ["d"])];
			expect(removeSection(layout, "detail")).toEqual([
				{ id: "overview", tiles: ["a", "b", "c"] },
				{ id: "later", tiles: [] },
				own(MINE, "Mine", ["d"]),
			]);
			expect(removeSection(layout, MINE)).toEqual(
				bands(["a"], ["b", "c"], ["d"]),
			);
			// On a dashboard without sections the one above is the untitled one.
			expect(
				removeSection([...flat("a"), own(MINE, "Mine", ["b"])], MINE),
			).toEqual(flat("a", "b"));
		});

		it("moves the first section's tiles to the start of the one below", () => {
			expect(removeSection(bands(["a", "b"], ["c"]), "overview")).toEqual([
				{ id: "detail", tiles: ["a", "b", "c"] },
				{ id: "later", tiles: [] },
			]);
		});

		it("does not remove the last section left", () => {
			const last = [{ id: "detail", tiles: ["a"] }];
			expect(canRemoveSection(last, "detail")).toBe(false);
			expect(removeSection(last, "detail")).toEqual(last);
			expect(canRemoveSection(bands([], []), "detail")).toBe(true);
			expect(canRemoveSection(bands([], []), "gone")).toBe(false);
		});

		it("moves a section to a place, and one place with the keyboard", () => {
			const layout = [...bands(["a"], ["b"]), own(MINE, "Mine")];
			const ids = (moved: Layout) => moved.map((section) => section.id);
			expect(ids(moveSection(layout, MINE, 0))).toEqual([
				MINE,
				"overview",
				"detail",
				"later",
			]);
			// Its tiles go with it.
			expect(moveSection(layout, "overview", 2)[2]).toEqual({
				id: "overview",
				tiles: ["a"],
			});
			expect(ids(moveSection(layout, "overview", 99))).toEqual([
				"detail",
				"later",
				MINE,
				"overview",
			]);
			expect(ids(moveSectionBy(layout, "detail", -1))).toEqual([
				"detail",
				"overview",
				"later",
				MINE,
			]);
			expect(moveSectionBy(layout, "overview", -1)).toEqual(layout);
			expect(moveSectionBy(layout, MINE, 1)).toEqual(layout);
			expect(moveSection(layout, "gone", 0)).toEqual(layout);
		});

		it("keeps the untitled section first when sections move", () => {
			const layout = [...flat("a"), own(MINE, "One"), own(YOURS, "Two")];
			const ids = (moved: Layout) => moved.map((section) => section.id);
			expect(ids(moveSection(layout, YOURS, 0))).toEqual([
				UNSECTIONED,
				YOURS,
				MINE,
			]);
			expect(moveSectionBy(layout, MINE, -1)).toEqual(layout);
			expect(moveSection(layout, UNSECTIONED, 2)).toEqual(layout);
		});

		it("round-trips added, renamed and reordered sections", () => {
			const layout = [
				{ id: "detail", title: "Items", tiles: ["w_items"] },
				own(MINE, "Mine", ["w_net"]),
				{ id: "overview", tiles: [] },
				{ id: "later", tiles: [] },
			];
			expect(read(layout)).toEqual(layout);
			// On a dashboard without sections, too.
			const plain = [...flat("w_items"), own(MINE, "Mine", ["w_net"])];
			expect(read(plain, main)).toEqual(plain);
		});

		it("keeps a removed declared section removed", () => {
			const removed = removeSection(defaultLayout(sectioned), "overview");
			expect(read(removed)).toEqual([
				{ id: "detail", tiles: ["w_net", "w_items"] },
				{ id: "later", tiles: [] },
			]);
		});

		it("adds a section declared since after the declared one before it, or first", () => {
			// Saved when the dashboard declared only detail, and a section of the
			// viewer's own after it.
			const before: Dashboard = {
				...sectioned,
				sections: [{ id: "detail", title: "Detail" }],
				widgets: sectioned.widgets.map((w) => ({ ...w, section: "detail" })),
			};
			const raw = serializeLayout(
				[{ id: "detail", tiles: ["w_items", "w_net"] }, own(MINE, "Mine")],
				before,
			);
			expect(parseLayout(raw, sectioned)).toEqual([
				{ id: "overview", tiles: [] },
				{ id: "detail", tiles: ["w_items", "w_net"] },
				{ id: "later", tiles: [] },
				own(MINE, "Mine"),
			]);
		});

		it("puts a widget declared since in its declared section, else the last one", () => {
			const before: Dashboard = { ...sectioned, widgets: [] };
			const kept = serializeLayout(
				[...bands([], []), own(MINE, "Mine")],
				before,
			);
			expect(parseLayout(kept, sectioned)).toEqual([
				...bands(["w_net"], ["w_items"]),
				own(MINE, "Mine"),
			]);
			// With overview removed, its new widget goes to the last section.
			const removed = serializeLayout(
				[{ id: "detail", tiles: [] }, own(MINE, "Mine")],
				before,
			);
			expect(parseLayout(removed, sectioned)).toEqual([
				{ id: "detail", tiles: ["w_items"] },
				own(MINE, "Mine", ["w_net"]),
			]);
		});

		it("drops a malformed section of the viewer's own, and sends its tiles home", () => {
			const raw = JSON.stringify({
				version: 2,
				sections: [
					{ id: "overview", tiles: [] },
					{ id: "detail", title: 7, tiles: [] },
					{ id: "later", tiles: [] },
					{ id: MINE, title: "Mine", tiles: [] },
					{ id: "custom:", title: "Empty token", tiles: [] },
					{ id: "custom:a1", title: "Short token", tiles: [] },
					{ id: "custom:0123456789ABCDEF", title: "Upper case", tiles: [] },
					{ id: YOURS, tiles: ["w_net"] },
					{ id: "custom:00000000000000c3", title: "   ", tiles: [] },
					{ id: MINE, title: "Again", tiles: ["w_items"] },
				],
				seen: ["w_items", "w_net"],
				seenSections: ["overview", "detail", "later"],
			});
			expect(parseLayout(raw, sectioned)).toEqual([
				// A declared section with a title that is not text keeps its own.
				...bands(["w_net"], [], []),
				// A repeated id is one section.
				own(MINE, "Mine", ["w_items"]),
			]);
		});

		it("falls back to the default when no section is left, or seenSections is not a list", () => {
			const fallback = defaultLayout(sectioned);
			for (const raw of [
				JSON.stringify({
					version: 2,
					sections: [{ id: "retired", tiles: ["w_net"] }],
					seen: ["w_items", "w_net"],
					seenSections: ["overview", "detail", "later"],
				}),
				JSON.stringify({
					version: 2,
					sections: [{ id: "overview", tiles: [] }],
					seen: [],
					seenSections: "overview",
				}),
			]) {
				expect(parseLayout(raw, sectioned), raw).toEqual(fallback);
			}
		});
	});

	it("reads nothing and writes nothing when storage is missing or blocked", () => {
		expect(readSavedLayout(blocked, "k")).toBeNull();
		expect(readSavedLayout(null, "k")).toBeNull();
		expect(writeSavedLayout(blocked, "k", "{}")).toBe(false);
		expect(writeSavedLayout(null, "k", "{}")).toBe(false);
	});
});
