import { describe, expect, it } from "vitest";

import {
	DEFAULT_FRONTEND_APPEARANCE,
	FRONTEND_APPEARANCE_FIELD_NAMES,
	type FrontendAppearanceDefinition,
	resolveFrontendAppearance,
	sanitizeFrontendAppearance,
} from "../src/index.js";

/**
 * A descriptor written against a different token vocabulary: a full palette
 * plus seven keys this contract has never defined. Before sanitizing, one of
 * those keys cost all of the others.
 */
const FOREIGN_VOCABULARY = {
	defaultTheme: "light",
	radius: "0.5rem",
	buttonRadius: "0.25rem",
	buttonHeight: "2.5rem",
	buttonPaddingX: "1.25rem",
	fontSans: "Inter, Arial, sans-serif",
	light: {
		background: "#FAFAFA",
		primary: "#0055CC",
		primaryHover: "#0044AA",
		primaryActive: "#08258C",
		disabled: "#ACB6C3",
		disabledForeground: "#2D3840",
	},
	dark: { background: "#080E1A", primary: "#83BBFF" },
} as const;

describe("sanitizeFrontendAppearance", () => {
	it("leaves a clean definition untouched and reports nothing", () => {
		const definition = {
			radius: "0.5rem",
			light: { primary: "#0055CC" },
		};
		const result = sanitizeFrontendAppearance(definition);
		expect(result.dropped).toEqual([]);
		expect(result.definition).toEqual({
			radius: "0.5rem",
			light: { primary: "#0055CC" },
			dark: undefined,
		});
	});

	it("drops unknown top-level fields and names them", () => {
		const { definition, dropped } = sanitizeFrontendAppearance({
			radius: "0.5rem",
			buttonRadius: "999px",
		});
		expect(dropped).toEqual(["buttonRadius"]);
		expect(definition).not.toHaveProperty("buttonRadius");
		expect(definition).toMatchObject({ radius: "0.5rem" });
	});

	it("drops unknown per-mode tokens with a mode-qualified path", () => {
		const { dropped } = sanitizeFrontendAppearance({
			light: { primary: "#0055CC", primaryHover: "#0044AA" },
			dark: { primaryActive: "#08258C" },
		});
		expect(dropped).toEqual(["light.primaryHover", "dark.primaryActive"]);
	});

	it("keeps every other token when a descriptor carries unknown keys", () => {
		const { definition, dropped } =
			sanitizeFrontendAppearance(FOREIGN_VOCABULARY);
		expect(dropped).toEqual([
			"buttonRadius",
			"buttonHeight",
			"buttonPaddingX",
			"light.primaryHover",
			"light.primaryActive",
			"light.disabled",
			"light.disabledForeground",
		]);

		// The whole point: what survives still resolves, and carries the brand.
		const resolved = resolveFrontendAppearance(
			definition as FrontendAppearanceDefinition,
		);
		expect(resolved.fontSans).toBe("Inter, Arial, sans-serif");
		expect(resolved.radius).toBe("0.5rem");
		expect(resolved.light.primary).toBe("#0055CC");
		expect(resolved.light.background).toBe("#FAFAFA");
		expect(resolved.dark.primary).toBe("#83BBFF");
	});

	it("rejects the same descriptor outright without sanitizing", () => {
		expect(() =>
			resolveFrontendAppearance(FOREIGN_VOCABULARY as never),
		).toThrow(/unknown field 'buttonRadius'/);
	});

	it("does not widen the injection gate: unsafe values still throw", () => {
		const { definition, dropped } = sanitizeFrontendAppearance({
			buttonRadius: "999px",
			light: { primary: "red; background: url(javascript:alert(1))" },
		});
		expect(dropped).toEqual(["buttonRadius"]);
		expect(() =>
			resolveFrontendAppearance(definition as FrontendAppearanceDefinition),
		).toThrow(/safe non-empty CSS value/);
	});

	it("passes non-object definitions through for the validator to reject", () => {
		for (const value of [undefined, null, []]) {
			const result = sanitizeFrontendAppearance(value);
			expect(result.dropped).toEqual([]);
			expect(result.definition).toBe(value);
		}
	});

	it("exposes exactly the fields the validator accepts", () => {
		expect([...FRONTEND_APPEARANCE_FIELD_NAMES]).toEqual([
			...Object.keys(DEFAULT_FRONTEND_APPEARANCE).filter(
				(key) => key !== "light" && key !== "dark",
			),
			"light",
			"dark",
		]);
	});
});
