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

	it("distinguishes metrics that differ only in description", () => {
		// description is the card's rendered subtitle, so two otherwise-identical
		// cards with different subtitles are distinct panels.
		const plain = base;
		const described: MetricDef = { ...base, description: "Successful sign-ins" };
		expect(metricIdentity(plain)).not.toBe(metricIdentity(described));
	});

	it("distinguishes metrics that differ only in limit", () => {
		// A top-5 and a top-10 of the same categorical metric render as different
		// cards, so their limit must disambiguate them.
		const ranked: MetricDef = {
			title: "Top actions",
			groupBy: "event_type",
			chart: "bar",
		};
		const topFive: MetricDef = { ...ranked, limit: 5 };
		const topTen: MetricDef = { ...ranked, limit: 10 };
		expect(metricIdentity(topFive)).not.toBe(metricIdentity(topTen));
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
