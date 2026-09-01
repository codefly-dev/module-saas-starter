import { describe, expect, it } from "vitest";
import { formatAxisKey, formatAxisValue } from "../format.js";

describe("formatAxisValue", () => {
	it("keeps small numbers plain and compacts large ones", () => {
		expect(formatAxisValue(3)).toBe("3");
		expect(formatAxisValue(0.5)).toBe("0.5");
		expect(formatAxisValue(1200)).toBe("1.2K");
	});
});

describe("formatAxisKey", () => {
	// The audit RPC emits day/week/month buckets as ISO timestamps at UTC
	// midnight with Postgres' bare-hour offset ("+00"), which Date.parse rejects
	// until it's normalized. A day bucket should read as a date, not a timestamp.
	it("formats a UTC-midnight ISO bucket as a date", () => {
		expect(formatAxisKey("2026-09-01T00:00:00+00")).toBe("Sep 1");
	});

	it("accepts a plain ISO date", () => {
		expect(formatAxisKey("2026-12-25")).toBe("Dec 25");
	});

	it("keeps the time on a sub-day bucket", () => {
		const label = formatAxisKey("2026-09-01T14:30:00+00");
		expect(label).toContain("Sep 1");
		expect(label).toMatch(/\d:\d\d|\d\d:\d\d/);
	});

	// A categorical key (a region, a plan name, a bare number) is not a time
	// bucket and must pass through untouched — never coerced through Date.
	it("passes non-time keys through unchanged", () => {
		expect(formatAxisKey("US East")).toBe("US East");
		expect(formatAxisKey("enterprise")).toBe("enterprise");
		expect(formatAxisKey("42")).toBe("42");
	});
});
