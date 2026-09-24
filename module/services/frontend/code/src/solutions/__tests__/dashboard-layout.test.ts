import type { Dashboard, DataGraph } from "@codefly/saas-plugin-manifest";
import { describe, expect, it } from "vitest";
import {
	addableTiles,
	addTile,
	defaultLayout,
	dropOn,
	layoutKey,
	moveBy,
	moveTile,
	parseLayout,
	readSavedLayout,
	removeTile,
	serializeLayout,
	tileWidget,
	writeSavedLayout,
} from "../dashboard-layout";

// A graph with one metric on no dashboard (`per_day`), one drawn only on the
// second dashboard (`returns`), and one derived from two others (`net`).
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
		expect(parseLayout(null, graph, main)).toEqual(["w_items", "w_net"]);
	});

	it("resolves a declared widget as declared", () => {
		expect(tileWidget(graph, main, "w_items")).toBe(main.widgets[0]);
		expect(tileWidget(graph, main, "w_returns")).toBeUndefined();
		expect(tileWidget(graph, main, "gone")).toBeUndefined();
	});

	it("draws an added metric with the widget another dashboard declares for it", () => {
		expect(tileWidget(graph, main, "metric:returns")).toEqual({
			id: "metric:returns",
			metric: "returns",
			visualization: "bar",
			title: "Returns by type",
		});
	});

	it("draws a metric no dashboard declares as a line over time, else a number", () => {
		expect(tileWidget(graph, main, "metric:per_day")).toEqual({
			id: "metric:per_day",
			metric: "per_day",
			visualization: "line",
			title: "Items per day",
		});
		const bare = withWidgets([main.widgets[0]]);
		expect(tileWidget(graph, bare, "metric:net")?.visualization).toBe("number");
		expect(tileWidget(graph, main, "metric:gone")).toBeUndefined();
	});

	it("offers removed widgets and undrawn metrics in metric order", () => {
		expect(addableTiles(graph, main, defaultLayout(main))).toEqual([
			{ id: "metric:returns", label: "Returns" },
			{ id: "metric:per_day", label: "Items per day" },
		]);
		const layout = removeTile(defaultLayout(main), "w_items");
		expect(layout).toEqual(["w_net"]);
		expect(addableTiles(graph, main, layout).map((tile) => tile.id)).toEqual([
			"w_items",
			"metric:returns",
			"metric:per_day",
		]);
		const back = addTile(layout, "metric:per_day");
		expect(back).toEqual(["w_net", "metric:per_day"]);
		expect(addableTiles(graph, main, back).map((tile) => tile.id)).toEqual([
			"w_items",
			"metric:returns",
		]);
		expect(addTile(back, "w_net")).toEqual(back);
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

	it("puts a dragged tile in the place of the tile it is dragged over", () => {
		const layout = ["a", "b", "c", "d"];
		expect(dropOn(layout, "d", "b")).toEqual(["a", "d", "b", "c"]);
		expect(dropOn(layout, "a", "c")).toEqual(["b", "c", "a", "d"]);
		expect(dropOn(layout, "a", "a")).toEqual(layout);
		expect(dropOn(layout, "a", "gone")).toEqual(layout);
	});

	it("keys a layout by solution and dashboard", () => {
		expect(layoutKey("example", "main")).toBe(
			"solution-dashboard:layout:example:main",
		);
	});

	it("round-trips a saved layout", () => {
		const storage = memoryStorage();
		const layout = ["metric:per_day", "w_net"];
		expect(writeSavedLayout(storage, "k", serializeLayout(layout, main))).toBe(
			true,
		);
		expect(parseLayout(readSavedLayout(storage, "k"), graph, main)).toEqual(
			layout,
		);
		expect(readSavedLayout(storage, "other")).toBeNull();
	});

	it("keeps a saved empty layout empty", () => {
		expect(parseLayout(serializeLayout([], main), graph, main)).toEqual([]);
	});

	it("drops tiles the graph no longer declares, and repeats", () => {
		const raw = JSON.stringify({
			version: 1,
			tiles: ["w_net", "w_gone", "metric:gone", "w_net", "w_items"],
			seen: ["w_items", "w_net"],
		});
		expect(parseLayout(raw, graph, main)).toEqual(["w_net", "w_items"]);
	});

	it("appends a widget declared after the save, but not one the viewer removed", () => {
		const before = withWidgets([main.widgets[0]]);
		const raw = serializeLayout(["metric:per_day"], before);
		expect(parseLayout(raw, graph, main)).toEqual(["metric:per_day", "w_net"]);
	});

	it("turns an added metric into the widget the dashboard now declares for it", () => {
		const raw = serializeLayout(["metric:per_day", "w_items"], main);
		const declared = withWidgets([
			...main.widgets,
			{ id: "w_per_day", metric: "per_day", visualization: "area" },
		]);
		expect(parseLayout(raw, graph, declared)).toEqual(["w_per_day", "w_items"]);
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
			expect(parseLayout(raw, graph, main), raw).toEqual(fallback);
		}
	});

	it("reads nothing and writes nothing when storage is missing or blocked", () => {
		expect(readSavedLayout(blocked, "k")).toBeNull();
		expect(readSavedLayout(null, "k")).toBeNull();
		expect(writeSavedLayout(blocked, "k", "{}")).toBe(false);
		expect(writeSavedLayout(null, "k", "{}")).toBe(false);
	});
});
