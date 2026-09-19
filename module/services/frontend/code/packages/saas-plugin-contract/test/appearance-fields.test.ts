import { describe, expect, it } from "vitest";

import {
	DEFAULT_FRONTEND_APPEARANCE,
	FRONTEND_APPEARANCE_FIELD_NAMES,
	resolveFrontendAppearance,
} from "../src/index.js";

// The exported vocabulary is only useful if it is the SAME list the validator
// enforces. A private copy would pass while the real validator disagreed, which
// is the failure this export exists to prevent — so hold the list to the
// resolved appearance's own keys in both directions.
describe("FRONTEND_APPEARANCE_FIELD_NAMES is the validator's own vocabulary", () => {
	it("names exactly the fields a resolved appearance carries", () => {
		expect([...FRONTEND_APPEARANCE_FIELD_NAMES].sort()).toEqual(
			Object.keys(DEFAULT_FRONTEND_APPEARANCE).sort(),
		);
	});

	it("accepts every named field on a descriptor", () => {
		for (const field of FRONTEND_APPEARANCE_FIELD_NAMES) {
			const value = DEFAULT_FRONTEND_APPEARANCE[field];
			expect(() => resolveFrontendAppearance({ [field]: value })).not.toThrow();
		}
	});

	it("rejects a field outside the list, naming it", () => {
		expect(() =>
			resolveFrontendAppearance({
				buttonRadius: "8px",
			} as unknown as Parameters<typeof resolveFrontendAppearance>[0]),
		).toThrow(/unknown field 'buttonRadius'/);
	});
});
