import type { Dashboard, DataGraph } from "@codefly/saas-plugin-manifest";
import { describe, expect, it } from "vitest";
import {
	addableTiles,
	addTile,
	defaultLayout,
	layoutKey,
	moveBy,
	moveTile,
	parseLayout,
	readSavedLayout,
	removeTile,
	serializeLayout,
	swapTiles,
	tileWidget,
	writeSavedLayout,
} from "../dashboard-layout";
import platformSignins from "./fixtures/platform-signins.json";

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

describe("dashboard layout", () => {
	it("starts from the declared widgets in declared order", () => {
		expect(defaultLayout(main)).toEqual(["w_items", "w_net"]);
		expect(parseLayout(null, main)).toEqual(["w_items", "w_net"]);
	});

	it("resolves a declared widget as declared", () => {
		expect(tileWidget(main, "w_items")).toBe(main.widgets[0]);
		expect(tileWidget(main, "w_returns")).toBeUndefined();
		expect(tileWidget(main, "gone")).toBeUndefined();
	});

	it("offers only the declared widgets the layout does not show, in declared order", () => {
		// Metrics this dashboard does not draw (returns, per_day) are never offered.
		expect(addableTiles(graph, main, defaultLayout(main))).toEqual([]);
		const layout = removeTile(defaultLayout(main), "w_items");
		expect(layout).toEqual(["w_net"]);
		expect(addableTiles(graph, main, layout)).toEqual([
			{ id: "w_items", label: "Items made" },
		]);
		const back = addTile(layout, "w_items");
		expect(back).toEqual(["w_net", "w_items"]);
		expect(addableTiles(graph, main, back)).toEqual([]);
		expect(addTile(back, "w_net")).toEqual(back);
		expect(addableTiles(graph, main, []).map((tile) => tile.id)).toEqual([
			"w_items",
			"w_net",
		]);
	});

	it("labels an offered widget by its title, else its metric", () => {
		const layout = removeTile(defaultLayout(main), "w_net");
		const offered = addableTiles(graph, main, layout);
		expect(offered.find((tile) => tile.id === "w_net")?.label).toBe("net");
		expect(
			addableTiles(graph, main, []).find((tile) => tile.id === "w_items")
				?.label,
		).toBe("Items made");
	});

	it("moves a tile to an index clamped to the layout", () => {
		const layout = ["a", "b", "c", "d"];
		expect(moveTile(layout, "a", 2)).toEqual(["b", "c", "a", "d"]);
		expect(moveTile(layout, "d", -5)).toEqual(["d", "a", "b", "c"]);
		expect(moveTile(layout, "b", 99)).toEqual(["a", "c", "d", "b"]);
		expect(moveTile(layout, "x", 0)).toEqual(layout);
	});

	it("moves a tile one place with the keyboard, stopping at either end", () => {
		const layout = ["a", "b", "c"];
		expect(moveBy(layout, "b", -1)).toEqual(["b", "a", "c"]);
		expect(moveBy(layout, "b", 1)).toEqual(["a", "c", "b"]);
		expect(moveBy(layout, "a", -1)).toEqual(layout);
		expect(moveBy(layout, "c", 1)).toEqual(layout);
		expect(moveBy(layout, "x", 1)).toEqual(layout);
	});

	it("swaps a dropped tile with the tile it is dropped on, moving no other", () => {
		const layout = ["a", "b", "c", "d"];
		expect(swapTiles(layout, "a", "b")).toEqual(["b", "a", "c", "d"]);
		// Two rows down a two-column grid: b and d stay where they are.
		expect(swapTiles(layout, "a", "c")).toEqual(["c", "b", "a", "d"]);
		expect(swapTiles(layout, "d", "a")).toEqual(["d", "b", "c", "a"]);
		expect(swapTiles(layout, "a", "a")).toEqual(layout);
		expect(swapTiles(layout, "a", "gone")).toEqual(layout);
		expect(swapTiles(layout, "gone", "a")).toEqual(layout);
	});

	it("keys a layout by solution and dashboard", () => {
		expect(layoutKey("example", "main")).toBe(
			"solution-dashboard:layout:example:main",
		);
	});

	it("round-trips a saved layout", () => {
		const storage = memoryStorage();
		const layout = ["w_net", "w_items"];
		expect(writeSavedLayout(storage, "k", serializeLayout(layout, main))).toBe(
			true,
		);
		expect(parseLayout(readSavedLayout(storage, "k"), main)).toEqual(layout);
		expect(readSavedLayout(storage, "other")).toBeNull();
	});

	it("keeps a saved empty layout empty", () => {
		expect(parseLayout(serializeLayout([], main), main)).toEqual([]);
	});

	it("drops tiles the dashboard does not declare, and repeats", () => {
		const raw = JSON.stringify({
			version: 1,
			tiles: ["w_net", "w_gone", "metric:gone", "w_net", "w_items"],
			seen: ["w_items", "w_net"],
		});
		expect(parseLayout(raw, main)).toEqual(["w_net", "w_items"]);
	});

	it("appends a widget declared after the save, but not one the viewer removed", () => {
		const before = withWidgets([main.widgets[0]]);
		const raw = serializeLayout([], before);
		expect(parseLayout(raw, main)).toEqual(["w_net"]);
	});

	it("ignores a stored metric tile or another dashboard's widget", () => {
		// A layout from before ADR 0007 was applied, or one edited by hand, may
		// hold ids this dashboard does not declare; none of them is drawn.
		const raw = JSON.stringify({
			version: 1,
			tiles: ["metric:per_day", "w_returns", "w_items", "metric:items"],
			seen: ["w_items", "w_net"],
		});
		expect(parseLayout(raw, main)).toEqual(["w_items"]);
	});

	it("falls back to the default for an unreadable or untrusted entry", () => {
		const fallback = defaultLayout(main);
		for (const raw of [
			"not json",
			"null",
			"[]",
			JSON.stringify({ version: 2, tiles: [], seen: [] }),
			JSON.stringify({ version: 1, tiles: "w_net", seen: [] }),
			JSON.stringify({ version: 1, tiles: ["w_net"] }),
			JSON.stringify({ version: 1, tiles: [1], seen: [] }),
		]) {
			expect(parseLayout(raw, main), raw).toEqual(fallback);
		}
	});

	it("reads nothing and writes nothing when storage is missing or blocked", () => {
		expect(readSavedLayout(blocked, "k")).toBeNull();
		expect(readSavedLayout(null, "k")).toBeNull();
		expect(writeSavedLayout(blocked, "k", "{}")).toBe(false);
		expect(writeSavedLayout(null, "k", "{}")).toBe(false);
	});
});

