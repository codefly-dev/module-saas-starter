// The kit's chart vocabulary, published once as an A2UI catalog.
//
// `@codefly-dev/ui/dashboard` is the one place a dashboard is drawn, and until
// now its vocabulary was a convention: `WidgetVisualization` here, the same five
// names restated in `@codefly-dev/saas-sdk`'s data-graph schema, and restated
// again by any agent offering a `visualization` enum. Nothing checked that the
// three agreed. This file is that vocabulary as a contract — an
// [A2UI](https://a2ui.org) catalog naming the components a renderer may be asked
// for, plus the data model each binds to — and the `satisfies` constraints below
// are what stop the kit drifting from what it publishes.
//
// Two documents, because A2UI says so. A catalog's root keys are closed (`$schema`,
// `$id`, `protocolVersion`, `title`, `description`, `catalogId`, `instructions`,
// `components`, `functions`, `$defs`) and its `$defs` may hold only
// `anyComponent`/`anyFunction`, so a conforming catalog has nowhere to put a
// shared series schema. The catalog therefore publishes component *properties*,
// and {@link DASHBOARD_DATA_SCHEMA} — a plain JSON Schema with its own `$id` —
// publishes the shapes those properties' pointers resolve to. The catalog names
// it in `instructions`; neither document ever carries a number.
//
// Pinned at `protocolVersion: "0.9"`. v1.0 is the specification's candidate and
// spells a binding `{"@path": …}`; the production version is v0.9.1, which spells
// it `{path: …}`, and the published renderers (`@a2ui/react`, `@a2ui/web_core`)
// expose `v0_8`/`v0_9` entry points and no `v1_0`. A v1.0 catalog would describe
// properties no shipped client can bind. The structural references below
// (`ComponentId`, `ChildList`, `Dynamic*`) are resolved against whichever
// protocol version's `common_types.json` the renderer maps, so moving to v1.0 is
// this one field, new bytes and a new digest — not a reshape.
//
// Nothing here names a solution, a product or an agent: the kit sits below every
// consumer, so the catalog publishes components and shapes, and a consumer's own
// metric ids and titles travel as values.

import type {
	DashboardLayoutKind,
	DashboardView,
	DashboardWidgetView,
	SeriesPoint,
	WidgetSeries,
	WidgetVisualization,
} from "./types.js";

/** A JSON Schema node, structurally — enough to type a document, not to validate one. */
export type JsonSchemaNode = { readonly [keyword: string]: unknown };

/**
 * Every property of `T` — required and optional alike — mapped to a schema node.
 * This is the drift guard that matters: a field added to {@link DashboardView}
 * and not described below is a compile error in this file, so the kit cannot
 * grow a view field it does not publish, and cannot publish one it does not have.
 */
type SchemaProperties<T> = { [K in keyof Required<T>]: JsonSchemaNode };

/** The keys of `T` that are not optional, for a schema's `required` list. */
type RequiredKeys<T> = {
	[K in keyof T]-?: undefined extends T[K] ? never : K;
}[keyof T];

/**
 * A list that must name every member of `Member` and nothing else. `List extends
 * readonly Member[]` refuses a stranger; the conditional refuses a list that has
 * dropped one, which a plain `satisfies` would wave through. Used for the
 * visualization and layout enums and for each `required` list, so a union that
 * gains a member, and a field that becomes required, both fail here first.
 */
const closedSet =
	<Member extends string>() =>
	<List extends readonly Member[]>(
		members: List & ([Member] extends [List[number]] ? unknown : never),
	): List =>
		members;

/** `Object.keys` with the key type a `Record` already proves. */
const keysOf = <Key extends string>(record: Record<Key, unknown>): Key[] =>
	Object.keys(record) as Key[];

/** The A2UI protocol version this catalog is written against. */
export const DASHBOARD_CATALOG_PROTOCOL_VERSION = "0.9";

/**
 * The catalog's identity, which an envelope's `catalogId` must equal. Stable
 * across kit releases: an installation freezes it, so bumping it per patch would
 * break every frozen catalog. The trailing `v1` moves only for a breaking change
 * to the vocabulary; a component or property added is new bytes and a new digest
 * under the same id.
 */
export const DASHBOARD_CATALOG_ID = "codefly-dev.ui:dashboard/v1";

/** The identity of the data model the catalog's bindings resolve against. */
export const DASHBOARD_DATA_SCHEMA_ID = "codefly-dev.ui:dashboard-data/v1";

/** The whole-canvas component: one `DashboardView`, bound by reference. */
export const DASHBOARD_COMPONENT = "Dashboard";
/** A container whose `widgets` are the chart components it lays out. */
export const DASHBOARD_GRID_COMPONENT = "DashboardGrid";
/** One metric drawn as a line, as an area, as bars, and as a scalar tile. */
export const METRIC_LINE_CHART_COMPONENT = "MetricLineChart";
export const METRIC_BAR_CHART_COMPONENT = "MetricBarChart";
export const METRIC_AREA_CHART_COMPONENT = "MetricAreaChart";
export const STAT_TILE_COMPONENT = "StatTile";

