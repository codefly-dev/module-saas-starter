// Runtime validation for the dashboard spec DSL. It mirrors the semantics of
// `assertDataGraph` (packages/saas-plugin-manifest) — structured rejection,
// exact-key checks, and dimensional-coherence rules — for the app's own,
// thinner spec: inline events, a chart kind per metric, no cross-metric
// references. It is the guard that keeps a malformed or incoherent spec from
// ever reaching <Dashboard>, whether the spec was authored in code, restored
// from localStorage, or set by an external driver.

import {
	type Bucket,
	type ChartKind,
	DASHBOARD_SPEC_VERSION,
	type DashboardDef,
	type Dimension,
	type MetricDef,
	type MetricOp,
} from "./schema";

const FIXED_DIMENSION: readonly Dimension[] = [
	"event_type",
	"category",
	"actor",
	"time",
];
// A payload field is addressed as `payload:<key>` with a non-empty key — both
// as a group dimension and as an aggregation's `field`.
const PAYLOAD_FIELD = /^payload:.+$/;
const CHART: readonly ChartKind[] = ["line", "bar", "stat"];
const BUCKET: readonly Bucket[] = ["day", "week", "month"];
const OP: readonly MetricOp[] = [
	"count",
	"count_distinct",
	"sum",
	"avg",
	"min",
	"max",
	"percentile",
];
// The numeric ops read a numeric payload value, so they require a
// `payload:<key>` field; count_distinct may instead read a bare column.
const NUMERIC_OP: readonly MetricOp[] = [
	"sum",
	"avg",
	"min",
	"max",
	"percentile",
];

function isDimension(value: unknown): value is Dimension {
	return (
		typeof value === "string" &&
		((FIXED_DIMENSION as readonly string[]).includes(value) ||
			PAYLOAD_FIELD.test(value))
	);
}

// DashboardSpecError is the structured rejection a caller catches when a spec
// is malformed or incoherent, so a bad spec surfaces as data to handle rather
// than a broken render or a bare Error.
export class DashboardSpecError extends Error {
	constructor(message: string, options?: { cause?: unknown }) {
		super(`Invalid dashboard spec: ${message}`, options);
		this.name = "DashboardSpecError";
	}
}

function assertSpec(condition: unknown, message: string): asserts condition {
	if (!condition) throw new DashboardSpecError(message);
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
	assertSpec(
		unknown.length === 0,
		`${context} has unknown field '${unknown[0]}'`,
	);
}

function assertNonEmptyString(value: unknown, context: string): void {
	assertSpec(
		typeof value === "string" && value.trim().length > 0,
		`${context} must be a non-empty string`,
	);
}

function assertOptionalText(value: unknown, context: string): void {
	assertSpec(
		value === undefined ||
			(typeof value === "string" && value.trim().length > 0),
		`${context} must be a non-empty string`,
	);
}

// A metric value is one aggregation. Its op determines the rest: count takes
// only `op`; count_distinct and the numeric ops carry a `field` (a
// `payload:<key>` for the numeric ops); percentile also carries a quantile. The
// per-op exact-key checks make the illegal shapes the types forbid also
// unrepresentable in a spec parsed from JSON.
function validateMetricValue(value: unknown, context: string): void {
	assertSpec(isObject(value), `${context} must be an object`);
	assertSpec(
		typeof value.op === "string" &&
			(OP as readonly string[]).includes(value.op),
		`${context} op '${String(value.op)}' is unsupported`,
	);
	if (value.op === "count") {
		assertExactKeys(value, ["op"], context);
		return;
	}
	if (value.op === "percentile") {
		assertExactKeys(value, ["op", "field", "percentile"], context);
		assertSpec(
			typeof value.percentile === "number" &&
				value.percentile > 0 &&
				value.percentile <= 1,
			`${context} percentile must be a quantile in (0, 1]`,
		);
	} else {
		assertExactKeys(value, ["op", "field"], context);
	}
	assertNonEmptyString(value.field, `${context} field`);
	if ((NUMERIC_OP as readonly string[]).includes(value.op)) {
		assertSpec(
			PAYLOAD_FIELD.test(value.field as string),
			`${context} op '${value.op}' needs a payload:<key> field`,
		);
	}
}

function assertOptionalTimestamp(value: unknown, context: string): void {
	if (value === undefined) return;
	assertSpec(
		typeof value === "string" && !Number.isNaN(Date.parse(value)),
		`${context} must be an ISO-8601 timestamp`,
	);
}

