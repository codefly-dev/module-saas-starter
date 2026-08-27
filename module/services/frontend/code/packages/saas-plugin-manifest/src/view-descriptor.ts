/**
 * The view descriptor: the contract between backend facets and frontend
 * display, consumed by `<Collection>` and reusable by `<Dashboard>`. Facets are
 * the seam — the backend attaches a named, kinded facet to each item and the
 * frontend renders it. A new facet becomes displayable by naming its kind and
 * adding one rule; no component changes.
 *
 * Two layers make that work. The **facet-kind registry** answers display-typing:
 * a facet's kind (e.g. `date`) fixes how its value renders (a `date` cell) and
 * the display affordances the kind supports (sort, filter, group-by). A view
 * cannot override a kind's render — that is the whole point of typing the facet.
 * A **view descriptor** then binds facets to presentation: which column, in what
 * order, with what icon/badge/color, and which of the kind's affordances the
 * view actually surfaces.
 *
 * Views are tweakable by precedence. A solution ships a default descriptor; a
 * user layers view preferences over it; a deployment layers skin tokens over
 * both. `resolveViewDescriptor` folds the base and its overrides down to one
 * fully-resolved descriptor a component can render without knowing the ladder
 * existed — the same defaults-then-overrides ladder the appearance resolver uses.
 */

/** How a facet's value is drawn in a cell. The closed set a renderer switches on. */
export type FacetRender =
	| "text"
	| "date"
	| "number"
	| "boolean"
	| "badge"
	| "avatar"
	| "tags";

/**
 * Kind of value a backend facet carries. Open the registry — add a member here
 * and a `FACET_KIND_HINTS` entry — to make a new kind displayable; a descriptor
 * rule then references it. That pair is the only change a new kind needs.
 */
export type FacetKind =
	| "text"
	| "date"
	| "number"
	| "boolean"
	| "enum"
	| "user"
	| "tag";

/** How a view arranges the items it lists. */
export type ViewType = "table" | "board" | "list" | "gallery";

/** Direction a facet sorts a view. */
export type SortDirection = "asc" | "desc";

/**
 * Display hints a facet kind exposes: the render its values take, and which
 * affordances the kind supports. These are typing facts — a view chooses which
 * supported affordances to surface, but cannot claim one the kind lacks.
 */
export interface FacetKindHint {
	render: FacetRender;
	sortable: boolean;
	filterable: boolean;
	groupable: boolean;
}

/**
 * The facet-kind registry. The seam that answers "how does the frontend know a
 * date facet renders as a date column": from its kind, here, once — not from
 * each descriptor rule.
 */
export const FACET_KIND_HINTS: Readonly<Record<FacetKind, FacetKindHint>> =
	Object.freeze({
		text: {
			render: "text",
			sortable: true,
			filterable: true,
			groupable: false,
		},
		date: { render: "date", sortable: true, filterable: true, groupable: true },
		number: {
			render: "number",
			sortable: true,
			filterable: true,
			groupable: false,
		},
		boolean: {
			render: "boolean",
			sortable: false,
			filterable: true,
			groupable: true,
		},
		enum: {
			render: "badge",
			sortable: false,
			filterable: true,
			groupable: true,
		},
		user: {
			render: "avatar",
			sortable: false,
			filterable: true,
			groupable: true,
		},
		tag: {
			render: "tags",
			sortable: false,
			filterable: true,
			groupable: false,
		},
	});

/**
 * One rule in a view descriptor: a facet, its kind, and how this view presents
 * it. Every field beyond `facet` and `kind` is optional and defaults on
 * resolution. `groupBy`, `sort`, and `filter` may only ask for affordances the
 * kind supports.
 */
export interface FacetRule {
	/** Facet name the backend attaches; graph-local, matches an item's facet key. */
	facet: string;
	kind: FacetKind;
	/** Column header; defaults to the facet name. */
	label?: string;
	/** Whether the facet occupies a column. Defaults to shown. */
	column?: boolean;
	/** Column position; defaults to the rule's index in the descriptor. */
	order?: number;
	/** Icon token, resolved against the deployment's icon set. */
	icon?: string;
	/** Render the value as a badge. */
	badge?: boolean;
	/** Color token, resolved against the deployment's skin. */
	color?: string;
	/** Group the view by this facet. Requires a groupable kind. */
	groupBy?: boolean;
	/** Sort the view by this facet in the given direction. Requires a sortable kind. */
	sort?: SortDirection;
	/** Surface a filter control for this facet. Requires a filterable kind. */
	filter?: boolean;
}