/**
 * The catalog component that draws each visualization on its own, or `null` where
 * the kit draws it only inside {@link DASHBOARD_COMPONENT}.
 *
 * `table` is `null` deliberately: `<Dashboard>` renders a table body for a
 * `table` widget and the kit exports no standalone table chart, so there is no
 * component for a catalog to name. Publishing a name nothing draws would be the
 * drift this file exists to prevent, and leaving the gap undeclared would hide
 * it — so the gap is a value, checked by the type below.
 */
export const DASHBOARD_WIDGET_COMPONENT_BY_VISUALIZATION = {
	line: METRIC_LINE_CHART_COMPONENT,
	bar: METRIC_BAR_CHART_COMPONENT,
	area: METRIC_AREA_CHART_COMPONENT,
	number: STAT_TILE_COMPONENT,
	table: null,
} as const satisfies Record<WidgetVisualization, string | null>;

/**
 * The visualizations, in the order the catalog publishes them. Derived from the
 * component map above rather than written twice, so the published enum and the
 * component mapping are one list.
 */
export const DASHBOARD_VISUALIZATIONS = keysOf(
	DASHBOARD_WIDGET_COMPONENT_BY_VISUALIZATION,
);

/** The dashboard arrangements a view may ask for. */
const DASHBOARD_LAYOUTS = closedSet<DashboardLayoutKind>()([
	"grid",
	"stack",
] as const);

// ---------------------------------------------------------------------------
// The data model: the shapes a catalog binding resolves to.
// ---------------------------------------------------------------------------

const SERIES_POINT_PROPERTIES = {
	key: {
		type: "string",
		description: "The group this point is of — a bucket, a label, a name.",
	},
	value: { type: "number", description: "The resolved value for that group." },
} satisfies SchemaProperties<SeriesPoint>;

const WIDGET_SERIES_PROPERTIES = {
	points: {
		type: "array",
		items: { $ref: "#/$defs/SeriesPoint" },
		description: "The resolved points, in the order they are drawn.",
	},
	total: {
		type: ["number", "null"],
		description:
			"Null when the series is empty or partial, or not additive across groups — not zero.",
	},
	coverage: {
		enum: ["complete", "partial", "empty"],
		description:
			"Completeness of the observed telemetry, not proof that every producer emitted.",
	},
} satisfies SchemaProperties<WidgetSeries>;

const DASHBOARD_WIDGET_PROPERTIES = {
	id: { type: "string", description: "Stable identity of this widget." },
	visualization: {
		enum: DASHBOARD_VISUALIZATIONS,
		description: "How the widget draws its series.",
	},
	title: { type: "string" },
	series: { $ref: "#/$defs/WidgetSeries" },
	span: {
		type: "integer",
		enum: [1, 2, 3, 4],
		description: "Grid columns this widget spans; ignored in a stack.",
	},
} satisfies SchemaProperties<DashboardWidgetView>;

const DASHBOARD_VIEW_PROPERTIES = {
	title: { type: "string" },
	description: { type: "string" },
	layout: { enum: DASHBOARD_LAYOUTS, description: "Defaults to a grid." },
	columns: {
		type: "integer",
		enum: [1, 2, 3, 4],
		description: "Grid column count; ignored in a stack.",
	},
	accent: {
		type: "string",
		description:
			"Any CSS colour, applied to this dashboard's charts through the primary token.",
	},
	widgets: { type: "array", items: { $ref: "#/$defs/DashboardWidgetView" } },
} satisfies SchemaProperties<DashboardView>;

/**
 * The data model a catalog binding resolves against: `DashboardView` and the
 * shapes beneath it, as a plain JSON Schema with its own identity. It describes
 * series, never carries them.
 */
export const DASHBOARD_DATA_SCHEMA = {
	$schema: "https://json-schema.org/draft/2020-12/schema",
	$id: DASHBOARD_DATA_SCHEMA_ID,
	title: "Codefly dashboard data model",
	description:
		"The resolved shapes a Codefly dashboard catalog binds to by JSON Pointer. Generated from @codefly-dev/ui's own DashboardView types.",
	$ref: "#/$defs/DashboardView",
	$defs: {
		SeriesPoint: {
			type: "object",
			properties: SERIES_POINT_PROPERTIES,
			required: closedSet<RequiredKeys<SeriesPoint>>()([
				"key",
				"value",
			] as const),
		},
		WidgetSeries: {
			type: "object",
			properties: WIDGET_SERIES_PROPERTIES,
			required: closedSet<RequiredKeys<WidgetSeries>>()([
				"points",
				"total",
			] as const),
		},
		DashboardWidgetView: {
			type: "object",
			properties: DASHBOARD_WIDGET_PROPERTIES,
			required: closedSet<RequiredKeys<DashboardWidgetView>>()([
				"id",
				"visualization",
				"series",
			] as const),
		},
		DashboardView: {
			type: "object",
			properties: DASHBOARD_VIEW_PROPERTIES,
			required: closedSet<RequiredKeys<DashboardView>>()(["widgets"] as const),
		},
	},
} satisfies JsonSchemaNode;

