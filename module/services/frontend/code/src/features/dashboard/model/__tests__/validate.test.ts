import { describe, expect, it } from "vitest";
import type { MetricDef } from "../schema";
import { DASHBOARD_SPEC_VERSION, dashboard, event, metric } from "../schema";
import {
	type AuditVocabulary,
	assertDashboardSpec,
	DashboardSpecError,
	parseDashboardSpec,
	validateDashboard,
	validateMetric,
} from "../validate";

const login = event("auth.login");
const validSpec = dashboard({
	title: "Activity",
	description: "Live from the audit trail.",
	metrics: [
		metric({
			title: "Logins over time",
			event: login,
			groupBy: "time",
			bucket: "day",
			chart: "line",
		}),
		metric({
			title: "Top event types",
			groupBy: "event_type",
			chart: "bar",
			limit: 6,
		}),
	],
});

describe("assertDashboardSpec", () => {
	it("accepts a spec built by dashboard()", () => {
		expect(() => assertDashboardSpec(validSpec)).not.toThrow();
	});

	it("rejects a spec whose version discriminant does not match", () => {
		const stale = { ...validSpec, version: 2 };
		expect(() => assertDashboardSpec(stale)).toThrow(DashboardSpecError);
		expect(() => assertDashboardSpec(stale)).toThrow(
			/version 2 is unsupported/,
		);
	});

	it("rejects an unknown top-level field", () => {
		expect(() => assertDashboardSpec({ ...validSpec, bogus: true })).toThrow(
			/unknown field 'bogus'/,
		);
	});

	it("accepts a dashboard with no metrics (the empty intermediate state)", () => {
		expect(() =>
			assertDashboardSpec({ ...validSpec, metrics: [] }),
		).not.toThrow();
	});

	it("rejects a metrics field that is not an array", () => {
		expect(() => assertDashboardSpec({ ...validSpec, metrics: {} })).toThrow(
			/metrics must be an array/,
		);
	});

	it("rejects two metrics that collide on rendered identity", () => {
		// <Dashboard> keys each card on metricIdentity, so two metrics with the
		// same title/chart/grouping/scope/value resolve to one key and React drops
		// the duplicate. The spec cannot render as authored, so it is rejected here
		// rather than silently losing a card.
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{ title: "Logins", groupBy: "event_type", chart: "bar" },
					{ title: "Logins", groupBy: "event_type", chart: "bar" },
				],
			}),
		).toThrow(/duplicates the identity/);
	});

	it("accepts metrics that share a title but differ in identity", () => {
		// A shared title alone is not a collision — identity also spans chart,
		// grouping, scope, and value — so two 'Logins' cards on different charts
		// stay distinct and are allowed.
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{ title: "Logins", groupBy: "event_type", chart: "bar" },
					{ title: "Logins", groupBy: "event_type", chart: "stat" },
				],
			}),
		).not.toThrow();
	});

	it("rejects an unsupported groupBy", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [{ title: "x", groupBy: "region", chart: "bar" }],
			}),
		).toThrow(/groupBy 'region' is unsupported/);
	});

	it("rejects an unsupported chart", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [{ title: "x", groupBy: "event_type", chart: "pie" }],
			}),
		).toThrow(/chart 'pie' is unsupported/);
	});

	it("requires a bucket when grouping by time", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [{ title: "x", groupBy: "time", chart: "line" }],
			}),
		).toThrow(/needs a bucket/);
	});

	it("forbids a bucket when not grouping by time", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{ title: "x", groupBy: "event_type", bucket: "day", chart: "bar" },
				],
			}),
		).toThrow(/does not group by time/);
	});

	it("forbids a limit on a time series", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{
						title: "x",
						groupBy: "time",
						bucket: "day",
						chart: "line",
						limit: 5,
					},
				],
			}),
		).toThrow(/cannot apply to a time series/);
	});

	it("rejects a non-positive limit", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{ title: "x", groupBy: "event_type", chart: "bar", limit: 0 },
				],
			}),
		).toThrow(/positive integer/);
	});

	it("rejects an empty event type", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{
						title: "x",
						event: { type: "" },
						groupBy: "time",
						bucket: "day",
						chart: "line",
					},
				],
			}),
		).toThrow(/event type must be a non-empty string/);
	});

	it("rejects a metric that sets both event and category", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{
						title: "x",
						event: { type: "auth.login" },
						category: "security",
						groupBy: "time",
						bucket: "day",
						chart: "line",
					},
				],
			}),
		).toThrow(/mutually exclusive/);
	});

	it("rejects a missing metric title", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [{ groupBy: "event_type", chart: "bar" }],
			}),
		).toThrow(/title must be a non-empty string/);
	});

	it("accepts a layout, theme, and per-widget span", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				layout: { kind: "grid", columns: 3 },
				theme: { accent: "oklch(0.6 0.2 20)" },
				metrics: [{ title: "x", groupBy: "event_type", chart: "bar", span: 2 }],
			}),
		).not.toThrow();
	});

	it("rejects an unsupported layout kind", () => {
		expect(() =>
			assertDashboardSpec({
				...validSpec,
				layout: { kind: "masonry" },
			}),
		).toThrow(/layout kind 'masonry' must be 'grid' or 'stack'/);
	});

	it("rejects a layout column count outside 1..4", () => {
		expect(() =>
			assertDashboardSpec({
				...validSpec,
				layout: { kind: "grid", columns: 5 },
			}),
		).toThrow(/columns must be an integer from 1 to 4/);
	});

	it("rejects an unknown theme field", () => {
		expect(() =>
			assertDashboardSpec({
				...validSpec,
				theme: { accent: "red", palette: "warm" },
			}),
		).toThrow(/unknown field 'palette'/);
	});

	it("rejects a span outside 1..4", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [{ title: "x", groupBy: "event_type", chart: "bar", span: 5 }],
			}),
		).toThrow(/span must be an integer from 1 to 4/);
	});

	it("accepts widened metrics: percentile-over-payload, multi-dim, window", () => {
		expect(() =>
			dashboard({
				metrics: [
					metric({
						title: "p95 latency",
						event: event("http.request_served"),
						groupBy: ["time", "payload:route"],
						bucket: "day",
						chart: "line",
						value: {
							op: "percentile",
							field: "payload:duration_ms",
							percentile: 0.95,
						},
						from: "2026-01-01T00:00:00.000Z",
						to: "2026-02-01T00:00:00.000Z",
					}),
					metric({
						title: "Verified login rate",
						groupBy: "event_type",
						chart: "bar",
						ratio: {
							numerator: { op: "count_distinct", field: "actor_id" },
							denominator: { op: "count" },
						},
					}),
				],
			}),
		).not.toThrow();
	});

	it("rejects a payload group dimension with an empty key", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [{ title: "x", groupBy: "payload:", chart: "bar" }],
			}),
		).toThrow(/groupBy 'payload:' is unsupported/);
	});

	it("rejects a non-count value with no field", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{
						title: "x",
						groupBy: "event_type",
						chart: "bar",
						value: { op: "count_distinct" },
					},
				],
			}),
		).toThrow(/value has unknown field|value field must be a non-empty string/);
	});

	it("rejects a numeric value whose field is not a payload key", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{
						title: "x",
						groupBy: "event_type",
						chart: "bar",
						value: { op: "sum", field: "actor_id" },
					},
				],
			}),
		).toThrow(/needs a payload:<key> field/);
	});

	it("rejects a percentile value out of range", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{
						title: "x",
						groupBy: "event_type",
						chart: "bar",
						value: {
							op: "percentile",
							field: "payload:duration_ms",
							percentile: 1.5,
						},
					},
				],
			}),
		).toThrow(/percentile must be a quantile in \(0, 1\]/);
	});

	it("rejects a metric declaring both value and ratio", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{
						title: "x",
						groupBy: "event_type",
						chart: "bar",
						value: { op: "count" },
						ratio: {
							numerator: { op: "count" },
							denominator: { op: "count" },
						},
					},
				],
			}),
		).toThrow(/declares both value and ratio/);
	});

	it("rejects a non-ISO time window", () => {
		expect(() =>
			assertDashboardSpec({
				version: DASHBOARD_SPEC_VERSION,
				metrics: [
					{
						title: "x",
						groupBy: "event_type",
						chart: "bar",
						from: "last tuesday",
					},
				],
			}),
		).toThrow(/from must be an ISO-8601 timestamp/);
	});
});