// The platform-event fixture is the actual nine-widget declaration supplied
// with #1044, serialized without changing its event, metric or widget shape.
describe("a declaration growing from three widgets to nine", () => {
	const current = platformSignins.dashboards[0] as Dashboard;
	const before = {
		...current,
		widgets: [current.widgets[0], current.widgets[4], current.widgets[6]],
	};
	it("follows the declaration before any reorder, including a saved old default", () => {
		expect(defaultLayout(current)).toEqual(current.widgets.map((w) => w.id));
		expect(
			parseLayout(serializeLayout(defaultLayout(before), before), current),
		).toEqual(defaultLayout(current));
	});
	it("retains a custom order by identity and appends new ids once", () => {
		const order = [before.widgets[2].id, before.widgets[0].id];
		const saved = serializeLayout(order, before);
		expect(parseLayout(saved, current)).toEqual([
			...order,
			...current.widgets
				.filter((w) => !before.widgets.some((old) => old.id === w.id))
				.map((w) => w.id),
		]);
		const renamed = {
			...current,
			widgets: current.widgets
				.map((w) => ({ ...w, title: "Updated title" }))
				.reverse(),
		};
		expect(parseLayout(saved, renamed).slice(0, 2)).toEqual(order);
		expect(parseLayout(saved, current)).not.toContain(before.widgets[1].id);
	});
});
