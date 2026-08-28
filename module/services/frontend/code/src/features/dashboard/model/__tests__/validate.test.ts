import { describe, expect, it } from "vitest";
import { DASHBOARD_SPEC_VERSION, dashboard, event, metric } from "../schema";
import {
	assertDashboardSpec,
	DashboardSpecError,
	parseDashboardSpec,
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