function validateMetric(
	value: unknown,
	index: number,
): asserts value is MetricDef {
	const context = `metric at index ${index}`;
	assertSpec(isObject(value), `${context} must be an object`);
	assertExactKeys(
		value,
		[
			"title",
			"description",
			"event",
			"category",
			"groupBy",
			"bucket",
			"chart",
			"limit",
			"span",
			"value",
			"ratio",
			"from",
			"to",
		],
		context,
	);
	assertNonEmptyString(value.title, `${context} title`);
	assertOptionalText(value.description, `${context} description`);

	// groupBy is one dimension or, for multi-dimensional grouping, a non-empty
	// list of them; each dimension is a fixed audit column or a payload field.
	const dimensions = Array.isArray(value.groupBy)
		? value.groupBy
		: [value.groupBy];
	assertSpec(
		!Array.isArray(value.groupBy) || dimensions.length > 0,
		`${context} groupBy must not be an empty list`,
	);
	for (const dimension of dimensions) {
		assertSpec(
			isDimension(dimension),
			`${context} groupBy '${String(dimension)}' is unsupported`,
		);
	}
	const groupsByTime = dimensions.includes("time");

	assertSpec(
		CHART.includes(value.chart as ChartKind),
		`${context} chart '${String(value.chart)}' is unsupported`,
	);

	if (value.event !== undefined) {
		assertSpec(isObject(value.event), `${context} event must be an object`);
		assertExactKeys(value.event, ["type"], `${context} event`);
		assertNonEmptyString(value.event.type, `${context} event type`);
	}
	assertSpec(
		value.category === undefined ||
			(typeof value.category === "string" && value.category.trim().length > 0),
		`${context} category must be a non-empty string`,
	);

	// A card renders one series, so value and ratio are mutually exclusive.
	assertSpec(
		value.value === undefined || value.ratio === undefined,
		`${context} declares both value and ratio`,
	);
	if (value.value !== undefined) {
		validateMetricValue(value.value, `${context} value`);
	}
	if (value.ratio !== undefined) {
		assertSpec(isObject(value.ratio), `${context} ratio must be an object`);
		assertExactKeys(
			value.ratio,
			["numerator", "denominator"],
			`${context} ratio`,
		);
		validateMetricValue(value.ratio.numerator, `${context} ratio numerator`);
		validateMetricValue(
			value.ratio.denominator,
			`${context} ratio denominator`,
		);
	}

	assertOptionalTimestamp(value.from, `${context} from`);
	assertOptionalTimestamp(value.to, `${context} to`);

	// A bucket is the time grain: required when the metric groups by time,
	// meaningless — and so forbidden — otherwise.
	if (groupsByTime) {
		assertSpec(
			BUCKET.includes(value.bucket as Bucket),
			`${context} groups by time and needs a bucket of ${BUCKET.join(", ")}`,
		);
	} else {
		assertSpec(
			value.bucket === undefined,
			`${context} declares a bucket but does not group by time`,
		);
	}

	// limit ranks a categorical series to its top N; a single time series is
	// ordered chronologically and ignores it, so a limit there is incoherent.
	if (value.limit !== undefined) {
		assertSpec(
			typeof value.limit === "number" &&
				Number.isInteger(value.limit) &&
				value.limit > 0,
			`${context} limit must be a positive integer`,
		);
		assertSpec(
			value.groupBy !== "time",
			`${context} limit ranks a categorical metric and cannot apply to a time series`,
		);
	}

	// span is how many grid columns the card occupies; the renderer clamps it to
	// the layout's column count, so any 1..4 is a coherent request.
	if (value.span !== undefined) {
		assertSpec(
			typeof value.span === "number" &&
				Number.isInteger(value.span) &&
				value.span >= 1 &&
				value.span <= 4,
			`${context} span must be an integer from 1 to 4`,
		);
	}
}

// A layout is optional; when present its kind is required and columns, if given,
// sizes the grid. Mirrors LayoutDef in schema.ts.
function validateLayout(value: unknown): void {
	assertSpec(isObject(value), "spec layout must be an object");
	assertExactKeys(value, ["kind", "columns"], "spec layout");
	assertSpec(
		value.kind === "grid" || value.kind === "stack",
		`spec layout kind '${String(value.kind)}' must be 'grid' or 'stack'`,
	);
	if (value.columns !== undefined) {
		assertSpec(
			typeof value.columns === "number" &&
				Number.isInteger(value.columns) &&
				value.columns >= 1 &&
				value.columns <= 4,
			"spec layout columns must be an integer from 1 to 4",
		);
	}
}

// A theme is optional; its only field, accent, is any non-empty CSS color
// string. Mirrors ThemeDef in schema.ts.
function validateTheme(value: unknown): void {
	assertSpec(isObject(value), "spec theme must be an object");
	assertExactKeys(value, ["accent"], "spec theme");
	assertOptionalText(value.accent, "spec theme accent");
}

/**
 * Validates an untyped value as a dashboard spec and narrows it to
 * `DashboardDef`, throwing `DashboardSpecError` on the first violation. Beyond
 * per-field shape it enforces the version discriminant and each metric's
 * dimensional coherence (time metrics carry a bucket, categorical limits do
 * not land on a time series).
 */
export function assertDashboardSpec(
	value: unknown,
): asserts value is DashboardDef {
	assertSpec(isObject(value), "spec must be an object");
	assertExactKeys(
		value,
		["version", "title", "description", "layout", "theme", "metrics"],
		"spec",
	);
	assertSpec(
		value.version === DASHBOARD_SPEC_VERSION,
		`spec version ${String(value.version)} is unsupported; expected ${DASHBOARD_SPEC_VERSION}`,
	);
	assertOptionalText(value.title, "spec title");
	assertOptionalText(value.description, "spec description");
	if (value.layout !== undefined) validateLayout(value.layout);
	if (value.theme !== undefined) validateTheme(value.theme);
	// An empty metric list is a coherent, renderable dashboard (title only) and
	// the natural intermediate state when the last widget is removed, so it is
	// allowed — only a non-array is rejected.
	assertSpec(Array.isArray(value.metrics), "spec metrics must be an array");
	value.metrics.forEach((entry, index) => {
		validateMetric(entry, index);
	});
}

/**
 * Parses a JSON string into a validated dashboard spec. Both a JSON syntax
 * error and a schema violation surface as `DashboardSpecError`, so a caller
 * restoring a persisted draft has one error type to handle.
 */
export function parseDashboardSpec(raw: string): DashboardDef {
	let value: unknown;
	try {
		value = JSON.parse(raw);
	} catch (cause) {
		throw new DashboardSpecError("spec is not valid JSON", { cause });
	}
	assertDashboardSpec(value);
	return value;
}