describe("parseDashboardSpec", () => {
	it("round-trips a spec through JSON", () => {
		const restored = parseDashboardSpec(JSON.stringify(validSpec));
		expect(restored).toEqual(validSpec);
	});

	it("wraps a JSON syntax error as DashboardSpecError", () => {
		expect(() => parseDashboardSpec("{ not json")).toThrow(DashboardSpecError);
		expect(() => parseDashboardSpec("{ not json")).toThrow(/not valid JSON/);
	});

	it("rejects a structurally valid JSON that is not a spec", () => {
		expect(() => parseDashboardSpec("{}")).toThrow(DashboardSpecError);
	});
});

const vocab: AuditVocabulary = {
	eventTypes: ["auth.login", "org.created"],
	categories: ["authentication", "organization"],
};

const loginMetric: MetricDef = {
	title: "Logins over time",
	event: event("auth.login"),
	groupBy: "time",
	bucket: "day",
	chart: "line",
};

describe("validateMetric", () => {
	it("accepts a shape-valid metric that references the vocabulary", () => {
		expect(validateMetric(loginMetric, vocab)).toEqual([]);
	});

	it("reports an event type outside the vocabulary against its path", () => {
		expect(
			validateMetric({ ...loginMetric, event: event("auth.nope") }, vocab),
		).toEqual([
			{
				path: "metric.event.type",
				code: "unknown_event_type",
				message: '"auth.nope" is not a registered audit event type.',
			},
		]);
	});

	it("reports a category outside the vocabulary", () => {
		expect(
			validateMetric(
				{
					title: "By category",
					category: "billing",
					groupBy: "category",
					chart: "bar",
				},
				vocab,
			),
		).toEqual([
			{
				path: "metric.category",
				code: "unknown_category",
				message: '"billing" is not a registered audit category.',
			},
		]);
	});

	it("collapses a shape violation to a single invalid_spec error", () => {
		const errors = validateMetric(
			{ title: "x", groupBy: "region" as never, chart: "bar" },
			vocab,
		);
		expect(errors).toHaveLength(1);
		expect(errors[0].code).toBe("invalid_spec");
	});

	it("reports the shape error, not a vocabulary miss, when both are present", () => {
		// A bucket-less time metric is shape-invalid; the unknown event must not
		// mask it, since a driver cannot judge the reference until the shape holds.
		const errors = validateMetric(
			{ title: "x", event: event("auth.nope"), groupBy: "time", chart: "line" },
			vocab,
		);
		expect(errors).toEqual([
			{
				path: "metric",
				code: "invalid_spec",
				message: expect.stringMatching(/needs a bucket/),
			},
		]);
	});

	it("prefixes errors with a caller-supplied path", () => {
		const errors = validateMetric(
			{ ...loginMetric, event: event("auth.nope") },
			vocab,
			"metrics[2]",
		);
		expect(errors[0].path).toBe("metrics[2].event.type");
	});

	it("addresses a shape error at the caller's path, not a fabricated index", () => {
		const errors = validateMetric(
			{ title: "x", groupBy: "time", chart: "line" },
			vocab,
			"metrics[2]",
		);
		expect(errors).toHaveLength(1);
		expect(errors[0].path).toBe("metrics[2]");
		expect(errors[0].message).toContain("metrics[2]");
		expect(errors[0].message).not.toContain("index");
	});
});

describe("validateDashboard", () => {
	it("accepts a spec whose metrics all reference the vocabulary", () => {
		const spec = dashboard({ title: "Activity", metrics: [loginMetric] });
		expect(validateDashboard(spec, vocab)).toEqual([]);
	});

	it("addresses a per-metric vocabulary miss by its index", () => {
		const spec = dashboard({
			metrics: [loginMetric, { ...loginMetric, event: event("auth.nope") }],
		});
		expect(validateDashboard(spec, vocab)).toEqual([
			{
				path: "metrics[1].event.type",
				code: "unknown_event_type",
				message: '"auth.nope" is not a registered audit event type.',
			},
		]);
	});

	it("collapses a shape violation to a single invalid_spec error", () => {
		const errors = validateDashboard(
			{ version: DASHBOARD_SPEC_VERSION, metrics: [{} as MetricDef] },
			vocab,
		);
		expect(errors).toHaveLength(1);
		expect(errors[0].code).toBe("invalid_spec");
	});
});