/** A solution's default view: a view type and the facets it displays. */
export interface ViewDescriptor {
	id: string;
	type: ViewType;
	facets: readonly FacetRule[];
}

/**
 * A per-facet tweak in an override layer. Names a facet already in the base
 * descriptor and sets any presentation or affordance field; it cannot introduce
 * a facet or restate its kind.
 */
export interface FacetOverride {
	facet: string;
	label?: string;
	column?: boolean;
	order?: number;
	icon?: string;
	badge?: boolean;
	color?: string;
	groupBy?: boolean;
	sort?: SortDirection;
	filter?: boolean;
}

/**
 * One layer over a base descriptor: user view preferences or a deployment skin.
 * Both are the same shape — they differ only in which fields they populate
 * (preferences reorder and re-sort; skins carry icon/color tokens).
 */
export interface ViewOverride {
	type?: ViewType;
	facets?: readonly FacetOverride[];
}

/** A facet resolved to everything a component needs to render it. */
export interface ResolvedFacet {
	facet: string;
	kind: FacetKind;
	render: FacetRender;
	sortable: boolean;
	filterable: boolean;
	groupable: boolean;
	label: string;
	column: boolean;
	order: number;
	icon?: string;
	badge: boolean;
	color?: string;
	groupBy: boolean;
	sort?: SortDirection;
	filter: boolean;
}

/** A descriptor folded down its precedence ladder, facets in column order. */
export interface ResolvedViewDescriptor {
	id: string;
	type: ViewType;
	facets: readonly ResolvedFacet[];
}

const LOGICAL_ID = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;
const SAFE_TOKEN = /^[a-z][a-z0-9]*(?:[-.][a-z0-9]+)*$/;
const VIEW_TYPES: readonly ViewType[] = ["table", "board", "list", "gallery"];
const FACET_KINDS = Object.keys(FACET_KIND_HINTS) as readonly FacetKind[];
const SORT_DIRECTIONS: readonly SortDirection[] = ["asc", "desc"];

const FACET_FIELDS: readonly string[] = [
	"facet",
	"kind",
	"label",
	"column",
	"order",
	"icon",
	"badge",
	"color",
	"groupBy",
	"sort",
	"filter",
];
// An override omits `kind`; a facet's kind is fixed by the base descriptor.
const OVERRIDE_FIELDS: readonly string[] = FACET_FIELDS.filter(
	(field) => field !== "kind",
);

function assertView(condition: unknown, message: string): asserts condition {
	if (!condition) throw new Error(`Invalid view descriptor: ${message}`);
}

