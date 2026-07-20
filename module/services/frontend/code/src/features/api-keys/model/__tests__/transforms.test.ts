import { describe, expect, it } from "vitest";
import { formatEnvironment, maskKeyPrefix } from "../transforms";

describe("formatEnvironment", () => {
	it("returns 'live' for environment 1", () => {
		expect(formatEnvironment(1)).toBe("live");
	});

	it("returns 'test' for environment 2", () => {
		expect(formatEnvironment(2)).toBe("test");
	});

	it("returns 'unknown' for environment 0", () => {
		expect(formatEnvironment(0)).toBe("unknown");
	});

	it("returns 'unknown' for unrecognized environment", () => {
		expect(formatEnvironment(99)).toBe("unknown");
	});
});

describe("maskKeyPrefix", () => {
	it("appends 24 asterisks to prefix", () => {
		const result = maskKeyPrefix("sk_live_");
		expect(result).toBe("sk_live_" + "*".repeat(24));
	});

	it("returns 4 asterisks for empty prefix", () => {
		expect(maskKeyPrefix("")).toBe("****");
	});

	it("handles short prefix", () => {
		const result = maskKeyPrefix("ab");
		expect(result).toBe("ab" + "*".repeat(24));
		expect(result.length).toBe(26);
	});
});
