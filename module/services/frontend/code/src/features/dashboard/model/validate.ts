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
	type GroupBy,
	type MetricDef,
} from "./schema";

const GROUP_BY: readonly GroupBy[] = [
	"event_type",
	"category",
	"actor",
	"time",
];
const CHART: readonly ChartKind[] = ["line", "bar", "stat"];
const BUCKET: readonly Bucket[] = ["day", "week", "month"];

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
		],
		context,
	);
	assertNonEmptyString(value.title, `${context} title`);
	assertOptionalText(value.description, `${context} description`);
	assertSpec(
		GROUP_BY.includes(value.groupBy as GroupBy),
		`${context} groupBy '${String(value.groupBy)}' is unsupported`,
	);
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

	// A bucket is the time grain: required when the metric groups by time,
	// meaningless — and so forbidden — otherwise.
	if (value.groupBy === "time") {
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

	// limit ranks a categorical series to its top N; a time series is ordered
	// chronologically and ignores it, so a limit there is incoherent.
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
		["version", "title", "description", "metrics"],
		"spec",
	);
	assertSpec(
		value.version === DASHBOARD_SPEC_VERSION,
		`spec version ${String(value.version)} is unsupported; expected ${DASHBOARD_SPEC_VERSION}`,
	);
	assertOptionalText(value.title, "spec title");
	assertOptionalText(value.description, "spec description");
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
