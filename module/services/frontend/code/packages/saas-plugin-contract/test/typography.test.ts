import { describe, expect, it } from "vitest";

import {
	DEFAULT_CONTROL_SIZES,
	DEFAULT_FRONTEND_APPEARANCE,
	DEFAULT_TYPE_ROLES,
	DEFAULT_TYPE_SCALE,
	DEFAULT_TYPE_SLOTS,
	FRONTEND_CONTROL_SIZE_NAMES,
	FRONTEND_TYPE_ROLE_NAMES,
	FRONTEND_TYPE_SCALE_STEPS,
	FRONTEND_TYPE_SLOT_NAMES,
	type FrontendAppearanceDefinition,
	resolveFrontendAppearance,
	resolveTypeSlot,
} from "../src/index.js";

const definition = (
	value: Record<string, unknown>,
): FrontendAppearanceDefinition =>
	value as unknown as FrontendAppearanceDefinition;

describe("the layers are internally total", () => {
	it("gives every declared role a declared scale step", () => {
		for (const [name, role] of Object.entries(DEFAULT_TYPE_ROLES)) {
			if (role.size === undefined) continue;
			expect(
				DEFAULT_TYPE_SCALE[role.size],
				`role '${name}' points at scale step '${role.size}', which has no value`,
			).toBeDefined();
		}
	});

	it("points every declared slot at a declared role", () => {
		for (const [slot, role] of Object.entries(DEFAULT_TYPE_SLOTS)) {
			expect(
				DEFAULT_TYPE_ROLES[role],
				`slot '${slot}' names role '${role}', which is not declared`,
			).toBeDefined();
		}
	});

	it("names every slot and role exactly once in its vocabulary", () => {
		expect(Object.keys(DEFAULT_TYPE_SLOTS).sort()).toEqual(
			[...FRONTEND_TYPE_SLOT_NAMES].sort(),
		);
		expect(Object.keys(DEFAULT_TYPE_ROLES).sort()).toEqual(
			[...FRONTEND_TYPE_ROLE_NAMES].sort(),
		);
		expect(Object.keys(DEFAULT_TYPE_SCALE).sort()).toEqual(
			[...FRONTEND_TYPE_SCALE_STEPS].sort(),
		);
		expect(Object.keys(DEFAULT_CONTROL_SIZES).sort()).toEqual(
			[...FRONTEND_CONTROL_SIZE_NAMES].sort(),
		);
	});

	it("gives every control rung a declared text role", () => {
		for (const [name, rung] of Object.entries(DEFAULT_CONTROL_SIZES)) {
			expect(
				DEFAULT_TYPE_ROLES[rung.text],
				`rung '${name}' names role '${rung.text}', which is not declared`,
			).toBeDefined();
		}
	});

	it("declares at least one property on every role", () => {
		for (const [name, role] of Object.entries(DEFAULT_TYPE_ROLES)) {
			expect(
				Object.keys(role).length,
				`role '${name}' decides nothing`,
			).toBeGreaterThan(0);
		}
	});
});

// The defaults exist to reproduce what the kit renders TODAY. These pin the
// flattened output of the slots whose values the migration replaces, so a change
// to the scale or a role that would move a pixel fails here rather than on screen.
describe("default slots flatten to the kit's current values", () => {
	it.each([
		[
			"card-title",
			{
				fontSize: "1rem",
				fontWeight: "500",
				lineHeight: "1.375",
				fontFamily: "heading",
			},
		],
		[
			"card-title-sm",
			{
				fontSize: "0.875rem",
				fontWeight: "500",
				lineHeight: "1.375",
				fontFamily: "heading",
			},
		],
		[
			"dialog-title",
			{
				fontSize: "1rem",
				fontWeight: "500",
				lineHeight: "1",
				fontFamily: "heading",
			},
		],
		[
			"sheet-title",
			{
				fontSize: "1rem",
				fontWeight: "500",
				lineHeight: "1.5rem",
				fontFamily: "heading",
			},
		],
		["card-description", { fontSize: "0.875rem", lineHeight: "1.25rem" }],
		[
			"page-title",
			{
				fontSize: "1.5rem",
				fontWeight: "700",
				lineHeight: "2rem",
				letterSpacing: "-0.025em",
			},
		],
		[
			"section-title",
			{
				fontSize: "1.125rem",
				fontWeight: "600",
				lineHeight: "1.75rem",
				letterSpacing: "-0.025em",
			},
		],
		["label", { fontSize: "0.875rem", fontWeight: "500", lineHeight: "1" }],
		[
			"command-shortcut",
			{ fontSize: "0.75rem", lineHeight: "1rem", letterSpacing: "0.1em" },
		],
		[
			"dropdown-menu-label",
			{ fontSize: "0.75rem", fontWeight: "500", lineHeight: "1rem" },
		],
		["select-label", { fontSize: "0.75rem", lineHeight: "1rem" }],
		["table-head", { fontWeight: "500" }],
		["input-touch", { fontSize: "1rem", lineHeight: "1.5rem" }],
		["input", { fontSize: "0.875rem", lineHeight: "1.25rem" }],
		[
			"metric-value",
			{
				fontSize: "1.5rem",
				fontWeight: "600",
				lineHeight: "2rem",
				letterSpacing: "-0.025em",
			},
		],
		[
			"metric-total",
			{
				fontSize: "2.25rem",
				fontWeight: "700",
				lineHeight: "2.5rem",
				letterSpacing: "-0.025em",
			},
		],
	] as const)("%s", (slot, expected) => {
		const resolved = resolveTypeSlot(DEFAULT_FRONTEND_APPEARANCE, slot);
		for (const [property, value] of Object.entries(expected))
			expect(resolved[property as keyof typeof resolved]).toBe(value);
	});

	// A role sets only what it decides. `table-head` is a weight today and
	// inherits its size; emitting a size there would change the render.
	it("leaves undeclared properties undefined rather than defaulting them", () => {
		const emphasis = resolveTypeSlot(DEFAULT_FRONTEND_APPEARANCE, "table-head");
		expect(emphasis.fontSize).toBeUndefined();
		expect(emphasis.lineHeight).toBeUndefined();
		expect(emphasis.letterSpacing).toBeUndefined();
		expect(emphasis.fontFamily).toBeUndefined();
	});

	it("keeps the kit's current control geometry, in spacing units", () => {
		expect(DEFAULT_CONTROL_SIZES.default).toEqual({
			height: "8",
			paddingX: "2.5",
			icon: "4",
			text: "control",
		});
		expect(DEFAULT_CONTROL_SIZES.xs.height).toBe("6");
		expect(DEFAULT_CONTROL_SIZES.sm.height).toBe("7");
		expect(DEFAULT_CONTROL_SIZES.lg.height).toBe("9");
	});
});

