import type {
	Dashboard,
	DataGraph,
	MetricWidget,
} from "@codefly/saas-plugin-manifest";

/**
 * One viewer's arrangement of a solution dashboard: which sections it has,
 * which of its declared widgets each shows, in what order. Pure data in, data
 * out — no React, no DOM; the caller hands in the storage.
 *
 * A layout is a list of sections, each with the ids of its tiles in order. It
 * starts as the dashboard's sections in declared order; a dashboard that
 * declares none has one untitled section. The viewer can rename a section,
 * remove one, and add their own after the last (`custom:<token>`). A declared
 * section id is a logical id and can never contain a colon, so a viewer's
 * section never collides with one the solution declares.
 *
 * A tile is a declared widget, by its id. A layout is a preference in the
 * sense of ADR 0007: it can reorder, remove and re-add what the dashboard
 * declares, and group it into sections, but never add a metric the dashboard
 * does not draw. It carries no query of its own, so a saved layout can never
 * make a widget ask for data the declaration does not already describe; a
 * section of the viewer's own is a title and a list of those same tiles.
 */

const LAYOUT_VERSION = 2;
// Saved before dashboards had sections: one flat list of tiles.
const FLAT_LAYOUT_VERSION = 1;
const CUSTOM_SECTION = "custom:";
// A section of the viewer's own, as newSectionId makes them: the prefix and
// 16 hex characters.
const CUSTOM_SECTION_ID = /^custom:[0-9a-f]{16}$/;

/**
 * The id of the one section of a dashboard that declares none. A section id
 * is a logical id, which is never empty, so the two never collide. It has no
 * title, and cannot be renamed or removed.
 */
export const UNSECTIONED = "";

/** What a section the viewer adds is called until they rename it. */
export const NEW_SECTION_TITLE = "New section";

/** The longest section title a viewer can give, in characters. */
export const SECTION_TITLE_MAX = 60;

/** One section of a layout: its tiles, in the order they are shown. */
export interface LayoutSection {
	id: string;
	/**
	 * The viewer's title for the section: one they added, or a declared one
	 * they renamed. Absent for a declared section under its declared title.
	 */
	title?: string;
	tiles: string[];
}

export type Layout = LayoutSection[];

// The sections a dashboard declares, in declared order, or the one untitled
// section.
function sectionIds(dashboard: Dashboard): string[] {
	return dashboard.sections && dashboard.sections.length > 0
		? dashboard.sections.map((section) => section.id)
		: [UNSECTIONED];
}

function declaredTitle(
	dashboard: Dashboard,
	sectionId: string,
): string | undefined {
	return dashboard.sections?.find((section) => section.id === sectionId)?.title;
}

// The section a declared widget is declared in: the one it names, or the
// untitled one of a dashboard that declares none. Undefined for an id the
// dashboard does not declare.
function declaredSection(
	dashboard: Dashboard,
	tileId: string,
): string | undefined {
	const widget = dashboard.widgets.find((w) => w.id === tileId);
	if (!widget) return undefined;
	const section = widget.section ?? UNSECTIONED;
	return sectionIds(dashboard).includes(section) ? section : undefined;
}

// Where a declared widget goes in the declared layout: its declared section,
// or the last section should the widget name none the dashboard declares
// (which the manifest validator refuses).
function homeSection(dashboard: Dashboard, tileId: string): string {
	const ids = sectionIds(dashboard);
	return declaredSection(dashboard, tileId) ?? ids[ids.length - 1];
}

/**
 * The declared widgets, in declared order, each in its declared section: the
 * layout every viewer starts with.
 */
export function defaultLayout(dashboard: Dashboard): Layout {
	return sectionIds(dashboard).map((id) => ({
		id,
		tiles: dashboard.widgets
			.filter((widget) => homeSection(dashboard, widget.id) === id)
			.map((widget) => widget.id),
	}));
}

