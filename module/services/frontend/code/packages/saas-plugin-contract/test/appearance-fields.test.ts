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
				!(
					FRONTEND_OPTIONAL_APPEARANCE_FIELD_NAMES as readonly string[]
				).includes(field),
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

// For achromatic OKLCH, relative luminance is L cubed. This checks the actual
// default token pairs used by small captions, not a rounded hex approximation.
it("default muted captions meet 4.5:1 on their supported surfaces in both modes", () => {
	const luminance = (value: string) => {
		const match = /^oklch\(([0-9.]+) 0 0\)$/.exec(value);
		if (!match)
			throw new Error("Recalculate contrast for a chromatic default token");
		return Number(match[1]) ** 3;
	};
	for (const mode of ["light", "dark"] as const) {
		const tokens = DEFAULT_FRONTEND_APPEARANCE[mode];
		const foreground = luminance(tokens.mutedForeground);
		for (const surface of [
			tokens.background,
			tokens.card,
			tokens.muted,
			tokens.accent,
		]) {
			const background = luminance(surface);
			expect(
				(Math.max(foreground, background) + 0.05) /
					(Math.min(foreground, background) + 0.05),
			).toBeGreaterThanOrEqual(4.5);
		}
	}
});
