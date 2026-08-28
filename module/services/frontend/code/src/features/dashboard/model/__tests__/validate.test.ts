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
		expect(() => assertDashboardSpec({ ...validSpec, layout: "grid" })).toThrow(
			/unknown field 'layout'/,
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
