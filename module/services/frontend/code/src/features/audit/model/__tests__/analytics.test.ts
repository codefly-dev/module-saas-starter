import { describe, expect, it } from "vitest";

import type { AuditAggregateBucket } from "../../service/queries";
import {
	auditWindows,
	defaultBucketFor,
	distinctCount,
	newUserCount,
	pivotByTime,
	relativeChange,
	topGroups,
	totalCount,
} from "../analytics";

const bucket = (keys: string[], count: number): AuditAggregateBucket => ({
	key: keys[0],
	keys,
	count,
	metrics: {},
});

describe("auditWindows", () => {
	const now = new Date("2026-09-20T12:00:00Z");

	it("ends now and spans the preset's days", () => {
		const { current } = auditWindows("7d", now);
		expect(current.to).toEqual(now);
		expect(current.from).toEqual(new Date("2026-09-13T12:00:00Z"));
	});

	// A delta compares like with like: the previous window is exactly as long
	// as the current one and ends where the current one starts.
	it("puts an equal-length previous window immediately before", () => {
		const { current, previous } = auditWindows("30d", now);
		expect(previous.to).toEqual(current.from);
		expect(current.to.getTime() - current.from.getTime()).toBe(
			previous.to.getTime() - previous.from.getTime(),
		);
	});

	it("widens the default bucket for a long range", () => {
		expect(defaultBucketFor("7d")).toBe("day");
		expect(defaultBucketFor("30d")).toBe("day");
		expect(defaultBucketFor("90d")).toBe("week");
	});
});

describe("relativeChange", () => {
	it("is a fraction of the previous value", () => {
		expect(relativeChange(112, 100)).toBeCloseTo(0.12);
		expect(relativeChange(50, 100)).toBeCloseTo(-0.5);
	});

	// A change from nothing is not a percentage; a tile shows no delta rather
	// than an infinite or misleading one.
	it("is undefined when there is nothing to compare against", () => {
		expect(relativeChange(5, 0)).toBeUndefined();
		expect(relativeChange(0, 0)).toBeUndefined();
	});
});

describe("totals", () => {
	const buckets = [bucket(["a"], 3), bucket(["b"], 4), bucket(["c"], 0)];
	it("sums counts and counts groups", () => {
		expect(totalCount(buckets)).toBe(7);
		expect(distinctCount(buckets)).toBe(3);
		expect(totalCount([])).toBe(0);
	});
});

describe("pivotByTime", () => {
	// The server returns one row per (time, group) that HAS events. A group
	// silent on some day has no row, and a chart aligns series by index, so
	// that day must be zero-filled or every later point shifts left.
	it("zero-fills the time buckets a group is silent in", () => {
		const { labels, series } = pivotByTime([
			bucket(["2026-09-01", "identity"], 5),
			bucket(["2026-09-02", "identity"], 2),
			bucket(["2026-09-02", "security"], 9),
			bucket(["2026-09-03", "identity"], 1),
		]);
		expect(labels).toEqual(["2026-09-01", "2026-09-02", "2026-09-03"]);
		const security = series.find((s) => s.name === "security");
		expect(security?.data.map((d) => d.value)).toEqual([0, 9, 0]);
	});

	it("sorts time ascending regardless of arrival order", () => {
		const { labels } = pivotByTime([
			bucket(["2026-09-03", "x"], 1),
			bucket(["2026-09-01", "x"], 1),
		]);
		expect(labels).toEqual(["2026-09-01", "2026-09-03"]);
	});

	it("orders series by total, largest first", () => {
		const { series } = pivotByTime([
			bucket(["d1", "small"], 1),
			bucket(["d1", "big"], 50),
			bucket(["d2", "medium"], 10),
		]);
		expect(series.map((s) => s.name)).toEqual(["big", "medium", "small"]);
	});

	// Folding rather than dropping keeps the stack summing to the true total.
	it("folds groups past the limit into one band that keeps the total", () => {
		const rows = ["a", "b", "c", "d"].map((g, i) => bucket(["d1", g], 10 - i));
		const { series } = pivotByTime(rows, 2);
		expect(series.map((s) => s.name)).toEqual(["a", "b", "other"]);
		expect(series.reduce((sum, s) => sum + s.data[0].value, 0)).toBe(
			10 + 9 + 8 + 7,
		);
	});

	it("returns nothing for nothing", () => {
		expect(pivotByTime([])).toEqual({ labels: [], series: [] });
	});
});

describe("topGroups", () => {
	it("takes the largest N without mutating the input", () => {
		const input = [bucket(["a"], 1), bucket(["b"], 9), bucket(["c"], 5)];
		expect(topGroups(input, 2)).toEqual([
			{ key: "b", count: 9 },
			{ key: "c", count: 5 },
		]);
		expect(input[0].key).toBe("a");
	});
});

describe("newUserCount", () => {
	it("counts only the registered new-user event types", () => {
		expect(
			newUserCount([
				bucket(["saas.user.created"], 3),
				bucket(["saas.user.registered"], 4),
				bucket(["saas.user.updated"], 100),
			]),
		).toBe(7);
		expect(newUserCount([])).toBe(0);
	});
});
