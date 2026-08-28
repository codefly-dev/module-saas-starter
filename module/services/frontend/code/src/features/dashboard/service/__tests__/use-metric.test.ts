import { describe, expect, it } from "vitest";
import { metric } from "../../model/schema";
import { compileMetricQuery } from "../use-metric";

describe("compileMetricQuery", () => {
	it("keeps the simple count form a metrics-free query read off the bucket", () => {
		const { params, valueAlias } = compileMetricQuery(
			metric({ title: "Top events", groupBy: "event_type", chart: "bar" }),
			"org_1",
		);

		expect(valueAlias).toBeNull();
		expect(params.metrics).toBeUndefined();
		expect(params.derived).toBeUndefined();
		expect(params).toMatchObject({ orgId: "org_1", groupBy: "event_type" });
		expect(params.groupBys).toBeUndefined();
	});

	it("compiles a percentile over a payload field", () => {
		const { params, valueAlias } = compileMetricQuery(
			metric({
				title: "p95 request latency",
				event: { type: "http.request_served" },
				groupBy: "time",
				bucket: "day",
				chart: "line",
				value: {
					op: "percentile",
					field: "payload:duration_ms",
					percentile: 0.95,
				},
			}),
			"org_1",
		);

		expect(valueAlias).toBe("value");
		expect(params.metrics).toEqual([
			{
				op: "percentile",
				field: "payload:duration_ms",
				percentile: 0.95,
				alias: "value",
			},
		]);
	});

	it("compiles a distinct-actor count", () => {
		const { params } = compileMetricQuery(
			metric({
				title: "Distinct actors",
				groupBy: "event_type",
				chart: "bar",
				value: { op: "count_distinct", field: "actor_id" },
			}),
			"org_1",
		);

		expect(params.metrics).toEqual([
			{ op: "count_distinct", field: "actor_id", alias: "value" },
		]);
	});

	it("compiles a ratio into its two operands plus the derived ratio", () => {
		const { params, valueAlias } = compileMetricQuery(
			metric({
				title: "Verified login rate",
				groupBy: "time",
				bucket: "day",
				chart: "line",
				ratio: {
					numerator: { op: "count_distinct", field: "actor_id" },
					denominator: { op: "count" },
				},
			}),
			"org_1",
		);

		expect(valueAlias).toBe("value");
		expect(params.metrics).toEqual([
			{ op: "count_distinct", field: "actor_id", alias: "numerator" },
			{ op: "count", alias: "denominator" },
		]);
		expect(params.derived).toEqual([
			{ alias: "value", numerator: "numerator", denominator: "denominator" },
		]);
	});

	it("lowers a multi-dimensional group-by onto groupBys", () => {
		const { params } = compileMetricQuery(
			metric({
				title: "Events by category and actor",
				groupBy: ["category", "actor"],
				chart: "bar",
			}),
			"org_1",
		);

		expect(params.groupBy).toBeUndefined();
		expect(params.groupBys).toEqual(["category", "actor"]);
	});

	it("converts a metric's ISO time window to query timestamps", () => {
		const from = "2026-01-01T00:00:00.000Z";
		const to = "2026-02-01T00:00:00.000Z";
		const { params } = compileMetricQuery(
			metric({
				title: "Windowed logins",
				groupBy: "event_type",
				chart: "stat",
				from,
				to,
			}),
			"org_1",
		);

		expect(params.from).toEqual(new Date(from));
		expect(params.to).toEqual(new Date(to));
	});
});