/** Every tile a layout shows, section by section. */
export function layoutTiles(layout: readonly LayoutSection[]): string[] {
	return layout.flatMap((section) => section.tiles);
}

function copy(layout: readonly LayoutSection[]): Layout {
	return layout.map((section) => ({ ...section, tiles: [...section.tiles] }));
}

/**
 * The title a section is drawn under: the viewer's, else the declared one.
 * Undefined for the untitled section of a dashboard that declares none.
 */
export function sectionTitle(
	dashboard: Dashboard,
	section: LayoutSection,
): string | undefined {
	return section.title ?? declaredTitle(dashboard, section.id);
}

// A title as the viewer typed it, trimmed and cut to the longest allowed; null
// when nothing is left of it.
function titleText(title: string): string | null {
	const text = Array.from(title.trim())
		.slice(0, SECTION_TITLE_MAX)
		.join("")
		.trim();
	return text === "" ? null : text;
}

function isCustomSection(sectionId: string): boolean {
	return CUSTOM_SECTION_ID.test(sectionId);
}

/**
 * A fresh id for a section the viewer adds, which no section of the layout
 * has. From `crypto.getRandomValues`, which a page served over plain http has
 * too; `crypto.randomUUID` needs a secure context.
 */
export function newSectionId(layout: readonly LayoutSection[]): string {
	let id: string;
	do {
		const token = Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) =>
			b.toString(16).padStart(2, "0"),
		).join("");
		id = `${CUSTOM_SECTION}${token}`;
	} while (layout.some((section) => section.id === id));
	return id;
}

/**
 * Appends an empty section of the viewer's own, titled "New section". It
 * holds no tile until the viewer drags one in or puts a removed one back, so
 * it adds nothing to the page. An id that is not a custom section id, or that
 * the layout already has, adds nothing.
 */
export function addSection(
	layout: readonly LayoutSection[],
	sectionId: string,
): Layout {
	const next = copy(layout);
	if (
		isCustomSection(sectionId) &&
		!next.some((section) => section.id === sectionId)
	) {
		next.push({ id: sectionId, title: NEW_SECTION_TITLE, tiles: [] });
	}
	return next;
}

/**
 * Gives a section the viewer's title, trimmed and cut to 60 characters. A
 * blank title changes nothing, and neither does renaming the untitled section.
 * A declared section renamed back to its declared title follows the
 * declaration again, so a title the solution changes later shows.
 */
export function renameSection(
	layout: readonly LayoutSection[],
	dashboard: Dashboard,
	sectionId: string,
	title: string,
): Layout {
	const next = copy(layout);
	const text = titleText(title);
	const at = next.findIndex((section) => section.id === sectionId);
	if (at === -1 || text === null || sectionId === UNSECTIONED) return next;
	const { id, tiles } = next[at];
	next[at] =
		text === declaredTitle(dashboard, id)
			? { id, tiles }
			: { id, title: text, tiles };
	return next;
}

/**
 * Whether the viewer can remove a section: any but the last one left, and
 * never the untitled section of a dashboard that declares none.
 */
export function canRemoveSection(
	layout: readonly LayoutSection[],
	sectionId: string,
): boolean {
	return (
		sectionId !== UNSECTIONED &&
		layout.length > 1 &&
		layout.some((section) => section.id === sectionId)
	);
}

/**
 * Removes a section. Its tiles go to the end of the section above it, or to
 * the start of the one below when it is the first, so nothing the viewer had
 * on the dashboard is lost. A section that cannot be removed stays.
 */
export function removeSection(
	layout: readonly LayoutSection[],
	sectionId: string,
): Layout {
	const next = copy(layout);
	if (!canRemoveSection(layout, sectionId)) return next;
	const at = next.findIndex((section) => section.id === sectionId);
	const [removed] = next.splice(at, 1);
	if (at > 0) next[at - 1].tiles.push(...removed.tiles);
	else next[0].tiles.unshift(...removed.tiles);
	return next;
}