// ---------------------------------------------------------------------------
// The catalog: the components a renderer may be asked for.
// ---------------------------------------------------------------------------

// The canonical structural and bindable references. A validator finds a child
// link by the exact `$ref`, so `ComponentId`/`ChildList` are spelled rather than
// described — a child declared as `{"type": "string"}` is read as static text.
// `common_types.json` is resolved by the renderer against the protocol version it
// speaks, which is what lets one document serve a v0.9 client and later a v1.0
// one without restating every property.
const CHILD_LIST = { $ref: "common_types.json#/$defs/ChildList" };
const DYNAMIC_STRING = { $ref: "common_types.json#/$defs/DynamicString" };
const DYNAMIC_NUMBER = { $ref: "common_types.json#/$defs/DynamicNumber" };
const DYNAMIC_VALUE = { $ref: "common_types.json#/$defs/DynamicValue" };

/**
 * One catalog entry. The discriminator property (`component: {const: name}`) is
 * what `$defs.anyComponent` dispatches on.
 *
 * `additionalProperties` is deliberately absent. The envelope composes
 * `ComponentCommon` (`id`, `catalogId`, `accessibility`) beside a catalog entry
 * with `allOf`, and `additionalProperties: false` only knows the properties
 * declared in its own schema object — so closing an entry would reject the
 * envelope's own `id`.
 */
function componentEntry(
	name: string,
	entry: {
		description: string;
		properties: Record<string, JsonSchemaNode>;
		required?: readonly string[];
		allowedChildren?: readonly string[];
	},
): JsonSchemaNode {
	return {
		type: "object",
		title: name,
		description: entry.description,
		properties: { component: { const: name }, ...entry.properties },
		required: ["component", ...(entry.required ?? [])],
		...(entry.allowedChildren ? { allowedChildren: entry.allowedChildren } : {}),
	};
}

const CHART_COMPONENTS = [
	METRIC_LINE_CHART_COMPONENT,
	METRIC_AREA_CHART_COMPONENT,
	METRIC_BAR_CHART_COMPONENT,
	STAT_TILE_COMPONENT,
] as const;

// One metric, drawn. `series` is a pointer to that metric's resolved points;
// `metricId` travels as a literal so a reader sees which declared metric a chart
// is of, and refreshing one chart is a single data-model update rather than a
// re-declared tree.
const chartProperties = {
	title: DYNAMIC_STRING,
	metricId: DYNAMIC_STRING,
	series: DYNAMIC_VALUE,
} satisfies Record<string, JsonSchemaNode>;

const COMPONENTS: Record<string, JsonSchemaNode> = {
	[DASHBOARD_COMPONENT]: componentEntry(DASHBOARD_COMPONENT, {
		description: `A whole resolved dashboard: one \`DashboardView\` from ${DASHBOARD_DATA_SCHEMA_ID}, bound by pointer. Its widgets travel in the data model, not as child components, so a renderer holding this one component draws every widget the view declares.`,
		properties: { data: DYNAMIC_VALUE },
		required: ["data"],
		allowedChildren: [],
	}),
	[DASHBOARD_GRID_COMPONENT]: componentEntry(DASHBOARD_GRID_COMPONENT, {
		description:
			"A grid of chart components. Use it when each chart is declared and refreshed on its own; use Dashboard when one resolved view is drawn whole.",
		properties: { title: DYNAMIC_STRING, widgets: CHILD_LIST },
		required: ["widgets"],
		allowedChildren: CHART_COMPONENTS,
	}),
	[METRIC_LINE_CHART_COMPONENT]: componentEntry(METRIC_LINE_CHART_COMPONENT, {
		description:
			"One metric as a line. `series` points at that metric's resolved `SeriesPoint[]`.",
		properties: chartProperties,
		required: ["series"],
		allowedChildren: [],
	}),
	[METRIC_AREA_CHART_COMPONENT]: componentEntry(METRIC_AREA_CHART_COMPONENT, {
		description:
			"One metric as a filled area. `series` points at that metric's resolved `SeriesPoint[]`.",
		properties: chartProperties,
		required: ["series"],
		allowedChildren: [],
	}),
	[METRIC_BAR_CHART_COMPONENT]: componentEntry(METRIC_BAR_CHART_COMPONENT, {
		description:
			"One metric as bars, one per group. `series` points at that metric's resolved `SeriesPoint[]`.",
		properties: chartProperties,
		required: ["series"],
		allowedChildren: [],
	}),
	[STAT_TILE_COMPONENT]: componentEntry(STAT_TILE_COMPONENT, {
		description:
			"One metric as a scalar, with its points behind it. `total` is the scalar; a null total is unavailable, not zero.",
		properties: {
			...chartProperties,
			total: DYNAMIC_NUMBER,
			coverage: DYNAMIC_STRING,
		},
		required: [],
		allowedChildren: [],
	}),
};

