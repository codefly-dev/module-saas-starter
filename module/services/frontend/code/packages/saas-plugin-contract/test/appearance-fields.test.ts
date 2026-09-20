import { describe, expect, it } from "vitest";

import {
	DEFAULT_FRONTEND_APPEARANCE,
	FRONTEND_APPEARANCE_FIELD_NAMES,
	FRONTEND_OPTIONAL_APPEARANCE_FIELD_NAMES,
	resolveFrontendAppearance,
} from "../src/index.js";

// The exported vocabulary is only useful if it is the SAME list the validator
// enforces. A private copy would pass while the real validator disagreed, which
// is the failure this export exists to prevent — so hold the list to the
// resolved appearance's own keys in both directions.
describe("FRONTEND_APPEARANCE_FIELD_NAMES is the validator's own vocabulary", () => {
	it("names exactly the fields a resolved appearance carries", () => {
		const required = FRONTEND_APPEARANCE_FIELD_NAMES.filter(
			(field) =>
				!(FRONTEND_OPTIONAL_APPEARANCE_FIELD_NAMES as readonly string[]).includes(
					field,
				),
		);
		expect([...required].sort()).toEqual(
			Object.keys(DEFAULT_FRONTEND_APPEARANCE).sort(),
		);
	});

	// An optional field has no neutral default — "the corner of a button" is
	// either decided by the design or derived from `radius` — so it is absent
	// from the resolved default and present exactly when a descriptor sets it.
	it("carries an optional field only when the descriptor decides it", () => {
		expect(DEFAULT_FRONTEND_APPEARANCE).not.toHaveProperty("buttonRadius");
		expect(
			resolveFrontendAppearance({ buttonRadius: "999px" }).buttonRadius,
		).toBe("999px");
		expect(() => resolveFrontendAppearance({ buttonRadius: "pill" })).toThrow(
			/buttonRadius/,
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
				buttonHeight: "2.5rem",
			} as unknown as Parameters<typeof resolveFrontendAppearance>[0]),
		).toThrow(/unknown field 'buttonHeight'/);
	});
});
