import { describe, expect, it } from "vitest";
import { parseEntitlementLimit } from "./limit";

describe("parseEntitlementLimit", () => {
	it.each(["", "1.5", "-2", "1e3", "NaN", "9223372036854775808"])(
		"rejects %s",
		(value) => {
			expect(parseEntitlementLimit(value)).toBeNull();
		},
	);
	it.each(["-1", "0", "12", "9223372036854775807"])(
		"preserves %s exactly",
		(value) => {
			expect(parseEntitlementLimit(value)).toBe(BigInt(value));
		},
	);
});