function isObject(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

function assertExactKeys(
	value: Record<string, unknown>,
	allowed: readonly string[],
	context: string,
): void {
	const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
	assertView(
		unknown.length === 0,
		`${context} has unknown field '${unknown[0]}'`,
	);
}

function assertLogicalId(
	value: unknown,
	context: string,
): asserts value is string {
	assertView(
		typeof value === "string" && LOGICAL_ID.test(value),
		`${context} '${String(value)}' is not a valid logical id`,
	);
}

function assertUnique(values: readonly string[], kind: string): void {
	const seen = new Set<string>();
	for (const value of values) {
		assertView(
			!seen.has(value),
			`${kind} '${value}' is declared more than once`,
		);
		seen.add(value);
	}
}

function assertText(value: unknown, context: string): void {
	assertView(
		value === undefined ||
			(typeof value === "string" && value.trim().length > 0),
		`${context} must be a non-empty string`,
	);
}

function assertToken(value: unknown, context: string): void {
	assertView(
		value === undefined ||
			(typeof value === "string" && SAFE_TOKEN.test(value)),
		`${context} must be a token of lowercase letters, digits, '-' or '.'`,
	);
}

function assertBoolean(value: unknown, context: string): void {
	assertView(
		value === undefined || typeof value === "boolean",
		`${context} must be a boolean`,
	);
}

function assertOrder(value: unknown, context: string): void {
	assertView(
		value === undefined ||
			(typeof value === "number" && Number.isInteger(value) && value >= 0),
		`${context} must be a non-negative integer`,
	);
}

function assertSort(value: unknown, context: string): void {
	assertView(
		value === undefined || SORT_DIRECTIONS.includes(value as SortDirection),
		`${context} must be 'asc' or 'desc'`,
	);
}

/**
 * Type and format of every presentation and affordance field a facet may carry.
 * Shared by rule and override validation so a base rule and a later tweak are
 * held to the same typing before either reaches resolution.
 */
function assertFacetFields(
	value: Record<string, unknown>,
	context: string,
): void {
	assertText(value.label, `${context} label`);
	assertBoolean(value.column, `${context} column`);
	assertOrder(value.order, `${context} order`);
	assertToken(value.icon, `${context} icon`);
	assertBoolean(value.badge, `${context} badge`);
	assertToken(value.color, `${context} color`);
	assertBoolean(value.groupBy, `${context} groupBy`);
	assertSort(value.sort, `${context} sort`);
	assertBoolean(value.filter, `${context} filter`);
}

/**
 * Gates the affordances a facet requests on its kind supporting them. Kept apart
 * from field validation because it needs the resolved kind — a base rule states
 * its own kind, while an override borrows the base facet's, so the override path
 * runs this at resolution instead.
 */
function assertAffordanceGates(
	value: Record<string, unknown>,
	hint: FacetKindHint,
	context: string,
): void {
	assertView(
		!value.groupBy || hint.groupable,
		`${context} groups by a facet whose kind is not groupable`,
	);
	assertView(
		value.sort === undefined || hint.sortable,
		`${context} sorts by a facet whose kind is not sortable`,
	);
	assertView(
		!value.filter || hint.filterable,
		`${context} filters a facet whose kind is not filterable`,
	);
}

function validateRule(
	value: unknown,
	context: string,
): asserts value is FacetRule {
	assertView(isObject(value), `${context} must be an object`);
	assertExactKeys(value, FACET_FIELDS, context);
	assertLogicalId(value.facet, `${context} facet`);
	assertView(
		FACET_KINDS.includes(value.kind as FacetKind),
		`${context} kind '${String(value.kind)}' is unsupported`,
	);
	assertFacetFields(value, context);
	assertAffordanceGates(
		value,
		FACET_KIND_HINTS[value.kind as FacetKind],
		context,
	);
}

/**
 * Validates a parsed value and narrows it to `ViewDescriptor`: a known view
 * type over a non-empty set of uniquely-named facets, each rule well-formed and
 * requesting only affordances its kind supports.
 */
export function assertViewDescriptor(
	value: unknown,
): asserts value is ViewDescriptor {
	assertView(isObject(value), "view must be an object");
	assertExactKeys(value, ["id", "type", "facets"], "view");
	assertLogicalId(value.id, "view id");
	assertView(
		VIEW_TYPES.includes(value.type as ViewType),
		`view '${String(value.id)}' type '${String(value.type)}' is unsupported`,
	);
	assertView(
		Array.isArray(value.facets),
		`view '${String(value.id)}' facets must be an array`,
	);
	assertView(
		value.facets.length > 0,
		`view '${String(value.id)}' must declare at least one facet`,
	);
	value.facets.forEach((rule: unknown, index: number) => {
		validateRule(rule, `view '${String(value.id)}' facet [${index}]`);
	});
	assertUnique(
		(value.facets as FacetRule[]).map((rule) => rule.facet),
		`facet in view '${String(value.id)}'`,
	);
}

function validateOverrideFacet(value: unknown, context: string): void {
	assertView(isObject(value), `${context} must be an object`);
	assertExactKeys(value, OVERRIDE_FIELDS, context);
	assertLogicalId(value.facet, `${context} facet`);
	assertFacetFields(value, context);
}

/**
 * Validates a parsed value and narrows it to `ViewOverride`: an optional view
 * type and per-facet tweaks that name a facet and set well-typed presentation or
 * affordance fields. Only the kind-affordance gating is deferred to
 * `resolveViewDescriptor`, where the facet's kind is known from the base.
 */
export function assertViewOverride(
	value: unknown,
): asserts value is ViewOverride {
	assertView(isObject(value), "view override must be an object");
	assertExactKeys(value, ["type", "facets"], "view override");
	assertView(
		value.type === undefined || VIEW_TYPES.includes(value.type as ViewType),
		`view override type '${String(value.type)}' is unsupported`,
	);
	if (value.facets !== undefined) {
		assertView(
			Array.isArray(value.facets),
			"view override facets must be an array",
		);
		value.facets.forEach((facet: unknown, index: number) => {
			validateOverrideFacet(facet, `view override facet [${index}]`);
		});
		assertUnique(
			(value.facets as FacetOverride[]).map((facet) => facet.facet),
			"facet in view override",
		);
	}
}

function resolveRule(rule: FacetRule, index: number): ResolvedFacet {
	const hint = FACET_KIND_HINTS[rule.kind];
	return {
		facet: rule.facet,
		kind: rule.kind,
		render: hint.render,
		sortable: hint.sortable,
		filterable: hint.filterable,
		groupable: hint.groupable,
		label: rule.label ?? rule.facet,
		column: rule.column ?? true,
		order: rule.order ?? index,
		icon: rule.icon,
		badge: rule.badge ?? false,
		color: rule.color,
		groupBy: rule.groupBy ?? false,
		sort: rule.sort,
		filter: rule.filter ?? false,
	};
}

function applyOverrideFacet(
	resolved: ResolvedFacet,
	override: FacetOverride,
): void {
	const context = `override for facet '${override.facet}'`;
	if (override.label !== undefined) resolved.label = override.label;
	if (override.column !== undefined) resolved.column = override.column;
	if (override.order !== undefined) resolved.order = override.order;
	if (override.icon !== undefined) resolved.icon = override.icon;
	if (override.badge !== undefined) resolved.badge = override.badge;
	if (override.color !== undefined) resolved.color = override.color;
	if (override.groupBy !== undefined) {
		assertView(
			!override.groupBy || resolved.groupable,
			`${context} groups by a facet whose kind is not groupable`,
		);
		resolved.groupBy = override.groupBy;
	}
	if (override.sort !== undefined) {
		assertView(
			resolved.sortable,
			`${context} sorts by a facet whose kind is not sortable`,
		);
		resolved.sort = override.sort;
	}
	if (override.filter !== undefined) {
		assertView(
			!override.filter || resolved.filterable,
			`${context} filters a facet whose kind is not filterable`,
		);
		resolved.filter = override.filter;
	}
}

/**
 * Folds a base descriptor and its ordered overrides into one resolved
 * descriptor. Each facet starts from its kind's hints and the base rule, then
 * every override layer is applied in turn — later layers win, matching the
 * solution → user-preferences → deployment-skin ladder. An override may only
 * tweak a facet the base already declares; naming an unknown facet is an error.
 * The result lists facets in column order.
 */
export function resolveViewDescriptor(
	base: ViewDescriptor,
	...overrides: readonly ViewOverride[]
): ResolvedViewDescriptor {
	assertViewDescriptor(base);
	for (const override of overrides) assertViewOverride(override);

	const resolved = base.facets.map(resolveRule);
	const byName = new Map(resolved.map((facet) => [facet.facet, facet]));
	let type = base.type;

	for (const override of overrides) {
		if (override.type !== undefined) type = override.type;
		for (const facet of override.facets ?? []) {
			const target = byName.get(facet.facet);
			assertView(
				target !== undefined,
				`view override tweaks unknown facet '${facet.facet}'`,
			);
			applyOverrideFacet(target, facet);
		}
	}

	// Stable sort keeps declaration order among facets sharing an `order`.
	resolved.sort((a, b) => a.order - b.order);
	return { id: base.id, type, facets: resolved };
}
