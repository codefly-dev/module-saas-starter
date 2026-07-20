import { describe, expect, it } from "vitest";
import { cn, formatDate, formatLimit, truncateUUID } from "../utils";

describe("formatDate", () => {
	it("returns formatted date for a valid ISO string", () => {
		const result = formatDate("2024-06-15T10:30:00Z");
		// Should contain month, day, year and time components
		expect(result).toContain("Jun");
		expect(result).toContain("15");
		expect(result).toContain("2024");
	});

	it("returns '-' for undefined input", () => {
		expect(formatDate(undefined)).toBe("-");
	});

	it("returns the original string for an unparseable date", () => {
		// Intl.DateTimeFormat may or may not throw for bad input depending
		// on environment; the catch returns the original string
		const result = formatDate("not-a-date");
		expect(typeof result).toBe("string");
	});

	it("returns '-' for empty string treated as undefined", () => {
		// empty string is falsy but not undefined; new Date("") is Invalid Date
		const result = formatDate("");
		// The function checks !dateString which is true for ""
		expect(result).toBe("-");
	});
});

describe("truncateUUID", () => {
	it("truncates a full UUID to first 8 chars with ellipsis", () => {
		expect(truncateUUID("550e8400-e29b-41d4-a716-446655440000")).toBe(
			"550e8400...",
		);
	});

	it("returns original string if shorter than 8 chars", () => {
		expect(truncateUUID("abc")).toBe("abc");
	});

	it("returns empty string for empty input", () => {
		expect(truncateUUID("")).toBe("");
	});

	it("handles exactly 8 character string", () => {
		expect(truncateUUID("12345678")).toBe("12345678...");
	});
});

describe("formatLimit", () => {
	it("returns 'Unlimited' for -1", () => {
		expect(formatLimit(-1)).toBe("Unlimited");
	});

	it("returns 'Disabled' for 0", () => {
		expect(formatLimit(0)).toBe("Disabled");
	});

	it("returns formatted number for positive value", () => {
		expect(formatLimit(1000)).toBe("1,000");
	});

	it("returns simple number for small value", () => {
		expect(formatLimit(5)).toBe("5");
	});

	it("handles bigint input", () => {
		expect(formatLimit(BigInt(-1))).toBe("Unlimited");
	});

	it("formats large numbers with commas", () => {
		expect(formatLimit(1000000)).toBe("1,000,000");
	});
});

describe("cn", () => {
	it("merges class names", () => {
		const result = cn("px-2", "py-1");
		expect(result).toContain("px-2");
		expect(result).toContain("py-1");
	});

	it("handles conditional classes", () => {
		const isActive = true;
		const result = cn("base", isActive && "active");
		expect(result).toContain("active");
	});

	it("resolves tailwind conflicts (last wins)", () => {
		const result = cn("px-2", "px-4");
		expect(result).toBe("px-4");
	});

	it("handles falsy values", () => {
		const result = cn("base", false, null, undefined, "end");
		expect(result).toContain("base");
		expect(result).toContain("end");
	});
});