const standaloneVisualizations = DASHBOARD_VISUALIZATIONS.filter(
	(visualization) =>
		DASHBOARD_WIDGET_COMPONENT_BY_VISUALIZATION[visualization] !== null,
);
const canvasOnlyVisualizations = DASHBOARD_VISUALIZATIONS.filter(
	(visualization) =>
		DASHBOARD_WIDGET_COMPONENT_BY_VISUALIZATION[visualization] === null,
);

// Written from the derived lists above rather than typed out, so the prose a
// model reads cannot disagree with the components the renderer has.
const INSTRUCTIONS = [
	`Every component here is drawn by \`@codefly-dev/ui/dashboard\`. This catalog publishes the names and their properties; the kit owns the drawing.`,
	`Data is bound by reference. A \`data\` or \`series\` property carries a data-model pointer and the catalog carries no values — the shapes those pointers resolve to are \`${DASHBOARD_DATA_SCHEMA_ID}\`.`,
	`A widget's visualization is one of: ${DASHBOARD_VISUALIZATIONS.map((v) => `\`${v}\``).join(", ")}. ${standaloneVisualizations
		.map(
			(visualization) =>
				`\`${visualization}\` → \`${DASHBOARD_WIDGET_COMPONENT_BY_VISUALIZATION[visualization]}\``,
		)
		.join(
			", ",
		)}. ${canvasOnlyVisualizations.map((v) => `\`${v}\``).join(", ")} is drawn only inside \`${DASHBOARD_COMPONENT}\`, from its view, and has no standalone component to name.`,
	`\`${DASHBOARD_COMPONENT}\` draws one resolved view whole; \`${DASHBOARD_GRID_COMPONENT}\` lays out charts declared one by one. Both are valid roots.`,
]
	.map((line) => `- ${line}`)
	.join("\n");

/**
 * The catalog document the kit publishes. The exact bytes a consumer freezes are
 * `@codefly-dev/ui/dashboard-catalog.json`, generated from this object by
 * `scripts/generate-dashboard-catalog.mjs`; this is the same document as a value,
 * for a consumer that would rather import it than read the file.
 */
export const DASHBOARD_CATALOG = {
	$schema: "https://json-schema.org/draft/2020-12/schema",
	$id: DASHBOARD_CATALOG_ID,
	protocolVersion: DASHBOARD_CATALOG_PROTOCOL_VERSION,
	title: "Codefly dashboard catalog",
	description:
		"The dashboard components @codefly-dev/ui/dashboard renders, as an A2UI catalog. Generated from the kit's own types.",
	catalogId: DASHBOARD_CATALOG_ID,
	instructions: INSTRUCTIONS,
	components: COMPONENTS,
	$defs: {
		anyComponent: {
			oneOf: Object.keys(COMPONENTS).map((name) => ({
				$ref: `#/components/${name}`,
			})),
			discriminator: { propertyName: "component" },
		},
	},
} satisfies JsonSchemaNode;

/** One component of the catalog, as a freezer records it: its name, and which of
 *  its properties carry child references. */
export interface DashboardCatalogComponent {
	name: string;
	children?: string[];
}

/**
 * The catalog's component names and child-bearing properties.
 *
 * This is the projection a consumer that freezes a catalog keeps — the names a
 * composer may use, and the properties a structural check follows — so it never
 * retypes them from the document. Derived by reading the document's own
 * `ComponentId`/`ChildList` references, so a property that becomes a child link
 * appears here without a second edit.
 */
export function dashboardCatalogComponents(): DashboardCatalogComponent[] {
	return Object.entries(DASHBOARD_CATALOG.components).map(([name, entry]) => {
		const properties = (entry as { properties?: Record<string, unknown> })
			.properties;
		const children = Object.entries(properties ?? {})
			.filter(([, schema]) => {
				const ref = (schema as { $ref?: unknown }).$ref;
				return (
					typeof ref === "string" &&
					(ref.endsWith("/ChildList") || ref.endsWith("/ComponentId"))
				);
			})
			.map(([property]) => property);
		return children.length > 0 ? { name, children } : { name };
	});
}
