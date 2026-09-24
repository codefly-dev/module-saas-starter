import type {
	Dashboard,
	DataGraph,
	MetricWidget,
} from "@codefly/saas-plugin-manifest";

/**
 * One viewer's arrangement of a solution dashboard: which tiles it shows, in
 * what order. Pure data in, data out — no React, no DOM; the caller hands in
 * the storage.
 *
 * A layout is a list of tile ids. A tile is either a widget the dashboard
 * declares (its widget id) or a metric the graph declares that this dashboard
 * has no widget for, added by the viewer (`metric:<id>`). A widget id is a
 * logical id and can never contain a colon, so the two never collide.
 *
 * A layout only picks from what the solution already declares. It carries no
 * query of its own, so a saved layout can never make a widget ask for data the
 * graph does not already describe.
 */

const LAYOUT_VERSION = 1;
const METRIC_TILE = "metric:";

/** The declared widgets, in declared order: the layout every viewer starts with. */
export function defaultLayout(dashboard: Dashboard): string[] {
	return dashboard.widgets.map((widget) => widget.id);
}

function metricTile(metricId: string): string {
	return `${METRIC_TILE}${metricId}`;
}

// Whether a metric's series is keyed by time. A derived metric has the shape of
// its inputs, which must all share one dimension to combine, so its first input
// answers for it.
function groupsByTime(
	graph: DataGraph,
	metricId: string,
	visited = new Set<string>(),
): boolean {
	const metric = graph.metrics.find((m) => m.id === metricId);
	if (!metric || visited.has(metricId)) return false;
	visited.add(metricId);
	if (metric.kind === "source") return metric.groupBy === "time";
	return groupsByTime(graph, metric.inputs[0], visited);
}

/**
 * The widget a tile renders, or undefined when the graph no longer declares
 * it. A metric tile borrows the widget another of the solution's dashboards
 * declares for that metric; a metric no dashboard draws gets a line when it
 * groups by time and a single number otherwise.
 */
export function tileWidget(
	graph: DataGraph,
	dashboard: Dashboard,
	tileId: string,
): MetricWidget | undefined {
	const declared = dashboard.widgets.find((widget) => widget.id === tileId);
	if (declared) return declared;
	if (!tileId.startsWith(METRIC_TILE)) return undefined;
	const metricId = tileId.slice(METRIC_TILE.length);
	const metric = graph.metrics.find((m) => m.id === metricId);
	if (!metric) return undefined;
	const borrowed = graph.dashboards
		.flatMap((d) => d.widgets)
		.find((widget) => widget.metric === metricId);
	return {
		id: tileId,
		metric: metricId,
		visualization:
			borrowed?.visualization ??
			(groupsByTime(graph, metricId) ? "line" : "number"),
		title: borrowed?.title ?? metric.title,
	};
}

/**
 * What the + menu offers, in the graph's metric order: every declared widget
 * the layout does not show, and every metric this dashboard has no widget for
 * that the viewer has not added.
 */
export function addableTiles(
	graph: DataGraph,
	dashboard: Dashboard,
	layout: readonly string[],
): { id: string; label: string }[] {
	const addable: { id: string; label: string }[] = [];
	for (const metric of graph.metrics) {
		const declared = dashboard.widgets.filter(
			(widget) => widget.metric === metric.id,
		);
		const tiles =
			declared.length > 0
				? declared.map((widget) => ({
						id: widget.id,
						label: widget.title ?? metric.title ?? metric.id,
					}))
				: [{ id: metricTile(metric.id), label: metric.title ?? metric.id }];
		for (const tile of tiles) {
			if (!layout.includes(tile.id)) addable.push(tile);
		}
	}
	return addable;
}

export function addTile(layout: readonly string[], tileId: string): string[] {
	return layout.includes(tileId) ? [...layout] : [...layout, tileId];
}

export function removeTile(
	layout: readonly string[],
	tileId: string,
): string[] {
	return layout.filter((id) => id !== tileId);
}

