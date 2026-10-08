import type {
	Dashboard,
	DataGraph,
	MetricWidget,
} from "@codefly/saas-plugin-manifest";

/**
 * One viewer's arrangement of a solution dashboard: which of its declared
 * widgets it shows, in what order. Pure data in, data out — no React, no DOM;
 * the caller hands in the storage.
 *
 * A layout is a list of declared widget ids. It is a preference in the sense of
 * ADR 0007: it can reorder, remove and re-add what the dashboard declares, and
 * never add a metric the dashboard does not draw. It carries no query of its
 * own, so a saved layout can never make a widget ask for data the declaration
 * does not already describe.
 */

const LAYOUT_VERSION = 1;

/** The declared widgets, in declared order: the layout every viewer starts with. */
export function defaultLayout(dashboard: Dashboard): string[] {
	return dashboard.widgets.map((widget) => widget.id);
}

/**
 * The widget a tile renders, or undefined when the dashboard no longer
 * declares it.
 */
export function tileWidget(
	dashboard: Dashboard,
	tileId: string,
): MetricWidget | undefined {
	return dashboard.widgets.find((widget) => widget.id === tileId);
}

/**
 * What the + menu offers, in declared order: every declared widget the layout
 * does not show.
 */
export function addableTiles(
	graph: DataGraph,
	dashboard: Dashboard,
	layout: readonly string[],
): { id: string; label: string }[] {
	return dashboard.widgets
		.filter((widget) => !layout.includes(widget.id))
		.map((widget) => {
			const metric = graph.metrics.find((m) => m.id === widget.metric);
			return {
				id: widget.id,
				label: widget.title ?? metric?.title ?? widget.metric,
			};
		});
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

/**
 * Dropping a tile on another swaps the two; no other tile moves. On a grid,
 * moving a tile into another's place instead would shift every tile between
 * them, which wraps across rows: a sideways drop would move two tiles and a
 * downward one three.
 */
export function swapTiles(
	layout: readonly string[],
	draggedId: string,
	targetId: string,
): string[] {
	const from = layout.indexOf(draggedId);
	const to = layout.indexOf(targetId);
	const next = [...layout];
	if (from === -1 || to === -1) return next;
	next[from] = targetId;
	next[to] = draggedId;
	return next;
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
 * Tiles the dashboard does not declare are dropped, and so are repeats: an id
 * the dashboard no longer declares, or anything else a stored value holds, never
 * reaches the page. A widget declared since the layout was saved is appended, so
 * a viewer who customized still sees what the solution adds.
 */
export function parseLayout(
	raw: string | null,
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
	// An unchanged saved default is not a customization. Rebase it on the
	// new declaration instead of appending new scalars behind old charts.
	if (
		tiles.length === seen.length &&
		tiles.every((id, index) => id === seen[index])
	)
		return fallback;
	const layout: string[] = [];
	for (const tileId of tiles) {
		if (tileWidget(dashboard, tileId) && !layout.includes(tileId)) {
			layout.push(tileId);
		}
	}
	for (const id of fallback) {
		if (!seen.includes(id) && !layout.includes(id)) layout.push(id);
	}
	return layout;
}