/**
 * Moves a section, with its tiles, to `toIndex`, clamped to the layout. The
 * untitled section of a dashboard that declares none stays first: it does not
 * move, and nothing goes before it. An unknown section is a no-op.
 */
export function moveSection(
	layout: readonly LayoutSection[],
	sectionId: string,
	toIndex: number,
): Layout {
	const next = copy(layout);
	const from = next.findIndex((section) => section.id === sectionId);
	if (from === -1 || sectionId === UNSECTIONED) return next;
	const first = next[0].id === UNSECTIONED ? 1 : 0;
	const [section] = next.splice(from, 1);
	next.splice(Math.max(first, Math.min(next.length, toIndex)), 0, section);
	return next;
}

/**
 * The keyboard alternative to dragging a section: one place up (-1) or down
 * (+1). Past either end it stays.
 */
export function moveSectionBy(
	layout: readonly LayoutSection[],
	sectionId: string,
	delta: number,
): Layout {
	const from = layout.findIndex((section) => section.id === sectionId);
	return from === -1
		? copy(layout)
		: moveSection(layout, sectionId, from + delta);
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
 * What a + menu offers, in declared order: every declared widget the layout
 * does not show, whichever section the menu belongs to.
 */
export function addableTiles(
	graph: DataGraph,
	dashboard: Dashboard,
	layout: readonly LayoutSection[],
): { id: string; label: string }[] {
	const shown = layoutTiles(layout);
	return dashboard.widgets
		.filter((widget) => !shown.includes(widget.id))
		.map((widget) => {
			const metric = graph.metrics.find((m) => m.id === widget.metric);
			return {
				id: widget.id,
				label: widget.title ?? metric?.title ?? widget.metric,
			};
		});
}

/**
 * Puts a removed widget back at the end of a section. A tile already shown is
 * left where it is, and an id the dashboard does not declare adds nothing.
 */
export function addTile(
	layout: readonly LayoutSection[],
	dashboard: Dashboard,
	tileId: string,
	sectionId: string,
): Layout {
	const next = copy(layout);
	if (!tileWidget(dashboard, tileId) || layoutTiles(next).includes(tileId)) {
		return next;
	}
	next.find((section) => section.id === sectionId)?.tiles.push(tileId);
	return next;
}

export function removeTile(
	layout: readonly LayoutSection[],
	tileId: string,
): Layout {
	return layout.map((section) => ({
		...section,
		tiles: section.tiles.filter((tile) => tile !== tileId),
	}));
}

// Moves one tile to `toIndex` in a section, clamped to the section. An unknown
// tile or section is a no-op.
function place(
	layout: readonly LayoutSection[],
	tileId: string,
	sectionId: string,
	toIndex: number,
): Layout {
	if (
		!layoutTiles(layout).includes(tileId) ||
		!layout.some((section) => section.id === sectionId)
	) {
		return copy(layout);
	}
	const next = removeTile(layout, tileId);
	const target = next.find((section) => section.id === sectionId);
	target?.tiles.splice(
		Math.max(0, Math.min(target.tiles.length, toIndex)),
		0,
		tileId,
	);
	return next;
}

/** Moves a tile to the end of a section, from its own or another. */
export function moveTileToSection(
	layout: readonly LayoutSection[],
	tileId: string,
	sectionId: string,
): Layout {
	return place(layout, tileId, sectionId, Number.POSITIVE_INFINITY);
}

/**
 * The keyboard alternative to dragging: one place earlier (-1) or later (+1).
 * Past either end of its section the tile crosses into the neighbouring one,
 * at the end nearer where it came from; past either end of the layout it stays.
 */
export function moveBy(
	layout: readonly LayoutSection[],
	tileId: string,
	delta: number,
): Layout {
	const at = layout.findIndex((section) => section.tiles.includes(tileId));
	if (at === -1) return copy(layout);
	const { id, tiles } = layout[at];
	const index = tiles.indexOf(tileId) + delta;
	if (index >= 0 && index < tiles.length) {
		return place(layout, tileId, id, index);
	}
	const neighbour = layout[at + Math.sign(delta)];
	if (!neighbour) return copy(layout);
	return place(
		layout,
		tileId,
		neighbour.id,
		delta < 0 ? neighbour.tiles.length : 0,
	);
}

/**
 * Dropping a tile on another swaps the two, in one section or across two; no
 * other tile moves. On a grid, moving a tile into another's place instead
 * would shift every tile between them, which wraps across rows: a sideways
 * drop would move two tiles and a downward one three.
 */
export function swapTiles(
	layout: readonly LayoutSection[],
	draggedId: string,
	targetId: string,
): Layout {
	const next = copy(layout);
	const find = (tileId: string) => {
		for (const section of next) {
			const index = section.tiles.indexOf(tileId);
			if (index !== -1) return { tiles: section.tiles, index };
		}
		return null;
	};
	const from = find(draggedId);
	const to = find(targetId);
	if (!from || !to) return next;
	from.tiles[from.index] = targetId;
	to.tiles[to.index] = draggedId;
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
 * A layout as stored: its sections, each with its tiles and the viewer's title
 * if they gave one, plus the declared widgets (`seen`) and sections
 * (`seenSections`) it was made against, so a widget or section the solution
 * declares later can be told apart from one the viewer removed.
 */
export function serializeLayout(
	layout: readonly LayoutSection[],
	dashboard: Dashboard,
): string {
	return JSON.stringify({
		version: LAYOUT_VERSION,
		sections: layout.map(({ id, title, tiles }) => ({ id, title, tiles })),
		seen: dashboard.widgets.map((widget) => widget.id),
		seenSections: sectionIds(dashboard),
	});
}

function isStringArray(value: unknown): value is string[] {
	return (
		Array.isArray(value) && value.every((item) => typeof item === "string")
	);
}

// A stored section: its id, its tiles, and whatever it holds as a title, which
// is checked where it is read.
interface StoredSection {
	id: string | null;
	title?: unknown;
	tiles: string[];
}

function isStoredSection(
	value: unknown,
): value is StoredSection & { id: string } {
	if (value === null || typeof value !== "object") return false;
	const { id, tiles } = value as Record<string, unknown>;
	return typeof id === "string" && isStringArray(tiles);
}

// A stored layout's sections, `seen` and `seenSections`, or null when it
// cannot be trusted. A layout saved in the flat shape of version 1 is one
// section with no id: none of its tiles has a place the layout says, so each
// goes to its declared section, and it was made against no section.
function storedLayout(saved: unknown): {
	sections: StoredSection[];
	seen: string[];
	seenSections: string[];
} | null {
	const { version, sections, tiles, seen, seenSections } = (saved ??
		{}) as Record<string, unknown>;
	if (!isStringArray(seen)) return null;
	if (version === FLAT_LAYOUT_VERSION) {
		return isStringArray(tiles)
			? { sections: [{ id: null, tiles }], seen, seenSections: [] }
			: null;
	}
	if (
		version !== LAYOUT_VERSION ||
		!Array.isArray(sections) ||
		!sections.every(isStoredSection) ||
		!isStringArray(seenSections)
	) {
		return null;
	}
	return { sections, seen, seenSections };
}

// The sections a stored layout keeps, in its order, with no tiles yet: each
// declared section it holds, under the viewer's title if they gave one, and
// each of the viewer's own whose id and title are well formed. A declared
// section it was made against but does not hold is one the viewer removed, and
// stays removed. One declared since goes after the nearest section declared
// before it that the layout has, or first when there is none. The untitled
// section of a dashboard that declares none is never removed.
function keptSections(
	stored: NonNullable<ReturnType<typeof storedLayout>>,
	dashboard: Dashboard,
): Layout {
	const declared = sectionIds(dashboard);
	const sections: Layout = [];
	const has = (id: string) => sections.some((section) => section.id === id);
	for (const { id, title } of stored.sections) {
		if (id === null || has(id)) continue;
		const text = typeof title === "string" ? titleText(title) : null;
		if (declared.includes(id)) {
			sections.push(
				text === null ||
					id === UNSECTIONED ||
					text === declaredTitle(dashboard, id)
					? { id, tiles: [] }
					: { id, title: text, tiles: [] },
			);
		} else if (isCustomSection(id) && text !== null) {
			sections.push({ id, title: text, tiles: [] });
		}
	}
	declared.forEach((id, index) => {
		if (has(id)) return;
		if (id !== UNSECTIONED && stored.seenSections.includes(id)) return;
		let at = 0;
		for (let before = index - 1; before >= 0; before--) {
			const found = sections.findIndex(
				(section) => section.id === declared[before],
			);
			if (found !== -1) {
				at = found + 1;
				break;
			}
		}
		sections.splice(at, 0, { id, tiles: [] });
	});
	return sections;
}

/**
 * The layout a stored value describes, or the default when there is none or it
 * cannot be trusted. A saved empty layout stays empty: the viewer removed every
 * tile. A layout always has at least one section.
 *
 * The sections are the ones the viewer kept, under their titles, in their
 * order, with each section the solution has declared since (see keptSections);
 * a section the layout holds that is neither declared nor well formed as the
 * viewer's own is dropped. Tiles the dashboard does not declare are dropped,
 * and so are repeats: an id the dashboard no longer declares, or anything else
 * a stored value holds, never reaches the page. A tile saved in a dropped
 * section (or in the flat shape saved before sections) goes to its declared
 * section if the layout has it, else to the last section. So does a widget
 * declared since the layout was saved, so a viewer who customized still sees
 * what the solution adds. When none of the sections the viewer kept is left
 * (the solution retired them), the layout gets the default's first section
 * back and the viewer's tiles go there: a widget they removed stays removed.
 */
export function parseLayout(raw: string | null, dashboard: Dashboard): Layout {
	const fallback = defaultLayout(dashboard);
	if (raw === null) return fallback;
	let saved: unknown;
	try {
		saved = JSON.parse(raw);
	} catch {
		return fallback;
	}
	const stored = storedLayout(saved);
	if (!stored) return fallback;
	const layout = keptSections(stored, dashboard);
	if (layout.length === 0) layout.push({ id: fallback[0].id, tiles: [] });

	const placed = new Set<string>();
	// Whether a saved id is a declared widget not yet placed.
	const placeable = (tileId: string) =>
		tileWidget(dashboard, tileId) !== undefined && !placed.has(tileId);
	const put = (sectionId: string, tileId: string) => {
		layout.find((section) => section.id === sectionId)?.tiles.push(tileId);
		placed.add(tileId);
	};
	const kept = (id: string | null): id is string =>
		id !== null && layout.some((section) => section.id === id);
	const home = (tileId: string): string => {
		const declared = declaredSection(dashboard, tileId);
		return declared !== undefined && kept(declared)
			? declared
			: layout[layout.length - 1].id;
	};

	for (const section of stored.sections) {
		if (!kept(section.id)) continue;
		for (const tileId of section.tiles) {
			if (placeable(tileId)) put(section.id, tileId);
		}
	}
	for (const section of stored.sections) {
		if (kept(section.id)) continue;
		for (const tileId of section.tiles) {
			if (placeable(tileId)) put(home(tileId), tileId);
		}
	}
	for (const widget of dashboard.widgets) {
		if (!stored.seen.includes(widget.id) && !placed.has(widget.id)) {
			put(home(widget.id), widget.id);
		}
	}
	return layout;
}