/** Moves one tile to `toIndex`, clamped to the layout. An unknown id is a no-op. */
export function moveTile(
	layout: readonly string[],
	tileId: string,
	toIndex: number,
): string[] {
	const from = layout.indexOf(tileId);
	if (from === -1) return [...layout];
	const to = Math.max(0, Math.min(layout.length - 1, toIndex));
	const next = layout.filter((id) => id !== tileId);
	next.splice(to, 0, tileId);
	return next;
}

/** The keyboard alternative to dragging: one place earlier (-1) or later (+1). */
export function moveBy(
	layout: readonly string[],
	tileId: string,
	delta: number,
): string[] {
	const from = layout.indexOf(tileId);
	return from === -1 ? [...layout] : moveTile(layout, tileId, from + delta);
}

/** Dragging a tile over another puts it in that tile's place. */
export function dropOn(
	layout: readonly string[],
	draggedId: string,
	targetId: string,
): string[] {
	const to = layout.indexOf(targetId);
	return to === -1 ? [...layout] : moveTile(layout, draggedId, to);
}

/**
 * The storage key of one dashboard's layout, before it is scoped to the viewer
 * and their organization. Solution ids are slugs and dashboard ids logical ids,
 * so neither can contain the separator.
 */
export function layoutKey(solutionId: string, dashboardId: string): string {
	return `solution-dashboard:layout:${solutionId}:${dashboardId}`;
}

/** The stored layout, or null when there is none or storage cannot be read. */
export function readSavedLayout(
	storage: Pick<Storage, "getItem"> | null,
	key: string,
): string | null {
	try {
		return storage?.getItem(key) ?? null;
	} catch {
		return null;
	}
}

/** Stores a layout; false when there is no storage or it refuses the write. */
export function writeSavedLayout(
	storage: Pick<Storage, "setItem"> | null,
	key: string,
	value: string,
): boolean {
	if (!storage) return false;
	try {
		storage.setItem(key, value);
		return true;
	} catch {
		return false;
	}
}

/**
 * A layout as stored: its tiles, plus the declared widgets it was made
 * against (`seen`), so a widget the solution declares later can be told apart
 * from one the viewer removed.
 */
export function serializeLayout(
	layout: readonly string[],
	dashboard: Dashboard,
): string {
	return JSON.stringify({
		version: LAYOUT_VERSION,
		tiles: layout,
		seen: defaultLayout(dashboard),
	});
}

function isStringArray(value: unknown): value is string[] {
	return (
		Array.isArray(value) && value.every((item) => typeof item === "string")
	);
}

/**
 * The layout a stored value describes, or the default when there is none or it
 * cannot be trusted. A saved empty layout stays empty: the viewer removed every
 * tile.
 *
 * Tiles the graph no longer declares are dropped, and so are repeats. A metric
 * tile whose metric this dashboard now declares a widget for becomes that
 * widget, in place. A widget declared since the layout was saved is appended,
 * so a viewer who customized still sees what the solution adds.
 */
export function parseLayout(
	raw: string | null,
	graph: DataGraph,
	dashboard: Dashboard,
): string[] {
	const fallback = defaultLayout(dashboard);
	if (raw === null) return fallback;
	let saved: unknown;
	try {
		saved = JSON.parse(raw);
	} catch {
		return fallback;
	}
	const { version, tiles, seen } = (saved ?? {}) as Record<string, unknown>;
	if (
		version !== LAYOUT_VERSION ||
		!isStringArray(tiles) ||
		!isStringArray(seen)
	) {
		return fallback;
	}
	const layout: string[] = [];
	for (const tileId of tiles) {
		const widget = tileWidget(graph, dashboard, tileId);
		if (!widget) continue;
		let id = tileId;
		if (tileId.startsWith(METRIC_TILE)) {
			const declared = dashboard.widgets.filter(
				(w) => w.metric === widget.metric,
			);
			if (declared.length > 0) {
				const free = declared.find((w) => !layout.includes(w.id));
				if (!free) continue;
				id = free.id;
			}
		}
		if (!layout.includes(id)) layout.push(id);
	}
	for (const id of fallback) {
		if (!seen.includes(id) && !layout.includes(id)) layout.push(id);
	}
	return layout;
}