describe("a skin overrides the layers partially and fail-closed", () => {
	it("merges a role field-wise, keeping what it did not restate", () => {
		const appearance = resolveFrontendAppearance(
			definition({ typeRoles: { body: { weight: "600" } } }),
		);
		const body = resolveTypeSlot(appearance, "table");
		expect(body.fontWeight).toBe("600");
		expect(body.fontSize).toBe("0.875rem");
		expect(body.lineHeight).toBe("1.25rem");
	});

	it("re-points a slot at another declared role", () => {
		const appearance = resolveFrontendAppearance(
			definition({ typeSlots: { "card-title": "page-title" } }),
		);
		expect(resolveTypeSlot(appearance, "card-title").fontSize).toBe("1.5rem");
		// The role it left is untouched for every other slot that uses it.
		expect(resolveTypeSlot(appearance, "sheet-title").fontSize).toBe("1rem");
	});

	it("re-scales every role that points at a step when the step moves", () => {
		const appearance = resolveFrontendAppearance(
			definition({ typeScale: { "3": "1rem" } }),
		);
		expect(resolveTypeSlot(appearance, "card-description").fontSize).toBe(
			"1rem",
		);
		expect(resolveTypeSlot(appearance, "command-item").fontSize).toBe("1rem");
	});

	it.each([
		[{ typeScale: { "3": "12" } }, /typeScale step '3'/],
		[{ typeScale: { "12": "1rem" } }, /typeScale has unknown field '12'/],
		[{ typeRoles: { body: { size: "42" } } }, /size '42' is not a scale step/],
		[{ typeRoles: { body: { weight: "450" } } }, /weight '450' is unsupported/],
		[{ typeRoles: { body: { lineHeight: "loose" } } }, /lineHeight must be/],
		[{ typeRoles: { body: { tracking: "wide" } } }, /tracking must be/],
		[{ typeRoles: { body: { shout: "yes" } } }, /unknown field 'shout'/],
		[
			{ typeRoles: { heroic: { size: "9" } } },
			/typeRoles has unknown field 'heroic'/,
		],
		[{ typeSlots: { "card-title": "heroic" } }, /names unknown role 'heroic'/],
		[
			{ typeSlots: { "hero-title": "body" } },
			/typeSlots has unknown field 'hero-title'/,
		],
		[
			{ controlSizes: { default: { height: "2rem" } } },
			/must be a positive number of spacing units/,
		],
		[
			{ controlSizes: { default: { text: "heroic" } } },
			/text names unknown role 'heroic'/,
		],
		[
			{ controlSizes: { xl: { height: "12" } } },
			/controlSizes has unknown field 'xl'/,
		],
	])("refuses %j", (bad, message) => {
		expect(() => resolveFrontendAppearance(definition(bad))).toThrow(message);
	});

	// Negative tracking is the normal case for a display role; a regex that only
	// allowed positive lengths would refuse every tight heading.
	it("accepts negative tracking", () => {
		expect(() =>
			resolveFrontendAppearance(
				definition({ typeRoles: { "page-title": { tracking: "-0.04em" } } }),
			),
		).not.toThrow();
	});
});
