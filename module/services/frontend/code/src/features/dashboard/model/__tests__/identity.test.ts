import { describe, expect, it } from "vitest";
import { metricIdentity } from "../identity";
import type { MetricDef } from "../schema";

// metricIdentity is the single source of truth for "the same rendered panel":
// <Dashboard> keys cards by it and the stub driver dedups against it. If it
// omitted a field that changes what a card plots, two distinct panels would
// collide on one React key. These lock the fields that must disambiguate.
const base: MetricDef = {
	title: "Logins",
	groupBy: "time",
	bucket: "day",
	chart: "line",
	event: { type: "auth.login" },
};

describe("metricIdentity", () => {
	it("is stable for identical metrics", () => {
		expect(metricIdentity(base)).toBe(metricIdentity({ ...base }));
	});

	it("distinguishes metrics that differ only in the plotted value", () => {
		const counted: MetricDef = { ...base, value: { op: "count" } };
		const summed: MetricDef = {
			...base,
			value: { op: "sum", field: "payload:amount" },
		};
		expect(metricIdentity(counted)).not.toBe(metricIdentity(summed));
	});

	it("distinguishes metrics that differ only in the audit window", () => {
		const allTime = base;
		const windowed: MetricDef = { ...base, from: "2026-01-01T00:00:00Z" };
		expect(metricIdentity(allTime)).not.toBe(metricIdentity(windowed));
	});

	it("distinguishes single- from multi-dimensional grouping", () => {
		const single: MetricDef = { ...base, groupBy: "actor", bucket: undefined };
		const multi: MetricDef = {
			...base,
			groupBy: ["actor", "time"],
		};
		expect(metricIdentity(single)).not.toBe(metricIdentity(multi));
	});
});
